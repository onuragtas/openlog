package app

import (
	"log/slog"

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
