package tailsampling

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/onuragtas/openlog/internal/queue"
)

const rawTopic = "p.otlp.traces.v1"

type fakeConsumer struct {
	mu      sync.Mutex
	polls   []kgo.Fetches
	commits []map[string]map[int32]kgo.EpochOffset
}

func (f *fakeConsumer) PollRecords(ctx context.Context, _ int) kgo.Fetches {
	f.mu.Lock()
	if len(f.polls) > 0 {
		p := f.polls[0]
		f.polls = f.polls[1:]
		f.mu.Unlock()
		return p
	}
	f.mu.Unlock()
	<-ctx.Done()
	return kgo.Fetches{}
}

func (f *fakeConsumer) CommitOffsetsSync(_ context.Context, off map[string]map[int32]kgo.EpochOffset,
	onDone func(*kgo.Client, *kmsg.OffsetCommitRequest, *kmsg.OffsetCommitResponse, error)) {
	f.mu.Lock()
	f.commits = append(f.commits, off)
	f.mu.Unlock()
	onDone(nil, nil, nil, nil)
}

func (f *fakeConsumer) AllowRebalance() {}

func (f *fakeConsumer) lastCommit(partition int32) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.commits) - 1; i >= 0; i-- {
		if o, ok := f.commits[i][rawTopic][partition]; ok {
			return o.Offset
		}
	}
	return -1
}

type fakeProducer struct {
	mu    sync.Mutex
	fail  int // fail this many calls
	msgs  []queue.Message
	calls int
}

func (p *fakeProducer) Produce(ctx context.Context, msgs ...queue.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.fail > 0 {
		p.fail--
		return errors.New("broker unavailable")
	}
	p.msgs = append(p.msgs, msgs...)
	return nil
}

func record(t *testing.T, partition int32, offset int64, req *coltrace.ExportTraceServiceRequest) *kgo.Record {
	t.Helper()
	b, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	rec := queue.Message{Topic: rawTopic, Key: "t1/x", Value: b, TenantID: "t1", RequestID: "req-" + strconv.FormatInt(offset, 10),
		ReceivedAt: time.Unix(1_700_000_000, 0)}.Record()
	rec.Partition, rec.Offset = partition, offset
	return rec
}

func newTestRunner(opts Options) (*Runner, *fakeConsumer, *fakeProducer) {
	m := NewMetrics(nil)
	eng := NewEngine(opts, StaticPolicies{Default: KeepAll()}, m)
	prod := &fakeProducer{}
	r := NewRunner(RunnerOptions{Prefix: "p", ShutdownTimeout: 2 * time.Second, ProduceTimeout: time.Second}, eng, m, prod,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	cl := &fakeConsumer{}
	r.SetConsumer(cl)
	return r, cl, prod
}

func TestRunnerRevocationDecidesProducesAndCommits(t *testing.T) {
	r, cl, prod := newTestRunner(Options{DecisionWait: time.Hour})
	r.addRecord(record(t, 0, 5, request(spanSpec{traceID: traceID(1), spanID: 1, service: "a"})))
	r.addRecord(record(t, 1, 7, request(spanSpec{traceID: traceID(2), spanID: 2, service: "a"})))
	r.onGone(context.Background(), nil, map[string][]int32{rawTopic: {0}}, "revoked")
	if len(prod.msgs) != 1 || prod.msgs[0].Topic != "p.otlp.traces.sampled.v1" || prod.msgs[0].TenantID != "t1" || prod.msgs[0].RequestID != "req-5" {
		t.Fatalf("produced %+v", prod.msgs)
	}
	if got := cl.lastCommit(0); got != 6 {
		t.Fatalf("revoked partition commit = %d, want 6", got)
	}
	if n, _ := r.eng.Buffered(); n != 1 {
		t.Fatalf("trace of partition 1 must stay buffered, buffered = %d", n)
	}
	if cl.lastCommit(1) != -1 {
		t.Fatal("partition 1 must not be committed while its record is buffered")
	}
}

func TestRunnerProduceFailureKeepsOffsets(t *testing.T) {
	r, cl, prod := newTestRunner(Options{DecisionWait: time.Hour})
	prod.fail = 1 << 30
	r.addRecord(record(t, 0, 0, request(spanSpec{traceID: traceID(1), spanID: 1, service: "a"})))
	r.eng.DecideAll(ReasonShutdown)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := r.flush(ctx, cl); err == nil {
		t.Fatal("flush must fail while the producer fails")
	}
	if len(cl.commits) != 0 || len(r.eng.out) != 1 {
		t.Fatalf("commits %v, pending outputs %d; want none and 1", cl.commits, len(r.eng.out))
	}
	prod.fail = 0
	if err := r.flush(context.Background(), cl); err != nil {
		t.Fatal(err)
	}
	if cl.lastCommit(0) != 1 || len(prod.msgs) != 1 {
		t.Fatalf("after recovery: commit %d, produced %d", cl.lastCommit(0), len(prod.msgs))
	}
}

func TestRunnerRunDecidesAfterWait(t *testing.T) {
	r, cl, prod := newTestRunner(Options{DecisionWait: 50 * time.Millisecond})
	recs := []*kgo.Record{
		record(t, 0, 0, request(spanSpec{traceID: traceID(1), spanID: 1, service: "a"})),
		record(t, 0, 1, request(spanSpec{traceID: traceID(1), spanID: 2, service: "b"})),
	}
	cl.polls = []kgo.Fetches{{{Topics: []kgo.FetchTopic{{Topic: rawTopic, Partitions: []kgo.FetchPartition{{Partition: 0, Records: recs}}}}}}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		prod.mu.Lock()
		n := len(prod.msgs)
		prod.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(prod.msgs) != 1 || prod.msgs[0].Key != "t1/"+"0000000000000001"+"9e3779b97f4a7c15" {
		t.Fatalf("produced %d records, key %q", len(prod.msgs), prod.msgs[0].Key)
	}
	var out coltrace.ExportTraceServiceRequest
	if err := proto.Unmarshal(prod.msgs[0].Value, &out); err != nil || len(out.GetResourceSpans()) != 2 {
		t.Fatalf("one record with both resources expected: %v %v", err, &out)
	}
	if cl.lastCommit(0) != 2 {
		t.Fatalf("commit = %d, want 2", cl.lastCommit(0))
	}
}

func TestSplitRequest(t *testing.T) {
	var specs []spanSpec
	for i := range 50 {
		specs = append(specs, spanSpec{traceID: traceID(1), spanID: uint64(i), service: "svc"})
	}
	req := request(specs...)
	parts := splitRequest(req, 1500)
	if len(parts) < 2 {
		t.Fatalf("expected a split, got %d part(s) for %d bytes", len(parts), proto.Size(req))
	}
	n := 0
	for _, p := range parts {
		if proto.Size(p) > 1500+400 {
			t.Errorf("part of %d bytes", proto.Size(p))
		}
		n += len(outputSpans([]Output{{Request: p}}))
	}
	if n != 50 {
		t.Fatalf("spans after split = %d, want 50", n)
	}
}
