//go:build integration

// Integration tests of data exports against PostgreSQL 16 and (optionally) a single-node ClickHouse cluster; see
// internal/deletion/integration_test.go for the containers:
//
//	OPENLOG_TEST_POSTGRES_DSN=… OPENLOG_TEST_CLICKHOUSE_ADDR=127.0.0.1:19030 go test -tags integration -count=1 ./internal/dataexport
package dataexport

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/migrate"
	"github.com/onuragtas/openlog/internal/objstore"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
	"github.com/onuragtas/openlog/internal/store/postgres"
	"github.com/onuragtas/openlog/migrations"
	"github.com/onuragtas/openlog/schema"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("OPENLOG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("OPENLOG_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	p, err := postgres.Open(ctx, postgres.Options{DSN: dsn, Application: "dataexport-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if err := postgres.WaitReady(ctx, p, quiet); err != nil {
		t.Fatal(err)
	}
	ms, err := postgres.LoadMigrations(migrations.Postgres, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, p, ms, quiet); err != nil {
		t.Fatal(err)
	}
	return p
}

func rnd() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func one(t *testing.T, p *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var id string
	if err := p.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return id
}

func exec(t *testing.T, p *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := p.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func TestExportJobEndToEnd(t *testing.T) {
	p := testPool(t)
	ctx := context.Background()
	s := rnd()
	tenantID := "exp-" + s
	org := one(t, p, `INSERT INTO organizations (tenant_id, name) VALUES ($1, $2) RETURNING id::text`, tenantID, "Export "+s)
	email := "owner-" + s + "@example.com"
	user := one(t, p, `INSERT INTO users (email, name, password_hash, email_verified_at) VALUES ($1, 'Owner', 'PASSWORD-HASH-VALUE', now()) RETURNING id::text`, email)
	exec(t, p, `INSERT INTO memberships (org_id, user_id, role) VALUES ($1, $2, 'owner')`, org, user)
	keyHash := make([]byte, 32)
	_, _ = rand.Read(keyHash)
	exec(t, p, `INSERT INTO license_keys (org_id, name, key_prefix, key_hash) VALUES ($1, 'prod', 'olk_ab', $2)`, org, keyHash)
	exec(t, p, `INSERT INTO sessions (user_id, token_hash, csrf_token, expires_at, ip, user_agent) VALUES ($1, $2, 'CSRF-TOKEN-VALUE', now() + interval '1 day', '10.2.2.2', 'Firefox')`, user, keyHash[:])
	exec(t, p, `INSERT INTO alert_channels (org_id, name, type, config, secrets, secrets_key_id, secret_hints)
		VALUES ($1, 'hook', 'webhook', '{"method":"POST"}', 'ol1:k:CHANNEL-SECRET-VALUE', 'k', '{"url":"https://example.com/…/•••f3a9"}')`, org)
	exec(t, p, `INSERT INTO sso_connections (org_id, protocol, name, config, secret_enc) VALUES ($1, 'oidc', 'Okta', '{"issuer":"https://idp.example.com"}', '\x5345435245542d454e43'::bytea)`, org)
	exec(t, p, `INSERT INTO audit_log (org_id, actor_user_id, actor_email, action, ip) VALUES ($1, $2, $3, 'org.create', '10.2.2.2')`, org, user, email)

	var rows RowSource
	var chConn clickhouse.Conn
	signals := []string{}
	from, to := time.Now().UTC().Add(-2*time.Hour), time.Now().UTC().Add(time.Minute)
	if addr := os.Getenv("OPENLOG_TEST_CLICKHOUSE_ADDR"); addr != "" {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		conn, err := clickhouse.OpenRetry(cctx, clickhouse.Options{Addr: []string{addr}, Database: "default", User: "openlog", Password: "openlog"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ms, err := migrate.Load(schema.ClickHouseFS(), "clickhouse", "openlog")
		if err != nil {
			t.Fatal(err)
		}
		if err := migrate.Run(cctx, conn, ms, quiet); err != nil {
			t.Fatal(err)
		}
		chConn = conn
		for i := 0; i < 25; i++ {
			for _, tid := range []string{tenantID, "other-" + s} {
				if err := conn.Exec(ctx, "INSERT INTO openlog.logs_local (tenant_id, timestamp, body) VALUES (?, ?, ?)", tid, time.Now().UTC().Add(-time.Duration(i)*time.Minute), "line "+tid); err != nil {
					t.Fatal(err)
				}
			}
		}
		rows = ClickHouseSource{Conn: conn, Database: "openlog"}
		signals = []string{"logs"}
	}

	svc := &Service{Store: PGStore{Pool: p}, Objects: objstore.Local{Dir: t.TempDir()},
		Limits: Limits{MaxBytes: 64 << 20, MaxRows: 1000, MaxRange: 24 * time.Hour, TTL: time.Hour, ChunkRows: 10}}
	e, err := svc.RequestOrg(ctx, org, user, "tr", from, to, signals)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RequestOrg(ctx, org, user, "tr", from, to, signals); err != ErrActive {
		t.Fatalf("second request while pending: %v", err)
	}
	personal, err := svc.RequestUser(ctx, user, "en")
	if err != nil {
		t.Fatal(err)
	}
	job := &Job{Service: svc, Pool: p, Rows: rows, TempDir: t.TempDir(), Log: quiet}
	for i := 0; i < 5; i++ {
		if ran, err := job.RunOnce(ctx); err != nil {
			t.Fatal(err)
		} else if !ran {
			break
		}
	}
	for _, id := range []string{e.ID, personal.ID} {
		got, err := svc.Get(ctx, id)
		if err != nil || got.Status != StatusCompleted || got.ObjectKey == "" || got.SizeBytes == 0 {
			t.Fatalf("export %s: %+v %v", id, got, err)
		}
	}

	files := readArchive(t, svc, e.ID)
	for _, name := range []string{"manifest.json", "organization/organization.json", "organization/members.json", "organization/alert_channels.json",
		"organization/sso.json", "organization/audit_log.json", "organization/license_keys.json"} {
		if _, ok := files[name]; !ok {
			t.Fatalf("archive lacks %s: %v", name, keys(files))
		}
	}
	all := strings.Join(values(files), "\n")
	for _, secret := range []string{"CHANNEL-SECRET-VALUE", "SECRET-ENC", hex.EncodeToString(keyHash), "PASSWORD-HASH-VALUE", "CSRF-TOKEN-VALUE"} {
		if strings.Contains(all, secret) {
			t.Errorf("archive contains %q", secret)
		}
	}
	for name, body := range files {
		if strings.HasSuffix(name, ".json") && !json.Valid([]byte(body)) {
			t.Errorf("%s is not valid JSON: %s", name, body)
		}
	}
	if !strings.Contains(files["organization/alert_channels.json"], "•••f3a9") || !strings.Contains(files["organization/sso.json"], "idp.example.com") {
		t.Errorf("exported settings incomplete: %s %s", files["organization/alert_channels.json"], files["organization/sso.json"])
	}
	var man Manifest
	if err := json.Unmarshal([]byte(files["manifest.json"]), &man); err != nil {
		t.Fatal(err)
	}
	if chConn != nil {
		lines := 0
		for name, body := range files {
			if strings.HasPrefix(name, "telemetry/logs/") {
				for _, l := range strings.Split(strings.TrimSpace(body), "\n") {
					if !strings.Contains(l, `"tenant_id":"`+tenantID+`"`) {
						t.Fatalf("foreign or malformed row in %s: %s", name, l)
					}
					lines++
				}
			}
		}
		if lines != 25 || man.TelemetryRows != 25 || man.Telemetry["logs"].Rows != 25 || len(man.Telemetry["logs"].Files) < 3 {
			t.Fatalf("telemetry rows %d, manifest %+v", lines, man.Telemetry)
		}
	}

	pfiles := readArchive(t, svc, personal.ID)
	if !strings.Contains(pfiles["user/profile.json"], email) || !strings.Contains(pfiles["user/sessions.json"], "Firefox") ||
		!strings.Contains(pfiles["user/audit_log.json"], "org.create") || strings.Contains(strings.Join(values(pfiles), ""), "CSRF-TOKEN-VALUE") {
		t.Fatalf("personal export: %v", pfiles)
	}

	// Organization deletion removes stored archives.
	if err := svc.DeleteOrgExports(ctx, org); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Get(ctx, e.ID); got.Status != StatusExpired || got.ObjectKey != "" {
		t.Fatalf("after DeleteOrgExports: %+v", got)
	}
}

func readArchive(t *testing.T, svc *Service, id string) map[string]string {
	t.Helper()
	e, err := svc.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	rc, _, err := svc.OpenArchive(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		r, _ := f.Open()
		var data []byte
		if strings.HasSuffix(f.Name, ".gz") {
			gz, err := gzip.NewReader(r)
			if err != nil {
				t.Fatal(err)
			}
			data, _ = io.ReadAll(gz)
		} else {
			data, _ = io.ReadAll(r)
		}
		r.Close()
		out[f.Name] = string(data)
	}
	return out
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func values(m map[string]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
