// Package app wires services together for the cmd binaries. Each Run*
// function blocks until ctx is cancelled and then shuts its service down
// gracefully.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/admin"
	"github.com/onuragtas/openlog/internal/api"
	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/apm"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/ingest"
	"github.com/onuragtas/openlog/internal/logging"
	"github.com/onuragtas/openlog/internal/processor"
	"github.com/onuragtas/openlog/internal/queue"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
	"github.com/onuragtas/openlog/internal/store/postgres"
	"github.com/onuragtas/openlog/internal/tenant"
	"github.com/onuragtas/openlog/web"
)

// ShutdownTimeout bounds graceful shutdown of HTTP/gRPC servers.
const ShutdownTimeout = 25 * time.Second

// Main is the common entry point: parse config, set up logging, admin server
// and signal handling, then call run.
func Main(service string, run func(ctx context.Context, cfg config.Config, adm *admin.Server, log *slog.Logger) error) {
	cfg, err := config.FromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: configuration error: %v\n", service, err)
		os.Exit(2)
	}
	log := logging.New(cfg.LogLevel, service)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	adm := admin.New(cfg.AdminAddr, log)
	if err := adm.Start(); err != nil {
		log.Error("cannot start admin server", "err", err)
		os.Exit(1)
	}
	go func() {
		<-ctx.Done()
		log.Info("shutdown signal received")
		adm.SetDraining()
	}()

	stopHeartbeat := startHeartbeat(cfg, service, log)
	err = run(ctx, cfg, adm, log)
	stopHeartbeat()
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = adm.Shutdown(sctx)
	cancel()
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Error("service failed", "err", err)
		os.Exit(1)
	}
	log.Info("stopped")
}

func staticResolver(cfg config.Config, log *slog.Logger) (*tenant.Static, error) {
	r, err := tenant.ParseStatic(cfg.LicenseKeys)
	if err != nil {
		return nil, err
	}
	log.Warn("OPENLOG_AUTH_MODE=static: license keys from OPENLOG_LICENSE_KEYS, no user accounts (development and tests only)")
	if r.Len() == 0 {
		log.Warn("OPENLOG_LICENSE_KEYS is empty: every request will be rejected with 401")
	}
	return r, nil
}

// OpenPostgres creates the PostgreSQL pool for service (it does not connect yet).
func OpenPostgres(ctx context.Context, cfg config.Config, service string) (*pgxpool.Pool, error) {
	return postgres.Open(ctx, postgres.Options{
		DSN: cfg.Postgres.DSN, Password: cfg.Postgres.Password, MaxConns: int32(cfg.Postgres.MaxConns), Application: service,
		TLSCAFile: cfg.Postgres.TLS.CAFile, TLSCertFile: cfg.Postgres.TLS.CertFile, TLSKeyFile: cfg.Postgres.TLS.KeyFile,
	})
}

// MigratePostgres waits for PostgreSQL and applies migrations/postgres (expand migrations, and
// contract migrations once every live instance is new enough; see migrate_phases.go).
func MigratePostgres(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	return migratePostgres(ctx, pool, log, MigrateOptions{})
}

// RunIngest runs the OTLP receiver.
func RunIngest(ctx context.Context, cfg config.Config, adm *admin.Server, log *slog.Logger) error {
	log = log.With("component", "ingest")
	var (
		res    tenant.Resolver
		pgPool *pgxpool.Pool // nil in static mode
	)
	if cfg.AuthMode == "static" {
		r, err := staticResolver(cfg, log)
		if err != nil {
			return err
		}
		res = r
	} else {
		// No startup wait and no readiness check on PostgreSQL: ingest keeps
		// accepting cached keys while PostgreSQL is briefly unavailable.
		pool, err := OpenPostgres(ctx, cfg, "openlog-ingest")
		if err != nil {
			return err
		}
		defer pool.Close()
		pgPool = pool
		cached := tenant.NewCached(postgres.NewStore(pool), tenant.CacheOptions{
			TTL: cfg.AuthCache.TTL, NegativeTTL: cfg.AuthCache.NegativeTTL, MaxStale: cfg.AuthCache.MaxStale,
			Registerer: adm.Registry(), Log: log,
		})
		done := make(chan struct{})
		defer func() { <-done }() // final last_used_at flush before the pool closes
		go func() { defer close(done); cached.Run(ctx) }()
		res = cached
	}
	kopts, err := queue.ClientOptions(cfg.Common)
	if err != nil {
		return err
	}
	prod, err := queue.NewProducer(cfg.KafkaBrokers, cfg.Ingest.ProduceTimeout, log, kopts...)
	if err != nil {
		return err
	}
	adm.AddCheck("kafka", prod.Ping)
	// D-019: not ready until the signal topics exist (cached, refreshed in the background).
	topics := queue.NewTopicWatcher(prod.Client(), queue.Topics(cfg.KafkaTopicPrefix), log)
	go topics.Run(ctx)
	adm.AddCheck("kafka_topics", topics.Check)
	svc := ingest.New(cfg.Ingest, cfg.KafkaTopicPrefix, res, prod, log, adm.Registry())
	// Agent sync + release mirror (internal/fleet); flushes queued reports before the pool closes.
	defer startFleetIngest(ctx, cfg, pgPool, res, svc, adm.Registry(), log)()
	err = svc.Run(ctx, ShutdownTimeout)
	cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	prod.Close(cctx)
	cancel()
	return err
}

func openClickHouse(ctx context.Context, cfg config.Config, database string, log *slog.Logger) (clickhouse.Conn, error) {
	o := clickhouse.OptionsFromConfig(cfg.Common)
	o.Database = database
	return clickhouse.OpenRetry(ctx, o, func(err error) {
		log.Warn("waiting for clickhouse", "err", err)
	})
}

// RunProcessor runs the Kafka → ClickHouse consumer.
func RunProcessor(ctx context.Context, cfg config.Config, adm *admin.Server, log *slog.Logger) error {
	log = log.With("component", "processor")
	conn, err := openClickHouse(ctx, cfg, cfg.ClickHouseDatabase, log)
	if err != nil {
		return err
	}
	defer conn.Close()
	adm.AddCheck("clickhouse", conn.Ping)
	var writer processor.Writer = processor.ClickHouseWriter{Conn: conn, Database: cfg.ClickHouseDatabase}
	if cfg.Processor.InsertMode == config.InsertModeDirect {
		sw, err := processor.OpenDirectWriter(ctx, conn, cfg, log, adm.Registry())
		if err != nil {
			return err
		}
		defer sw.Close()
		go sw.Run(ctx)
		adm.AddCheck("clickhouse_topology", sw.Check)
		writer = processor.DirectWriter{W: sw}
	}
	events := processor.NewPartitionEvents()
	kopts, err := queue.ClientOptions(cfg.Common)
	if err != nil {
		return err
	}
	cl, err := queue.NewConsumerClient(cfg.KafkaBrokers, cfg.Processor.Group, cfg.KafkaTopicPrefix, log, append(kopts, events.KafkaOpts()...)...)
	if err != nil {
		return err
	}
	adm.AddCheck("kafka_consumer", cl.Ping)
	defer cl.Close() // leaves the consumer group
	topics := queue.NewTopicWatcher(cl, queue.Topics(cfg.KafkaTopicPrefix), log)
	go topics.Run(ctx)
	adm.AddCheck("kafka_topics", topics.Check)
	p := processor.New(cfg.Processor, cfg.KafkaTopicPrefix, cl, writer, log, adm.Registry())
	p.SetPartitionEvents(events)
	go p.RunLagMonitor(ctx, 15*time.Second, func(ctx context.Context) ([]queue.PartitionLag, error) {
		return queue.MemberLag(ctx, cl, cfg.Processor.Group)
	})
	log.Info("processor started", "group", cfg.Processor.Group, "insert_mode", cfg.Processor.InsertMode)
	return p.Run(ctx)
}

// RunAPI runs the query API.
func RunAPI(ctx context.Context, cfg config.Config, adm *admin.Server, log *slog.Logger) error {
	log = log.With("component", "api")
	var (
		authn  auth.Authenticator
		svc    *auth.Service
		pgPool *pgxpool.Pool // nil in static mode
	)
	if cfg.AuthMode == "static" {
		r, err := staticResolver(cfg, log)
		if err != nil {
			return err
		}
		authn = auth.StaticAuthenticator{Resolver: r}
	} else {
		a := cfg.API.Auth
		proxies, err := auth.ParseCIDRs(a.TrustedProxies)
		if err != nil {
			return fmt.Errorf("OPENLOG_API_TRUSTED_PROXIES: %w", err)
		}
		pool, err := OpenPostgres(ctx, cfg, "openlog-api")
		if err != nil {
			return err
		}
		defer pool.Close()
		if err := postgres.WaitReady(ctx, pool, log); err != nil {
			return err
		}
		adm.AddCheck("postgres", pool.Ping)
		pgPool = pool
		store := postgres.NewStore(pool)
		svc = auth.NewService(store, auth.Config{
			SessionTTL: a.SessionTTL, SessionIdleTimeout: a.SessionIdleTimeout,
			CookieSecure: a.CookieSecure, CookieDomain: a.CookieDomain, SignupEnabled: a.SignupEnabled,
			LoginMaxFailures: a.LoginMaxFailures, LoginWindow: a.LoginWindow, InvitationTTL: a.InvitationTTL,
			TrustedProxies: proxies,
		}, log)
		if !a.CookieSecure {
			log.Warn("OPENLOG_COOKIE_SECURE=false: session cookies are sent over plain HTTP (development only)")
		}
		go cleanupLoop(ctx, store, log)
		authn = svc
	}
	conn, err := openClickHouse(ctx, cfg, cfg.ClickHouseDatabase, log)
	if err != nil {
		return err
	}
	defer conn.Close()
	db := query.New(conn, cfg.ClickHouseDatabase, cfg.API.QueryTimeout)
	adm.AddCheck("clickhouse", db.Ping)
	srv := api.New(cfg.API, db, authn, log, adm.Registry())
	if svc != nil {
		srv.SetAccounts(svc)
	}
	var apmSettings apm.SettingsStore // nil in static mode: default Apdex T
	if pgPool != nil {
		apmSettings = apm.PGSettings{Pool: pgPool}
	}
	srv.SetAPM(apmSettings, cfg.APM.DefaultApdexT)
	if err := startAlertAPI(cfg, pgPool, srv, apmSettings, log); err != nil { // alert.go
		return err
	}
	var apmLinker func(ctx context.Context)
	if cfg.APM.LinkEnabled {
		apmLinker = apm.NewLinker(conn, apm.LinkerOptions{
			Database: cfg.ClickHouseDatabase, Cluster: cfg.ClickHouseCluster, Conn: clickhouse.OptionsFromConfig(cfg.Common),
			Interval: cfg.APM.LinkInterval, Lookback: cfg.APM.LinkLookback, Delay: cfg.APM.LinkDelay,
		}, log.With("job", "apm-link"), adm.Registry()).Run
	}
	fleetController := startFleetAPI(ctx, cfg, pgPool, srv, adm.Registry(), log)
	startLeaderTasks(ctx, cfg, pgPool, srv, log, fleetController, apmLinker)
	if cfg.API.UIEnabled {
		srv.SetUI(web.Handler(web.Dist()))
		log.Info("web UI enabled", "path", "/")
	}
	return srv.Run(ctx, cfg.API.HTTPAddr, ShutdownTimeout)
}

// cleanupLoop deletes ended sessions and old login failures hourly.
func cleanupLoop(ctx context.Context, store *postgres.Store, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cctx, cancel := context.WithTimeout(ctx, time.Minute)
			if sessions, failures, err := store.Cleanup(cctx, time.Now()); err != nil {
				log.Warn("auth cleanup failed", "err", err)
			} else if sessions+failures > 0 {
				log.Info("auth cleanup", "sessions_deleted", sessions, "login_failures_deleted", failures)
			}
			cancel()
		}
	}
}

// RunMigrate applies the PostgreSQL schema (postgres auth mode), then the
// ClickHouse schema, and creates Kafka topics, retrying until dependencies are
// reachable or ctx is done.
func RunMigrate(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	return RunMigrateWith(ctx, cfg, log, MigrateOptions{})
}

// RunMigrateWith is RunMigrate with openlog-migrate's options (-plan, -force-contract).
func RunMigrateWith(ctx context.Context, cfg config.Config, log *slog.Logger, opts MigrateOptions) error {
	log = log.With("component", "migrate")
	if cfg.AuthMode == "postgres" || cfg.Postgres.DSN != "" {
		pool, err := OpenPostgres(ctx, cfg, "openlog-migrate")
		if err != nil {
			return err
		}
		err = migratePostgres(ctx, pool, log, opts)
		pool.Close()
		if err != nil {
			return err
		}
	}
	if cfg.ClickHouseDatabase != "openlog" {
		return fmt.Errorf("OPENLOG_CLICKHOUSE_DATABASE=%q is not supported by the M0 schema (must be \"openlog\")", cfg.ClickHouseDatabase)
	}
	conn, err := openClickHouse(ctx, cfg, "default", log)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := migrateClickHouse(ctx, cfg, conn, log, opts); err != nil {
		return err
	}
	if opts.Plan {
		return nil
	}
	if cfg.Migrate.SkipKafka {
		log.Info("skipping kafka topic creation")
		return nil
	}
	ts := queue.TopicSettings{
		Prefix:            cfg.KafkaTopicPrefix,
		Partitions:        cfg.Migrate.KafkaPartitions,
		ReplicationFactor: cfg.Migrate.KafkaReplicationFactor,
		MinInsyncReplicas: cfg.Migrate.KafkaMinInsyncReplicas,
		RetentionMs:       cfg.Migrate.KafkaRetentionMs,
		MaxMessageBytes:   cfg.Migrate.KafkaMaxMessageBytes,
	}
	kopts, err := queue.ClientOptions(cfg.Common)
	if err != nil {
		return err
	}
	backoff := time.Second
	for {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := queue.CreateTopics(cctx, cfg.KafkaBrokers, ts, kopts...)
		cancel()
		if err == nil {
			log.Info("kafka topics ready", "prefix", ts.Prefix, "partitions", ts.Partitions, "replication_factor", ts.ReplicationFactor)
			return nil
		}
		log.Warn("waiting for kafka", "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 10*time.Second)
	}
}

// RunAll runs fns concurrently; when one returns an error the shared context
// is cancelled so the others shut down. It returns the first error.
func RunAll(ctx context.Context, fns ...func(ctx context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	for _, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(ctx); err != nil && !errors.Is(err, context.Canceled) {
				once.Do(func() { firstErr = err })
				cancel()
			}
		}()
	}
	wg.Wait()
	return firstErr
}
