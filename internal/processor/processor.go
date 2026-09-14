// Package processor consumes OTLP export requests from Kafka, converts them
// into ClickHouse rows and writes them in deduplicated batches. Offsets are
// committed only after every row derived from the committed records is inserted.
//
// Batching (docs/contracts/kafka.md, "Consumption semantics"): records are grouped
// per partition into chunks whose boundaries are, in the normal case, a function
// of the records alone (record-timestamp window, byte size). A chunk is inserted
// table by table with insert_deduplication_token
// <topic>:<partition>:<first>-<last>:<table>, so a range that is re-delivered after
// a crash or rebalance produces the same chunks and tokens and ClickHouse drops the
// repeated blocks.
package processor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/twmb/franz-go/pkg/kgo"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/queue"
)

// Writer inserts rows into a table using a deduplication token.
type Writer interface {
	Write(ctx context.Context, table, token string, columns []string, rows [][]any) error
}

// Consumer is the subset of *kgo.Client used by the processor.
type Consumer interface {
	PollRecords(ctx context.Context, maxPollRecords int) kgo.Fetches
	CommitRecords(ctx context.Context, rs ...*kgo.Record) error
	AllowRebalance()
}

const maxPollRecords = 10000

// Chunk cut reasons (openlog_processor_chunk_cuts_total{reason}). window and bytes
// are deterministic; idle, memory and shutdown depend on timing.
const (
	cutWindow   = "window"
	cutBytes    = "bytes"
	cutIdle     = "idle"
	cutMemory   = "memory"
	cutShutdown = "shutdown"
)

type topicPartition struct {
	topic     string
	partition int32
}

func (tp topicPartition) less(o topicPartition) bool {
	if tp.topic != o.topic {
		return tp.topic < o.topic
	}
	return tp.partition < o.partition
}

// chunk is a contiguous run of records of one partition.
type chunk struct {
	tp     topicPartition
	window int64
	recs   []*kgo.Record
	bytes  int64
	opened time.Time
	done   bool // all tables inserted (set by the flush worker)
}

func (c *chunk) last() *kgo.Record { return c.recs[len(c.recs)-1] }

// token is insert_deduplication_token for table per kafka.md.
func (c *chunk) token(table string) string {
	return c.tp.topic + ":" + strconv.Itoa(int(c.tp.partition)) + ":" +
		strconv.FormatInt(c.recs[0].Offset, 10) + "-" + strconv.FormatInt(c.last().Offset, 10) + ":" + table
}

type partState struct {
	open   *chunk
	closed []*chunk
	// lastOffset is the offset of the last record added; hwm the partition high
	// watermark from the latest fetch that returned the partition.
	lastOffset int64
	hwm        int64
	// lastCommitTS is the timestamp of the last committed record.
	lastCommitTS time.Time
}

func (ps *partState) caughtUp() bool { return ps.hwm <= ps.lastOffset+1 }

// PartitionEvents collects partitions revoked from or lost by this group member.
// Create it before the Kafka client, pass KafkaOpts to the client and the events
// to Processor.SetPartitionEvents. With BlockRebalanceOnPoll the callbacks run
// only after AllowRebalance and before the next poll returns, so the processor
// applies them before it touches the next records.
type PartitionEvents struct {
	mu   sync.Mutex
	gone []topicPartition
}

// NewPartitionEvents creates an empty event collector.
func NewPartitionEvents() *PartitionEvents { return &PartitionEvents{} }

// KafkaOpts registers the revoke and lost callbacks.
func (e *PartitionEvents) KafkaOpts() []kgo.Opt {
	fn := func(_ context.Context, _ *kgo.Client, m map[string][]int32) { e.Add(m) }
	return []kgo.Opt{kgo.OnPartitionsRevoked(fn), kgo.OnPartitionsLost(fn)}
}

// Add records partitions that this member no longer owns.
func (e *PartitionEvents) Add(m map[string][]int32) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for t, ps := range m {
		for _, p := range ps {
			e.gone = append(e.gone, topicPartition{t, p})
		}
	}
}

func (e *PartitionEvents) take() []topicPartition {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.gone
	e.gone = nil
	return out
}

// Processor is the consumer loop.
type Processor struct {
	cfg      config.Processor
	prefix   string
	consumer Consumer
	writer   Writer
	log      *slog.Logger
	events   *PartitionEvents
	now      func() time.Time

	parts    map[topicPartition]*partState
	buffered int64 // record bytes in open and closed chunks
	closedN  int

	oldestMu sync.Mutex
	oldest   map[topicPartition]time.Time // timestamp of the oldest unprocessed record

	rejected   *prometheus.CounterVec
	dropped    *prometheus.CounterVec
	inserted   *prometheus.CounterVec
	flushDur   prometheus.Histogram
	failures   prometheus.Counter
	cuts       *prometheus.CounterVec
	lagRecords *prometheus.GaugeVec
	relink     *relinkEnqueuer // relink.go
}

// New creates a processor. reg may be nil.
func New(cfg config.Processor, topicPrefix string, consumer Consumer, writer Writer, log *slog.Logger, reg prometheus.Registerer) *Processor {
	if cfg.BatchBytes <= 0 {
		cfg.BatchBytes = 8 << 20
	}
	if cfg.MaxBufferedBytes < cfg.BatchBytes {
		cfg.MaxBufferedBytes = max(128<<20, cfg.BatchBytes)
	}
	if cfg.InsertConcurrency <= 0 {
		cfg.InsertConcurrency = 4
	}
	p := &Processor{
		cfg: cfg, prefix: topicPrefix, consumer: consumer, writer: writer, log: log, now: time.Now,
		parts: map[topicPartition]*partState{}, oldest: map[topicPartition]time.Time{},
		rejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_processor_records_rejected_total", Help: "Kafka records skipped because they could not be processed.",
		}, []string{"reason"}),
		dropped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_processor_items_dropped_total", Help: "Individual telemetry items dropped during conversion.",
		}, []string{"reason"}),
		inserted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_processor_rows_inserted_total", Help: "Rows inserted into ClickHouse by table.",
		}, []string{"table"}),
		flushDur: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "openlog_processor_flush_duration_seconds", Help: "Time to insert all closed chunks of one flush round.",
			Buckets: prometheus.ExponentialBuckets(0.005, 2, 14),
		}),
		failures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "openlog_processor_insert_failures_total", Help: "Failed insert attempts (retried).",
		}),
		cuts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_processor_chunk_cuts_total",
			Help: "Per-partition chunks closed, by reason. window and bytes are deterministic; idle, memory and shutdown cuts can change dedup tokens on re-delivery.",
		}, []string{"reason"}),
		lagRecords: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "openlog_processor_consumer_lag_records", Help: "Log end offset minus committed offset for partitions assigned to this processor.",
		}, []string{"topic", "partition"}),
		relink: newRelinkEnqueuer(),
	}
	if reg != nil {
		reg.MustRegister(p.relink.enqueued, p.relink.tooOld)
		reg.MustRegister(p.rejected, p.dropped, p.inserted, p.flushDur, p.failures, p.cuts, p.lagRecords, &lagSecondsCollector{p: p,
			desc: prometheus.NewDesc("openlog_processor_consumer_lag_seconds",
				"Age of the oldest record not yet inserted and committed by this processor (record timestamp), max over its partitions.",
				[]string{"topic"}, nil)})
	}
	return p
}

// SetPartitionEvents makes the processor drop buffered records of partitions it
// loses in a rebalance. Must be called before Run.
func (p *Processor) SetPartitionEvents(e *PartitionEvents) { p.events = e }

func (p *Processor) signalOf(topic string) (queue.Signal, bool) {
	if topic == queue.SampledTracesTopic(p.prefix) { // tail sampling enabled (D-075)
		return queue.SignalTraces, true
	}
	for _, s := range queue.AllSignals {
		if queue.Topic(p.prefix, s) == topic {
			return s, true
		}
	}
	return "", false
}

func (p *Processor) window(ts time.Time) int64 {
	if ts.IsZero() {
		return 0
	}
	return ts.UnixNano() / int64(p.cfg.FlushInterval)
}

func (p *Processor) windowEnd(w int64) time.Time {
	return time.Unix(0, (w+1)*int64(p.cfg.FlushInterval))
}

func (p *Processor) grace() time.Duration { return p.cfg.FlushInterval / 2 }

func (p *Processor) part(tp topicPartition) *partState {
	ps := p.parts[tp]
	if ps == nil {
		ps = &partState{lastOffset: -1}
		p.parts[tp] = ps
	}
	return ps
}

// add appends rec to its partition's open chunk. A chunk is closed before a
// record of a later timestamp window and after it reaches BatchBytes; both rules
// depend only on the record sequence.
func (p *Processor) add(rec *kgo.Record) {
	tp := topicPartition{rec.Topic, rec.Partition}
	ps := p.part(tp)
	if rec.Offset <= ps.lastOffset {
		return // already buffered
	}
	ps.lastOffset = rec.Offset
	w := p.window(rec.Timestamp)
	// Only a later window closes the chunk; a slightly late record (e.g. from an
	// ingest pod with a lagging clock) joins the open chunk.
	if ps.open != nil && w > ps.open.window {
		p.close(ps, cutWindow)
	}
	if ps.open == nil {
		ps.open = &chunk{tp: tp, window: w, opened: p.now()}
		if len(ps.closed) == 0 {
			p.setOldest(tp, rec.Timestamp)
		}
	}
	size := int64(len(rec.Value))
	ps.open.recs = append(ps.open.recs, rec)
	ps.open.bytes += size
	p.buffered += size
	if ps.open.bytes >= p.cfg.BatchBytes {
		p.close(ps, cutBytes)
	}
}

func (p *Processor) close(ps *partState, reason string) {
	ps.closed = append(ps.closed, ps.open)
	ps.open = nil
	p.closedN++
	p.cuts.WithLabelValues(reason).Inc()
}

func (p *Processor) closeAll(reason string) {
	for _, ps := range p.parts {
		if ps.open != nil {
			p.close(ps, reason)
		}
	}
}

// closeIdle closes open chunks of partitions that are caught up once their window
// (plus a grace period for records still in flight) has passed. This is the only
// timing-dependent cut in steady state: it yields a different chunk on re-delivery
// only if a record of the same window reaches the partition after the grace period.
func (p *Processor) closeIdle(now time.Time) {
	for _, ps := range p.parts {
		if c := ps.open; c != nil && ps.caughtUp() && p.idleDue(c, now) {
			p.close(ps, cutIdle)
		}
	}
}

func (p *Processor) idleDue(c *chunk, now time.Time) bool {
	// The second bound covers record timestamps far ahead of this host's clock.
	return !now.Before(p.windowEnd(c.window).Add(p.grace())) || now.Sub(c.opened) >= 2*p.cfg.FlushInterval+p.grace()
}

func (p *Processor) pollTimeout() time.Duration {
	d := p.cfg.FlushInterval
	now := p.now()
	for _, ps := range p.parts {
		if c := ps.open; c != nil && ps.caughtUp() {
			d = min(d, p.windowEnd(c.window).Add(p.grace()).Sub(now), c.opened.Add(2*p.cfg.FlushInterval+p.grace()).Sub(now))
		}
	}
	return max(d, 10*time.Millisecond)
}

// applyRevocations drops everything buffered for partitions this member lost.
// Nothing of it was committed; the new owner re-reads from the committed offset
// and cuts the same chunks.
func (p *Processor) applyRevocations() {
	if p.events == nil {
		return
	}
	for _, tp := range p.events.take() {
		ps := p.parts[tp]
		if ps == nil {
			continue
		}
		var n int
		chunks := ps.closed
		if ps.open != nil {
			chunks = append(chunks, ps.open)
		}
		for _, c := range chunks {
			p.buffered -= c.bytes
			n += len(c.recs)
		}
		p.closedN -= len(ps.closed)
		delete(p.parts, tp)
		p.setOldest(tp, time.Time{})
		p.lagRecords.DeleteLabelValues(tp.topic, strconv.Itoa(int(tp.partition)))
		if n > 0 {
			p.log.Info("partition revoked, dropped uncommitted buffered records", "topic", tp.topic, "partition", tp.partition, "records", n)
		}
	}
}

func (p *Processor) setOldest(tp topicPartition, ts time.Time) {
	p.oldestMu.Lock()
	defer p.oldestMu.Unlock()
	if ts.IsZero() {
		delete(p.oldest, tp)
	} else {
		p.oldest[tp] = ts
	}
}

// refreshOldest recomputes the oldest unprocessed record of a partition after a commit.
func (p *Processor) refreshOldest(tp topicPartition, ps *partState) {
	switch {
	case len(ps.closed) > 0:
		p.setOldest(tp, ps.closed[0].recs[0].Timestamp)
	case ps.open != nil:
		p.setOldest(tp, ps.open.recs[0].Timestamp)
	case !ps.caughtUp():
		// Behind, but nothing buffered: the next record is at most as old as the
		// last committed one.
		p.setOldest(tp, ps.lastCommitTS)
	default:
		p.setOldest(tp, time.Time{})
	}
}

// decode converts a chunk's records into rows. Rejected records are counted and skipped.
func (p *Processor) decode(c *chunk) *Rows {
	rows := NewRows()
	for _, rec := range c.recs {
		p.decodeRecord(rows, rec)
	}
	return rows
}

func (p *Processor) decodeRecord(rows *Rows, rec *kgo.Record) {
	reject := func(reason string, err error) {
		p.rejected.WithLabelValues(reason).Inc()
		p.log.Warn("record rejected", "reason", reason, "topic", rec.Topic, "partition", rec.Partition, "offset", rec.Offset, "err", err)
	}
	if v, _ := queue.HeaderValue(rec, queue.HeaderSchemaVersion); v != queue.SchemaVersion {
		reject("schema_version", fmt.Errorf("unsupported schema version %q", v))
		return
	}
	tenantID, _ := queue.HeaderValue(rec, queue.HeaderTenantID)
	if tenantID == "" {
		reject("missing_tenant", nil)
		return
	}
	receivedAt := rec.Timestamp
	if v, ok := queue.HeaderValue(rec, queue.HeaderReceivedAt); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			receivedAt = time.Unix(0, n)
		}
	}
	sig, ok := p.signalOf(rec.Topic)
	if !ok {
		reject("unknown_topic", nil)
		return
	}
	switch sig {
	case queue.SignalMetrics:
		var req colmetrics.ExportMetricsServiceRequest
		if err := proto.Unmarshal(rec.Value, &req); err != nil {
			reject("decode", err)
			return
		}
		rows.AddMetrics(tenantID, receivedAt, &req)
		rows.AddIngestUsage(tenantID, string(sig), receivedAt, len(rec.Value)) // usage.go (D-079)
	case queue.SignalLogs:
		var req collogs.ExportLogsServiceRequest
		if err := proto.Unmarshal(rec.Value, &req); err != nil {
			reject("decode", err)
			return
		}
		rows.AddLogs(tenantID, receivedAt, &req)
		rows.AddIngestUsage(tenantID, string(sig), receivedAt, len(rec.Value))
	case queue.SignalTraces:
		var req coltrace.ExportTraceServiceRequest
		if err := proto.Unmarshal(rec.Value, &req); err != nil {
			reject("decode", err)
			return
		}
		rows.AddTraces(tenantID, receivedAt, &req)
		rows.AddIngestUsage(tenantID, string(sig), receivedAt, len(rec.Value))
	}
}

// blockToken is the token of block i of n blocks of a chunk's table rows. Blocks
// are split at BatchRows rows; the row limit is part of the token so a different
// OPENLOG_PROCESSOR_BATCH_ROWS never reuses a token for different rows.
func (p *Processor) blockToken(base string, i, n int) string {
	if n == 1 {
		return base
	}
	return base + "#" + strconv.Itoa(i) + "/" + strconv.Itoa(p.cfg.BatchRows)
}

// writeChunk inserts every table of a chunk, retrying with identical tokens until
// success or ctx cancellation. Blocks already inserted are skipped on retry (and
// would be deduplicated by ClickHouse anyway).
func (p *Processor) writeChunk(ctx context.Context, c *chunk) error {
	rows := p.decode(c)
	p.enqueueLate(rows)
	for reason, n := range rows.Dropped {
		p.dropped.WithLabelValues(reason).Add(float64(n))
	}
	values := map[string][][]any{}
	for _, t := range Tables {
		if rows.Len(t) > 0 {
			values[t] = rows.Values(t)
		}
	}
	done := map[string]bool{}
	backoff := 200 * time.Millisecond
	for {
		err := p.writeTables(ctx, c, values, done)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			// Shutdown or revocation cancelled the insert: not an insert failure. The
			// uncommitted records are re-read by the next owner.
			p.log.Info("insert cancelled", "reason", context.Cause(ctx), "topic", c.tp.topic, "partition", c.tp.partition,
				"first_offset", c.recs[0].Offset, "last_offset", c.last().Offset)
			return ctx.Err()
		}
		p.failures.Inc()
		p.log.Error("insert failed, retrying", "err", err, "backoff", backoff.String(), "topic", c.tp.topic, "partition", c.tp.partition,
			"first_offset", c.recs[0].Offset, "last_offset", c.last().Offset)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(jitter(backoff)):
		}
		backoff = min(backoff*2, 10*time.Second)
	}
}

func (p *Processor) writeTables(ctx context.Context, c *chunk, values map[string][][]any, done map[string]bool) error {
	for _, t := range Tables { // hosts last: a host appears once its telemetry is stored
		vals := values[t]
		if len(vals) == 0 {
			continue
		}
		n := (len(vals) + p.cfg.BatchRows - 1) / p.cfg.BatchRows
		base := c.token(t)
		for i := range n {
			tok := p.blockToken(base, i, n)
			if done[tok] {
				continue
			}
			block := vals[i*p.cfg.BatchRows : min((i+1)*p.cfg.BatchRows, len(vals))]
			ictx, cancel := context.WithTimeout(ctx, p.cfg.InsertTimeout)
			err := p.writer.Write(ictx, t, tok, Columns[t], block)
			cancel()
			if err != nil {
				return err
			}
			done[tok] = true
			p.inserted.WithLabelValues(t).Add(float64(len(block)))
		}
	}
	return nil
}

// flushClosed writes all closed chunks (InsertConcurrency at a time) and commits,
// per partition, the chunks inserted without a gap. It returns an error only if
// ctx ended before every chunk was written.
func (p *Processor) flushClosed(ctx context.Context) error {
	if p.closedN == 0 {
		return nil
	}
	tps := make([]topicPartition, 0, len(p.parts))
	for tp, ps := range p.parts {
		if len(ps.closed) > 0 {
			tps = append(tps, tp)
		}
	}
	sort.Slice(tps, func(i, j int) bool { return tps[i].less(tps[j]) })
	var work []*chunk
	for _, tp := range tps {
		work = append(work, p.parts[tp].closed...)
	}

	start := time.Now()
	errs := make([]error, len(work))
	sem := make(chan struct{}, p.cfg.InsertConcurrency)
	var wg sync.WaitGroup
	for i, c := range work {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := p.writeChunk(ctx, c); err != nil {
				errs[i] = err
				return
			}
			c.done = true
		}()
	}
	wg.Wait()

	var commit []*kgo.Record
	for _, tp := range tps {
		ps := p.parts[tp]
		n := 0
		for n < len(ps.closed) && ps.closed[n].done {
			p.buffered -= ps.closed[n].bytes
			n++
		}
		if n == 0 {
			continue
		}
		last := ps.closed[n-1].last()
		commit = append(commit, last)
		ps.lastCommitTS = last.Timestamp
		ps.closed = ps.closed[n:]
		p.closedN -= n
		p.refreshOldest(tp, ps)
	}
	if len(commit) > 0 {
		if err := p.consumer.CommitRecords(ctx, commit...); err != nil {
			// Rows are stored; a failed commit only causes re-delivery, which produces
			// the same chunks and tokens (see package doc).
			p.log.Error("offset commit failed", "err", err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	p.flushDur.Observe(time.Since(start).Seconds())
	return nil
}

// shutdown flushes everything buffered (bounded by the insert timeout) and commits.
func (p *Processor) shutdown() error {
	p.closeAll(cutShutdown)
	ctx, cancel := context.WithTimeout(context.Background(), p.cfg.InsertTimeout)
	defer cancel()
	err := p.flushClosed(ctx)
	p.consumer.AllowRebalance()
	if err != nil {
		return fmt.Errorf("final flush: %w", err)
	}
	return nil
}

// Run consumes until ctx is cancelled. On cancellation everything buffered is
// flushed and committed (bounded by the insert timeout) before returning.
// Rebalances are allowed whenever no closed chunk is waiting to be inserted; open
// chunks of revoked partitions are dropped (applyRevocations).
func (p *Processor) Run(ctx context.Context) error {
	for {
		pollCtx, cancel := context.WithTimeout(ctx, p.pollTimeout())
		fetches := p.consumer.PollRecords(pollCtx, maxPollRecords)
		cancel()
		if fetches.IsClientClosed() {
			return errors.New("kafka client closed")
		}
		p.applyRevocations()
		fetches.EachError(func(topic string, partition int32, err error) {
			if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
				p.log.Warn("fetch error", "topic", topic, "partition", partition, "err", err)
			}
		})
		var failed error
		fetches.EachPartition(func(fp kgo.FetchTopicPartition) {
			if failed != nil || fp.Err != nil {
				return
			}
			ps := p.part(topicPartition{fp.Topic, fp.Partition})
			if fp.HighWatermark > ps.hwm {
				ps.hwm = fp.HighWatermark
			}
			for i, rec := range fp.Records {
				p.add(rec)
				fp.Records[i] = nil
				// Bound memory by bytes, not by the poll size: with a backlog one poll can
				// return up to maxPollRecords records.
				if p.buffered >= p.cfg.MaxBufferedBytes {
					if failed = p.relieve(ctx); failed != nil {
						return
					}
				}
			}
		})
		if failed != nil || ctx.Err() != nil {
			return p.shutdown()
		}
		p.closeIdle(p.now())
		if err := p.flushClosed(ctx); err != nil {
			return p.shutdown()
		}
		for tp, ps := range p.parts {
			if ps.open == nil && len(ps.closed) == 0 && !ps.caughtUp() {
				p.refreshOldest(tp, ps)
			}
		}
		p.consumer.AllowRebalance()
	}
}

// relieve flushes closed chunks and, if the buffer is still over its limit, cuts
// and flushes the open ones too (a timing-dependent cut, counted as "memory").
func (p *Processor) relieve(ctx context.Context) error {
	if err := p.flushClosed(ctx); err != nil {
		return err
	}
	if p.buffered >= p.cfg.MaxBufferedBytes {
		p.closeAll(cutMemory)
		return p.flushClosed(ctx)
	}
	return nil
}

// RunLagMonitor exports openlog_processor_consumer_lag_records for this member's
// partitions every interval until ctx is done.
func (p *Processor) RunLagMonitor(ctx context.Context, interval time.Duration, fetch func(context.Context) ([]queue.PartitionLag, error)) {
	prev := map[[2]string]bool{}
	for {
		fctx, cancel := context.WithTimeout(ctx, interval)
		lags, err := fetch(fctx)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				p.log.Debug("consumer lag fetch failed", "err", err)
			}
		} else {
			cur := map[[2]string]bool{}
			for _, l := range lags {
				k := [2]string{l.Topic, strconv.Itoa(int(l.Partition))}
				cur[k] = true
				p.lagRecords.WithLabelValues(k[0], k[1]).Set(float64(l.Lag))
			}
			for k := range prev {
				if !cur[k] {
					p.lagRecords.DeleteLabelValues(k[0], k[1])
				}
			}
			prev = cur
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

type lagSecondsCollector struct {
	p    *Processor
	desc *prometheus.Desc
}

func (c *lagSecondsCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *lagSecondsCollector) Collect(ch chan<- prometheus.Metric) {
	for topic, age := range c.p.lagSeconds(time.Now()) {
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, age.Seconds(), topic)
	}
}

// lagSeconds returns, per signal topic, the age of the oldest unprocessed record.
func (p *Processor) lagSeconds(now time.Time) map[string]time.Duration {
	out := make(map[string]time.Duration, len(queue.AllSignals))
	for _, t := range queue.Topics(p.prefix) {
		out[t] = 0
	}
	p.oldestMu.Lock()
	defer p.oldestMu.Unlock()
	for tp, ts := range p.oldest {
		if age := now.Sub(ts); age > out[tp.topic] {
			out[tp.topic] = age
		}
	}
	return out
}

// jitter returns a random duration in [d/2, d]. Processors whose inserts fail together
// (e.g. ClickHouse over its memory limit) otherwise retry in lockstep with identical
// exponential backoff and keep overloading it together; observed as a livelock with 4
// processors and 50k-row batches against a memory-bound ClickHouse.
func jitter(d time.Duration) time.Duration {
	if d <= 1 {
		return d
	}
	half := d / 2
	return half + rand.N(d-half+1)
}
