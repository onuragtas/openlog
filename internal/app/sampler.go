package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/admin"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/queue"
	"github.com/onuragtas/openlog/internal/tailsampling"
)

// RunSampler runs the tail sampling stage (D-075): raw traces topic → per-trace buffer → policy decision →
// sampled traces topic. Policies come from PostgreSQL (postgres auth mode) with
// OPENLOG_TAILSAMPLING_DEFAULT_POLICY for tenants without one.
func RunSampler(ctx context.Context, cfg config.Config, adm *admin.Server, log *slog.Logger) error {
	log = log.With("component", "sampler")
	ts := cfg.TailSampling
	def := tailsampling.KeepAll()
	if ts.DefaultPolicy != "" {
		p, err := tailsampling.ParsePolicy([]byte(ts.DefaultPolicy))
		if err != nil {
			return fmt.Errorf("OPENLOG_TAILSAMPLING_DEFAULT_POLICY: %w", err)
		}
		def = p
	}
	m := tailsampling.NewMetrics(adm.Registry())
	cache := &tailsampling.PolicyCache{Default: def, Interval: ts.PolicyRefresh, Log: log, Metrics: m}
	if cfg.AuthMode == "postgres" {
		var pool *pgxpool.Pool
		pool, err := OpenPostgres(ctx, cfg, "openlog-sampler")
		if err != nil {
			return err
		}
		defer pool.Close()
		cache.Load = tailsampling.PGStore{Pool: pool}.LoadAll
		// Start without waiting for PostgreSQL: until the first load, the default policy applies.
		if err := cache.Refresh(ctx); err != nil {
			log.Warn("tail sampling policies not loaded yet, using the default policy", "err", err)
		}
		go cache.Run(ctx)
	}
	kopts, err := queue.ClientOptions(cfg.Common)
	if err != nil {
		return err
	}
	prod, err := queue.NewProducer(cfg.KafkaBrokers, ts.ProduceTimeout, log, kopts...)
	if err != nil {
		return err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		prod.Close(cctx)
		cancel()
	}()
	eng := tailsampling.NewEngine(tailsampling.Options{
		DecisionWait: ts.DecisionWait, MaxTraces: ts.MaxTraces, MaxSpansPerTrace: ts.MaxSpansPerTrace,
		MaxBufferedBytes: ts.MaxBufferedBytes, DecisionCacheTTL: ts.DecisionCacheTTL, DecisionCacheSize: ts.DecisionCacheSize,
	}, cache, m)
	runner := tailsampling.NewRunner(tailsampling.RunnerOptions{
		Prefix: cfg.KafkaTopicPrefix, ProduceTimeout: ts.ProduceTimeout, ShutdownTimeout: ShutdownTimeout,
	}, eng, m, prod, log)
	raw := queue.Topic(cfg.KafkaTopicPrefix, queue.SignalTraces)
	cl, err := queue.NewConsumerClientTopics(cfg.KafkaBrokers, ts.Group, []string{raw}, log, append(kopts, runner.KafkaOpts()...)...)
	if err != nil {
		return err
	}
	defer cl.Close() // leaves the group; the revoke callback finds nothing buffered after Run
	runner.SetConsumer(cl)
	adm.AddCheck("kafka", prod.Ping)
	adm.AddCheck("kafka_consumer", cl.Ping)
	topics := queue.NewTopicWatcher(cl, []string{raw, queue.SampledTracesTopic(cfg.KafkaTopicPrefix)}, log)
	go topics.Run(ctx)
	adm.AddCheck("kafka_topics", topics.Check)
	go runner.RunLagMonitor(ctx, 15*time.Second, func(ctx context.Context) ([]queue.PartitionLag, error) {
		return queue.MemberLag(ctx, cl, ts.Group)
	})
	log.Info("tail sampler started", "group", ts.Group, "decision_wait", ts.DecisionWait.String(), "max_traces", ts.MaxTraces)
	return runner.Run(ctx)
}
