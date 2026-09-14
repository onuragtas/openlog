package apm

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
)

// Late-span re-link (apm.md §6): the processor enqueues the client minutes of spans that arrive later than
// OPENLOG_APM_RELINK_AFTER into apm_relink_queue; the leader re-links them every RelinkInterval.
const (
	// relinkSettle: only queue rows at least this old are read, so rows still in flight (processor clock skew,
	// replication) are not skipped once their minute is marked done.
	relinkSettle = 2 * time.Minute
	// relinkOverlap re-reads rows this far before the watermark (rows that became visible late).
	relinkOverlap = 10 * time.Minute
	// relinkRescan is the queue range read after a start or leader change (re-linking twice is harmless).
	relinkRescan = time.Hour
	// relinkMaxRows bounds the distinct minutes read per pass.
	relinkMaxRows = 100000
)

// LinkMinutes returns the client minutes [first, last] (inclusive, minute starts) whose trace-linked edges a span can
// change, as computed by LinkSQL for a window of those minutes: the span's own minute for a client/producer span of a
// service with sample weight > 0; for a server/consumer span of a service with a parent, every client minute m whose
// child range [m, m + 1m + childSlack) contains the span. ok is false for spans that never take part in linking.
func LinkMinutes(kind, service, parentSpanID string, sampleWeight float64, ts time.Time) (first, last time.Time, ok bool) {
	if service == "" {
		return time.Time{}, time.Time{}, false
	}
	ts = ts.UTC()
	switch kind {
	case KindClient, KindProducer:
		if sampleWeight <= 0 {
			return time.Time{}, time.Time{}, false
		}
		m := ts.Truncate(time.Minute)
		return m, m, true
	case KindServer, KindConsumer:
		if parentSpanID == "" {
			return time.Time{}, time.Time{}, false
		}
		return ts.Add(-childSlack - time.Minute).Truncate(time.Minute).Add(time.Minute), ts.Truncate(time.Minute), true
	}
	return time.Time{}, time.Time{}, false
}

// CoalesceMinutes groups sorted, distinct minute starts into windows [start, end) of adjacent minutes, each at most
// maxLen long (0 = unbounded).
func CoalesceMinutes(minutes []time.Time, maxLen time.Duration) [][2]time.Time {
	var out [][2]time.Time
	for _, m := range minutes {
		if n := len(out); n > 0 && out[n-1][1].Equal(m) && (maxLen <= 0 || m.Add(time.Minute).Sub(out[n-1][0]) <= maxLen) {
			out[n-1][1] = m.Add(time.Minute)
			continue
		}
		out = append(out, [2]time.Time{m, m.Add(time.Minute)})
	}
	return out
}

// QueuedMinute is one minute of apm_relink_queue with the newest enqueued_at of its rows in the read range.
type QueuedMinute struct {
	Minute time.Time
	Last   time.Time
}

// relinkState is the leader's in-memory progress over the queue. It is reset when Run starts (leader change).
type relinkState struct {
	wm   time.Time               // rows with enqueued_at <= wm - relinkOverlap are not read again
	done map[time.Time]time.Time // minute -> read bound (until) of the pass that re-linked it
}

func newRelinkState(now time.Time) *relinkState {
	return &relinkState{wm: now.Add(-relinkRescan), done: map[time.Time]time.Time{}}
}

// since is the lower bound (exclusive) of the next read.
func (s *relinkState) since() time.Time { return s.wm.Add(-relinkOverlap) }

// plan returns the minutes to re-link now (oldest first, at most max) out of the rows read, and the pending ones
// left for the next pass. A minute is pending when it has a row newer than the bound of the pass that re-linked it.
func (s *relinkState) plan(rows []QueuedMinute, max int) (batch, rest []QueuedMinute) {
	var pending []QueuedMinute
	for _, r := range rows {
		if d, ok := s.done[r.Minute]; ok && !r.Last.After(d) {
			continue
		}
		pending = append(pending, r)
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].Minute.Before(pending[j].Minute) })
	if max > 0 && len(pending) > max {
		return pending[:max], pending[max:]
	}
	return pending, nil
}

// finish records the minutes re-linked by a pass that read rows up to until and moves the watermark: to until, or
// just before the oldest row of a minute still pending so that the next pass reads it again.
func (s *relinkState) finish(done []time.Time, pending []QueuedMinute, until time.Time) {
	for _, m := range done {
		s.done[m] = until
	}
	wm := until
	for _, p := range pending {
		if b := p.Last.Add(-time.Millisecond); b.Before(wm) {
			wm = b
		}
	}
	if wm.After(s.wm) {
		s.wm = wm
	}
	since := s.since()
	for m, d := range s.done {
		if !d.After(since) {
			delete(s.done, m)
		}
	}
}

// RelinkQueueSQL reads the distinct queued minutes (exported for tests and documentation).
func RelinkQueueSQL(database string) string {
	return "SELECT minute, max(enqueued_at) AS last FROM `" + database + "`.apm_relink_queue " +
		"WHERE enqueued_at > fromUnixTimestamp64Milli({since:Int64}) AND enqueued_at <= fromUnixTimestamp64Milli({until:Int64}) " +
		"AND minute >= toDateTime({oldest:Int64}, 'UTC') AND minute < toDateTime({before:Int64}, 'UTC') " +
		"GROUP BY minute ORDER BY minute LIMIT " + strconv.Itoa(relinkMaxRows)
}

// RelinkResult summarizes one re-link pass.
type RelinkResult struct {
	Minutes   int     // minutes re-linked
	Pending   int     // minutes left for the next pass (OPENLOG_APM_RELINK_MAX_MINUTES, failed windows)
	LateCalls float64 // weighted calls linked that were not linked before
}

// maybeRelink starts a re-link pass in the background every RelinkInterval unless the catch-up pass (or a previous
// re-link pass) is running; both are idempotent, but they would recompute the same minutes concurrently.
func (l *Linker) maybeRelink(ctx context.Context, wg *sync.WaitGroup) {
	if !l.opts.Relink {
		return
	}
	now := l.now()
	l.mu.Lock()
	due := now.Sub(l.relinkStarted) >= l.opts.RelinkInterval
	l.mu.Unlock()
	if !due {
		return
	}
	if !l.background.CompareAndSwap(bgNone, bgRelink) {
		l.relinkRuns.WithLabelValues("skipped").Inc()
		return
	}
	l.mu.Lock()
	l.relinkStarted = now
	l.mu.Unlock()
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer l.background.Store(bgNone)
		res, err := l.Relink(ctx)
		if err != nil && ctx.Err() == nil {
			l.log.Warn("apm late-span re-link failed", "minutes", res.Minutes, "pending", res.Pending, "err", err)
			return
		}
		if res.Minutes > 0 {
			l.log.Info("apm late-span re-link done", "minutes", res.Minutes, "pending", res.Pending, "late_calls", res.LateCalls)
		}
	}()
}

// Relink runs one pass: it reads the minutes enqueued since the watermark that are older than the regular window
// (and not older than RelinkMaxAge), re-links at most RelinkMaxMinutes of them (oldest first) on every shard in
// windows of adjacent minutes (at most CatchUpBatch long, max_threads=2) and advances the watermark.
func (l *Linker) Relink(ctx context.Context) (RelinkResult, error) {
	var res RelinkResult
	began := time.Now()
	now := l.now()
	l.mu.Lock()
	if l.relink == nil {
		l.relink = newRelinkState(now)
	}
	st := l.relink
	l.mu.Unlock()

	until := now.Add(-relinkSettle).Truncate(time.Millisecond)
	before, _ := l.Window(now)
	oldest := now.Add(-l.opts.RelinkMaxAge).UTC().Truncate(time.Minute)
	rows, err := l.queuedMinutes(ctx, st.since(), until, oldest, before)
	if err != nil {
		l.relinkRuns.WithLabelValues("error").Inc()
		return res, fmt.Errorf("read relink queue: %w", err)
	}
	batch, rest := st.plan(rows, l.opts.RelinkMaxMinutes)
	minutes := make([]time.Time, len(batch))
	for i, b := range batch {
		minutes[i] = b.Minute
	}
	var (
		done []time.Time
		errs []error
	)
	for _, w := range CoalesceMinutes(minutes, l.opts.CatchUpBatch) {
		_, late, werr := l.runWindow(ctx, w[0], w[1], true)
		res.LateCalls += late
		if werr != nil {
			errs = append(errs, fmt.Errorf("window %s: %w", w[0].Format(time.RFC3339), werr))
			if ctx.Err() != nil {
				break
			}
			continue
		}
		for m := w[0]; m.Before(w[1]); m = m.Add(time.Minute) {
			done = append(done, m)
		}
	}
	doneSet := make(map[time.Time]bool, len(done))
	for _, m := range done {
		doneSet[m] = true
	}
	for _, b := range batch {
		if !doneSet[b.Minute] {
			rest = append(rest, b)
		}
	}
	l.mu.Lock()
	st.finish(done, rest, until)
	l.mu.Unlock()

	res.Minutes, res.Pending = len(done), len(rest)
	l.relinkMinutes.Add(float64(res.Minutes))
	l.relinkLate.Add(res.LateCalls)
	l.relinkBacklog.Set(float64(res.Pending))
	l.relinkSeconds.Observe(time.Since(began).Seconds())
	if err := errors.Join(errs...); err != nil {
		l.relinkRuns.WithLabelValues("error").Inc()
		return res, err
	}
	l.relinkRuns.WithLabelValues("ok").Inc()
	return res, nil
}

func (l *Linker) queuedMinutes(ctx context.Context, since, until, oldest, before time.Time) ([]QueuedMinute, error) {
	qctx := ch.Context(ctx, ch.WithParameters(ch.Parameters{
		"since":  strconv.FormatInt(since.UnixMilli(), 10),
		"until":  strconv.FormatInt(until.UnixMilli(), 10),
		"oldest": strconv.FormatInt(oldest.Unix(), 10),
		"before": strconv.FormatInt(before.Unix(), 10),
	}))
	rows, err := l.bootstrap.Query(qctx, RelinkQueueSQL(l.opts.Database))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueuedMinute
	for rows.Next() {
		var q QueuedMinute
		if err := rows.Scan(&q.Minute, &q.Last); err != nil {
			return nil, err
		}
		q.Minute, q.Last = q.Minute.UTC(), q.Last.UTC()
		out = append(out, q)
	}
	return out, rows.Err()
}
