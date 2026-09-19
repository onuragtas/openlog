package queue

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
)

func TestTopicWatcherCachesResult(t *testing.T) {
	var calls atomic.Int32
	var missing atomic.Value
	missing.Store([]string{"openlog.otlp.traces.v1"})
	w := newTopicWatcher(Topics("openlog"), func(context.Context, []string) ([]string, error) {
		calls.Add(1)
		return missing.Load().([]string), nil
	}, nil)

	if err := w.Check(context.Background()); err == nil {
		t.Fatal("not ready before the first check")
	}
	_ = w.Refresh(context.Background())
	for range 100 { // readiness probes
		err := w.Check(context.Background())
		if err == nil || !strings.Contains(err.Error(), "openlog.otlp.traces.v1") {
			t.Fatalf("err = %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("probes hit kafka: %d list calls", calls.Load())
	}
	missing.Store([]string(nil))
	_ = w.Refresh(context.Background())
	if err := w.Check(context.Background()); err != nil {
		t.Errorf("ready after topics appear: %v", err)
	}

	w.list = func(context.Context, []string) ([]string, error) { return nil, errors.New("dial tcp: refused") }
	_ = w.Refresh(context.Background())
	if err := w.Check(context.Background()); err == nil {
		t.Error("kafka unreachable must be not ready")
	}
}

func TestMissingTopics(t *testing.T) {
	topics := Topics("p")
	details := kadm.TopicDetails{
		"p.otlp.metrics.v1": {Topic: "p.otlp.metrics.v1", Partitions: kadm.PartitionDetails{0: {}}},
		"p.otlp.logs.v1":    {Topic: "p.otlp.logs.v1", Err: kerr.UnknownTopicOrPartition},
	}
	missing, err := missingTopics(details, topics)
	if err != nil || len(missing) != 3 || missing[0] != "p.otlp.logs.v1" || missing[1] != "p.otlp.traces.v1" ||
		missing[2] != "p.otlp.profiles.v1" {
		t.Errorf("missing %v err %v", missing, err)
	}
}
