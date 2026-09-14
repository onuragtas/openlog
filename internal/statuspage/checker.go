package statuspage

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/onuragtas/openlog/internal/store/clickhouse"
	"github.com/onuragtas/openlog/internal/store/postgres"
)

// Probes are the raw signals of one self-check (Evaluate turns them into component statuses).
type Probes struct {
	PostgresOK       bool
	ClickHouseOK     bool
	Live             map[string]int // live instances per service (component_heartbeats)
	AlertEvaluators  int            // evaluators seen within 2 minutes
	EnabledRules     int
	OverdueRules     int // enabled rules not evaluated within 5 minutes of their schedule
	NewestMetricAge  time.Duration
	HasRecentMetrics bool
	Maintenance      map[string]bool // components in an active maintenance window
}

// Evaluate derives component statuses from probes.
func Evaluate(p Probes) map[string]string {
	st := map[string]string{}
	live := func(services ...string) int {
		n := 0
		for _, s := range services {
			n += p.Live[s]
		}
		return n
	}
	// ingest: OTLP receivers alive.
	if live("openlog-ingest", "openlog-allinone") > 0 {
		st["ingest"] = Operational
	} else {
		st["ingest"] = MajorOutage
	}
	// query_api: this api pod reaches PostgreSQL and ClickHouse.
	switch {
	case p.PostgresOK && p.ClickHouseOK:
		st["query_api"] = Operational
	case p.PostgresOK || p.ClickHouseOK:
		st["query_api"] = MajorOutage
	default:
		st["query_api"] = MajorOutage
	}
	// alerting: evaluators alive and rules evaluated on time.
	switch {
	case p.EnabledRules == 0:
		st["alerting"] = Operational
	case p.AlertEvaluators == 0:
		st["alerting"] = MajorOutage
	case p.OverdueRules*2 > p.EnabledRules:
		st["alerting"] = PartialOutage
	case p.OverdueRules*10 > p.EnabledRules:
		st["alerting"] = Degraded
	default:
		st["alerting"] = Operational
	}
	// processing: consumers alive and stored data fresh.
	switch {
	case live("openlog-processor", "openlog-allinone") == 0:
		st["processing"] = MajorOutage
	case !p.ClickHouseOK:
		st["processing"] = MajorOutage
	case p.HasRecentMetrics && p.NewestMetricAge > 30*time.Minute:
		st["processing"] = PartialOutage
	case p.HasRecentMetrics && p.NewestMetricAge > 10*time.Minute:
		st["processing"] = Degraded
	default:
		st["processing"] = Operational
	}
	for c := range p.Maintenance {
		if _, ok := st[c]; ok && p.Maintenance[c] {
			st[c] = Maintenance
		}
	}
	return st
}

// Checker runs the self-checks (api leader task).
type Checker struct {
	Store    PGStore
	CH       clickhouse.Conn // nil: query path and processing report an outage
	Database string
	Interval time.Duration // default 1m
	Log      *slog.Logger
	Now      func() time.Time
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Probe collects Probes.
func (c *Checker) Probe(ctx context.Context) Probes {
	p := Probes{Live: map[string]int{}, Maintenance: map[string]bool{}}
	pool := c.Store.Pool
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	p.PostgresOK = pool.Ping(pctx) == nil
	cancel()
	if p.PostgresOK {
		if insts, err := postgres.LiveInstances(ctx, pool); err == nil {
			for _, in := range insts {
				p.Live[in.Component]++
			}
		}
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM alert_evaluators WHERE last_seen > now() - interval '2 minutes'`).Scan(&p.AlertEvaluators)
		_ = pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE true), count(*) FILTER (WHERE l.next_eval_at < now() - interval '5 minutes')
			FROM alert_rules r LEFT JOIN alert_rule_leases l ON l.rule_id = r.id WHERE r.enabled`).Scan(&p.EnabledRules, &p.OverdueRules)
		if ins, err := c.Store.Public(ctx, c.now()); err == nil {
			for _, in := range ins {
				if in.ActiveMaintenance(c.now()) {
					for _, comp := range in.Components {
						p.Maintenance[comp] = true
					}
				}
			}
		}
	}
	if c.CH != nil {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		p.ClickHouseOK = c.CH.Ping(cctx) == nil
		cancel()
		if p.ClickHouseOK {
			db := c.Database
			if db == "" {
				db = "openlog"
			}
			var newest time.Time
			qctx := ch.Context(ctx, ch.WithSettings(ch.Settings{"max_execution_time": 10, "skip_unavailable_shards": 1}))
			// Aggregate over all tenants: only a timestamp leaves ClickHouse.
			if err := c.CH.QueryRow(qctx, "SELECT max(timestamp) FROM `"+strings.ReplaceAll(db, "`", "")+"`.metrics WHERE timestamp > now() - INTERVAL 1 HOUR").Scan(&newest); err == nil && newest.Unix() > 0 {
				p.HasRecentMetrics = true
				p.NewestMetricAge = max(0, c.now().Sub(newest))
			}
		}
	}
	return p
}

// RunOnce checks, records and returns the snapshot.
func (c *Checker) RunOnce(ctx context.Context) (Snapshot, error) {
	p := c.Probe(ctx)
	snap := Snapshot{CheckedAt: c.now().UTC(), Components: Evaluate(p), ProcessingLagSeconds: -1}
	if p.HasRecentMetrics {
		snap.ProcessingLagSeconds = int64(p.NewestMetricAge.Seconds())
	}
	if !p.PostgresOK {
		return snap, nil // nowhere to record; the page shows the last snapshot as stale
	}
	return snap, c.Store.RecordCheck(ctx, snap)
}

// Run checks every Interval until ctx is done.
func (c *Checker) Run(ctx context.Context) {
	interval := c.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	lastPrune := time.Time{}
	for {
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if _, err := c.RunOnce(rctx); err != nil && ctx.Err() == nil && c.Log != nil {
			c.Log.Warn("status page check failed", "err", err)
		}
		if time.Since(lastPrune) > 24*time.Hour {
			if err := c.Store.Prune(rctx, c.now()); err == nil {
				lastPrune = time.Now()
			}
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// Page is the public status document (GET /api/v1/status).
type Page struct {
	Status      string          `json:"status"`
	CheckedAt   *time.Time      `json:"checked_at"`
	Components  []ComponentPage `json:"components"`
	Incidents   []Incident      `json:"incidents"`
	Maintenance []Incident      `json:"maintenance"`
	History     []Incident      `json:"history"`
}

// ComponentPage is one component on the page.
type ComponentPage struct {
	ID        string   `json:"id"`
	Status    string   `json:"status"`
	Uptime90d *float64 `json:"uptime_90d"`
	Days      []Day    `json:"days"`
}

// Service builds the public page from PostgreSQL (any api pod), cached for 30 seconds.
type Service struct {
	Store PGStore
	Now   func() time.Time
	// StaleAfter marks the snapshot unknown when the leader stopped checking (default 5m).
	StaleAfter time.Duration

	mu      sync.Mutex
	cached  *Page
	expires time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Invalidate drops the cached page (after an incident change on this pod).
func (s *Service) Invalidate() {
	s.mu.Lock()
	s.cached = nil
	s.mu.Unlock()
}

// Page returns the public status page.
func (s *Service) Page(ctx context.Context) (Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.cached != nil && now.Before(s.expires) {
		return *s.cached, nil
	}
	page, err := s.build(ctx, now)
	if err != nil {
		return Page{}, err
	}
	s.cached, s.expires = &page, now.Add(30*time.Second)
	return page, nil
}

func (s *Service) build(ctx context.Context, now time.Time) (Page, error) {
	snap, found, err := s.Store.Snapshot(ctx)
	if err != nil {
		return Page{}, err
	}
	stale := s.StaleAfter
	if stale <= 0 {
		stale = 5 * time.Minute
	}
	counts, err := s.Store.Counts(ctx, now.AddDate(0, 0, -90))
	if err != nil {
		return Page{}, err
	}
	incidents, err := s.Store.Public(ctx, now.AddDate(0, 0, -14))
	if err != nil {
		return Page{}, err
	}
	page := Page{Components: []ComponentPage{}, Incidents: []Incident{}, Maintenance: []Incident{}, History: []Incident{}}
	fresh := found && now.Sub(snap.CheckedAt) <= stale
	if found {
		t := snap.CheckedAt
		page.CheckedAt = &t
	}
	var statuses []string
	for _, id := range Components {
		st := Unknown
		if fresh {
			if v, ok := snap.Components[id]; ok {
				st = v
			}
		}
		days, uptime := History(counts[id], now.UTC(), 90)
		page.Components = append(page.Components, ComponentPage{ID: id, Status: st, Uptime90d: uptime, Days: days})
		statuses = append(statuses, st)
	}
	for _, in := range incidents {
		switch {
		case Finished(in.Status):
			page.History = append(page.History, in)
		case in.Kind == KindMaintenance:
			page.Maintenance = append(page.Maintenance, in)
			if in.ActiveMaintenance(now) {
				statuses = append(statuses, Maintenance)
			}
		default:
			page.Incidents = append(page.Incidents, in)
			switch in.Impact {
			case "critical":
				statuses = append(statuses, MajorOutage)
			case "major":
				statuses = append(statuses, PartialOutage)
			case "minor":
				statuses = append(statuses, Degraded)
			}
		}
	}
	page.Status = Worst(statuses...)
	return page, nil
}
