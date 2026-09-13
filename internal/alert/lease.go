package alert

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// LeaseOptions configure a LeaseManager.
type LeaseOptions struct {
	Instance      string
	Version       string
	TTL           time.Duration
	RenewInterval time.Duration
	Log           *slog.Logger
	Registerer    prometheus.Registerer
}

// LeaseManager keeps this evaluator's fair share of rule leases (docs/contracts/alerting.md §4).
type LeaseManager struct {
	store LeaseStore
	o     LeaseOptions

	mu    sync.Mutex
	owned map[string]Lease
	live  int

	gOwned   prometheus.Gauge
	gLive    prometheus.Gauge
	cChanges *prometheus.CounterVec
}

// NewLeaseManager creates a lease manager.
func NewLeaseManager(store LeaseStore, o LeaseOptions) *LeaseManager {
	if o.TTL <= 0 {
		o.TTL = 30 * time.Second
	}
	if o.RenewInterval <= 0 {
		o.RenewInterval = o.TTL / 3
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	m := &LeaseManager{store: store, o: o, owned: map[string]Lease{},
		gOwned:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "openlog_alert_rules_owned", Help: "Alert rules whose lease this evaluator holds."}),
		gLive:    prometheus.NewGauge(prometheus.GaugeOpts{Name: "openlog_alert_evaluators_live", Help: "Live alert evaluators seen by this evaluator."}),
		cChanges: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "openlog_alert_lease_changes_total", Help: "Rule lease changes by kind."}, []string{"change"}),
	}
	if o.Registerer != nil {
		o.Registerer.MustRegister(m.gOwned, m.gLive, m.cChanges)
	}
	return m
}

// Tick runs one renew/rebalance round.
func (m *LeaseManager) Tick(ctx context.Context) error {
	live, err := m.store.Heartbeat(ctx, m.o.Instance, m.o.Version, m.o.TTL)
	if err != nil {
		return err
	}
	live = max(live, 1)
	renewed, err := m.store.Renew(ctx, m.o.Instance, m.o.TTL)
	if err != nil {
		// Leases are not extended: stop evaluating once they expire (Owned filters by LeaseUntil).
		return err
	}
	total, err := m.store.CountEnabled(ctx)
	if err != nil {
		return err
	}
	share := (total + live - 1) / live
	held := map[string]Lease{}
	for _, l := range renewed {
		held[l.RuleID] = l
	}
	switch {
	case len(held) > share:
		released, err := m.store.Release(ctx, m.o.Instance, len(held)-share)
		if err != nil {
			return err
		}
		for _, id := range released {
			delete(held, id)
		}
		m.cChanges.WithLabelValues("released").Add(float64(len(released)))
	case len(held) < share:
		claimed, err := m.store.Claim(ctx, m.o.Instance, share-len(held), m.o.TTL)
		if err != nil {
			return err
		}
		for _, l := range claimed {
			held[l.RuleID] = l
		}
		m.cChanges.WithLabelValues("claimed").Add(float64(len(claimed)))
	}
	m.mu.Lock()
	for id := range m.owned {
		if _, ok := held[id]; !ok {
			m.cChanges.WithLabelValues("lost").Inc()
		}
	}
	// Keep the locally known schedule of rules we already had (it is newer than the database row after a commit).
	for id, l := range held {
		if old, ok := m.owned[id]; ok && old.NextEvalAt.After(l.NextEvalAt) {
			l.NextEvalAt, l.LastEvalEnd = old.NextEvalAt, old.LastEvalEnd
			held[id] = l
		}
	}
	m.owned, m.live = held, live
	m.mu.Unlock()
	m.gOwned.Set(float64(len(held)))
	m.gLive.Set(float64(live))
	return nil
}

// Owned returns the leases held at now (unexpired), ordered by next evaluation.
func (m *LeaseManager) Owned(now time.Time) []Lease {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Lease, 0, len(m.owned))
	for _, l := range m.owned {
		if l.LeaseUntil.After(now) {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NextEvalAt.Before(out[j].NextEvalAt) })
	return out
}

// Scheduled records the next evaluation of a rule after an evaluation.
func (m *LeaseManager) Scheduled(ruleID string, next, lastEnd time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.owned[ruleID]; ok {
		l.NextEvalAt = next
		if !lastEnd.IsZero() {
			l.LastEvalEnd = lastEnd
		}
		m.owned[ruleID] = l
	}
}

// Drop forgets a lease (the store reported it lost).
func (m *LeaseManager) Drop(ruleID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.owned[ruleID]; ok {
		delete(m.owned, ruleID)
		m.cChanges.WithLabelValues("lost").Inc()
	}
}

// Run ticks every renew interval until ctx is done, then releases all leases.
func (m *LeaseManager) Run(ctx context.Context) {
	t := time.NewTicker(m.o.RenewInterval)
	defer t.Stop()
	failing := false
	for {
		tctx, cancel := context.WithTimeout(ctx, m.o.RenewInterval)
		err := m.Tick(tctx)
		cancel()
		switch {
		case err != nil && ctx.Err() == nil:
			if !failing {
				m.o.Log.Warn("alert lease round failed", "err", err)
			}
			failing = true
		case err == nil && failing:
			m.o.Log.Info("alert lease round recovered")
			failing = false
		}
		select {
		case <-ctx.Done():
			lctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := m.store.Leave(lctx, m.o.Instance); err != nil {
				m.o.Log.Warn("cannot release alert leases", "err", err)
			} else {
				m.o.Log.Info("released alert rule leases")
			}
			cancel()
			m.mu.Lock()
			m.owned = map[string]Lease{}
			m.mu.Unlock()
			return
		case <-t.C:
		}
	}
}
