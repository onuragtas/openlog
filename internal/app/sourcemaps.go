package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/objstore"
	"github.com/onuragtas/openlog/internal/sourcemaps"
	"github.com/onuragtas/openlog/internal/store/postgres"
)

// newSourceMaps builds the source map service (docs/contracts/rum.md §8), or nil when the installation has
// no PostgreSQL or turned maps off — the API endpoints then answer 404 rather than half-working.
//
// The storage is constructed exactly like the data export's (internal/app/privacy.go): the same local-or-S3
// choice and the same credential chain, because a map is the same kind of object and an operator who has
// pointed one at a bucket should not meet a second scheme here.
func newSourceMaps(cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) *sourcemaps.Service {
	m := cfg.RUM.SourceMaps
	if pool == nil || !cfg.RUM.Enabled || !m.Enabled {
		return nil
	}
	var objects objstore.Store = objstore.Local{Dir: m.LocalPath}
	if m.MapStorage() == "s3" {
		s3 := &objstore.S3{BaseURL: m.S3URL, Region: m.S3Region, AccessKeyID: m.S3AccessKeyID, SecretAccessKey: m.S3SecretAccessKey}
		if m.S3Credentials == "auto" {
			// AWS default chain subset (D-116): env, web identity (IRSA), ECS/EKS container, EC2 IMDSv2.
			s3.AccessKeyID, s3.SecretAccessKey = "", ""
			s3.Credentials = objstore.DefaultChain(objstore.ChainOptions{Region: m.S3Region})
		}
		objects = s3
	}
	log.Info("source maps enabled", "storage", objects.Kind(), "s3_credentials", m.S3Credentials, "max_bytes", sourcemaps.MaxMapBytes)
	return &sourcemaps.Service{Meta: postgres.NewStore(pool), Objects: objects}
}

// sourceMapPruneInterval is how often the leader looks for maps of builds nobody deploys any more. Nothing here
// is urgent: the thing being reclaimed grows by one object per bundle per deploy.
const sourceMapPruneInterval = time.Hour

// sourceMapPruneBatch bounds one pass, so a first run on an installation that has accumulated years of maps
// deletes steadily instead of in one long transaction against object storage.
const sourceMapPruneBatch = 500

// startSourceMapPrune deletes the maps nobody has uploaded for OPENLOG_SOURCE_MAPS_RETENTION_DAYS (leader task).
//
// Why this is needed at all: a re-upload of the same script replaces the row and keeps the object key, so maps
// never orphan — but a bundler that hashes file names ("main.3f2a1b9c.js") makes every deploy a new script, and
// those rows and objects are never touched again.
func startSourceMapPrune(cfg config.Config, svc *sourcemaps.Service, log *slog.Logger) []leaderTask {
	if svc == nil {
		return nil
	}
	days := cfg.RUM.SourceMaps.RetentionDays
	if days == 0 {
		log.Info("OPENLOG_SOURCE_MAPS_RETENTION_DAYS=0: uploaded source maps are kept forever")
		return nil
	}
	if days < 0 {
		// Refused rather than clamped: a negative retention is a mistake, and quietly reading it as "off" would
		// hide it until someone wondered why the bucket kept growing.
		log.Warn("OPENLOG_SOURCE_MAPS_RETENTION_DAYS is negative; source map pruning is off", "days", days)
		return nil
	}
	retention := time.Duration(days) * 24 * time.Hour
	run := func(ctx context.Context) {
		l := log.With("job", "source-map-prune")
		for {
			rctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			n, err := svc.Prune(rctx, time.Now().Add(-retention), sourceMapPruneBatch)
			cancel()
			if err != nil && ctx.Err() == nil {
				l.Warn("source map prune failed", "deleted", n, "err", err)
			}
			if n > 0 {
				// Deleting an upload is worth a line: the only other record of it is the audit of whoever
				// uploaded it, months earlier.
				l.Info("deleted source maps of builds older than the retention", "deleted", n, "days", days)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(sourceMapPruneInterval):
			}
		}
	}
	return []leaderTask{{"source-map-prune", run}}
}
