package agent

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/buffer"
	"github.com/onuragtas/openlog/agents/infra/internal/exporter"
	"github.com/onuragtas/openlog/agents/infra/internal/selfmon"
)

// Sender delivers one serialized OTLP payload (implemented by *exporter.Exporter).
type Sender interface {
	Send(ctx context.Context, signal exporter.Signal, payload []byte) error
}

// Pipeline defaults.
const (
	defaultQueueItems    = 64
	defaultQueueBytes    = 16 << 20
	defaultRetryBase     = time.Second
	defaultRetryMax      = 30 * time.Second
	defaultMaxRetryAfter = 5 * time.Minute
)

type queued struct {
	seq    uint64
	signal exporter.Signal
	items  int
	data   []byte
	ack    func() // called once when the payload was sent, persisted or dropped
}

// pipeline decouples collection from export. Collectors call Enqueue, which
// never touches the network; a single exporter goroutine (run) sends payloads.
//
// Ordering: every payload gets a sequence number from the disk buffer when it
// is enqueued. Pending payloads live either in the memory queue or in the disk
// buffer, and the exporter always sends the lowest pending sequence number, so
// delivery is FIFO no matter where a payload waited.
type pipeline struct {
	sender Sender
	buf    *buffer.Buffer
	stats  *selfmon.Stats
	log    *slog.Logger

	maxItems      int
	maxBytes      int
	retryBase     time.Duration
	retryMax      time.Duration
	maxRetryAfter time.Duration

	// onSent, when set, runs after every payload the ingest accepted (2xx). The update manager
	// uses it to confirm a freshly installed version.
	onSent func()

	mu         sync.Mutex
	queue      []queued
	queueBytes int
	wake       chan struct{}
}

func (p *pipeline) sent() {
	if p.onSent != nil {
		p.onSent()
	}
}

func newPipeline(sender Sender, buf *buffer.Buffer, stats *selfmon.Stats, log *slog.Logger) *pipeline {
	return &pipeline{
		sender: sender, buf: buf, stats: stats, log: log,
		maxItems: defaultQueueItems, maxBytes: defaultQueueBytes,
		retryBase: defaultRetryBase, retryMax: defaultRetryMax, maxRetryAfter: defaultMaxRetryAfter,
		wake: make(chan struct{}, 1),
	}
}

// Enqueue hands a payload to the exporter without blocking on the network.
// When the memory queue is full, the queue and the payload spill to disk.
func (p *pipeline) Enqueue(signal exporter.Signal, items int, data []byte) {
	p.EnqueueAck(signal, items, data, nil)
}

// EnqueueAck is Enqueue with a callback that runs once the payload is no
// longer at risk of being lost by a crash: sent, written to the disk buffer or
// dropped for good. Log inputs use it to commit offsets.
func (p *pipeline) EnqueueAck(signal exporter.Signal, items int, data []byte, ack func()) {
	q := queued{seq: p.buf.NextSeq(), signal: signal, items: items, data: data, ack: ack}
	p.mu.Lock()
	if len(p.queue) < p.maxItems && p.queueBytes+len(data) <= p.maxBytes {
		p.queue = append(p.queue, q)
		p.queueBytes += len(data)
		p.mu.Unlock()
		p.notify()
		return
	}
	p.mu.Unlock()
	// Spill everything so the disk buffer holds a contiguous tail of the
	// backlog and its drop-oldest policy really drops the oldest payloads.
	p.spillAll()
	p.spill(q)
	p.notify()
}

func (p *pipeline) notify() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *pipeline) queueLen() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.queue)
}

// spill persists one payload to the disk buffer under its sequence number.
func (p *pipeline) spill(q queued) {
	dropped, stored, err := p.buf.PushSeq(q.seq, string(q.signal), q.items, q.data)
	if err != nil {
		p.log.Error("buffer write failed", "error", err)
	}
	p.recordDropped(dropped)
	if stored {
		p.stats.AddExportItems(string(q.signal), "buffered", q.items)
	}
	p.stats.SetBufferBytes(p.buf.Bytes())
	q.done()
}

func (q queued) done() {
	if q.ack != nil {
		q.ack()
	}
}

// backlogged reports an export backlog: payloads wait in the disk buffer or
// the memory queue is half full. Log inputs pause while it lasts.
func (p *pipeline) backlogged() bool {
	return p.buf.Len() > 0 || p.queueLen() >= p.maxItems/2
}

// spillAll moves the whole memory queue to disk and returns the payload count.
func (p *pipeline) spillAll() int {
	p.mu.Lock()
	qs := p.queue
	p.queue, p.queueBytes = nil, 0
	p.mu.Unlock()
	for _, q := range qs {
		p.spill(q)
	}
	return len(qs)
}

func (p *pipeline) recordDropped(entries []buffer.Entry) {
	for _, e := range entries {
		p.stats.AddExportItems(e.Signal, "dropped", e.Items)
	}
	if len(entries) > 0 {
		p.log.Warn("disk buffer full; dropped oldest payloads", "count", len(entries))
	}
}

// step sends the oldest pending payload. progress reports whether a payload
// was consumed (sent or permanently rejected); err is non-nil for a retryable
// failure or cancellation, in which case nothing was lost.
func (p *pipeline) step(ctx context.Context) (progress bool, err error) {
	e, data, onDisk, dropped := p.buf.Peek()
	p.recordDropped(dropped)

	p.mu.Lock()
	if len(p.queue) > 0 && (!onDisk || p.queue[0].seq < e.Seq) {
		q := p.queue[0]
		p.queue[0] = queued{}
		p.queue = p.queue[1:]
		p.queueBytes -= len(q.data)
		p.mu.Unlock()

		err := p.sender.Send(ctx, q.signal, q.data)
		switch {
		case err == nil:
			p.stats.AddExportItems(string(q.signal), "sent", q.items)
			q.done()
			p.sent()
			return true, nil
		case ctx.Err() != nil || exporter.IsRetryable(err):
			// The send keeps failing: persist it and the rest of the queue.
			p.spill(q)
			p.spillAll()
			return false, err
		default:
			p.log.Error("export rejected; dropping payload", "signal", q.signal, "error", err)
			p.stats.AddExportItems(string(q.signal), "dropped", q.items)
			q.done()
			return true, nil
		}
	}
	p.mu.Unlock()
	if !onDisk {
		return false, nil
	}

	err = p.sender.Send(ctx, exporter.Signal(e.Signal), data)
	if err != nil && (ctx.Err() != nil || exporter.IsRetryable(err)) {
		return false, err
	}
	if err != nil {
		p.log.Error("buffered payload rejected; dropping", "signal", e.Signal, "error", err)
		p.stats.AddExportItems(e.Signal, "dropped", e.Items)
	} else {
		p.stats.AddExportItems(e.Signal, "sent", e.Items)
		p.sent()
	}
	if err := p.buf.Remove(e); err != nil {
		p.log.Error("buffer remove failed", "error", err)
	}
	p.stats.SetBufferBytes(p.buf.Bytes())
	return true, nil
}

// flush sends pending payloads until none are left or a send fails.
func (p *pipeline) flush(ctx context.Context) error {
	for {
		progress, err := p.step(ctx)
		if err != nil || !progress {
			return err
		}
	}
}

// run is the exporter goroutine. It returns when ctx is cancelled, or once
// draining is closed and the memory queue is empty (or a send fails).
func (p *pipeline) run(ctx context.Context, draining <-chan struct{}) {
	failures := 0
	for {
		if closed(draining) && p.queueLen() == 0 {
			return
		}
		progress, err := p.step(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if closed(draining) {
				return
			}
			failures++
			d := p.retryDelay(err, failures)
			p.log.Warn("export failed; payloads kept for retry", "error", err, "retry_in", d, "buffered_entries", p.buf.Len())
			t := time.NewTimer(d)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-draining:
				t.Stop()
			case <-t.C:
			}
			continue
		}
		failures = 0
		if !progress {
			select {
			case <-ctx.Done():
				return
			case <-draining:
			case <-p.wake:
			}
		}
	}
}

// retryDelay honours Retry-After, otherwise backs off exponentially with jitter.
func (p *pipeline) retryDelay(err error, failures int) time.Duration {
	var e *exporter.Error
	if errors.As(err, &e) && e.RetryAfter > 0 {
		return min(e.RetryAfter, p.maxRetryAfter)
	}
	d := p.retryBase << min(failures-1, 20)
	if d <= 0 || d > p.retryMax {
		d = p.retryMax
	}
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
