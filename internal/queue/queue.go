// Package queue wraps Kafka (franz-go) according to docs/contracts/kafka.md.
package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"

	"github.com/onuragtas/openlog/internal/config"
)

// ClientOptions returns the TLS and SASL options every Kafka client of a service must use
// (OPENLOG_KAFKA_TLS_*, OPENLOG_KAFKA_SASL_*; docs/contracts/config.md). Certificate files are
// validated here and re-read on every new broker connection after they change (D-048).
func ClientOptions(c config.Common) ([]kgo.Opt, error) {
	var opts []kgo.Opt
	tr, err := c.KafkaTLS.Reloader()
	if err != nil {
		return nil, fmt.Errorf("kafka tls: %w", err)
	}
	if tr != nil {
		// Like kgo.DialTLSConfig: 10 s timeout, ServerName = each broker's host when not configured.
		opts = append(opts, kgo.Dialer(func(ctx context.Context, network, host string) (net.Conn, error) {
			return tr.DialContext(ctx, 10*time.Second, network, host)
		}))
	}
	s := c.KafkaSASL
	switch s.Mechanism {
	case "":
	case config.SASLPlain:
		opts = append(opts, kgo.SASL(plain.Auth{User: s.Username, Pass: s.Password}.AsMechanism()))
	case config.SASLScramSHA256:
		opts = append(opts, kgo.SASL(scram.Auth{User: s.Username, Pass: s.Password}.AsSha256Mechanism()))
	case config.SASLScramSHA512:
		opts = append(opts, kgo.SASL(scram.Auth{User: s.Username, Pass: s.Password}.AsSha512Mechanism()))
	default:
		return nil, fmt.Errorf("kafka sasl: unsupported mechanism %q", s.Mechanism)
	}
	return opts, nil
}

// Header names and the current schema version.
const (
	HeaderSchemaVersion = "openlog-schema-version"
	HeaderTenantID      = "openlog-tenant-id"
	HeaderReceivedAt    = "openlog-received-at"
	HeaderRequestID     = "openlog-request-id"

	SchemaVersion = "1"

	// MaxMessageBytes is the topic max.message.bytes (12 MiB).
	MaxMessageBytes = 12582912
)

// Signal identifies an OTLP signal.
type Signal string

// Signals.
const (
	SignalMetrics Signal = "metrics"
	SignalLogs    Signal = "logs"
	SignalTraces  Signal = "traces"
)

// AllSignals lists all signals in a stable order.
var AllSignals = []Signal{SignalMetrics, SignalLogs, SignalTraces}

// Topic returns <prefix>.otlp.<signal>.v1.
func Topic(prefix string, s Signal) string {
	return prefix + ".otlp." + string(s) + ".v1"
}

// SampledTracesTopic returns <prefix>.otlp.traces.sampled.v1: traces kept by openlog-sampler (tail sampling, D-075).
func SampledTracesTopic(prefix string) string {
	return prefix + ".otlp.traces.sampled.v1"
}

// ProcessorTopics returns the topics the processor consumes: metrics, logs and the raw traces topic, or the
// sampled traces topic instead when tail sampling is enabled.
func ProcessorTopics(prefix string, tailSampling bool) []string {
	topics := Topics(prefix)
	if tailSampling {
		for i, t := range topics {
			if t == Topic(prefix, SignalTraces) {
				topics[i] = SampledTracesTopic(prefix)
			}
		}
	}
	return topics
}

// Message is a record to produce.
type Message struct {
	Topic      string
	Key        string
	Value      []byte
	TenantID   string
	RequestID  string
	ReceivedAt time.Time
}

// Record converts a Message to a franz-go record with the contract headers.
func (m Message) Record() *kgo.Record {
	return &kgo.Record{
		Topic: m.Topic,
		Key:   []byte(m.Key),
		Value: m.Value,
		Headers: []kgo.RecordHeader{
			{Key: HeaderSchemaVersion, Value: []byte(SchemaVersion)},
			{Key: HeaderTenantID, Value: []byte(m.TenantID)},
			{Key: HeaderReceivedAt, Value: []byte(strconv.FormatInt(m.ReceivedAt.UnixNano(), 10))},
			{Key: HeaderRequestID, Value: []byte(m.RequestID)},
		},
	}
}

// HeaderValue returns the value of header key on r, if present.
func HeaderValue(r *kgo.Record, key string) (string, bool) {
	for _, h := range r.Headers {
		if h.Key == key {
			return string(h.Value), true
		}
	}
	return "", false
}

// Producer produces synchronously (acks=all, idempotent, zstd).
type Producer struct {
	cl *kgo.Client
	// rp produces records; it is cl in production and replaceable in tests.
	rp recordProducer
}

// recordProducer is the asynchronous produce call of *kgo.Client.
type recordProducer interface {
	Produce(ctx context.Context, r *kgo.Record, promise func(*kgo.Record, error))
}

// NewProducer creates a producer. deliveryTimeout bounds how long a record may
// be retried before failing. extra carries ClientOptions (TLS, SASL).
func NewProducer(brokers []string, deliveryTimeout time.Duration, log *slog.Logger, extra ...kgo.Opt) (*Producer, error) {
	opts := []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProducerBatchCompression(kgo.ZstdCompression()),
		kgo.ProducerBatchMaxBytes(MaxMessageBytes),
		kgo.RecordDeliveryTimeout(deliveryTimeout),
		kgo.ProducerLinger(5 * time.Millisecond),
		kgo.WithLogger(newKgoLogger(log)),
	}
	cl, err := kgo.NewClient(append(opts, extra...)...)
	if err != nil {
		return nil, err
	}
	return &Producer{cl: cl, rp: cl}, nil
}

// Produce writes msgs and waits for the broker acknowledgement, but never
// longer than ctx allows.
//
// kgo's ProduceSync does not return on ctx expiry for records that may already
// have been sent: with the idempotent producer, a batch in flight when the broker
// disappears cannot be failed until the connection outcome is known, so the call
// blocks until Kafka is back. Ingest must answer 503 within its produce timeout,
// so the wait is bounded here. Such a record may still be delivered later; the
// client retries and the pipeline is at-least-once (docs/contracts/kafka.md).
func (p *Producer) Produce(ctx context.Context, msgs ...Message) error {
	if len(msgs) == 0 {
		return nil
	}
	// Buffered so that promises firing after we stopped waiting never block the client.
	done := make(chan error, len(msgs))
	for _, m := range msgs {
		p.rp.Produce(ctx, m.Record(), func(_ *kgo.Record, err error) { done <- err })
	}
	var first error
	for range msgs {
		select {
		case err := <-done:
			if err != nil && first == nil {
				first = err
			}
		case <-ctx.Done():
			return fmt.Errorf("produce: waiting for acknowledgement: %w", ctx.Err())
		}
	}
	return first
}

// Ping checks broker connectivity.
func (p *Producer) Ping(ctx context.Context) error { return p.cl.Ping(ctx) }

// Client returns the underlying Kafka client (metadata / admin requests).
func (p *Producer) Client() *kgo.Client { return p.cl }

// Close flushes pending records (bounded by ctx) and closes the client.
func (p *Producer) Close(ctx context.Context) {
	_ = p.cl.Flush(ctx)
	p.cl.Close()
}

// Consumer fetch buffering limits. franz-go decompresses every fetch response into
// records as soon as it arrives and, by default, keeps one in-flight fetch per broker.
// FetchMaxBytes counts compressed bytes; zstd OTLP expands ~5x (measured on logs), so
// with the former 64 MiB x 3 brokers a processor starting on a consumer-lag backlog
// buffered > 1 GiB before its first insert and was OOM/liveness-killed in a loop.
// 13 MiB x 2 bounds that to ~130 MiB decoded while one fetch still carries far more rows
// than a processor inserts per second. FetchMaxBytes must stay >= max.message.bytes:
// franz-go clamps FetchMaxPartitionBytes to it, and one maximum-size record must fit.
const (
	ConsumerFetchMaxBytes        = MaxMessageBytes + (1 << 20)
	ConsumerMaxConcurrentFetches = 2
)

// NewConsumerClient creates a consumer group client for all signal topics.
// Auto-commit is disabled and rebalances are blocked while polled records are
// being processed; the caller must call AllowRebalance after committing.
func NewConsumerClient(brokers []string, group, prefix string, log *slog.Logger, extra ...kgo.Opt) (*kgo.Client, error) {
	return NewConsumerClientTopics(brokers, group, Topics(prefix), log, extra...)
}

// NewConsumerClientTopics is NewConsumerClient for an explicit topic list.
func NewConsumerClientTopics(brokers []string, group string, topics []string, log *slog.Logger, extra ...kgo.Opt) (*kgo.Client, error) {
	opts := []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(topics...),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
		kgo.FetchMaxBytes(ConsumerFetchMaxBytes),
		kgo.MaxConcurrentFetches(ConsumerMaxConcurrentFetches),
		kgo.FetchMaxPartitionBytes(MaxMessageBytes + (1 << 20)),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.WithLogger(newKgoLogger(log)),
	}
	return kgo.NewClient(append(opts, extra...)...)
}

// TopicSettings controls topic creation.
type TopicSettings struct {
	Prefix            string
	Partitions        int
	ReplicationFactor int
	MinInsyncReplicas int
	RetentionMs       int64
	MaxMessageBytes   int64
}

// TopicConfigs returns the topic-level configs applied on creation.
func (ts TopicSettings) TopicConfigs() map[string]*string {
	minISR := strconv.Itoa(ts.MinInsyncReplicas)
	retention := strconv.FormatInt(ts.RetentionMs, 10)
	maxBytes := strconv.FormatInt(ts.MaxMessageBytes, 10)
	return map[string]*string{
		"min.insync.replicas": &minISR,
		"retention.ms":        &retention,
		"max.message.bytes":   &maxBytes,
	}
}

// CreateTopics creates the signal topics with the configured partitions,
// replication factor and topic configs. Existing topics are left untouched
// (neither their partition count nor their configs are changed). extra carries ClientOptions.
func CreateTopics(ctx context.Context, brokers []string, ts TopicSettings, extra ...kgo.Opt) error {
	cl, err := kgo.NewClient(append([]kgo.Opt{kgo.SeedBrokers(brokers...)}, extra...)...)
	if err != nil {
		return err
	}
	defer cl.Close()
	adm := kadm.NewClient(cl)
	configs := ts.TopicConfigs()
	var topics []string
	for _, s := range AllSignals {
		topics = append(topics, Topic(ts.Prefix, s))
	}
	// Created even while tail sampling is off, so enabling it needs no topic migration.
	topics = append(topics, SampledTracesTopic(ts.Prefix))
	resp, err := adm.CreateTopics(ctx, int32(ts.Partitions), int16(ts.ReplicationFactor), configs, topics...)
	if err != nil {
		return err
	}
	var errs []error
	for _, t := range resp.Sorted() {
		if t.Err != nil && !errors.Is(t.Err, kerr.TopicAlreadyExists) {
			errs = append(errs, fmt.Errorf("create topic %s: %w", t.Topic, t.Err))
		}
	}
	return errors.Join(errs...)
}

// kgoLogger adapts slog to franz-go's logger, only surfacing warnings and errors.
type kgoLogger struct{ l *slog.Logger }

func newKgoLogger(l *slog.Logger) kgo.Logger {
	if l == nil {
		l = slog.Default()
	}
	return kgoLogger{l.With("component", "kafka")}
}

func (k kgoLogger) Level() kgo.LogLevel { return kgo.LogLevelWarn }

func (k kgoLogger) Log(level kgo.LogLevel, msg string, keyvals ...any) {
	switch level {
	case kgo.LogLevelError:
		k.l.Error(msg, keyvals...)
	case kgo.LogLevelWarn:
		k.l.Warn(msg, keyvals...)
	default:
		k.l.Debug(msg, keyvals...)
	}
}
