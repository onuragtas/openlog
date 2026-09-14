//go:build integration

// Integration tests of the SaaS operations store against PostgreSQL 16 with all migrations applied:
//
//	OPENLOG_TEST_POSTGRES_DSN=postgres://openlog:openlog@127.0.0.1:55470/openlog?sslmode=disable \
//	  go test -tags integration -count=1 ./internal/operator
package operator_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/operator"
	"github.com/onuragtas/openlog/internal/quota"
	"github.com/onuragtas/openlog/internal/store/postgres"
	"github.com/onuragtas/openlog/internal/usage"
	"github.com/onuragtas/openlog/migrations"
)

func pgPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("OPENLOG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("OPENLOG_TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	pool, err := postgres.Open(ctx, postgres.Options{DSN: dsn, MaxConns: 10, Application: "openlog-operatortest"})
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

type org struct{ id, tenant, owner, ownerSession string }

func createOrg(t *testing.T, pool *pgxpool.Pool, prefix string) org {
	t.Helper()
	ctx := context.Background()
	o := org{tenant: fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())}
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (tenant_id, name) VALUES ($1, $1) RETURNING id::text`, o.tenant).Scan(&o.id); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (email, name) VALUES ($1, 'Owner') RETURNING id::text`, o.tenant+"@example.com").Scan(&o.owner); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO memberships (org_id, user_id, role) VALUES ($1, $2, 'owner')`, o.id, o.owner); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO sessions (user_id, token_hash, csrf_token, expires_at) VALUES ($1, sha256(random()::text::bytea), 'c', now() + interval '1 day')
		RETURNING id::text`, o.owner).Scan(&o.ownerSession); err != nil {
		t.Fatal(err)
	}
	return o
}

func auditActions(t *testing.T, pool *pgxpool.Pool, orgID string) map[string]int {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT action FROM audit_log WHERE org_id = $1::uuid`, orgID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		out[a]++
	}
	return out
}

func TestSuspendListDetailLogout(t *testing.T) {
	pool := pgPool(t)
	ctx := context.Background()
	s := operator.Store{Pool: pool}
	o := createOrg(t, pool, "opsusp")
	operatorUser := createOrg(t, pool, "opuser")
	a := operator.Actor{UserID: operatorUser.owner, Email: "ops@example.com", IP: "192.0.2.1"}

	l, err := s.Suspend(ctx, o.tenant, "fraud report", a, false)
	if err != nil || !l.Suspended || l.SuspendReason != "fraud report" {
		t.Fatalf("suspend: %+v %v", l, err)
	}
	byTenant, byOrg, err := s.SuspendedTenants(ctx)
	if err != nil || byTenant[o.tenant] == "" || byOrg[o.id] == "" {
		t.Fatalf("suspended tenants %v %v %v", byTenant, byOrg, err)
	}
	orgs, total, err := s.ListOrgs(ctx, operator.OrgFilter{Query: o.tenant, State: "suspended", DefaultPlan: "free"})
	if err != nil || total != 1 || len(orgs) != 1 || orgs[0].State != "suspended" || orgs[0].Members != 1 || orgs[0].PlanID != "free" {
		t.Fatalf("list %+v %d %v", orgs, total, err)
	}
	if _, _, err := s.ListOrgs(ctx, operator.OrgFilter{State: "nope"}); !errors.Is(err, operator.ErrInvalidState) {
		t.Errorf("invalid state: %v", err)
	}
	for _, sort := range []string{"name", "ingest", "members", "last_ingest"} {
		if _, _, err := s.ListOrgs(ctx, operator.OrgFilter{Sort: sort, Query: "example.com"}); err != nil {
			t.Errorf("sort %s: %v", sort, err)
		}
	}
	d, err := s.GetOrg(ctx, o.id, "free")
	if err != nil || len(d.MemberList) != 1 || d.MemberList[0].Role != "owner" || d.SSOConnections == nil || d.Flags == nil {
		t.Fatalf("detail %+v %v", d, err)
	}
	n, err := s.ForceLogout(ctx, o.id, "compromised", a)
	if err != nil || n != 1 {
		t.Fatalf("force logout %d %v", n, err)
	}
	if _, err := s.Unsuspend(ctx, o.id, "resolved", a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Unsuspend(ctx, o.id, "again", a); !errors.Is(err, operator.ErrInvalidState) {
		t.Errorf("unsuspend twice: %v", err)
	}
	period := usage.PeriodOf(time.Now())
	if _, err := pool.Exec(ctx, `INSERT INTO usage_notifications (org_id, period_start, metric, threshold) VALUES ($1, $2, 'hosts', 80)`, o.id, period.Start); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ResetQuotaNotifications(ctx, o.id, period.Start, "customer asked", a); err != nil || n != 1 {
		t.Fatalf("reset %d %v", n, err)
	}
	got := auditActions(t, pool, o.id)
	for _, action := range []string{"org.suspend", "org.unsuspend", "org.force_logout", "quota.notifications_reset"} {
		if got[action] != 1 {
			t.Errorf("audit %s: %v", action, got)
		}
	}
	entries, err := s.RecentAudit(ctx, o.id, 10)
	if err != nil || len(entries) != 4 || entries[0].ActorEmail != "ops@example.com" {
		t.Errorf("recent audit %+v %v", entries, err)
	}
}

func TestSupportAccessAndSessions(t *testing.T) {
	pool := pgPool(t)
	ctx := context.Background()
	s := operator.Store{Pool: pool}
	o := createOrg(t, pool, "opsupport")
	ops := createOrg(t, pool, "opops")
	a := operator.Actor{UserID: ops.owner, Email: "ops@example.com"}
	owner := operator.Actor{UserID: o.owner, Email: o.tenant + "@example.com"}

	if _, err := s.StartSupportSession(ctx, o.id, "ticket 1", ops.ownerSession, time.Hour, a); !errors.Is(err, operator.ErrNoAccess) {
		t.Fatalf("without grant: %v", err)
	}
	l, err := s.GrantSupportAccess(ctx, o.id, 24*time.Hour, owner)
	if err != nil || !l.SupportAccessActive(time.Now()) {
		t.Fatalf("grant %+v %v", l, err)
	}
	ss, err := s.StartSupportSession(ctx, o.tenant, "ticket 1", ops.ownerSession, time.Hour, a)
	if err != nil || ss.OrgID != o.id || ss.TenantID != o.tenant {
		t.Fatalf("start %+v %v", ss, err)
	}
	if _, err := s.ResolveSupportSession(ctx, ss.ID, ops.owner, ops.ownerSession, time.Now()); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := s.ResolveSupportSession(ctx, ss.ID, ops.owner, o.ownerSession, time.Now()); !errors.Is(err, operator.ErrNoAccess) {
		t.Errorf("other auth session: %v", err)
	}
	if _, err := s.ResolveSupportSession(ctx, ss.ID, o.owner, ops.ownerSession, time.Now()); !errors.Is(err, operator.ErrNoAccess) {
		t.Errorf("other user: %v", err)
	}
	if _, err := s.ResolveSupportSession(ctx, ss.ID, ops.owner, ops.ownerSession, time.Now().Add(2*time.Hour)); !errors.Is(err, operator.ErrNoAccess) {
		t.Errorf("expired: %v", err)
	}
	open, err := s.ListOperatorSupportSessions(ctx, ops.owner)
	if err != nil || len(open) != 1 {
		t.Fatalf("open sessions %v %v", open, err)
	}
	if _, err := s.RevokeSupportAccess(ctx, o.id, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveSupportSession(ctx, ss.ID, ops.owner, ops.ownerSession, time.Now()); !errors.Is(err, operator.ErrNoAccess) {
		t.Errorf("after revoke: %v", err)
	}
	got := auditActions(t, pool, o.id)
	for _, action := range []string{"support_access.grant", "support_access.revoke", "support.session_start"} {
		if got[action] != 1 {
			t.Errorf("audit %s: %v", action, got)
		}
	}
}

func TestTrialsFlagsHostsMembers(t *testing.T) {
	pool := pgPool(t)
	ctx := context.Background()
	s := operator.Store{Pool: pool}
	plans := quota.PGStore{Pool: pool}
	catalog, err := quota.ParseCatalog(`{"default":"free","plans":[{"id":"free","limits":{"hosts":2,"users":2}},{"id":"pro","trial_days":14,"limits":{"users":10}}]}`, "")
	if err != nil {
		t.Fatal(err)
	}
	o := createOrg(t, pool, "optrial")
	a := operator.Actor{Email: "ops@example.com"}

	// Trial: start, extend, end by the job (plan moves to the fallback).
	l, err := s.StartTrialWithPlan(ctx, o.id, "pro", time.Now().Add(time.Hour), "sales", a)
	if err != nil || !l.TrialActive() || l.TrialPlanID != "pro" {
		t.Fatalf("start trial %+v %v", l, err)
	}
	if op, err := plans.FindOrgPlan(ctx, o.id); err != nil || op.PlanID != "pro" {
		t.Fatalf("trial plan %+v %v", op, err)
	}
	if _, err := s.ExtendTrial(ctx, o.id, time.Now().Add(2*time.Hour), "more time", a); err != nil {
		t.Fatal(err)
	}
	job := &operator.TrialJob{Store: s, Owners: plans, Catalog: catalog, NotifyDays: []int{7, 3, 1}, Now: func() time.Time { return time.Now().Add(3 * time.Hour) }}
	if err := job.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	l, err = s.GetLifecycle(ctx, o.id)
	if err != nil || l.TrialActive() || l.TrialEndedAt == nil {
		t.Fatalf("trial not ended %+v %v", l, err)
	}
	if op, err := plans.FindOrgPlan(ctx, o.id); err != nil || op.PlanID != "free" {
		t.Fatalf("fallback plan %+v %v", op, err)
	}
	if _, err := s.ExtendTrial(ctx, o.id, time.Now().Add(time.Hour), "late", a); !errors.Is(err, operator.ErrInvalidState) {
		t.Errorf("extend ended trial: %v", err)
	}
	got := auditActions(t, pool, o.id)
	if got["trial.start"] != 1 || got["trial.extend"] != 1 || got["trial.end"] != 1 || got["plan.update"] != 2 {
		t.Errorf("trial audit %v", got)
	}

	// Users limit (free: 2): 1 member + 1 pending invitation fills it.
	ml := operator.MemberLimiter{Catalog: catalog, Plans: plans, Pool: pool}
	if err := ml.Check(ctx, o.id, true); err != nil {
		t.Fatalf("1 of 2: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO invitations (org_id, email, role, token_hash, expires_at) VALUES ($1, 'x@example.com', 'member', sha256(random()::text::bytea), now() + interval '1 day')`, o.id); err != nil {
		t.Fatal(err)
	}
	err = ml.Check(ctx, o.id, true)
	var ae *auth.Error
	if !errors.As(err, &ae) || ae.Code != auth.CodeQuotaExceeded {
		t.Fatalf("2 of 2 with invitation: %v", err)
	}
	if err := ml.Check(ctx, o.id, false); err != nil {
		t.Errorf("accepting uses the reserved seat: %v", err)
	}

	// Host limits.
	day := time.Now().UTC().Truncate(24 * time.Hour)
	err = s.SyncHostLimits(ctx, map[string]int64{o.tenant: 2}, map[string][]usage.HostSeen{o.tenant: {{HostID: "h1", LastSeen: day}, {HostID: "old", LastSeen: day.AddDate(0, 0, -5)}}},
		day.AddDate(0, 0, -1))
	if err != nil {
		t.Fatal(err)
	}
	limits, err := s.LoadHostLimits(ctx)
	if err != nil || limits[o.tenant] == nil || limits[o.tenant].Limit != 2 || len(limits[o.tenant].Known) != 1 {
		t.Fatalf("host limits %+v %v", limits[o.tenant], err)
	}
	if err := s.SyncHostLimits(ctx, map[string]int64{}, nil, day); err != nil {
		t.Fatal(err)
	}
	if limits, _ := s.LoadHostLimits(ctx); limits[o.tenant] != nil {
		t.Error("limit not removed")
	}

	// Flags.
	id, isNew, err := s.RaiseFlag(ctx, o.id, operator.FlagIngestSpike, map[string]any{"x": 1})
	if err != nil || !isNew {
		t.Fatalf("raise %v %v", isNew, err)
	}
	id2, isNew, err := s.RaiseFlag(ctx, o.id, operator.FlagIngestSpike, map[string]any{"x": 2})
	if err != nil || isNew || id2 != id {
		t.Fatalf("raise again %d %v %v", id2, isNew, err)
	}
	flags, err := s.ListFlags(ctx, "open", 500)
	found := false
	for _, f := range flags {
		found = found || (f.ID == id && f.Occurrences == 2)
	}
	if err != nil || !found {
		t.Fatalf("list flags %v", err)
	}
	f, err := s.ResolveFlag(ctx, id, "dismissed", "known customer", a)
	if err != nil || f.Status != "dismissed" {
		t.Fatalf("resolve %+v %v", f, err)
	}
	if _, err := s.ResolveFlag(ctx, id, "dismissed", "again", a); !errors.Is(err, operator.ErrNotFound) {
		t.Errorf("resolve twice: %v", err)
	}

	// Source counts: max over instances.
	hour := time.Now().UTC().Truncate(time.Hour)
	_ = s.SaveSourceCounts(ctx, hour, "pod-a", map[string]int{o.tenant: 5})
	_ = s.SaveSourceCounts(ctx, hour, "pod-b", map[string]int{o.tenant: 9})
	_ = s.SaveSourceCounts(ctx, hour, "pod-b", map[string]int{o.tenant: 3})
	counts, err := s.SourceIPCounts(ctx, hour)
	if err != nil || counts[o.tenant] != 9 {
		t.Errorf("source counts %v %v", counts, err)
	}
}
