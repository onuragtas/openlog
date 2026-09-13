package processor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/onuragtas/openlog/internal/config"
)

// A shutdown that cancels an in-flight insert is not an insert failure: it must not be
// counted or logged at ERROR (the mixed-version test treats ERROR lines as failures).
func TestWriteChunkCancelledIsNotAFailure(t *testing.T) {
	var logs bytes.Buffer
	cfg := config.Processor{BatchRows: 1000, FlushInterval: time.Second, InsertTimeout: time.Second,
		BatchBytes: 1 << 20, MaxBufferedBytes: 64 << 20, InsertConcurrency: 4}
	ctx, cancel := context.WithCancel(context.Background())
	w := &cancellingWriter{cancel: cancel}
	p := New(cfg, "openlog", &fakeConsumer{}, w, slog.New(slog.NewTextHandler(&logs, nil)), prometheus.NewRegistry())

	c := &chunk{tp: topicPartition{topic: lt, partition: 0},
		recs: []*kgo.Record{record(t, lt, 0, 5, logsMsg(), goodHeaders())}}
	err := p.writeChunk(ctx, c)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("writeChunk error = %v, want context.Canceled", err)
	}
	if n := testutil.ToFloat64(p.failures); n != 0 {
		t.Errorf("failures counter = %v, want 0", n)
	}
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("unexpected ERROR log:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "insert cancelled") {
		t.Errorf("missing cancellation log:\n%s", logs.String())
	}
}

// cancellingWriter simulates shutdown during an insert: it cancels the context and fails.
type cancellingWriter struct{ cancel context.CancelFunc }

func (w *cancellingWriter) Write(ctx context.Context, _, _ string, _ []string, _ [][]any) error {
	w.cancel()
	return ctx.Err()
}
