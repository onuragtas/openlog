package app

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/api"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/release"
	"github.com/onuragtas/openlog/internal/store/postgres"
	"github.com/onuragtas/openlog/internal/updatecheck"
	"github.com/onuragtas/openlog/internal/updatereq"
	"github.com/onuragtas/openlog/internal/version"
)

// startLeaderTasks wires GET /api/v1/version and starts the cluster-wide background jobs that
// run on exactly one openlog-api pod (postgres.Leader). pool is nil in static auth mode, where
// there is no shared state: the endpoint then reports the build only.
//
// Other leader-only jobs (e.g. fleet rollout transitions) should be registered here with
// leader.Add before leader.Run.
//
// apmLinker (nil when disabled) is the APM edge-linking job (docs/contracts/apm.md §6). Without
// PostgreSQL (static auth mode, development) there is no leader election and it runs in this process.
func startLeaderTasks(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, srv *api.Server, log *slog.Logger, fleetController, apmLinker func(ctx context.Context)) {
	uc := updatecheck.Config{
		Enabled: cfg.UpdateCheck.Enabled, Interval: cfg.UpdateCheck.Interval, IndexURL: cfg.UpdateCheck.IndexURL,
		Channel: cfg.UpdateCheck.Channel, TrustedKeysFile: cfg.UpdateCheck.TrustedKeysFile,
	}
	if pool == nil {
		srv.SetVersionSource(updatecheck.NewReader(updatecheck.Config{}, version.String(), nil, nil))
		if apmLinker != nil {
			go apmLinker(ctx)
		}
		return
	}
	store := pgUpdateStore{pool: pool}
	srv.SetVersionSource(updatecheck.NewReader(uc, version.String(), store, store.loadUpdater))

	leader := postgres.NewLeader(pool, log.With("job", "leader"))
	if fleetController != nil {
		leader.Add("fleet-rollouts", fleetController) // internal/fleet rollout controller
	}
	if apmLinker != nil {
		leader.Add("apm-edge-linking", apmLinker) // internal/apm, per shard
	}
	var checkNow func(context.Context) error
	if uc.Enabled {
		keys, err := release.TrustedKeys(uc.TrustedKeysFile)
		if err != nil {
			log.Warn("update check: cannot load trusted release keys", "err", err)
		}
		checker := updatecheck.NewChecker(uc, store, updatecheck.FetchLatest(release.NewFetcher(keys)), log.With("job", "update-check"))
		leader.Add("update-check", checker.Run)
		checkNow = checker.CheckNow // "Check now": any pod, rate-limited through update_requests
	}
	// "Check now" / "Update now" (update_requests, D-041); needs user accounts (postgres auth mode).
	srv.SetUpdateRequests(updatereq.PGQueue{Pool: pool}, checkNow)
	go leader.Run(ctx)
}

type pgUpdateStore struct{ pool *pgxpool.Pool }

func (s pgUpdateStore) LoadCheck(ctx context.Context) (updatecheck.State, bool, error) {
	var st updatecheck.State
	_, found, err := postgres.GetSystemState(ctx, s.pool, updatecheck.StateKey, &st)
	return st, found, err
}

func (s pgUpdateStore) SaveCheck(ctx context.Context, st updatecheck.State) error {
	return postgres.PutSystemState(ctx, s.pool, updatecheck.StateKey, st)
}

func (s pgUpdateStore) loadUpdater(ctx context.Context) (json.RawMessage, error) {
	var raw json.RawMessage
	_, found, err := postgres.GetSystemState(ctx, s.pool, updatecheck.UpdaterStateKey, &raw)
	if err != nil || !found {
		return nil, err
	}
	return raw, nil
}
