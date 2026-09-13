package processor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/twmb/franz-go/pkg/kgo"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/proto"

	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/queue"
)

var (
	mt = queue.Topic("openlog", queue.SignalMetrics)
	lt = queue.Topic("openlog", queue.SignalLogs)
	// t0 is the start of a 1s window.
	t0 = time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
)

func record(t *testing.T, topic string, partition int32, offset int64, msg proto.Message, headers map[string]string) *kgo.Record {
	t.Helper()
	b, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	r := &kgo.Record{Topic: topic, Partition: partition, Offset: offset, Value: b}
	for k, v := range headers {
		r.Headers = append(r.Headers, kgo.RecordHeader{Key: k, Value: []byte(v)})
	}
	return r
}

func at(r *kgo.Record, ts time.Time) *kgo.Record { r.Timestamp = ts; return r }

func goodHeaders() map[string]string {
	return map[string]string{queue.HeaderSchemaVersion: "1", queue.HeaderTenantID: "t1", queue.HeaderReceivedAt: "1757757600000000000"}
}

func metricsMsg() *colmetrics.ExportMetricsServiceRequest {
	return &colmetrics.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource: hostResource(),
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{{Name: "system.uptime",
			Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{{TimeUnixNano: ts1}}}}}}}},
	}}}
}

func logsMsg() *collogs.ExportLogsServiceRequest { return logsMsgN(1) }

func logsMsgN(n int) *collogs.ExportLogsServiceRequest {
	recs := make([]*logspb.LogRecord, n)
	for i := range recs {
		recs[i] = &logspb.LogRecord{TimeUnixNano: ts1, Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "x"}}}
	}
	return &collogs.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		Resource: hostResource(), ScopeLogs: []*logspb.ScopeLogs{{LogRecords: recs}},
	}}}
}

func newTestProcessor(w Writer, c Consumer) *Processor {
	cfg := config.Processor{BatchRows: 1000, FlushInterval: time.Second, InsertTimeout: time.Second,
		BatchBytes: 1 << 20, MaxBufferedBytes: 64 << 20, InsertConcurrency: 4}
	p := New(cfg, "openlog", c, w, slog.New(slog.NewTextHandler(io.Discard, nil)), prometheus.NewRegistry())
	return p
}

type fakeWriter struct {
	mu     sync.Mutex
	fail   int
	calls  []string
	tokens map[string][]string
	rows   map[string]int
}

func (f *fakeWriter) Write(_ context.Context, table, token string, _ []string, rows [][]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, table)
	if f.fail > 0 {
		f.fail--
		return errors.New("clickhouse down")
	}
	if f.tokens == nil {
		f.tokens, f.rows = map[string][]string{}, map[string]int{}
	}
	f.tokens[table] = append(f.tokens[table], token)
	f.rows[token] += len(rows)
	return nil
}

func (f *fakeWriter) sortedTokens(table string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string(nil), f.tokens[table]...)
	sort.Strings(out)
	return out
}

// poll is one PollRecords result: records plus the high watermark per partition.
type poll struct {
	recs []*kgo.Record
	hwm  map[topicPartition]int64
	// before runs before the poll returns (e.g. a rebalance callback).
	before func()
}

type fakeConsumer struct {
	mu        sync.Mutex
	polls     []poll
	committed []*kgo.Record
	commits   [][]int64
	cancel    context.CancelFunc
}

func (f *fakeConsumer) PollRecords(ctx context.Context, _ int) kgo.Fetches {
	f.mu.Lock()
	if len(f.polls) == 0 {
		f.mu.Unlock()
		if f.cancel != nil {
			f.cancel() // nothing more: request shutdown
		}
		<-ctx.Done()
		return kgo.Fetches{}
	}
	pl := f.polls[0]
	f.polls = f.polls[1:]
	f.mu.Unlock()
	if pl.before != nil {
		pl.before()
	}
	return fetchesOf(pl)
}

func fetchesOf(pl poll) kgo.Fetches {
	byTP := map[topicPartition][]*kgo.Record{}
	var order []topicPartition
	for _, r := range pl.recs {
		tp := topicPartition{r.Topic, r.Partition}
		if _, ok := byTP[tp]; !ok {
			order = append(order, tp)
		}
		byTP[tp] = append(byTP[tp], r)
	}
	var f kgo.Fetch
	for _, tp := range order {
		f.Topics = append(f.Topics, kgo.FetchTopic{Topic: tp.topic, Partitions: []kgo.FetchPartition{{
			Partition: tp.partition, Records: byTP[tp], HighWatermark: pl.hwm[tp],
		}}})
	}
	return kgo.Fetches{f}
}

func (f *fakeConsumer) CommitRecords(_ context.Context, rs ...*kgo.Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.committed = append(f.committed, rs...)
	offs := make([]int64, len(rs))
	for i, r := range rs {
		offs[i] = r.Offset
	}
	f.commits = append(f.commits, offs)
	return nil
}

func (f *fakeConsumer) AllowRebalance() {}

func TestChunkTokensFollowWindowsAndBytes(t *testing.T) {
	w := &fakeWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &fakeConsumer{cancel: cancel, polls: []poll{{recs: []*kgo.Record{
		at(record(t, mt, 3, 10, metricsMsg(), goodHeaders()), t0.Add(100*time.Millisecond)),
		at(record(t, mt, 3, 11, metricsMsg(), goodHeaders()), t0.Add(900*time.Millisecond)),
		at(record(t, mt, 3, 12, metricsMsg(), goodHeaders()), t0.Add(1100*time.Millisecond)), // next window
		at(record(t, mt, 3, 13, metricsMsg(), goodHeaders()), t0.Add(950*time.Millisecond)),  // late: joins open chunk
		at(record(t, mt, 1, 7, metricsMsg(), goodHeaders()), t0),
		at(record(t, lt, 0, 5, logsMsg(), goodHeaders()), t0),
	}}}}
	p := newTestProcessor(w, c)
	p.now = func() time.Time { return t0.Add(time.Hour) }
	if err := p.Run(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"openlog.otlp.metrics.v1:1:7-7:metrics", "openlog.otlp.metrics.v1:3:10-11:metrics", "openlog.otlp.metrics.v1:3:12-13:metrics"}
	if got := w.sortedTokens(TableMetrics); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("metrics tokens %v, want %v", got, want)
	}
	if got := w.sortedTokens(TableLogs); len(got) != 1 || got[0] != "openlog.otlp.logs.v1:0:5-5:logs" {
		t.Errorf("logs tokens %v", got)
	}
	// Hosts: one row per (tenant, host) per chunk, same token shape.
	if got := w.sortedTokens(TableHosts); len(got) != 4 || !strings.HasSuffix(got[0], ":hosts") {
		t.Errorf("hosts tokens %v", got)
	}
	if n := testutil.ToFloat64(p.cuts.WithLabelValues(cutWindow)); n != 1 {
		t.Errorf("window cuts = %v", n)
	}

	// Bytes: a chunk closes after the record that reaches BatchBytes.
	w2 := &fakeWriter{}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	var recs []*kgo.Record
	for off := range int64(5) {
		recs = append(recs, at(record(t, lt, 0, off, logsMsg(), goodHeaders()), t0))
	}
	p2 := newTestProcessor(w2, &fakeConsumer{cancel: cancel2, polls: []poll{{recs: recs}}})
	p2.cfg.BatchBytes = int64(len(recs[0].Value)) * 2
	p2.now = func() time.Time { return t0.Add(time.Hour) }
	if err := p2.Run(ctx2); err != nil {
		t.Fatal(err)
	}
	want = []string{"openlog.otlp.logs.v1:0:0-1:logs", "openlog.otlp.logs.v1:0:2-3:logs", "openlog.otlp.logs.v1:0:4-4:logs"}
	if got := w2.sortedTokens(TableLogs); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("logs tokens %v, want %v", got, want)
	}
}

// A range re-delivered to another member (after a crash between insert and commit)
// must produce the same chunks and tokens even when the polls are split differently.
func TestRedeliveredRangeProducesSameTokens(t *testing.T) {
	mk := func() []*kgo.Record {
		var recs []*kgo.Record
		for off := range int64(6) {
			ts := t0.Add(time.Duration(off) * 300 * time.Millisecond) // windows: 0-3 | 4-5
			recs = append(recs, at(record(t, lt, 2, 100+off, logsMsgN(3), goodHeaders()), ts))
		}
		return recs
	}
	hwm := map[topicPartition]int64{{lt, 2}: 106}
	run := func(polls []poll, now time.Time) []string {
		w := &fakeWriter{}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		p := newTestProcessor(w, &fakeConsumer{cancel: cancel, polls: polls})
		p.now = func() time.Time { return now }
		if err := p.Run(ctx); err != nil {
			t.Fatal(err)
		}
		return w.sortedTokens(TableLogs)
	}
	a := mk()
	first := run([]poll{{recs: a, hwm: hwm}}, t0.Add(time.Minute))
	b := mk()
	second := run([]poll{{recs: b[:2], hwm: hwm}, {recs: b[2:5], hwm: hwm}, {recs: b[5:], hwm: hwm}}, t0.Add(time.Minute))
	want := "openlog.otlp.logs.v1:2:100-103:logs openlog.otlp.logs.v1:2:104-105:logs"
	if strings.Join(first, " ") != want || strings.Join(second, " ") != want {
		t.Errorf("tokens differ:\n first  %v\n second %v\n want   %s", first, second, want)
	}
}

// While the partition is not caught up (records still to be fetched), no timing cut happens.
func TestNoIdleCutWhileBehind(t *testing.T) {
	p := newTestProcessor(&fakeWriter{}, &fakeConsumer{})
	p.now = func() time.Time { return t0.Add(time.Hour) }
	ps := p.part(topicPartition{lt, 0})
	ps.hwm = 50
	p.add(at(record(t, lt, 0, 10, logsMsg(), goodHeaders()), t0))
	p.closeIdle(p.now())
	if p.closedN != 0 {
		t.Fatal("idle cut while behind the high watermark")
	}
	ps.hwm = 11
	p.closeIdle(p.now())
	if p.closedN != 1 {
		t.Fatal("caught up partition past its window must be cut")
	}
}

func TestRunRetriesWithSameTokenAndCommitsAfterInsert(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &fakeConsumer{cancel: cancel, polls: []poll{{recs: []*kgo.Record{
		record(t, mt, 0, 100, metricsMsg(), goodHeaders()),
		record(t, mt, 0, 101, metricsMsg(), goodHeaders()),
	}}}}
	w := &fakeWriter{fail: 2}
	p := newTestProcessor(w, c)
	if err := p.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(w.tokens[TableMetrics]) != 1 || w.tokens[TableMetrics][0] != "openlog.otlp.metrics.v1:0:100-101:metrics" {
		t.Errorf("tokens %v", w.tokens)
	}
	if w.calls[0] != TableMetrics || w.calls[1] != TableMetrics {
		t.Errorf("calls %v", w.calls)
	}
	if len(c.committed) != 1 || c.committed[0].Offset != 101 {
		t.Errorf("committed %v", c.committed)
	}
}

func TestRunDoesNotCommitWhenInsertNeverSucceeds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	c := &fakeConsumer{polls: []poll{{recs: []*kgo.Record{record(t, mt, 0, 1, metricsMsg(), goodHeaders())}}}}
	w := &fakeWriter{fail: 1 << 30}
	p := newTestProcessor(w, c)
	p.cfg.InsertTimeout = 300 * time.Millisecond
	if err := p.Run(ctx); err == nil {
		t.Error("expected final flush error")
	}
	if len(c.committed) != 0 {
		t.Errorf("committed without insert: %v", c.committed)
	}
}

// Buffered, uncommitted records of a revoked partition are dropped, never inserted
// or committed; other partitions are unaffected.
func TestRevokedPartitionDropsBufferedRecords(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ev := NewPartitionEvents()
	future := time.Now().Add(time.Hour) // windows not over: chunks stay open
	c := &fakeConsumer{cancel: cancel, polls: []poll{
		{recs: []*kgo.Record{at(record(t, lt, 0, 1, logsMsg(), goodHeaders()), future), at(record(t, lt, 1, 1, logsMsg(), goodHeaders()), future)}},
		{before: func() { ev.Add(map[string][]int32{lt: {0}}) }},
	}}
	w := &fakeWriter{}
	p := newTestProcessor(w, c)
	p.SetPartitionEvents(ev)
	p.cfg.FlushInterval = time.Hour
	if err := p.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got := w.sortedTokens(TableLogs); len(got) != 1 || got[0] != "openlog.otlp.logs.v1:1:1-1:logs" {
		t.Errorf("tokens %v, want only partition 1 (flushed at shutdown)", got)
	}
	for _, r := range c.committed {
		if r.Partition == 0 {
			t.Errorf("revoked partition committed: %v", r)
		}
	}
	if p.buffered != 0 || p.closedN != 0 {
		t.Errorf("buffer accounting: buffered=%d closed=%d", p.buffered, p.closedN)
	}
}

// A single poll carrying far more data than the buffer limit (a consumer-lag backlog)
// is written and committed in bounded steps instead of being held at once.
func TestRunFlushesWithinOnePollAtBufferLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var recs []*kgo.Record
	for off := range int64(10) {
		recs = append(recs, at(record(t, lt, 0, off, logsMsgN(400), goodHeaders()), time.Now().Add(time.Hour)))
	}
	c := &fakeConsumer{cancel: cancel, polls: []poll{{recs: recs, hwm: map[topicPartition]int64{{lt, 0}: 10}}}}
	w := &fakeWriter{}
	p := newTestProcessor(w, c)
	p.cfg.BatchRows = 100000
	p.cfg.BatchBytes = int64(len(recs[0].Value)) * 3
	p.cfg.MaxBufferedBytes = int64(len(recs[0].Value)) * 3
	if err := p.Run(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"openlog.otlp.logs.v1:0:0-2:logs", "openlog.otlp.logs.v1:0:3-5:logs", "openlog.otlp.logs.v1:0:6-8:logs", "openlog.otlp.logs.v1:0:9-9:logs"}
	if got := w.tokens[TableLogs]; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("tokens %v, want %v", got, want)
	}
	if len(c.commits) != 4 || c.commits[0][0] != 2 || c.commits[3][0] != 9 {
		t.Errorf("commits %v, want one per chunk, first at offset 2 (before later records were processed)", c.commits)
	}
}

func TestBlocksSplitAtBatchRowsWithDistinctTokens(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &fakeConsumer{cancel: cancel, polls: []poll{{recs: []*kgo.Record{record(t, lt, 0, 7, logsMsgN(2500), goodHeaders())}}}}
	w := &fakeWriter{}
	p := newTestProcessor(w, c) // BatchRows = 1000
	if err := p.Run(ctx); err != nil {
		t.Fatal(err)
	}
	base := "openlog.otlp.logs.v1:0:7-7:logs"
	want := []string{base + "#0/1000", base + "#1/1000", base + "#2/1000"}
	if got := w.tokens[TableLogs]; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("tokens %v, want %v", got, want)
	}
	if w.rows[want[0]] != 1000 || w.rows[want[2]] != 500 {
		t.Errorf("rows %v", w.rows)
	}
}

func TestLagSeconds(t *testing.T) {
	p := newTestProcessor(&fakeWriter{}, &fakeConsumer{})
	now := time.Now()
	p.now = func() time.Time { return now }
	p.cfg.FlushInterval = time.Hour
	p.add(at(record(t, lt, 0, 1, logsMsg(), goodHeaders()), now.Add(-30*time.Second)))
	p.add(at(record(t, lt, 0, 2, logsMsg(), goodHeaders()), now.Add(-5*time.Second)))
	p.add(at(record(t, lt, 1, 9, logsMsg(), goodHeaders()), now.Add(-50*time.Second)))
	lag := p.lagSeconds(now)
	if lag[lt] != 50*time.Second || lag[mt] != 0 {
		t.Errorf("lag %v", lag)
	}
	p.closeAll(cutShutdown)
	if err := p.flushClosed(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lag := p.lagSeconds(now); lag[lt] != 0 {
		t.Errorf("lag after commit %v", lag)
	}
}

func TestLagMonitorExportsOwnPartitions(t *testing.T) {
	p := newTestProcessor(&fakeWriter{}, &fakeConsumer{})
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.RunLagMonitor(ctx, 10*time.Millisecond, func(context.Context) ([]queue.PartitionLag, error) {
			calls++
			switch calls {
			case 1:
				return []queue.PartitionLag{{Topic: lt, Partition: 0, Lag: 5}, {Topic: lt, Partition: 1, Lag: 7}}, nil
			case 2:
				return nil, errors.New("coordinator loading")
			case 3:
				return []queue.PartitionLag{{Topic: lt, Partition: 1, Lag: 3}}, nil
			default:
				cancel()
				return nil, context.Canceled
			}
		})
	}()
	<-done
	if n := testutil.CollectAndCount(p.lagRecords); n != 1 {
		t.Errorf("series = %d, want only the partition still assigned", n)
	}
	if v := testutil.ToFloat64(p.lagRecords.WithLabelValues(lt, "1")); v != 3 {
		t.Errorf("lag = %v", v)
	}
}
