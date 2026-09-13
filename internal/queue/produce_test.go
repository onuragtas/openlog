package queue

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// stuckProducer models kgo with an idempotent batch in flight when the broker
// disappears: the record cannot be failed, so its promise does not fire (and
// ctx is not honoured) until Kafka is reachable again.
type stuckProducer struct {
	mu       sync.Mutex
	promises []func(*kgo.Record, error)
	recs     []*kgo.Record
}

func (s *stuckProducer) Produce(_ context.Context, r *kgo.Record, promise func(*kgo.Record, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs = append(s.recs, r)
	s.promises = append(s.promises, promise)
}

// release fires the pending promises, as kgo does once the broker is back.
func (s *stuckProducer) release(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.promises {
		p(s.recs[i], err)
	}
	s.promises = nil
}

// Regression (e2e Kafka outage): ingest hung for 60s+ instead of answering 503
// within OPENLOG_INGEST_PRODUCE_TIMEOUT because the produce call ignored ctx.
func TestProduceHonoursContextWhenAckNeverArrives(t *testing.T) {
	sp := &stuckProducer{}
	p := &Producer{rp: sp}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	errc := make(chan error, 1)
	go func() {
		errc <- p.Produce(ctx, Message{Topic: "t", Value: []byte("v")}, Message{Topic: "t", Value: []byte("w")})
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("Produce returned after %s, want ≈ ctx deadline", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Produce did not return after its context expired")
	}
	// Late acknowledgements must not block or panic.
	sp.release(nil)
}

func TestProduceWaitsForAllAcks(t *testing.T) {
	sp := &stuckProducer{}
	p := &Producer{rp: sp}
	errc := make(chan error, 1)
	go func() {
		errc <- p.Produce(context.Background(), Message{Topic: "t", TenantID: "a"}, Message{Topic: "t", TenantID: "b"})
	}()
	for {
		sp.mu.Lock()
		n := len(sp.promises)
		sp.mu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-errc:
		t.Fatalf("Produce returned before acknowledgement: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	brokerErr := errors.New("record too large")
	sp.mu.Lock()
	sp.promises[0](sp.recs[0], nil)
	sp.promises[1](sp.recs[1], brokerErr)
	sp.promises = nil
	sp.mu.Unlock()
	if err := <-errc; !errors.Is(err, brokerErr) {
		t.Errorf("err = %v, want the record error", err)
	}
	if got := string(sp.recs[0].Headers[1].Value) + string(sp.recs[1].Headers[1].Value); got != "ab" {
		t.Errorf("tenant headers = %q", got)
	}
	if err := p.Produce(context.Background()); err != nil {
		t.Errorf("empty produce: %v", err)
	}
}
