package exporter

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

type recorder struct {
	mu       sync.Mutex
	statuses []int
	headers  []http.Header
	bodies   [][]byte
	paths    []string
	retryAft string
}

func (r *recorder) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		zr, err := gzip.NewReader(req.Body)
		if err != nil {
			t.Errorf("body not gzip: %v", err)
			return
		}
		b, _ := io.ReadAll(zr)
		r.bodies = append(r.bodies, b)
		r.headers = append(r.headers, req.Header.Clone())
		r.paths = append(r.paths, req.URL.Path)
		status := http.StatusOK
		if len(r.statuses) > 0 {
			status = r.statuses[0]
			r.statuses = r.statuses[1:]
		}
		if r.retryAft != "" && status != http.StatusOK {
			w.Header().Set("Retry-After", r.retryAft)
		}
		w.WriteHeader(status)
	}
}

func newTest(t *testing.T, rec *recorder) (*Exporter, *[]time.Duration) {
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)
	e := New(srv.URL+"/", "key-123", "1.2.3", 5*time.Second)
	var sleeps []time.Duration
	e.Sleep = func(_ context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil }
	return e, &sleeps
}

func TestSendHeadersAndPath(t *testing.T) {
	rec := &recorder{}
	e, _ := newTest(t, rec)
	if err := e.Send(context.Background(), SignalLogs, []byte("payload")); err != nil {
		t.Fatal(err)
	}
	h := rec.headers[0]
	if rec.paths[0] != "/v1/logs" || h.Get(LicenseHeader) != "key-123" || h.Get("Content-Encoding") != "gzip" ||
		h.Get("Content-Type") != "application/x-protobuf" || h.Get("User-Agent") != "openlog-infra-agent/1.2.3" || string(rec.bodies[0]) != "payload" {
		t.Errorf("request = %s %v %q", rec.paths[0], h, rec.bodies[0])
	}
}

func TestRetryHonorsRetryAfter(t *testing.T) {
	rec := &recorder{statuses: []int{503, 429, 200}, retryAft: "2"}
	e, sleeps := newTest(t, rec)
	if err := e.Send(context.Background(), SignalMetrics, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if len(rec.bodies) != 3 || len(*sleeps) != 2 || (*sleeps)[0] != 2*time.Second {
		t.Errorf("attempts=%d sleeps=%v", len(rec.bodies), *sleeps)
	}
}

func TestBackoffWithoutRetryAfter(t *testing.T) {
	rec := &recorder{statuses: []int{502, 504, 502}}
	e, sleeps := newTest(t, rec)
	err := e.Send(context.Background(), SignalMetrics, []byte("x"))
	if !IsRetryable(err) || len(rec.bodies) != 3 {
		t.Fatalf("err=%v attempts=%d", err, len(rec.bodies))
	}
	s := *sleeps
	if len(s) != 2 || s[0] < 500*time.Millisecond || s[0] >= time.Second || s[1] < time.Second || s[1] >= 2*time.Second {
		t.Errorf("sleeps = %v", s)
	}
}

func TestLongRetryAfterReturnsForBuffering(t *testing.T) {
	rec := &recorder{statuses: []int{429}, retryAft: "3600"}
	e, sleeps := newTest(t, rec)
	err := e.Send(context.Background(), SignalMetrics, []byte("x"))
	if !IsRetryable(err) || len(rec.bodies) != 1 || len(*sleeps) != 0 {
		t.Errorf("err=%v attempts=%d sleeps=%v", err, len(rec.bodies), *sleeps)
	}
}

func TestPermanentFailure(t *testing.T) {
	rec := &recorder{statuses: []int{401}}
	e, _ := newTest(t, rec)
	err := e.Send(context.Background(), SignalMetrics, []byte("x"))
	if err == nil || IsRetryable(err) || len(rec.bodies) != 1 {
		t.Errorf("err=%v attempts=%d", err, len(rec.bodies))
	}
}

func TestTransportErrorRetryable(t *testing.T) {
	e := New("http://127.0.0.1:1", "k", "v", time.Second)
	e.MaxAttempts = 1
	if err := e.Send(context.Background(), SignalLogs, []byte("x")); !IsRetryable(err) {
		t.Errorf("err = %v", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if ParseRetryAfter("7", now) != 7*time.Second {
		t.Error("seconds")
	}
	if d := ParseRetryAfter(now.Add(90*time.Second).Format(http.TimeFormat), now); d != 90*time.Second {
		t.Errorf("date = %v", d)
	}
	if ParseRetryAfter("garbage", now) != 0 || ParseRetryAfter("", now) != 0 {
		t.Error("invalid")
	}
}

func TestSplitLogsKeepsSnapshotRecordLast(t *testing.T) {
	sl := &logspb.ScopeLogs{Scope: &commonpb.InstrumentationScope{Name: "s"}}
	body := strings.Repeat("x", 1000)
	for i := 0; i < 500; i++ {
		sl.LogRecords = append(sl.LogRecords, &logspb.LogRecord{Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: body}}})
	}
	complete := &logspb.LogRecord{Attributes: []*commonpb.KeyValue{{Key: "event.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "openlog.inventory.snapshot"}}}}}
	sl.LogRecords = append(sl.LogRecords, complete)
	data := &logspb.LogsData{ResourceLogs: []*logspb.ResourceLogs{{
		Resource:  &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{Key: "host.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "h"}}}}},
		ScopeLogs: []*logspb.ScopeLogs{sl},
	}}}
	const limit = 64 << 10
	parts := SplitLogs(data, limit)
	if len(parts) < 7 {
		t.Fatalf("parts = %d", len(parts))
	}
	total := 0
	for i, p := range parts {
		if sz := proto.Size(p); sz > limit {
			t.Errorf("part %d size %d > %d", i, sz, limit)
		}
		if p.ResourceLogs[0].Resource.Attributes[0].Key != "host.id" {
			t.Error("resource missing in part")
		}
		total += len(p.ResourceLogs[0].ScopeLogs[0].LogRecords)
	}
	if total != 501 {
		t.Errorf("records = %d", total)
	}
	lastRecs := parts[len(parts)-1].ResourceLogs[0].ScopeLogs[0].LogRecords
	if !proto.Equal(lastRecs[len(lastRecs)-1], complete) {
		t.Error("snapshot-complete record must be last")
	}
	if got := SplitLogs(&logspb.LogsData{}, limit); len(got) != 1 {
		t.Error("small payload must not split")
	}
}
