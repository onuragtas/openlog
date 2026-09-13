package app

import (
	"context"
	"log/slog"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/alert/secrets"
	"github.com/onuragtas/openlog/internal/api"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/fleet"
	"github.com/onuragtas/openlog/internal/fleet/catalog"
	"github.com/onuragtas/openlog/internal/ingest"
	"github.com/onuragtas/openlog/internal/intsettings"
	"github.com/onuragtas/openlog/internal/release"
	"github.com/onuragtas/openlog/internal/tenant"
)

var (
	catalogsMu sync.Mutex
	catalogs   = map[prometheus.Registerer]*catalog.Catalog{}
)

// releaseCatalog returns the verified release catalog of this process, started once per metrics
// registry (openlog-allinone shares one catalog between ingest and api).
func releaseCatalog(ctx context.Context, cfg config.Config, reg prometheus.Registerer, log *slog.Logger) *catalog.Catalog {
	catalogsMu.Lock()
	defer catalogsMu.Unlock()
	if c, ok := catalogs[reg]; ok && reg != nil {
		return c
	}
	keys, keysErr := release.TrustedKeys(cfg.UpdateCheck.TrustedKeysFile)
	if keysErr != nil {
		log.Warn("release catalog: cannot load trusted release keys", "err", keysErr)
		keys = nil
	}
	c := catalog.New(catalog.Options{
		IndexURL: cfg.UpdateCheck.IndexURL, MirrorDir: cfg.Fleet.ReleaseMirrorDir, TrustedKeys: keys, KeysError: keysErr,
		Refresh: cfg.Fleet.CatalogRefresh, Registerer: reg, Log: log.With("job", "release-catalog"),
	})
	go c.Run(ctx)
	if reg != nil {
		catalogs[reg] = c
	}
	return c
}

// startFleetIngest serves POST /v1/openlog/agent/sync and the release mirror on the OTLP/HTTP
// listener. pool is nil in static auth mode (sync answers without updates or integration config); keys
// decrypts integration setting passwords. The returned function waits for the final write of queued sync
// reports; call it before closing pool.
func startFleetIngest(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, keys *secrets.Keyring, res tenant.Resolver, svc *ingest.Service, reg prometheus.Registerer, log *slog.Logger) (wait func()) {
	log = log.With("job", "fleet-sync")
	var (
		states *fleet.StateCache
		rec    *fleet.Recorder
		done   = make(chan struct{})
	)
	cat := releaseCatalog(ctx, cfg, reg, log)
	if pool != nil {
		store := fleet.NewPGStore(pool)
		states = fleet.NewStateCache(store, fleet.StateCacheOptions{TTL: cfg.Fleet.PolicyCacheTTL, Registerer: reg, Log: log})
		rec = fleet.NewRecorder(store, fleet.RecorderOptions{MaxPending: cfg.Fleet.ReportQueueSize, Registerer: reg, Log: log})
		go func() { defer close(done); rec.Run(ctx) }()
	} else {
		close(done)
	}
	syncSvc := fleet.NewSyncService(res, states, rec, cat, fleet.SyncOptions{
		PollInterval: cfg.Fleet.SyncInterval, RolloutPollInterval: cfg.Fleet.RolloutSyncInterval, ServeMirror: cfg.Fleet.ReleaseServeMirror, MirrorBaseURL: cfg.Fleet.ReleaseMirrorBaseURL,
		Keys: keys, Registerer: reg, Log: log,
	})
	svc.SetHTTPRoutes(syncSvc.Register)
	return func() { <-done }
}

// startIntegrationSettingsAPI enables /api/v1/integrations/settings (docs/contracts/api.md). pool is nil in static
// auth mode, where the endpoints are not available.
func startIntegrationSettingsAPI(cfg config.Config, pool *pgxpool.Pool, srv *api.Server, log *slog.Logger) error {
	if pool == nil {
		return nil
	}
	kr, err := alertKeyring(cfg)
	if err != nil {
		return err
	}
	srv.SetIntegrationSettings(intsettings.NewManager(intsettings.NewPGStore(pool), intsettings.ManagerOptions{Keys: kr, Log: log}))
	return nil
}

// startFleetAPI enables the fleet management API and returns the rollout controller, to be run
// by the leader. It returns nil in static auth mode (pool == nil).
func startFleetAPI(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, srv *api.Server, reg prometheus.Registerer, log *slog.Logger) func(ctx context.Context) {
	if pool == nil {
		return nil
	}
	cat := releaseCatalog(ctx, cfg, reg, log)
	store := fleet.NewPGStore(pool)
	srv.SetFleet(fleet.NewManager(store, cat, fleet.ManagerOptions{StaleAfter: cfg.Fleet.HostStaleAfter, Log: log}))
	ctl := fleet.NewController(store, cat.Snapshot, fleet.ControllerOptions{
		Interval: cfg.Fleet.ControllerInterval, StaleAfter: cfg.Fleet.HostStaleAfter, Registerer: reg, Log: log.With("job", "fleet-rollouts"),
	})
	return ctl.Run
}
