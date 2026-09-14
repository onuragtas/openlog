//go:build apmga

// Integration tests of the APM GA error workflow: the PostgreSQL store (0025_apm_error_workflow) and regression
// detection on the ClickHouse views of 0035_apm_ga. They need a single-node ClickHouse cluster "openlog" (embedded
// Keeper, deploy/compose/clickhouse/openlog-cluster.xml) and PostgreSQL 16:
//
//	OPENLOG_APMGA_CLICKHOUSE_ADDR=127.0.0.1:19001 OPENLOG_APMGA_POSTGRES_DSN=postgres://openlog:openlog@127.0.0.1:55434/openlog?sslmode=disable \
//	  go test -tags apmga -p 1 -count=1 -run APMGA ./internal/apm ./internal/api
package apm_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/apm"
	"github.com/onuragtas/openlog/internal/migrate"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
	"github.com/onuragtas/openlog/internal/store/postgres"
	"github.com/onuragtas/openlog/migrations"
	"github.com/onuragtas/openlog/schema"
)

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func gaPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := postgres.Open(ctx, postgres.Options{DSN: env("OPENLOG_APMGA_POSTGRES_DSN", "postgres://openlog:openlog@127.0.0.1:55434/openlog?sslmode=disable"), MaxConns: 10, Application: "openlog-apmga-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
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

func gaClickHouse(t *testing.T) clickhouse.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	conn, err := clickhouse.OpenRetry(ctx, clickhouse.Options{Addr: []string{env("OPENLOG_APMGA_CLICKHOUSE_ADDR", "127.0.0.1:19001")}, Database: "default", User: "openlog", Password: "openlog"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	ms, err := migrate.Load(schema.ClickHouse, "clickhouse", "openlog")
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Run(ctx, conn, ms, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	return conn
}

type gaFixture struct {
	orgID, tenant, userA, userB, outsider string
}

func newGAFixture(t *testing.T, pool *pgxpool.Pool) gaFixture {
	t.Helper()
	ctx := context.Background()
	sfx := fmt.Sprintf("%d", time.Now().UnixNano())
	f := gaFixture{tenant: "t-apmga-" + sfx}
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (tenant_id, name) VALUES ($1, $1) RETURNING id::text`, f.tenant).Scan(&f.orgID); err != nil {
		t.Fatal(err)
	}
	for i, dst := range []*string{&f.userA, &f.userB, &f.outsider} {
		if err := pool.QueryRow(ctx, `INSERT INTO users (email, name) VALUES ($1, $2) RETURNING id::text`, fmt.Sprintf("u%d-%s@example.com", i, sfx), fmt.Sprintf("User %d", i)).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []string{f.userA, f.userB} {
		if _, err := pool.Exec(ctx, `INSERT INTO memberships (org_id, user_id, role) VALUES ($1, $2, 'member')`, f.orgID, u); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func ptr[T any](v T) *T { return &v }

func TestAPMGAErrorStore(t *testing.T) {
	pool := gaPostgres(t)
	f := newGAFixture(t, pool)
	store := apm.PGErrorStates{Pool: pool}
	ctx := context.Background()
	orders := apm.ServiceKey{Name: "orders", Namespace: "shop", Environment: "prod"}
	refA, refB := apm.ErrorGroupRef{GroupID: 0xa1, Key: orders}, apm.ErrorGroupRef{GroupID: 0xb2, Key: apm.ServiceKey{Name: "frontend"}}
	actor := apm.Actor{UserID: f.userA, Email: "a@example.com", IP: "10.0.0.1"}

	// Bulk resolve in a version.
	states, err := store.Update(ctx, f.orgID, []apm.ErrorGroupRef{refA, refB}, apm.ErrorGroupPatch{Status: ptr(apm.StatusResolved), ResolvedInVersion: ptr("1.4.3")}, actor)
	if err != nil || len(states) != 2 {
		t.Fatalf("resolve: %+v %v", states, err)
	}
	for _, st := range states {
		if st.Status != apm.StatusResolved || st.ResolvedAt == nil || st.ResolvedInVersion != "1.4.3" || st.ResolvedByEmail == "" || st.UpdatedByEmail == "" {
			t.Errorf("resolved state %+v", st)
		}
	}
	// Assignee must be a member.
	if _, err := store.Update(ctx, f.orgID, []apm.ErrorGroupRef{refA}, apm.ErrorGroupPatch{AssigneeUserID: &f.outsider}, actor); !errors.Is(err, apm.ErrNotMember) {
		t.Errorf("outsider assignee: %v", err)
	}
	if _, err := store.Update(ctx, f.orgID, []apm.ErrorGroupRef{refA}, apm.ErrorGroupPatch{AssigneeUserID: &f.userB}, actor); err != nil {
		t.Fatal(err)
	}
	// An unchanged patch writes no audit event.
	if _, err := store.Update(ctx, f.orgID, []apm.ErrorGroupRef{refA}, apm.ErrorGroupPatch{AssigneeUserID: &f.userB}, actor); err != nil {
		t.Fatal(err)
	}
	// Ignore B: resolved_at and version are cleared (CHECK constraints).
	if st, err := store.Update(ctx, f.orgID, []apm.ErrorGroupRef{refB}, apm.ErrorGroupPatch{Status: ptr(apm.StatusIgnored)}, actor); err != nil || st[0].ResolvedAt != nil || st[0].ResolvedInVersion != "" {
		t.Fatalf("ignore: %+v %v", st, err)
	}

	// Filters.
	check := func(name string, filter apm.ErrorStateFilter, want ...uint64) {
		t.Helper()
		got, err := store.States(ctx, f.orgID, filter)
		if err != nil {
			t.Fatal(err)
		}
		ids := map[uint64]bool{}
		for _, st := range got {
			ids[st.GroupID] = true
		}
		if len(ids) != len(want) {
			t.Errorf("%s: %+v", name, got)
		}
		for _, id := range want {
			if !ids[id] {
				t.Errorf("%s: missing %x in %+v", name, id, got)
			}
		}
	}
	check("resolved", apm.ErrorStateFilter{Statuses: []apm.ErrorStatus{apm.StatusResolved}}, 0xa1)
	check("assignee B", apm.ErrorStateFilter{Assignee: &f.userB}, 0xa1)
	check("unassigned", apm.ErrorStateFilter{Assignee: ptr("")}, 0xb2)
	check("service", apm.ErrorStateFilter{Service: "orders", Environment: ptr("prod")}, 0xa1)
	check("ids", apm.ErrorStateFilter{GroupIDs: []uint64{0xb2, 0xff}}, 0xb2)
	if got, _ := store.States(ctx, f.orgID, apm.ErrorStateFilter{Assignee: &f.userB}); len(got) != 1 || got[0].AssigneeEmail == "" || got[0].AssigneeName != "User 1" {
		t.Errorf("assignee join: %+v", got)
	}

	// Regression: only a still-resolved group with resolved_at before the occurrence is reopened, once.
	resolved, _ := store.States(ctx, f.orgID, apm.ErrorStateFilter{GroupIDs: []uint64{0xa1}})
	if ok, err := store.MarkRegressed(ctx, f.orgID, refA, apm.Regression{At: resolved[0].ResolvedAt.Add(-time.Minute)}); err != nil || ok {
		t.Errorf("occurrence before resolve reopened: %v %v", ok, err)
	}
	at := resolved[0].ResolvedAt.Add(time.Minute)
	if ok, err := store.MarkRegressed(ctx, f.orgID, refA, apm.Regression{At: at, Version: "1.4.3"}); err != nil || !ok {
		t.Fatalf("regression: %v %v", ok, err)
	}
	if ok, _ := store.MarkRegressed(ctx, f.orgID, refA, apm.Regression{At: at.Add(time.Minute)}); ok {
		t.Error("reopened twice")
	}
	check("regressed since", apm.ErrorStateFilter{RegressedSince: ptr(at.Add(-time.Second)), Statuses: []apm.ErrorStatus{apm.StatusUnresolved}}, 0xa1)
	st, _ := store.States(ctx, f.orgID, apm.ErrorStateFilter{GroupIDs: []uint64{0xa1}})
	if st[0].RegressionCount != 1 || st[0].ResolvedAt != nil || st[0].AssigneeUserID != f.userB {
		t.Errorf("after regression %+v", st[0])
	}

	// Comments.
	c1, err := store.AddComment(ctx, f.orgID, refA, "  looking into it  ", actor)
	if err != nil || c1.Body != "looking into it" || c1.ID == "" {
		t.Fatalf("comment: %+v %v", c1, err)
	}
	if _, err := store.AddComment(ctx, f.orgID, refA, "   ", actor); !errors.Is(err, apm.ErrInvalidErrorPatch) {
		t.Errorf("empty comment: %v", err)
	}
	c2, _ := store.AddComment(ctx, f.orgID, refA, "fixed in 1.4.4", apm.Actor{UserID: f.userB, Email: "b@example.com"})
	if cs, _ := store.Comments(ctx, f.orgID, 0xa1); len(cs) != 2 || cs[0].ID != c1.ID || cs[1].AuthorName != "User 1" {
		t.Errorf("comments %+v", cs)
	}
	if err := store.DeleteComment(ctx, f.orgID, 0xa1, c2.ID, actor, false); !errors.Is(err, apm.ErrCommentNotFound) {
		t.Errorf("delete other's comment: %v", err)
	}
	if err := store.DeleteComment(ctx, f.orgID, 0xa1, c2.ID, actor, true); err != nil {
		t.Errorf("admin delete: %v", err)
	}
	if st, _ := store.States(ctx, f.orgID, apm.ErrorStateFilter{GroupIDs: []uint64{0xa1}}); st[0].CommentCount != 1 {
		t.Errorf("comment count %+v", st[0])
	}

	// Activity: newest first; resolve, assign (not the no-op), regression, comments.
	acts, err := store.Activity(ctx, f.orgID, 0xa1, 50)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, a := range acts {
		actions = append(actions, a.Action)
	}
	want := []string{"apm.error_group.comment_delete", "apm.error_group.comment", "apm.error_group.comment", "apm.error_group.regressed", "apm.error_group.update", "apm.error_group.update"}
	if fmt.Sprint(actions) != fmt.Sprint(want) {
		t.Errorf("activity %v", actions)
	}
	if acts[3].ActorEmail != apm.RegressionActor || acts[3].Details["version"] != "1.4.3" {
		t.Errorf("regression audit %+v", acts[3])
	}

	// The alert rule type check constraint accepts apm_error.
	if _, err := pool.Exec(ctx, `INSERT INTO alert_rules (org_id, name, type, interval_seconds, condition) VALUES ($1, 'x', 'apm_error', 60, '{}')`, f.orgID); err != nil {
		t.Errorf("apm_error rule: %v", err)
	}
}

func insertErrorSpan(t *testing.T, conn clickhouse.Conn, tenant string, ts time.Time, group uint64, version, host string) {
	t.Helper()
	sql := fmt.Sprintf(`INSERT INTO openlog.spans_local (tenant_id, timestamp, duration_ns, trace_id, span_id, name, kind, status_code,
		service_name, service_namespace, deployment_environment, host_id, resource_attributes, is_entry, transaction_type, transaction_name,
		is_error, sample_weight, error_group_id, error_type, error_message)
		VALUES ('%s', fromUnixTimestamp64Nano(%d), 1000000, '%032x', '%016x', 'GET /orders/{id}', 'server', 'error',
		'orders', 'shop', 'prod', '%s', map('service.version', '%s', 'host.id', '%s'), true, 'web', 'GET /orders/{id}',
		true, 1, %d, 'Timeout', 'db <n>')`, tenant, ts.UnixNano(), ts.UnixNano(), ts.UnixNano(), host, version, host, group)
	if err := conn.Exec(context.Background(), sql); err != nil {
		t.Fatal(err)
	}
}

func TestAPMGARegressionOnClickHouse(t *testing.T) {
	pool := gaPostgres(t)
	conn := gaClickHouse(t)
	f := newGAFixture(t, pool)
	ctx := context.Background()
	store := apm.PGErrorStates{Pool: pool}
	const group = 0x5a1f0c9e3b2d4e71
	key := apm.ServiceKey{Name: "orders", Namespace: "shop", Environment: "prod"}
	ref := apm.ErrorGroupRef{GroupID: group, Key: key}
	now := time.Now().UTC()

	insertErrorSpan(t, conn, f.tenant, now.Add(-time.Hour), group, "1.0", "h1")
	if _, err := store.Update(ctx, f.orgID, []apm.ErrorGroupRef{ref}, apm.ErrorGroupPatch{Status: ptr(apm.StatusResolved), ResolvedInVersion: ptr("1.1")}, apm.Actor{UserID: f.userA}); err != nil {
		t.Fatal(err)
	}
	sc, err := query.New(conn, "openlog", 30*time.Second).Scope(f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	checkNow := func() map[uint64]apm.Regression {
		t.Helper()
		states, err := store.States(ctx, f.orgID, apm.ErrorStateFilter{Statuses: []apm.ErrorStatus{apm.StatusResolved}})
		if err != nil {
			t.Fatal(err)
		}
		_, reopened, err := apm.CheckRegressions(ctx, sc, store, f.orgID, states)
		if err != nil {
			t.Fatal(err)
		}
		return reopened
	}
	// The old version keeps failing after the resolve (draining instances): not a regression.
	insertErrorSpan(t, conn, f.tenant, now.Add(2*time.Minute), group, "1.0", "h1")
	if r := checkNow(); len(r) != 0 {
		t.Fatalf("draining version regressed: %+v", r)
	}
	// The fixed version 1.1 is deployed and fails: regression, persisted.
	insertErrorSpan(t, conn, f.tenant, now.Add(3*time.Minute), group, "1.1", "h2")
	r := checkNow()
	if len(r) != 1 || r[group].Version != "1.1" {
		t.Fatalf("regression: %+v", r)
	}
	st, _ := store.States(ctx, f.orgID, apm.ErrorStateFilter{GroupIDs: []uint64{group}})
	if st[0].EffectiveStatus() != apm.StatusUnresolved || st[0].RegressedAt == nil || st[0].RegressionCount != 1 {
		t.Errorf("stored %+v", st[0])
	}

	// The views: occurrences per version/host and the version table (first seen per version).
	rows, err := conn.Query(ctx, `SELECT dim, value, sum(samples) FROM openlog.apm_error_group_dims WHERE tenant_id = ? AND error_group_id = ? GROUP BY dim, value ORDER BY dim, value`, f.tenant, uint64(group))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]uint64{}
	for rows.Next() {
		var dim, value string
		var n uint64
		if err := rows.Scan(&dim, &value, &n); err != nil {
			t.Fatal(err)
		}
		got[dim+"="+value] = n
	}
	rows.Close()
	want := map[string]uint64{"version=1.0": 2, "version=1.1": 1, "host=h1": 2, "host=h2": 1, "transaction=GET /orders/{id}": 3}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("dims %v, want %v", got, want)
	}
	var versions uint64
	if err := conn.QueryRow(ctx, `SELECT uniqExact(service_version) FROM openlog.apm_service_versions_1m WHERE tenant_id = ?`, f.tenant).Scan(&versions); err != nil || versions != 2 {
		t.Errorf("versions %d %v", versions, err)
	}
}
