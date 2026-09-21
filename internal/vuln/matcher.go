package vuln

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/store/clickhouse"
)

// The matcher is the leader task that turns the catalog and the inventory into findings: it reads the
// latest inventory snapshot of every host, matches each installed package against the advisories of that
// host's ecosystem, and writes one row per finding.
//
// It reads across tenants, like the APM linker, because it is a background job of the installation rather
// than an API read; every row it writes carries the tenant it belongs to, and everything that reads them
// back goes through the tenant-scoped query layer.

// MatcherOptions configure a Matcher.
type MatcherOptions struct {
	Database string
	Cluster  string
	// Interval is how often hosts are matched again (default 1h): the inventory changes when a package is
	// installed, and the catalog when the feed syncs, so neither is a per-minute affair.
	Interval time.Duration
	// SyncInterval is how often the feed is downloaded (default 12h).
	SyncInterval time.Duration
	// MaxHostsPerRun bounds one pass; the rest are taken by the next one.
	MaxHostsPerRun int
	Log            *slog.Logger
	Registerer     prometheus.Registerer
	Now            func() time.Time
}

// Matcher runs the feed sync and the matching.
type Matcher struct {
	conn    clickhouse.Conn
	catalog Catalog
	fetcher *Fetcher
	writer  FindingWriter
	o       MatcherOptions

	lastSync time.Time

	cFindings *prometheus.GaugeVec
	cRuns     *prometheus.CounterVec
	cDuration prometheus.Histogram
}

// FindingWriter stores the findings of one run.
type FindingWriter interface {
	WriteFindings(ctx context.Context, rows []Finding) error
}

// Finding is one row of host_vulnerabilities.
type Finding struct {
	Match
	FirstSeen  time.Time
	LastSeen   time.Time
	ResolvedAt time.Time
}

// NewMatcher creates a matcher; call Run.
func NewMatcher(conn clickhouse.Conn, catalog Catalog, fetcher *Fetcher, writer FindingWriter, o MatcherOptions) *Matcher {
	if o.Interval <= 0 {
		o.Interval = time.Hour
	}
	if o.SyncInterval <= 0 {
		o.SyncInterval = 12 * time.Hour
	}
	if o.MaxHostsPerRun <= 0 {
		o.MaxHostsPerRun = 5000
	}
	if o.Database == "" {
		o.Database = "openlog"
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	m := &Matcher{conn: conn, catalog: catalog, fetcher: fetcher, writer: writer, o: o,
		cFindings: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "openlog_vulnerability_findings",
			Help: "Open vulnerability findings of the last match run, by severity."}, []string{"severity"}),
		cRuns: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "openlog_vulnerability_match_runs_total",
			Help: "Vulnerability match runs by outcome."}, []string{"result"}),
		cDuration: prometheus.NewHistogram(prometheus.HistogramOpts{Name: "openlog_vulnerability_match_duration_seconds",
			Help: "Duration of a vulnerability match run.", Buckets: prometheus.DefBuckets}),
	}
	for _, s := range Severities {
		m.cFindings.WithLabelValues(s)
	}
	m.cRuns.WithLabelValues("ok")
	m.cRuns.WithLabelValues("error")
	if o.Registerer != nil {
		o.Registerer.MustRegister(m.cFindings, m.cRuns, m.cDuration)
	}
	return m
}

// Run matches every Interval until ctx is done.
func (m *Matcher) Run(ctx context.Context) {
	t := time.NewTicker(m.o.Interval)
	defer t.Stop()
	for {
		m.Once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Once syncs the feed when it is due and matches every host.
func (m *Matcher) Once(ctx context.Context) {
	start := m.o.Now()
	hosts, err := m.hosts(ctx)
	if err != nil {
		if ctx.Err() == nil {
			m.cRuns.WithLabelValues("error").Inc()
			m.o.Log.Warn("cannot read the inventory for vulnerability matching", "err", err)
		}
		return
	}
	if len(hosts) == 0 {
		return
	}
	ecosystems := HostEcosystems(hosts)
	// The feed is downloaded for the ecosystems the installation actually runs; a Debian-only installation
	// never pays for Alpine's export.
	if m.fetcher != nil && start.Sub(m.lastSync) >= m.o.SyncInterval {
		m.lastSync = start
		if _, err := Sync(ctx, m.fetcher, m.catalog, ecosystems, m.o.Log); err != nil {
			m.o.Log.Warn("the vulnerability feed did not sync completely; matching against what is stored", "err", err)
		}
	}
	rows, err := m.catalog.Affected(ctx, ecosystems)
	if err != nil {
		m.cRuns.WithLabelValues("error").Inc()
		m.o.Log.Warn("cannot read the vulnerability catalog", "err", err)
		return
	}
	if len(rows) == 0 {
		m.o.Log.Info("the vulnerability catalog is empty for these ecosystems; nothing to match",
			"ecosystems", ecosystems)
		return
	}
	index := NewIndex(rows)

	open, err := m.openFindings(ctx)
	if err != nil {
		m.o.Log.Warn("cannot read the open vulnerability findings; they will be rewritten as new", "err", err)
		open = map[string]Finding{}
	}

	now := m.o.Now().UTC()
	counts := map[string]int{}
	var out []Finding
	found := map[string]bool{}
	for _, h := range hosts {
		for _, match := range index.Match(h, now) {
			key := findingKey(match.TenantID, match.HostID, match.VulnID, match.Package)
			found[key] = true
			f := Finding{Match: match, FirstSeen: now, LastSeen: now}
			// A finding that was already open keeps the moment it was first seen: "since when" is the
			// question a person asks about a vulnerability they have not patched yet.
			if prev, ok := open[key]; ok && !prev.FirstSeen.IsZero() {
				f.FirstSeen = prev.FirstSeen
			}
			out = append(out, f)
			counts[match.Severity]++
		}
	}
	// Findings this run did not reproduce are resolved: the package was upgraded, the host stopped
	// reporting it, or the advisory was withdrawn.
	for key, prev := range open {
		if found[key] {
			continue
		}
		prev.ResolvedAt = now
		prev.LastSeen = now
		out = append(out, prev)
	}

	if err := m.writer.WriteFindings(ctx, out); err != nil {
		m.cRuns.WithLabelValues("error").Inc()
		m.o.Log.Warn("cannot write vulnerability findings", "rows", len(out), "err", err)
		return
	}
	for _, s := range Severities {
		m.cFindings.WithLabelValues(s).Set(float64(counts[s]))
	}
	m.cRuns.WithLabelValues("ok").Inc()
	m.cDuration.Observe(time.Since(start).Seconds())
	m.o.Log.Info("vulnerability match complete", "hosts", len(hosts), "findings", len(found),
		"resolved", len(out)-len(found), "advisories", index.Size(),
		"duration", time.Since(start).Round(time.Millisecond))
}

func findingKey(tenant, host, vulnID, pkg string) string {
	return tenant + "\x00" + host + "\x00" + vulnID + "\x00" + pkg
}

// hosts reads the latest inventory snapshot of every host and turns it into what the matcher needs: the
// host's ecosystem (from its operating system item) and its installed packages.
func (m *Matcher) hosts(ctx context.Context) ([]Host, error) {
	q := fmt.Sprintf(`SELECT i.tenant_id, i.host_id, i.category, i.data
		FROM %s.inventory_items i
		INNER JOIN (
			SELECT tenant_id, host_id, argMax(snapshot_id, snapshot_time) AS snapshot_id
			FROM %s.inventory_snapshots GROUP BY tenant_id, host_id
		) s ON s.tenant_id = i.tenant_id AND s.host_id = i.host_id AND s.snapshot_id = i.snapshot_id
		WHERE i.category IN ('package', 'os')
		ORDER BY i.tenant_id, i.host_id
		LIMIT %d`, m.o.Database, m.o.Database, maxInventoryRows)
	rows, err := m.conn.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	names, err := m.hostNames(ctx)
	if err != nil {
		// A missing name is cosmetic; the finding is still about a host id.
		names = map[string]string{}
	}

	var out []Host
	var cur *Host
	var curOS osItem
	for rows.Next() {
		var tenantID, hostID, category, data string
		if err := rows.Scan(&tenantID, &hostID, &category, &data); err != nil {
			return nil, err
		}
		if cur == nil || cur.TenantID != tenantID || cur.HostID != hostID {
			if cur != nil {
				out = append(out, finishHost(*cur, curOS))
			}
			cur = &Host{TenantID: tenantID, HostID: hostID, HostName: names[tenantID+"\x00"+hostID]}
			curOS = osItem{}
			if len(out) >= m.o.MaxHostsPerRun {
				break
			}
		}
		switch category {
		case "os":
			_ = json.Unmarshal([]byte(data), &curOS)
		case "package":
			var p InstalledPackage
			if err := json.Unmarshal([]byte(data), &p); err == nil && p.Name != "" {
				cur.Packages = append(cur.Packages, p)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if cur != nil {
		out = append(out, finishHost(*cur, curOS))
	}
	return out, nil
}

// maxInventoryRows bounds one pass over the inventory (hosts × packages).
const maxInventoryRows = 20_000_000

// osItem is the inventory's operating system item (agents/infra/internal/inventory).
type osItem struct {
	ID        string `json:"id"`
	VersionID string `json:"version_id"`
	Name      string `json:"name"`
	Version   string `json:"version"`
}

func finishHost(h Host, os osItem) Host {
	manager := ""
	if len(h.Packages) > 0 {
		manager = h.Packages[0].Manager
	}
	id := os.ID
	if id == "" {
		id = os.Name
	}
	version := os.VersionID
	if version == "" {
		version = os.Version
	}
	h.Ecosystem = EcosystemFor(manager, id, version)
	return h
}

func (m *Matcher) hostNames(ctx context.Context) (map[string]string, error) {
	rows, err := m.conn.Query(ctx, fmt.Sprintf(`SELECT tenant_id, host_id, argMax(host_name, last_seen)
		FROM %s.hosts GROUP BY tenant_id, host_id LIMIT %d`, m.o.Database, maxHostRows))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var tenantID, hostID, name string
		if err := rows.Scan(&tenantID, &hostID, &name); err != nil {
			return nil, err
		}
		out[tenantID+"\x00"+hostID] = name
	}
	return out, rows.Err()
}

const maxHostRows = 500_000

// openFindings reads the findings that are still open, so a run can keep their first-seen time and resolve
// the ones it no longer reproduces.
func (m *Matcher) openFindings(ctx context.Context) (map[string]Finding, error) {
	rows, err := m.conn.Query(ctx, fmt.Sprintf(`SELECT tenant_id, host_id, host_name, vuln_id, cve, severity,
		score, ecosystem, package, version, fixed_in, summary, first_seen
		FROM %s.host_vulnerabilities FINAL
		WHERE resolved_at = toDateTime(0) LIMIT %d`, m.o.Database, maxOpenFindings))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Finding{}
	for rows.Next() {
		var f Finding
		var score float32
		if err := rows.Scan(&f.TenantID, &f.HostID, &f.HostName, &f.VulnID, &f.CVE, &f.Severity, &score,
			&f.Ecosystem, &f.Package, &f.Version, &f.FixedIn, &f.Summary, &f.FirstSeen); err != nil {
			return nil, err
		}
		f.Score = float64(score)
		out[findingKey(f.TenantID, f.HostID, f.VulnID, f.Package)] = f
	}
	return out, rows.Err()
}

const maxOpenFindings = 5_000_000
