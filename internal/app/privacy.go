package app

import (
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/api"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/dataexport"
	"github.com/onuragtas/openlog/internal/deletion"
	"github.com/onuragtas/openlog/internal/mail"
	"github.com/onuragtas/openlog/internal/objstore"
	"github.com/onuragtas/openlog/internal/statuspage"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
)

// startPrivacyAPI enables data exports and account/organization deletion (D-107) and returns the leader tasks: the
// export queue and the organization hard deletion job. Postgres auth mode only.
func startPrivacyAPI(cfg config.Config, pool *pgxpool.Pool, conn clickhouse.Conn, srv *api.Server, log *slog.Logger) ([]leaderTask, error) {
	if pool == nil {
		return nil, nil
	}
	var mailer auth.Mailer
	if s := cfg.Alert.SMTP; s.Host != "" {
		m, err := mail.New(mail.Config{Host: s.Host, Port: s.Port, Username: s.Username, Password: s.Password, From: s.From,
			TLS: s.TLS, InsecureSkipVerify: s.InsecureSkipVerify})
		if err != nil {
			return nil, fmt.Errorf("OPENLOG_SMTP_*: %w", err)
		}
		mailer = m
	}
	p := cfg.Privacy
	store := deletion.PGStore{Pool: pool}
	deps := api.PrivacyDeps{Deletions: store, Grace: p.OrgDeletionGrace, Mailer: mailer, PublicURL: cfg.Alert.PublicURL}
	job := &deletion.Job{Store: store, Mailer: mailer, PublicURL: cfg.Alert.PublicURL, Log: log.With("job", "org-deletion"),
		Purger: &deletion.Purger{Conn: conn, Database: cfg.ClickHouseDatabase, Cluster: cfg.ClickHouseCluster, Log: log.With("job", "org-deletion")}}
	var tasks []leaderTask
	if e := p.Export; e.Enabled {
		var objects objstore.Store = objstore.Local{Dir: e.LocalPath}
		if e.ExportStorage() == "s3" {
			s3 := &objstore.S3{BaseURL: e.S3URL, Region: e.S3Region, AccessKeyID: e.S3AccessKeyID, SecretAccessKey: e.S3SecretAccessKey}
			if e.S3Credentials == "auto" {
				// AWS default chain subset (D-116): env, web identity (IRSA), ECS/EKS container, EC2 instance profile.
				s3.AccessKeyID, s3.SecretAccessKey = "", ""
				s3.Credentials = objstore.DefaultChain(objstore.ChainOptions{Region: e.S3Region})
			}
			objects = s3
		}
		svc := &dataexport.Service{Store: dataexport.PGStore{Pool: pool}, Objects: objects,
			Limits: dataexport.Limits{MaxBytes: e.MaxBytes, MaxRows: e.MaxRows, MaxRange: e.MaxRange, RowsPerSecond: e.RowsPerSecond, TTL: e.TTL}}
		deps.Exports, deps.Cleaner, job.Exports = svc, svc, svc
		tasks = append(tasks, leaderTask{"data-exports", (&dataexport.Job{Service: svc, Pool: pool, TempDir: filepath.Join(e.LocalPath, ".tmp"),
			Rows: dataexport.ClickHouseSource{Conn: conn, Database: cfg.ClickHouseDatabase}, Mailer: mailer, PublicURL: cfg.Alert.PublicURL,
			Log: log.With("job", "data-exports")}).Run})
		log.Info("data exports enabled", "storage", objects.Kind(), "s3_credentials", e.S3Credentials, "ttl", e.TTL, "max_bytes", e.MaxBytes)
	}
	tasks = append(tasks, leaderTask{"org-deletion", job.Run})
	srv.SetPrivacy(deps)
	return tasks, nil
}

// startStatusPage enables GET /api/v1/status and the self-check leader task (OPENLOG_STATUS_PAGE_ENABLED, D-108).
func startStatusPage(cfg config.Config, pool *pgxpool.Pool, conn clickhouse.Conn, srv *api.Server, log *slog.Logger) []leaderTask {
	if pool == nil || !cfg.StatusPage.Enabled {
		return nil
	}
	store := statuspage.PGStore{Pool: pool}
	srv.SetStatusPage(api.StatusPageDeps{Service: &statuspage.Service{Store: store}, Store: store})
	log.Info("public status page enabled", "path", "/status")
	return []leaderTask{{"status-page-checks", (&statuspage.Checker{Store: store, CH: conn, Database: cfg.ClickHouseDatabase,
		Log: log.With("job", "status-page-checks")}).Run}}
}
