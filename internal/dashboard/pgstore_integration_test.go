//go:build integration

// Integration tests of the dashboard PostgreSQL store against a real PostgreSQL 16 with all migrations applied.
//
//	OPENLOG_TEST_POSTGRES_DSN=postgres://openlog:openlog@127.0.0.1:55433/openlog?sslmode=disable \
//	  go test -tags integration -count=1 ./internal/dashboard
package dashboard_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/dashboard"
	"github.com/onuragtas/openlog/internal/store/postgres"
	"github.com/onuragtas/openlog/migrations"
)

func pgPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("OPENLOG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("OPENLOG_TEST_POSTGRES_DSN is not set")
	}
	pool, err := postgres.Open(ctx, postgres.Options{DSN: dsn, MaxConns: 10, Application: "openlog-dashboardtest"})
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

type pgFixture struct {
	orgID, otherOrg string
	alice, bob      string
}

func newPGFixture(t *testing.T, pool *pgxpool.Pool) pgFixture {
	t.Helper()
	sfx := uuid.NewString()[:8]
	var f pgFixture
	for _, o := range []*string{&f.orgID, &f.otherOrg} {
		if err := pool.QueryRow(ctx, `INSERT INTO organizations (tenant_id, name) VALUES ($1, $2) RETURNING id::text`, "t-"+uuid.NewString()[:8], "Org "+sfx).Scan(o); err != nil {
			t.Fatal(err)
		}
	}
	for i, u := range []*string{&f.alice, &f.bob} {
		if err := pool.QueryRow(ctx, `INSERT INTO users (email, name) VALUES ($1, 'U') RETURNING id::text`, fmt.Sprintf("u%d-%s@example.com", i, sfx)).Scan(u); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func TestPGStore(t *testing.T) {
	pool := pgPool(t)
	f := newPGFixture(t, pool)
	store := dashboard.NewPGStore(pool)
	m := dashboard.NewManager(store)
	va := dashboard.Viewer{UserID: f.alice, CanWrite: true}
	vb := dashboard.Viewer{UserID: f.bob, CanWrite: true}

	in := sample()
	in.Pages = append(in.Pages, dashboard.Page{Name: "Second", Widgets: []dashboard.Widget{
		{Title: "Hosts", Visualization: "table", Layout: dashboard.Layout{X: 0, Y: 0, W: 12, H: 4}, Query: "SELECT uniqueCount(host.id) FROM Host FACET os.type", Unit: "", Options: dashboard.WidgetOptions{Stacked: true}},
	}})
	d, err := m.Create(ctx, f.orgID, in, va)
	if err != nil {
		t.Fatal(err)
	}
	if d.Version != 1 || len(d.Pages) != 2 || len(d.Pages[0].Widgets) != 2 || d.Pages[1].Widgets[0].Options.Stacked != true ||
		d.Pages[0].Widgets[0].Thresholds[0].Severity != "warning" || *d.Pages[0].Widgets[0].Options.Legend != true ||
		d.Variables[0].Name != "host" || !d.Variables[0].Multi || d.CreatedBy != f.alice || d.CreatedByEmail == "" {
		t.Fatalf("created %+v", d)
	}
	// Round trip equals the normalized input.
	got, err := store.Get(ctx, f.orgID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	gj, _ := json.Marshal(got.ToExport())
	dj, _ := json.Marshal(d.ToExport())
	if string(gj) != string(dj) {
		t.Errorf("round trip\n%v\n%v", got.ToExport(), d.ToExport())
	}

	priv := sample()
	priv.Name, priv.Visibility = "alpha private", "private"
	pd, err := m.Create(ctx, f.orgID, priv, va)
	if err != nil {
		t.Fatal(err)
	}
	if list, _ := m.List(ctx, f.orgID, va, ""); len(list) != 2 || list[0].ID != pd.ID || list[1].WidgetCount != 3 || list[1].PageCount != 2 {
		t.Errorf("alice list %+v", list)
	}
	if list, _ := m.List(ctx, f.orgID, vb, "CHECK"); len(list) != 1 || list[0].ID != d.ID {
		t.Errorf("bob list %+v", list)
	}
	if list, _ := m.List(ctx, f.otherOrg, va, ""); len(list) != 0 {
		t.Errorf("other org list %+v", list)
	}
	if _, err := store.Get(ctx, f.otherOrg, d.ID); !errors.Is(err, dashboard.ErrNotFound) {
		t.Errorf("cross-org get: %v", err)
	}
	if _, err := store.Get(ctx, f.orgID, "nope"); !errors.Is(err, dashboard.ErrNotFound) {
		t.Errorf("invalid id: %v", err)
	}

	// Update: ids kept, pages removed (widgets cascade), version bumped; stale versions conflict.
	upd := dashboard.Input{Name: "Checkout v2", Version: d.Version, Variables: nil, Pages: []dashboard.Page{d.Pages[1], d.Pages[0]}}
	u, err := m.Update(ctx, f.orgID, d.ID, upd, va)
	if err != nil {
		t.Fatal(err)
	}
	if u.Version != 2 || u.Pages[0].ID != d.Pages[1].ID || u.Pages[1].Widgets[1].ID != d.Pages[0].Widgets[1].ID || len(u.Variables) != 0 {
		t.Errorf("updated %+v", u)
	}
	if _, err := m.Update(ctx, f.orgID, d.ID, upd, va); !errors.Is(err, dashboard.ErrConflict) {
		t.Errorf("stale update: %v", err)
	}

	// Concurrent writers with the same version: exactly one wins.
	var wg sync.WaitGroup
	results := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			in := upd
			in.Version = u.Version
			in.Name = fmt.Sprintf("writer %d", i)
			_, results[i] = m.Update(ctx, f.orgID, d.ID, in, va)
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range results {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, dashboard.ErrConflict):
			t.Errorf("concurrent update: %v", err)
		}
	}
	if ok != 1 {
		t.Errorf("%d concurrent updates succeeded", ok)
	}

	// Deleting the creator keeps the dashboard (created_by SET NULL); admins then see the private one.
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, f.alice); err != nil {
		t.Fatal(err)
	}
	admin := dashboard.Viewer{UserID: f.bob, CanWrite: true, Admin: true}
	if list, _ := m.List(ctx, f.orgID, admin, ""); len(list) != 2 {
		t.Errorf("admin list after user deletion %+v", list)
	}
	if _, err := m.Delete(ctx, f.orgID, pd.ID, admin); err != nil {
		t.Errorf("admin delete: %v", err)
	}
	if _, err := m.Delete(ctx, f.orgID, d.ID, vb); !errors.Is(err, dashboard.ErrForbidden) {
		// d has no creator now and bob is not admin
		t.Errorf("member delete of orphan: %v", err)
	}
	if err := store.Delete(ctx, f.orgID, d.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dashboard_widgets WHERE dashboard_id = $1`, d.ID).Scan(&n); err != nil || n != 0 {
		t.Errorf("widgets left after delete: %d %v", n, err)
	}
	if err := store.Delete(ctx, f.orgID, d.ID); !errors.Is(err, dashboard.ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	// Deleting the organization removes its dashboards.
	e, err := m.Create(ctx, f.otherOrg, sample(), vb)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, f.otherOrg); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dashboards WHERE id = $1`, e.ID).Scan(&n); err != nil || n != 0 {
		t.Errorf("dashboard of deleted org: %d %v", n, err)
	}
	_ = time.Now
}

func TestPGStoreCheckConstraintsBackValidation(t *testing.T) {
	pool := pgPool(t)
	f := newPGFixture(t, pool)
	id := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO dashboards (id, org_id, name) VALUES ($1, $2, 'x')`, id, f.orgID); err != nil {
		t.Fatal(err)
	}
	page := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO dashboard_pages (id, dashboard_id, position, name) VALUES ($1, $2, 0, 'p')`, page, id); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`INSERT INTO dashboard_widgets (id, dashboard_id, page_id, position, visualization, x, y, w, h) VALUES (gen_random_uuid(), $1, $2, 0, 'gauge', 0, 0, 1, 1)`,
		`INSERT INTO dashboard_widgets (id, dashboard_id, page_id, position, visualization, x, y, w, h) VALUES (gen_random_uuid(), $1, $2, 0, 'line', 8, 0, 6, 1)`,
		`INSERT INTO dashboards (org_id, name, visibility) VALUES ($1::uuid, 'y', 'public') -- $2`,
	} {
		args := []any{id, page}
		if _, err := pool.Exec(ctx, bad, args...); err == nil {
			t.Errorf("accepted: %s", bad)
		}
	}
	var types string
	if err := pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'alert_rules_type_check'`).Scan(&types); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"'oql'", "'apm_error'"} {
		if !contains(types, want) {
			t.Errorf("alert_rules_type_check %s lacks %s", types, want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
