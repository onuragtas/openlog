package processor

import (
	"sort"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/apm"
)

// TableRelinkQueue is the late-span re-link queue (docs/contracts/apm.md §6, schema 0020).
const TableRelinkQueue = "apm_relink_queue"

// RelinkOptions configure enqueuing late spans for re-linking (OPENLOG_APM_RELINK_*).
type RelinkOptions struct {
	Enabled bool
	// After: client minutes older than this when their spans are converted are enqueued (the regular link runs
	// cover newer minutes). Keep it <= OPENLOG_APM_LINK_LOOKBACK of the api.
	After time.Duration
	// MaxAge: spans older than this are not enqueued, only counted.
	MaxAge time.Duration
}

// RelinkQueueRow is one queued client minute of a tenant.
type RelinkQueueRow struct {
	TenantID   string
	Minute     time.Time
	TraceID    string // first late trace of the minute in the chunk (sharding key, like spans)
	Spans      uint32 // late spans of the chunk that enqueued the minute
	EnqueuedAt time.Time
}

// Values implements row.
func (r *RelinkQueueRow) Values() []any {
	return []any{r.TenantID, r.Minute, r.TraceID, r.Spans, r.EnqueuedAt}
}

type relinkEnqueuer struct {
	opts     RelinkOptions
	enqueued prometheus.Counter
	tooOld   prometheus.Counter
}

func newRelinkEnqueuer() *relinkEnqueuer {
	return &relinkEnqueuer{
		enqueued: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "openlog_apm_relink_enqueued_total", Help: "Late (tenant, client minute) rows built for the APM re-link queue.",
		}),
		tooOld: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "openlog_apm_relink_too_old_total", Help: "Late spans relevant for edge linking older than OPENLOG_APM_RELINK_MAX_AGE (not enqueued).",
		}),
	}
}

// SetRelink enables enqueuing late spans for re-linking. Must be called before Run.
func (p *Processor) SetRelink(o RelinkOptions) { p.relink.opts = o }

// RelinkRows returns the queue rows for the spans of one chunk converted at now: per (tenant, client minute) of
// spans relevant for linking (apm.LinkMinutes) with minute < now - After and span timestamp >= now - MaxAge, sorted
// by tenant and minute. tooOld counts relevant late spans older than MaxAge.
func RelinkRows(spans []SpanRow, now time.Time, o RelinkOptions) (rows []RelinkQueueRow, tooOld int) {
	if !o.Enabled || o.After <= 0 {
		return nil, 0
	}
	now = now.UTC()
	threshold := now.Add(-o.After)
	type key struct {
		tenant string
		minute int64
	}
	idx := map[key]int{}
	for i := range spans {
		s := &spans[i]
		first, last, ok := apm.LinkMinutes(s.Kind, s.ServiceName, s.ParentSpanID, s.APM.SampleWeight, s.Timestamp)
		if !ok || !first.Before(threshold) {
			continue
		}
		if o.MaxAge > 0 && s.Timestamp.Before(now.Add(-o.MaxAge)) {
			tooOld++
			continue
		}
		for m := first; !m.After(last) && m.Before(threshold); m = m.Add(time.Minute) {
			k := key{s.TenantID, m.Unix()}
			if j, ok := idx[k]; ok {
				rows[j].Spans++
				continue
			}
			idx[k] = len(rows)
			rows = append(rows, RelinkQueueRow{TenantID: s.TenantID, Minute: m, TraceID: s.TraceID, Spans: 1, EnqueuedAt: now.Truncate(time.Millisecond)})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].TenantID != rows[j].TenantID {
			return rows[i].TenantID < rows[j].TenantID
		}
		return rows[i].Minute.Before(rows[j].Minute)
	})
	return rows, tooOld
}

// enqueueLate adds the queue rows of a decoded chunk.
func (p *Processor) enqueueLate(rows *Rows) {
	q, tooOld := RelinkRows(rows.Spans, p.now(), p.relink.opts)
	rows.RelinkQueue = q
	p.relink.enqueued.Add(float64(len(q)))
	p.relink.tooOld.Add(float64(tooOld))
}
