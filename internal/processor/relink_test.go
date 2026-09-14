package processor

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/twmb/franz-go/pkg/kgo"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/onuragtas/openlog/internal/apm"
	"github.com/onuragtas/openlog/internal/queue"
)

func TestRelinkRows(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 20, 0, time.UTC)
	opts := RelinkOptions{Enabled: true, After: 10 * time.Minute, MaxAge: 24 * time.Hour}
	span := func(tenant, trace, kind, service, parent string, weight float64, ts time.Time) SpanRow {
		return SpanRow{TenantID: tenant, TraceID: trace, Kind: kind, ServiceName: service, ParentSpanID: parent, Timestamp: ts,
			APM: apm.Derived{SampleWeight: weight}}
	}
	old := now.Add(-time.Hour).Truncate(time.Minute) // 11:00
	spans := []SpanRow{
		span("t2", "tr1", "client", "fe", "p", 1, old.Add(10*time.Second)),
		span("t2", "tr2", "client", "fe", "p", 2, old.Add(40*time.Second)),      // same minute: counted, not a new row
		span("t1", "tr3", "server", "orders", "x", 1, old.Add(30*time.Second)),  // minutes 10:55..11:00
		span("t1", "tr4", "client", "fe", "", 0, old),                           // weight 0: ignored
		span("t1", "tr5", "internal", "fe", "p", 1, old),                        // not linked
		span("t1", "tr6", "client", "fe", "", 1, now.Add(-2*time.Minute)),       // regular window
		span("t1", "tr7", "client", "fe", "", 1, now.Add(-48*time.Hour)),        // too old
		span("t1", "tr8", "server", "orders", "x", 1, now.Add(-12*time.Minute)), // 11:48:20: minutes 11:43..11:48, all < 11:50:20
		span("t1", "tr9", "server", "orders", "x", 1, now.Add(-7*time.Minute)),  // 11:53:20: minutes 11:48..11:53, only 11:48..11:50
		span("t1", "tr10", "server", "orders", "x", 1, now.Add(-time.Minute)),   // 11:59:20: minutes 11:54..11:59, none late
	}
	rows, tooOld := RelinkRows(spans, now, opts)
	if tooOld != 1 {
		t.Errorf("too old %d, want 1", tooOld)
	}
	type r struct {
		tenant string
		minute string
		trace  string
		spans  uint32
	}
	var got []r
	for _, row := range rows {
		if !row.EnqueuedAt.Equal(now) {
			t.Errorf("enqueued_at %v", row.EnqueuedAt)
		}
		got = append(got, r{row.TenantID, row.Minute.Format("15:04"), row.TraceID, row.Spans})
		if n := len(row.Values()); n != len(Columns[TableRelinkQueue]) {
			t.Fatalf("values %d, columns %d", n, len(Columns[TableRelinkQueue]))
		}
	}
	want := []r{
		{"t1", "10:55", "tr3", 1}, {"t1", "10:56", "tr3", 1}, {"t1", "10:57", "tr3", 1}, {"t1", "10:58", "tr3", 1}, {"t1", "10:59", "tr3", 1},
		{"t1", "11:00", "tr3", 1},
		{"t1", "11:43", "tr8", 1}, {"t1", "11:44", "tr8", 1}, {"t1", "11:45", "tr8", 1}, {"t1", "11:46", "tr8", 1}, {"t1", "11:47", "tr8", 1},
		{"t1", "11:48", "tr8", 2}, {"t1", "11:49", "tr9", 1}, {"t1", "11:50", "tr9", 1},
		{"t2", "11:00", "tr1", 2},
	}
	if len(got) != len(want) {
		t.Fatalf("rows %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d: %v, want %v", i, got[i], want[i])
		}
	}
	if rows, _ := RelinkRows(spans, now, RelinkOptions{}); rows != nil {
		t.Errorf("disabled: %v", rows)
	}
}

// A chunk with late spans writes the queue rows after its spans, with the chunk's token.
func TestWriteChunkEnqueuesLateSpans(t *testing.T) {
	w := &fakeWriter{}
	p := newTestProcessor(w, &fakeConsumer{})
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }
	p.SetRelink(RelinkOptions{Enabled: true, After: 10 * time.Minute, MaxAge: 7 * 24 * time.Hour})
	start := uint64(now.Add(-time.Hour).UnixNano())
	client := &tracepb.Span{TraceId: mustHex("5b8efff798038103d269b633813fc60c"), SpanId: mustHex("eee19b7ec3c1b174"), ParentSpanId: mustHex("eee19b7ec3c1b173"),
		Name: "GET", Kind: tracepb.Span_SPAN_KIND_CLIENT, StartTimeUnixNano: start, EndTimeUnixNano: start + 1e6}
	req := &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{resourceSpans("frontend", client)}}
	rec := at(record(t, queue.Topic("openlog", queue.SignalTraces), 0, 7, req, goodHeaders()), now)
	c := &chunk{tp: topicPartition{rec.Topic, 0}, recs: []*kgo.Record{rec}}
	if err := p.writeChunk(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	// usage_ingest_1h (usage.go) is written last for every chunk.
	if len(w.calls) != 3 || w.calls[0] != TableSpans || w.calls[1] != TableRelinkQueue || w.calls[2] != TableUsageIngest {
		t.Fatalf("calls %v", w.calls)
	}
	if tok := w.tokens[TableRelinkQueue]; len(tok) != 1 || tok[0] != c.token(TableRelinkQueue) || w.rows[tok[0]] != 1 {
		t.Errorf("queue tokens %v rows %v", tok, w.rows)
	}
	if v := testutil.ToFloat64(p.relink.enqueued); v != 1 {
		t.Errorf("enqueued metric %v", v)
	}
}
