package usage

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/onuragtas/openlog/internal/store/clickhouse"
)

// Signal names of the usage tables (queue.Signal).
var Signals = []string{"traces", "logs", "metrics", "profiles"}

// SignalUsage is the usage of one signal over a range.
type SignalUsage struct {
	Signal string `json:"signal"`
	// Items are stored spans, log records or metric data points.
	Items uint64 `json:"items"`
	// Bytes is the estimated uncompressed size of the stored rows.
	Bytes uint64 `json:"bytes"`
	// IngestBytes are uncompressed OTLP protobuf bytes accepted by ingest and stored by the processor.
	IngestBytes    uint64 `json:"ingest_bytes"`
	IngestRequests uint64 `json:"ingest_requests"`
}

// QueryUsage is query compute of api/alert queries.
type QueryUsage struct {
	Queries         uint64  `json:"queries"`
	Failed          uint64  `json:"failed"`
	ReadRows        uint64  `json:"read_rows"`
	ReadBytes       uint64  `json:"read_bytes"`
	CPUSeconds      float64 `json:"cpu_seconds"`
	MemoryBytesPeak uint64  `json:"memory_bytes"`
}

// Totals is a tenant's usage over a range.
type Totals struct {
	Signals     []SignalUsage `json:"signals"`
	IngestBytes uint64        `json:"ingest_bytes"`
	// Distinct entities seen in the range.
	Hosts      uint64 `json:"hosts"`
	Containers uint64 `json:"containers"`
	Services   uint64 `json:"services"`
	// ActiveHosts is the larger distinct host count of the day of "to" and the day before (what the hosts limit is
	// evaluated against).
	ActiveHosts uint64     `json:"active_hosts"`
	Query       QueryUsage `json:"query"`
}

// Day is one day of usage.
type Day struct {
	Day         string        `json:"day"`
	Signals     []SignalUsage `json:"signals"`
	IngestBytes uint64        `json:"ingest_bytes"`
	Hosts       uint64        `json:"hosts"`
	Containers  uint64        `json:"containers"`
	Services    uint64        `json:"services"`
	Query       QueryUsage    `json:"query"`
}

// TopEntry is a service or host ranked by stored bytes.
type TopEntry struct {
	Key   string            `json:"key"`
	Items uint64            `json:"items"`
	Bytes uint64            `json:"bytes"`
	By    map[string]uint64 `json:"bytes_by_signal"`
}

// TenantPeriod is the usage the quota evaluator needs for every tenant.
type TenantPeriod struct {
	IngestBytes uint64
	ActiveHosts uint64
}

// Reader reads the usage tables. Every tenant query binds tenant_id as a server-side parameter.
type Reader struct {
	Conn     clickhouse.Conn
	Database string
}

var dbRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (r *Reader) table(name string) (string, error) {
	db := r.Database
	if db == "" {
		db = "openlog"
	}
	if !dbRe.MatchString(db) {
		return "", fmt.Errorf("invalid database %q", db)
	}
	return "`" + db + "`." + name, nil
}

const chTime = "2006-01-02 15:04:05"

func params(tenant string, from, to time.Time) ch.Parameters {
	last := to.Add(-time.Second)
	if last.Before(from) {
		last = from
	}
	return ch.Parameters{
		"tenant": tenant, "from": from.UTC().Format(chTime), "to": to.UTC().Format(chTime),
		"d1": from.UTC().Format(time.DateOnly), "d2": last.UTC().Format(time.DateOnly),
	}
}

func (r *Reader) query(ctx context.Context, p ch.Parameters, sql string, table string, scan func(rows scanner) error) error {
	t, err := r.table(table)
	if err != nil {
		return err
	}
	qctx := ch.Context(ctx, ch.WithParameters(p))
	rows, err := r.Conn.Query(qctx, fmt.Sprintf(sql, t))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

// Totals returns the tenant's usage in [from, to).
func (r *Reader) Totals(ctx context.Context, tenant string, from, to time.Time) (Totals, error) {
	p := params(tenant, from, to)
	sig := map[string]*SignalUsage{}
	get := func(s string) *SignalUsage {
		if sig[s] == nil {
			sig[s] = &SignalUsage{Signal: s}
		}
		return sig[s]
	}
	for _, s := range Signals {
		get(s)
	}
	var out Totals
	err := r.query(ctx, p, "SELECT signal, sum(items), sum(bytes) FROM %s WHERE tenant_id = {tenant:String} "+
		"AND hour >= {from:DateTime('UTC')} AND hour < {to:DateTime('UTC')} GROUP BY signal", "usage_signals_1h", func(rows scanner) error {
		var s string
		var items, bytes uint64
		if err := rows.Scan(&s, &items, &bytes); err != nil {
			return err
		}
		u := get(s)
		u.Items, u.Bytes = items, bytes
		return nil
	})
	if err != nil {
		return out, fmt.Errorf("usage signals: %w", err)
	}
	err = r.query(ctx, p, "SELECT signal, sum(requests), sum(bytes) FROM %s WHERE tenant_id = {tenant:String} "+
		"AND hour >= {from:DateTime('UTC')} AND hour < {to:DateTime('UTC')} GROUP BY signal", "usage_ingest_1h", func(rows scanner) error {
		var s string
		var reqs, bytes uint64
		if err := rows.Scan(&s, &reqs, &bytes); err != nil {
			return err
		}
		u := get(s)
		u.IngestRequests, u.IngestBytes = reqs, bytes
		out.IngestBytes += bytes
		return nil
	})
	if err != nil {
		return out, fmt.Errorf("usage ingest: %w", err)
	}
	p["a1"] = to.Add(-time.Second).AddDate(0, 0, -1).UTC().Format(time.DateOnly)
	err = r.query(ctx, p, "SELECT uniqExactIf(entity, kind = 'host'), uniqExactIf(entity, kind = 'container'), uniqExactIf(entity, kind = 'service'), "+
		"greatest(uniqExactIf(entity, kind = 'host' AND day = {d2:Date}), uniqExactIf(entity, kind = 'host' AND day = {a1:Date})) "+
		"FROM %s WHERE tenant_id = {tenant:String} AND day >= least({d1:Date}, {a1:Date}) AND day <= {d2:Date}", "usage_entities_1d", func(rows scanner) error {
		return rows.Scan(&out.Hosts, &out.Containers, &out.Services, &out.ActiveHosts)
	})
	if err != nil {
		return out, fmt.Errorf("usage entities: %w", err)
	}
	// The entity counts above include the day before "from" (for active hosts); recount the range itself.
	err = r.query(ctx, p, "SELECT uniqExactIf(entity, kind = 'host'), uniqExactIf(entity, kind = 'container'), uniqExactIf(entity, kind = 'service') "+
		"FROM %s WHERE tenant_id = {tenant:String} AND day >= {d1:Date} AND day <= {d2:Date}", "usage_entities_1d", func(rows scanner) error {
		return rows.Scan(&out.Hosts, &out.Containers, &out.Services)
	})
	if err != nil {
		return out, fmt.Errorf("usage entities: %w", err)
	}
	err = r.query(ctx, p, "SELECT sum(queries), sum(failed), sum(read_rows), sum(read_bytes), sum(cpu_microseconds), sum(memory_bytes) "+
		"FROM %s FINAL WHERE tenant_id = {tenant:String} AND hour >= {from:DateTime('UTC')} AND hour < {to:DateTime('UTC')}", "usage_queries_1h", func(rows scanner) error {
		var cpu uint64
		if err := rows.Scan(&out.Query.Queries, &out.Query.Failed, &out.Query.ReadRows, &out.Query.ReadBytes, &cpu, &out.Query.MemoryBytesPeak); err != nil {
			return err
		}
		out.Query.CPUSeconds = float64(cpu) / 1e6
		return nil
	})
	if err != nil {
		return out, fmt.Errorf("usage queries: %w", err)
	}
	for _, s := range Signals {
		out.Signals = append(out.Signals, *sig[s])
	}
	return out, nil
}

// Daily returns one entry per day of [from, to) that has any usage, ordered by day.
func (r *Reader) Daily(ctx context.Context, tenant string, from, to time.Time) ([]Day, error) {
	p := params(tenant, from, to)
	days := map[string]*Day{}
	get := func(d time.Time) *Day {
		k := d.UTC().Format(time.DateOnly)
		if days[k] == nil {
			days[k] = &Day{Day: k}
			for _, s := range Signals {
				days[k].Signals = append(days[k].Signals, SignalUsage{Signal: s})
			}
		}
		return days[k]
	}
	sigOf := func(d *Day, s string) *SignalUsage {
		for i := range d.Signals {
			if d.Signals[i].Signal == s {
				return &d.Signals[i]
			}
		}
		d.Signals = append(d.Signals, SignalUsage{Signal: s})
		return &d.Signals[len(d.Signals)-1]
	}
	err := r.query(ctx, p, "SELECT toDate(hour) AS d, signal, sum(items), sum(bytes) FROM %s WHERE tenant_id = {tenant:String} "+
		"AND hour >= {from:DateTime('UTC')} AND hour < {to:DateTime('UTC')} GROUP BY d, signal", "usage_signals_1h", func(rows scanner) error {
		var d time.Time
		var s string
		var items, bytes uint64
		if err := rows.Scan(&d, &s, &items, &bytes); err != nil {
			return err
		}
		u := sigOf(get(d), s)
		u.Items, u.Bytes = items, bytes
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("daily signals: %w", err)
	}
	err = r.query(ctx, p, "SELECT toDate(hour) AS d, signal, sum(requests), sum(bytes) FROM %s WHERE tenant_id = {tenant:String} "+
		"AND hour >= {from:DateTime('UTC')} AND hour < {to:DateTime('UTC')} GROUP BY d, signal", "usage_ingest_1h", func(rows scanner) error {
		var d time.Time
		var s string
		var reqs, bytes uint64
		if err := rows.Scan(&d, &s, &reqs, &bytes); err != nil {
			return err
		}
		day := get(d)
		u := sigOf(day, s)
		u.IngestRequests, u.IngestBytes = reqs, bytes
		day.IngestBytes += bytes
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("daily ingest: %w", err)
	}
	err = r.query(ctx, p, "SELECT day, uniqExactIf(entity, kind = 'host'), uniqExactIf(entity, kind = 'container'), uniqExactIf(entity, kind = 'service') "+
		"FROM %s WHERE tenant_id = {tenant:String} AND day >= {d1:Date} AND day <= {d2:Date} GROUP BY day", "usage_entities_1d", func(rows scanner) error {
		var d time.Time
		var h, c, s uint64
		if err := rows.Scan(&d, &h, &c, &s); err != nil {
			return err
		}
		day := get(d)
		day.Hosts, day.Containers, day.Services = h, c, s
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("daily entities: %w", err)
	}
	err = r.query(ctx, p, "SELECT toDate(hour) AS d, sum(queries), sum(failed), sum(read_rows), sum(read_bytes), sum(cpu_microseconds), sum(memory_bytes) "+
		"FROM %s FINAL WHERE tenant_id = {tenant:String} AND hour >= {from:DateTime('UTC')} AND hour < {to:DateTime('UTC')} GROUP BY d", "usage_queries_1h", func(rows scanner) error {
		var d time.Time
		var q QueryUsage
		var cpu uint64
		if err := rows.Scan(&d, &q.Queries, &q.Failed, &q.ReadRows, &q.ReadBytes, &cpu, &q.MemoryBytesPeak); err != nil {
			return err
		}
		q.CPUSeconds = float64(cpu) / 1e6
		get(d).Query = q
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("daily queries: %w", err)
	}
	out := make([]Day, 0, len(days))
	for _, d := range days {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out, nil
}

// TopDimensions are the columns Top can rank by.
var TopDimensions = map[string]string{"service": "service_name", "host": "host_id"}

// Top returns the services or hosts (dim) with the most stored bytes in [from, to).
func (r *Reader) Top(ctx context.Context, tenant, dim string, from, to time.Time, limit int) ([]TopEntry, error) {
	col, ok := TopDimensions[dim]
	if !ok {
		return nil, fmt.Errorf("unknown dimension %q", dim)
	}
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	p := params(tenant, from, to)
	out := []TopEntry{}
	// One sumIf per signal, derived from Signals rather than spelled out: the breakdown used to name the
	// three signals in the SQL and again in the scan, which is exactly the pair a fourth signal gets added
	// to only once.
	byCols := make([]string, len(Signals))
	for i, s := range Signals {
		byCols[i] = "sumIf(bytes, signal = '" + s + "')"
	}
	err := r.query(ctx, p, "SELECT "+col+" AS k, sum(items) AS n, sum(bytes) AS b, "+strings.Join(byCols, ", ")+
		" FROM %s WHERE tenant_id = {tenant:String} AND hour >= {from:DateTime('UTC')} AND hour < {to:DateTime('UTC')} "+
		"AND "+col+" != '' GROUP BY k ORDER BY b DESC, k LIMIT "+strconv.Itoa(limit), "usage_signals_1h", func(rows scanner) error {
		var e TopEntry
		by := make([]uint64, len(Signals))
		dest := make([]any, 0, 3+len(by))
		dest = append(dest, &e.Key, &e.Items, &e.Bytes)
		for i := range by {
			dest = append(dest, &by[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		e.By = make(map[string]uint64, len(Signals))
		for i, s := range Signals {
			e.By[s] = by[i]
		}
		out = append(out, e)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("usage top %s: %w", dim, err)
	}
	return out, nil
}

// SignalBytesSince returns the stored bytes per signal of rows with an hour >= since per signal (the data still
// inside each signal's retention). since maps signal -> start.
func (r *Reader) SignalBytesSince(ctx context.Context, tenant string, since map[string]time.Time, to time.Time) (map[string]uint64, error) {
	out := map[string]uint64{}
	for _, s := range Signals {
		from, ok := since[s]
		if !ok {
			continue
		}
		p := params(tenant, from, to)
		p["signal"] = s
		err := r.query(ctx, p, "SELECT sum(bytes) FROM %s WHERE tenant_id = {tenant:String} AND signal = {signal:String} "+
			"AND hour >= {from:DateTime('UTC')} AND hour < {to:DateTime('UTC')}", "usage_signals_1h", func(rows scanner) error {
			var b uint64
			if err := rows.Scan(&b); err != nil {
				return err
			}
			out[s] = b
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("stored bytes: %w", err)
		}
	}
	return out, nil
}

// CompressionRatios returns, per signal, compressed/uncompressed bytes of the active parts of its raw table on the
// server answering the query (an estimate for all shards).
func (r *Reader) CompressionRatios(ctx context.Context) (map[string]float64, error) {
	tables := map[string]string{"spans_local": "traces", "logs_local": "logs", "metrics_local": "metrics"}
	db := r.Database
	if db == "" {
		db = "openlog"
	}
	qctx := ch.Context(ctx, ch.WithParameters(ch.Parameters{"db": db}))
	rows, err := r.Conn.Query(qctx, "SELECT table, sum(data_compressed_bytes), sum(data_uncompressed_bytes) FROM system.parts "+
		"WHERE database = {db:String} AND active AND table IN ('spans_local', 'logs_local', 'metrics_local') GROUP BY table")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var t string
		var c, u uint64
		if err := rows.Scan(&t, &c, &u); err != nil {
			return nil, err
		}
		if u > 0 {
			out[tables[t]] = float64(c) / float64(u)
		}
	}
	return out, rows.Err()
}

// AllTenants returns, for every tenant with usage, the period's ingest bytes in [from, to) and active hosts at to.
func (r *Reader) AllTenants(ctx context.Context, from, to time.Time) (map[string]TenantPeriod, error) {
	p := params("", from, to)
	p["a1"] = to.Add(-time.Second).AddDate(0, 0, -1).UTC().Format(time.DateOnly)
	out := map[string]TenantPeriod{}
	err := r.query(ctx, p, "SELECT tenant_id, sum(bytes) FROM %s WHERE hour >= {from:DateTime('UTC')} AND hour < {to:DateTime('UTC')} GROUP BY tenant_id",
		"usage_ingest_1h", func(rows scanner) error {
			var t string
			var b uint64
			if err := rows.Scan(&t, &b); err != nil {
				return err
			}
			tp := out[t]
			tp.IngestBytes = b
			out[t] = tp
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("usage ingest: %w", err)
	}
	err = r.query(ctx, p, "SELECT tenant_id, greatest(uniqExactIf(entity, day = {d2:Date}), uniqExactIf(entity, day = {a1:Date})) FROM %s "+
		"WHERE kind = 'host' AND day >= {a1:Date} AND day <= {d2:Date} GROUP BY tenant_id", "usage_entities_1d", func(rows scanner) error {
		var t string
		var h uint64
		if err := rows.Scan(&t, &h); err != nil {
			return err
		}
		tp := out[t]
		tp.ActiveHosts = h
		out[t] = tp
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("usage hosts: %w", err)
	}
	return out, nil
}

// DayTotals is one tenant's usage of one day (billing push).
type DayTotals struct {
	IngestBytes uint64
	Hosts       uint64
}

// DayAllTenants returns every tenant's ingest bytes and distinct hosts of the UTC day of day.
func (r *Reader) DayAllTenants(ctx context.Context, day time.Time) (map[string]DayTotals, error) {
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	p := params("", start, start.AddDate(0, 0, 1))
	out := map[string]DayTotals{}
	err := r.query(ctx, p, "SELECT tenant_id, sum(bytes) FROM %s WHERE hour >= {from:DateTime('UTC')} AND hour < {to:DateTime('UTC')} GROUP BY tenant_id",
		"usage_ingest_1h", func(rows scanner) error {
			var t string
			var b uint64
			if err := rows.Scan(&t, &b); err != nil {
				return err
			}
			d := out[t]
			d.IngestBytes = b
			out[t] = d
			return nil
		})
	if err != nil {
		return nil, err
	}
	err = r.query(ctx, p, "SELECT tenant_id, uniqExact(entity) FROM %s WHERE kind = 'host' AND day = {d1:Date} GROUP BY tenant_id",
		"usage_entities_1d", func(rows scanner) error {
			var t string
			var h uint64
			if err := rows.Scan(&t, &h); err != nil {
				return err
			}
			d := out[t]
			d.Hosts = h
			out[t] = d
			return nil
		})
	return out, err
}
