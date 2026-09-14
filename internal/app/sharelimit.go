package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/api"
	"github.com/onuragtas/openlog/internal/ratelimit"
)

// startShareRateLimit makes the public dashboard share link rate limits cluster-wide (PostgreSQL rate_limit_counters,
// D-096): it starts the limiter's background flush and returns the leader task pruning old counters. Without
// PostgreSQL (static auth mode) the limits stay per pod.
func startShareRateLimit(ctx context.Context, pool *pgxpool.Pool, srv *api.Server, log *slog.Logger) []leaderTask {
	if pool == nil {
		return nil
	}
	store := ratelimit.PGStore{Pool: pool}
	lim := ratelimit.New(ratelimit.Options{Store: store, Log: log.With("component", "share-rate-limit")})
	srv.SetShareRateLimiter(lim)
	go lim.Run(ctx)
	return []leaderTask{{"rate-limit-prune", ratelimit.PruneJob(store, time.Minute, log.With("job", "rate-limit-prune"))}}
}
