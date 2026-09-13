package queue

import (
	"io"
	"log/slog"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
)

// The consumer must bound how much fetched (and decompressed) data it buffers, so a
// processor starting on a large consumer-lag backlog does not exhaust its memory.
func TestConsumerClientBoundsFetchBuffering(t *testing.T) {
	cl, err := NewConsumerClient([]string{"127.0.0.1:1"}, "g", "openlog", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()

	if got := cl.OptValue(kgo.FetchMaxBytes).(int32); got <= 0 || got > 16<<20 {
		t.Errorf("FetchMaxBytes = %d, want a bound <= 16 MiB", got)
	}
	if got := cl.OptValue(kgo.MaxConcurrentFetches).(int); got <= 0 || got > 8 {
		t.Errorf("MaxConcurrentFetches = %d, want a small positive bound (<= 0 means unlimited)", got)
	}
	// A single record may be as large as max.message.bytes and must still be fetchable.
	if got := cl.OptValue(kgo.FetchMaxPartitionBytes).(int32); got < MaxMessageBytes {
		t.Errorf("FetchMaxPartitionBytes = %d, want >= MaxMessageBytes (%d)", got, MaxMessageBytes)
	}
}
