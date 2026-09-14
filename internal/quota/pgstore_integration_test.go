//go:build integration

// Integration tests of the quota and billing PostgreSQL stores against PostgreSQL 16 with all migrations applied
// (internal/usage/testdata/docker-compose.yml):
//
//	OPENLOG_TEST_POSTGRES_DSN=postgres://openlog:openlog@127.0.0.1:55441/openlog?sslmode=disable \
//	  go test -tags integration -count=1 ./internal/quota
package quota_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/billing"
	"github.com/onuragtas/openlog/internal/migrate/phase"
	"github.com/onuragtas/openlog/internal/quota"
	"github.com/onuragtas/openlog/internal/store/postgres"
	"github.com/onuragtas/openlog/migrations"
)

func pgPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("OPENLOG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("OPENLOG_TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	pool, err := postgres.Open(ctx, postgres.Options{DSN: dsn, MaxConns: 10, Application: "openlog-quotatest"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := postgres.WaitReady(ctx, pool, log); err != nil {
		t.Fatal(err)
	}
	ms, err := postgres.LoadMigrations(migrations.PostgresFS(), "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, pool, ms, log); err != nil {
		t.Fatal(err)
	}
	return pool
}

func createOrg(t *testing.T, pool *pgxpool.Pool, tenant string) (orgID, ownerID string) {
	t.Helper()
	ctx := context.Background()
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (tenant_id, name) VALUES ($1, $1) RETURNING id::text`, tenant).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (email, name) VALUES ($1, 'Owner') RETURNING id::text`, tenant+"@example.com").Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO memberships (org_id, user_id, role) VALUES ($1, $2, 'owner')`, orgID, ownerID); err != nil {
		t.Fatal(err)
	}
	return orgID, ownerID
}

func TestPGStorePlansStatusNotifications(t *testing.T) {
	pool := pgPool(t)
	ctx := context.Background()
	s := quota.PGStore{Pool: pool}
	tenant := fmt.Sprintf("quota-%d", time.Now().UnixNano())
	orgID, ownerID := createOrg(t, pool, tenant)

	op, err := s.FindOrgPlan(ctx, tenant)
	if err != nil || op.Assigned || op.OrgID != orgID {
		t.Fatalf("unassigned org: %+v %v", op, err)
	}
	if _, err := s.FindOrgPlan(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, quota.ErrOrgNotFound) {
		t.Errorf("unknown org: %v", err)
	}
	gb := 42.0
	op.PlanID, op.Overrides, op.Note = "pro", quota.Overrides{IngestGBMonth: &gb}, "deal"
	op.BillingProvider, op.BillingCustomerID = "noop", "cus-"+tenant
	if err := s.PutOrgPlan(ctx, op, quota.Actor{UserID: ownerID, Email: "ops@openlog.test", IP: "10.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.FindOrgPlan(ctx, orgID)
	if err != nil || !got.Assigned || got.PlanID != "pro" || *got.Overrides.IngestGBMonth != 42 || got.UpdatedByEmail != tenant+"@example.com" || got.UpdatedAt == nil {
		t.Fatalf("assigned: %+v %v", got, err)
	}
	var audits int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE org_id = $1 AND action = 'plan.update' AND actor_email = 'ops@openlog.test'`, orgID).Scan(&audits)
	if audits != 1 {
		t.Errorf("audit events %d", audits)
	}
	if byCus, err := s.FindOrgByBillingCustomer(ctx, "noop", "cus-"+tenant); err != nil || byCus.OrgID != orgID {
		t.Errorf("by customer: %+v %v", byCus, err)
	}
	if err := s.PutOrgPlan(ctx, quota.OrgPlan{OrgID: "00000000-0000-0000-0000-000000000000", PlanID: "pro"}, quota.Actor{}); !errors.Is(err, quota.ErrOrgNotFound) {
		t.Errorf("put unknown org: %v", err)
	}
	counts, err := s.MemberCounts(ctx)
	if err != nil || counts[orgID] != 1 {
		t.Errorf("member counts %v %v", counts[orgID], err)
	}
	if owners, err := s.Owners(ctx, orgID); err != nil || len(owners) != 1 || owners[0].Locale != "" {
		t.Errorf("owners %v %v", owners, err)
	}

	period := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	st := quota.StoredStatus{Status: quota.Status{PlanID: "pro", Level: quota.LevelExceeded, IngestBlocked: true, IngestLimitBytes: 100,
		RateBytesPerSecond: 500, BurstBytes: 1000, Metrics: []quota.MetricStatus{{Metric: "ingest_bytes", Used: 120, Limit: 100, Percent: 120, Level: quota.LevelExceeded}}},
		TenantID: tenant, OrgID: orgID, PeriodStart: period, IngestBytes: 120}
	if err := s.SaveStatuses(ctx, []quota.StoredStatus{st}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveStatuses(ctx, []quota.StoredStatus{st}); err != nil { // upsert
		t.Fatal(err)
	}
	loaded, found, err := s.GetStatus(ctx, tenant)
	if err != nil || !found || !loaded.IngestBlocked || loaded.Metrics[0].Percent != 120 || !loaded.PeriodStart.Equal(period) {
		t.Errorf("status %+v %v %v", loaded, found, err)
	}
	ing, err := s.LoadIngestStatuses(ctx)
	if err != nil || !ing[tenant].Blocked || ing[tenant].RateBytesPerSecond != 500 {
		t.Errorf("ingest statuses %+v %v", ing[tenant], err)
	}
	if err := postgres.UpsertHeartbeat(ctx, pool, phase.Instance{Component: "openlog-ingest", InstanceID: tenant, Version: "1.0.0", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.LiveIngestPods(ctx); err != nil || n < 1 {
		t.Errorf("pods %d %v", n, err)
	}
	_ = postgres.DeleteHeartbeat(ctx, pool, "openlog-ingest", tenant)

	ok, err := s.ClaimNotification(ctx, orgID, period, "ingest_bytes", 80)
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	if ok, _ := s.ClaimNotification(ctx, orgID, period, "ingest_bytes", 80); ok {
		t.Error("claimed twice")
	}
	if err := s.CompleteNotification(ctx, orgID, period, "ingest_bytes", 80, 0, false); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ClaimNotification(ctx, orgID, period, "ingest_bytes", 80); !ok {
		t.Error("released claim not claimable")
	}
	if err := s.CompleteNotification(ctx, orgID, period, "ingest_bytes", 80, 1, true); err != nil {
		t.Fatal(err)
	}

	task := quota.RetentionTask{Table: "logs_local", PartitionID: "20260901", Days: 7, Tenants: []string{tenant}, Hash: tenant}
	if recent, _ := s.RecentMutation(ctx, task.Table, task.PartitionID, task.Hash, time.Now().Add(-time.Hour)); recent {
		t.Error("recent before record")
	}
	if err := s.RecordMutation(ctx, task); err != nil {
		t.Fatal(err)
	}
	if recent, _ := s.RecentMutation(ctx, task.Table, task.PartitionID, task.Hash, time.Now().Add(-time.Hour)); !recent {
		t.Error("not recent after record")
	}

	bs := billing.PGStore{Pool: pool}
	rec := billing.PushRecord{IdempotencyKey: billing.IdempotencyKey(orgID, period, "hosts"), OrgID: orgID, Provider: "noop", Metric: "hosts", Day: period, Quantity: 3}
	if ok, err := bs.ClaimPush(ctx, rec, 2); err != nil || !ok {
		t.Fatalf("claim push %v %v", ok, err)
	}
	if ok, _ := bs.ClaimPush(ctx, rec, 2); ok {
		t.Error("pending push claimed again")
	}
	if err := bs.FinishPush(ctx, rec.IdempotencyKey, errors.New("down")); err != nil {
		t.Fatal(err)
	}
	if ok, _ := bs.ClaimPush(ctx, rec, 2); !ok {
		t.Error("failed push not re-claimed")
	}
	if err := bs.FinishPush(ctx, rec.IdempotencyKey, errors.New("down")); err != nil {
		t.Fatal(err)
	}
	if ok, _ := bs.ClaimPush(ctx, rec, 2); ok {
		t.Error("claimed past max attempts")
	}
}
