package tailsampling

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/onuragtas/openlog/internal/queue"
)

// Producer produces to the sampled traces topic and waits for the acknowledgement.
type Producer interface {
	Produce(ctx context.Context, msgs ...queue.Message) error
}

// Consumer is the subset of *kgo.Client the runner uses.
type Consumer interface {
	PollRecords(ctx context.Context, maxPollRecords int) kgo.Fetches
	CommitOffsetsSync(ctx context.Context, uncommitted map[string]map[int32]kgo.EpochOffset,
		onDone func(*kgo.Client, *kmsg.OffsetCommitRequest, *kmsg.OffsetCommitResponse, error))
	AllowRebalance()
}

// RunnerOptions configure the Kafka loop.
type RunnerOptions struct {
	// Prefix is OPENLOG_KAFKA_TOPIC_PREFIX; the runner reads <prefix>.otlp.traces.v1 and writes
	// <prefix>.otlp.traces.sampled.v1.
	Prefix         string
	ProduceTimeout time.Duration
	// ShutdownTimeout bounds the final decide + produce + commit on shutdown and revocation.
	ShutdownTimeout time.Duration
	// MaxRecordBytes splits a kept trace into several records above this size.
	MaxRecordBytes int
}

const (
	maxPollRecords = 5000
	// flushOutputs flushes in the middle of a poll once this many outputs are pending.
	flushOutputs = 2000
)

// Runner consumes the raw traces topic, feeds the engine and produces its decisions.
type Runner struct {
	opts RunnerOptions
	eng  *Engine
	m    *Metrics
	prod Producer
	cl   Consumer
	log  *slog.Logger

	// mu serializes the poll loop with the rebalance callbacks (which run in kgo's group goroutine
	// while the loop waits in PollRecords after AllowRebalance).
	mu sync.Mutex
}

// NewRunner creates a runner. Pass KafkaOpts to the consumer client, then call SetConsumer.
func NewRunner(opts RunnerOptions, eng *Engine, m *Metrics, prod Producer, log *slog.Logger) *Runner {
	if opts.ProduceTimeout <= 0 {
		opts.ProduceTimeout = 10 * time.Second
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = 25 * time.Second
	}
	if opts.MaxRecordBytes <= 0 {
		opts.MaxRecordBytes = 8 << 20
	}
	return &Runner{opts: opts, eng: eng, m: m, prod: prod, log: log}
}

// SetConsumer sets the consumer group client. Must be called before Run.
func (r *Runner) SetConsumer(cl Consumer) { r.cl = cl }

// KafkaOpts are the consumer client options the runner needs (rebalance callbacks).
func (r *Runner) KafkaOpts() []kgo.Opt {
	return []kgo.Opt{
		kgo.OnPartitionsRevoked(func(ctx context.Context, cl *kgo.Client, m map[string][]int32) { r.onGone(ctx, cl, m, "revoked") }),
		kgo.OnPartitionsLost(func(ctx context.Context, cl *kgo.Client, m map[string][]int32) { r.onGone(ctx, cl, m, "lost") }),
	}
}

// onGone decides every trace that has spans from the partitions this member gives up, produces the kept
// ones and commits, so the next owner starts after them instead of buffering half traces again.
func (r *Runner) onGone(ctx context.Context, _ *kgo.Client, m map[string][]int32, how string) {
	cl := r.cl // the same client; the interface keeps the callback testable
	if len(m) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	traces, _ := r.eng.Buffered()
	r.eng.DecidePartitions(m)
	after, _ := r.eng.Buffered()
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.opts.ShutdownTimeout)
	defer cancel()
	outs := r.eng.TakeOutputs()
	if err := r.produce(cctx, outs); err != nil {
		r.log.Error("partitions "+how+": producing decided traces failed; the next owner re-reads them", "err", err)
	} else {
		r.eng.Release(outs)
		if off := r.eng.CommitOffsets(m); len(off) > 0 && how == "revoked" {
			r.commit(cctx, cl, off)
		}
	}
	r.eng.DropPartitions(m)
	r.log.Info("partitions "+how+", decided their buffered traces", "traces", traces-after, "partitions", partitionsString(m))
}

func partitionsString(m map[string][]int32) string {
	var s string
	for t, ps := range m {
		for _, p := range ps {
			if s != "" {
				s += ","
			}
			s += t + "/" + strconv.Itoa(int(p))
		}
	}
	return s
}

// Run consumes until ctx is cancelled; then every buffered trace is decided, produced and committed.
func (r *Runner) Run(ctx context.Context) error {
	for {
		timeout := time.Second
		if d := r.eng.NextDeadline(); !d.IsZero() {
			timeout = min(max(time.Until(d), 20*time.Millisecond), time.Second)
		}
		pctx, cancel := context.WithTimeout(ctx, timeout)
		fetches := r.cl.PollRecords(pctx, maxPollRecords)
		cancel()
		if fetches.IsClientClosed() {
			return errors.New("kafka client closed")
		}
		fetches.EachError(func(topic string, partition int32, err error) {
			if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
				r.log.Warn("fetch error", "topic", topic, "partition", partition, "err", err)
			}
		})
		r.mu.Lock()
		var failed error
		fetches.EachRecord(func(rec *kgo.Record) {
			if failed != nil {
				return
			}
			r.addRecord(rec)
			if len(r.eng.out) >= flushOutputs {
				failed = r.flush(ctx, r.cl)
			}
		})
		if failed == nil {
			r.eng.Tick()
			failed = r.flush(ctx, r.cl)
		}
		r.mu.Unlock()
		if failed != nil || ctx.Err() != nil {
			return r.shutdown()
		}
		r.cl.AllowRebalance()
	}
}

func (r *Runner) shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), r.opts.ShutdownTimeout)
	defer cancel()
	r.mu.Lock()
	r.eng.DecideAll(ReasonShutdown)
	err := r.flush(ctx, r.cl)
	r.mu.Unlock()
	r.cl.AllowRebalance()
	if err != nil {
		return err
	}
	return nil
}

func (r *Runner) addRecord(rec *kgo.Record) {
	src := Source{Topic: rec.Topic, Partition: rec.Partition, Offset: rec.Offset, Epoch: rec.LeaderEpoch, ReceivedAt: rec.Timestamp}
	req := &coltrace.ExportTraceServiceRequest{}
	reject := func(reason string, err error) {
		r.m.RecordsRejected.WithLabelValues(reason).Inc()
		r.log.Warn("record rejected", "reason", reason, "topic", rec.Topic, "partition", rec.Partition, "offset", rec.Offset, "err", err)
		r.eng.Add(src, &coltrace.ExportTraceServiceRequest{}) // keeps the offset moving
	}
	if v, _ := queue.HeaderValue(rec, queue.HeaderSchemaVersion); v != queue.SchemaVersion {
		reject("schema_version", nil)
		return
	}
	src.TenantID, _ = queue.HeaderValue(rec, queue.HeaderTenantID)
	if src.TenantID == "" {
		reject("missing_tenant", nil)
		return
	}
	src.RequestID, _ = queue.HeaderValue(rec, queue.HeaderRequestID)
	if v, ok := queue.HeaderValue(rec, queue.HeaderReceivedAt); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			src.ReceivedAt = time.Unix(0, n)
		}
	}
	if err := proto.Unmarshal(rec.Value, req); err != nil {
		reject("decode", err)
		return
	}
	r.eng.Add(src, req)
}

// flush produces pending outputs (retrying until ctx ends) and commits what became committable.
func (r *Runner) flush(ctx context.Context, cl Consumer) error {
	outs := r.eng.TakeOutputs()
	if err := r.produce(ctx, outs); err != nil {
		r.eng.out = append(outs, r.eng.out...) // not produced: keep them (and their offsets) pending
		return err
	}
	r.eng.Release(outs)
	if off := r.eng.CommitOffsets(nil); len(off) > 0 {
		r.commit(ctx, cl, off)
	}
	return nil
}

func (r *Runner) commit(ctx context.Context, cl Consumer, off map[string]map[int32]kgo.EpochOffset) {
	cl.CommitOffsetsSync(ctx, off, func(_ *kgo.Client, _ *kmsg.OffsetCommitRequest, resp *kmsg.OffsetCommitResponse, err error) {
		if err == nil && resp != nil {
			for _, t := range resp.Topics {
				for _, p := range t.Partitions {
					if p.ErrorCode != 0 {
						err = errors.New("offset commit error code " + strconv.Itoa(int(p.ErrorCode)))
					}
				}
			}
		}
		if err != nil {
			// Decided traces are already produced: re-delivery only duplicates them.
			r.log.Warn("offset commit failed", "err", err)
		}
	})
}

// produce writes the outputs to the sampled topic, retrying with backoff until success or ctx end.
func (r *Runner) produce(ctx context.Context, outs []Output) error {
	if len(outs) == 0 {
		return nil
	}
	topic := queue.SampledTracesTopic(r.opts.Prefix)
	var msgs []queue.Message
	for _, o := range outs {
		key := o.TenantID + "/" + hex.EncodeToString(o.TraceID)
		for _, req := range splitRequest(o.Request, r.opts.MaxRecordBytes) {
			b, err := proto.Marshal(req)
			if err != nil {
				return err
			}
			msgs = append(msgs, queue.Message{Topic: topic, Key: key, Value: b, TenantID: o.TenantID, RequestID: o.RequestID, ReceivedAt: o.ReceivedAt})
		}
	}
	backoff := 200 * time.Millisecond
	for {
		pctx, cancel := context.WithTimeout(ctx, r.opts.ProduceTimeout)
		err := r.prod.Produce(pctx, msgs...)
		cancel()
		if err == nil {
			return nil
		}
		r.m.ProduceFailures.Inc()
		r.log.Error("produce to sampled traces topic failed, retrying", "err", err, "records", len(msgs), "backoff", backoff.String())
		select {
		case <-ctx.Done():
			return err
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 10*time.Second)
	}
}

// splitRequest splits req into requests of at most maxBytes (approximately) keeping span order.
func splitRequest(req *coltrace.ExportTraceServiceRequest, maxBytes int) []*coltrace.ExportTraceServiceRequest {
	if proto.Size(req) <= maxBytes {
		return []*coltrace.ExportTraceServiceRequest{req}
	}
	var out []*coltrace.ExportTraceServiceRequest
	cur, size := &coltrace.ExportTraceServiceRequest{}, 0
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				n := proto.Size(sp) + 64
				if size > 0 && size+n > maxBytes {
					out = append(out, cur)
					cur, size = &coltrace.ExportTraceServiceRequest{}, 0
				}
				if size == 0 {
					size = proto.Size(rs.GetResource()) + 64
				}
				appendToRequest(cur, bufferedSpan{res: rs, scope: ss, span: sp})
				size += n
			}
		}
	}
	if size > 0 {
		out = append(out, cur)
	}
	return out
}

// RunLagMonitor exports openlog_tailsampling_consumer_lag_records for this member's partitions.
func (r *Runner) RunLagMonitor(ctx context.Context, interval time.Duration, fetch func(context.Context) ([]queue.PartitionLag, error)) {
	prev := map[[2]string]bool{}
	for {
		fctx, cancel := context.WithTimeout(ctx, interval)
		lags, err := fetch(fctx)
		cancel()
		if err == nil {
			cur := map[[2]string]bool{}
			for _, l := range lags {
				k := [2]string{l.Topic, strconv.Itoa(int(l.Partition))}
				cur[k] = true
				r.m.Lag.WithLabelValues(k[0], k[1]).Set(float64(l.Lag))
			}
			for k := range prev {
				if !cur[k] {
					r.m.Lag.DeleteLabelValues(k[0], k[1])
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
