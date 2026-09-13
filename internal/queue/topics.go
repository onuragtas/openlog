package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Topics returns the three signal topics for prefix.
func Topics(prefix string) []string {
	out := make([]string, 0, len(AllSignals))
	for _, s := range AllSignals {
		out = append(out, Topic(prefix, s))
	}
	return out
}

// TopicWatcher caches whether the signal topics exist (D-019). Readiness probes
// read the cached result; Kafka is asked only every CheckInterval (RetryInterval
// while topics are missing or Kafka is unreachable).
type TopicWatcher struct {
	topics []string
	list   func(ctx context.Context, topics []string) (missing []string, err error)
	log    *slog.Logger

	CheckInterval time.Duration
	RetryInterval time.Duration
	Timeout       time.Duration

	mu      sync.RWMutex
	err     error
	checked bool
}

// NewTopicWatcher watches topics through cl (metadata requests never auto-create topics).
func NewTopicWatcher(cl *kgo.Client, topics []string, log *slog.Logger) *TopicWatcher {
	adm := kadm.NewClient(cl)
	return newTopicWatcher(topics, func(ctx context.Context, topics []string) ([]string, error) {
		details, err := adm.ListTopics(ctx, topics...)
		if err != nil {
			return nil, err
		}
		return missingTopics(details, topics)
	}, log)
}

func newTopicWatcher(topics []string, list func(context.Context, []string) ([]string, error), log *slog.Logger) *TopicWatcher {
	return &TopicWatcher{
		topics: topics, list: list, log: log,
		CheckInterval: 30 * time.Second, RetryInterval: 5 * time.Second, Timeout: 10 * time.Second,
		err: errors.New("topics not checked yet"),
	}
}

func missingTopics(details kadm.TopicDetails, topics []string) ([]string, error) {
	var missing []string
	var errs []error
	for _, t := range topics {
		d, ok := details[t]
		switch {
		case !ok || errors.Is(d.Err, kerr.UnknownTopicOrPartition) || (d.Err == nil && len(d.Partitions) == 0):
			missing = append(missing, t)
		case d.Err != nil:
			errs = append(errs, fmt.Errorf("topic %s: %w", t, d.Err))
		}
	}
	return missing, errors.Join(errs...)
}

// Refresh checks the topics once and stores the result.
func (w *TopicWatcher) Refresh(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, w.Timeout)
	defer cancel()
	missing, err := w.list(ctx, w.topics)
	if err == nil && len(missing) > 0 {
		sort.Strings(missing)
		err = fmt.Errorf("missing kafka topics: %s", strings.Join(missing, ", "))
	}
	w.mu.Lock()
	changed := !w.checked || (w.err == nil) != (err == nil)
	w.err, w.checked = err, true
	w.mu.Unlock()
	if changed && w.log != nil {
		if err != nil {
			w.log.Warn("kafka topics not ready", "err", err)
		} else {
			w.log.Info("kafka topics present", "topics", strings.Join(w.topics, ","))
		}
	}
	return err
}

// Run refreshes until ctx is done.
func (w *TopicWatcher) Run(ctx context.Context) {
	for {
		interval := w.CheckInterval
		if w.Refresh(ctx) != nil {
			interval = w.RetryInterval
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// Check returns the cached result (an admin.CheckFunc).
func (w *TopicWatcher) Check(context.Context) error {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.err
}

// PartitionLag is the lag of one partition assigned to this group member:
// log end offset minus committed offset.
type PartitionLag struct {
	Topic     string
	Partition int32
	Lag       int64
}

// MemberLag returns the lag of the partitions assigned to cl's member of group.
// It returns nothing while cl is not in the group.
func MemberLag(ctx context.Context, cl *kgo.Client, group string) ([]PartitionLag, error) {
	member, _ := cl.GroupMetadata()
	if member == "" {
		return nil, nil
	}
	lags, err := kadm.NewClient(cl).Lag(ctx, group)
	if err != nil {
		return nil, err
	}
	gl, ok := lags[group]
	if !ok {
		return nil, fmt.Errorf("group %s not described", group)
	}
	if err := gl.Error(); err != nil {
		return nil, err
	}
	var out []PartitionLag
	for _, parts := range gl.Lag {
		for _, pl := range parts {
			if pl.Member == nil || pl.Member.MemberID != member || pl.Lag < 0 {
				continue
			}
			out = append(out, PartitionLag{Topic: pl.Topic, Partition: pl.Partition, Lag: pl.Lag})
		}
	}
	return out, nil
}
