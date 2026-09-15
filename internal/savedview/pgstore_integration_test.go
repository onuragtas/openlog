//go:build integration

// Integration tests of the saved view PostgreSQL store against a real PostgreSQL 16 with all migrations applied.
//
//	OPENLOG_TEST_POSTGRES_DSN=postgres://openlog:openlog@127.0.0.1:55473/openlog?sslmode=disable \
//	  go test -tags integration -count=1 ./internal/savedview
package savedview_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/onuragtas/openlog/internal/savedview"
	"github.com/onuragtas/openlog/internal/store/postgres"
	"github.com/onuragtas/openlog/migrations"
)

func TestPGStoreIntegration(t *testing.T) {
	dsn := os.Getenv("OPENLOG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("OPENLOG_TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	pool, err := postgres.Open(ctx, postgres.Options{DSN: dsn, MaxConns: 5, Application: "openlog-savedviewtest"})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
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
	sfx := uuid.NewString()[:8]
	var orgID, otherOrg, alice, bob string
	for _, o := range []*string{&orgID, &otherOrg} {
		if err := pool.QueryRow(ctx, `INSERT INTO organizations (tenant_id, name) VALUES ($1, 'Org') RETURNING id::text`, "t-"+uuid.NewString()[:8]).Scan(o); err != nil {
			t.Fatal(err)
		}
	}
	for i, u := range []*string{&alice, &bob} {
		if err := pool.QueryRow(ctx, `INSERT INTO users (email, name) VALUES ($1, 'U') RETURNING id::text`, fmt.Sprintf("sv%d-%s@example.com", i, sfx)).Scan(u); err != nil {
			t.Fatal(err)
		}
	}
	store := savedview.NewPGStore(pool)
	m := savedview.NewManager(store)
	av := savedview.Viewer{UserID: alice, CanWrite: true}
	bv := savedview.Viewer{UserID: bob, CanWrite: true}
	admin := savedview.Viewer{UserID: bob, CanWrite: true, Admin: true}
	state := json.RawMessage(`{"filters":[{"key":"service.name","op":"=","value":"api"}],"columns":["body","attributes.http.route"],"range":"1h"}`)

	private, err := m.Create(ctx, orgID, av, savedview.Input{Signal: "logs", Name: "Errors", Visibility: "private", State: state})
	if err != nil {
		t.Fatal(err)
	}
	if private.CreatedBy != alice || private.CreatedByEmail == "" || private.CreatedAt.IsZero() {
		t.Errorf("created %+v", private)
	}
	var got map[string]any
	if err := json.Unmarshal(private.State, &got); err != nil || got["range"] != "1h" {
		t.Errorf("state round trip %s %v", private.State, err)
	}
	shared, err := m.Create(ctx, orgID, av, savedview.Input{Signal: "metrics", Name: "cpu", Visibility: "org", State: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if l, _ := m.List(ctx, orgID, bv, ""); len(l) != 1 || l[0].ID != shared.ID {
		t.Errorf("bob list %+v", l)
	}
	if l, _ := m.List(ctx, orgID, av, "logs"); len(l) != 1 || l[0].ID != private.ID {
		t.Errorf("alice logs list %+v", l)
	}
	if l, _ := m.List(ctx, otherOrg, av, ""); len(l) != 0 {
		t.Errorf("other org list %+v", l)
	}
	if _, err := m.Get(ctx, otherOrg, shared.ID, av); !errors.Is(err, savedview.ErrNotFound) {
		t.Errorf("cross-org get: %v", err)
	}
	if _, err := m.Update(ctx, orgID, shared.ID, bv, savedview.Input{Signal: "metrics", Name: "x", Visibility: "org", State: json.RawMessage(`{}`)}); !errors.Is(err, savedview.ErrForbidden) {
		t.Errorf("bob updated: %v", err)
	}
	upd, err := m.Update(ctx, orgID, shared.ID, admin, savedview.Input{Signal: "metrics", Name: "cpu (all hosts)", Description: "d", Visibility: "org", State: json.RawMessage(`{"a":1}`)})
	if err != nil || upd.Name != "cpu (all hosts)" || string(upd.State) != `{"a": 1}` || !upd.UpdatedAt.After(upd.CreatedAt) && !upd.UpdatedAt.Equal(upd.CreatedAt) {
		t.Errorf("admin update %+v %v", upd, err)
	}
	// Deleting the creator keeps the views; the private one becomes visible to admins.
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, alice); err != nil {
		t.Fatal(err)
	}
	if l, _ := m.List(ctx, orgID, admin, ""); len(l) != 2 {
		t.Errorf("admin list after creator deletion %+v", l)
	}
	if _, err := m.Delete(ctx, orgID, private.ID, admin); err != nil {
		t.Errorf("admin delete of orphaned private view: %v", err)
	}
	// Per-organization limit.
	for i := 0; i < 2; i++ {
		v := &savedview.View{ID: uuid.NewString(), OrgID: otherOrg, Signal: "logs", Name: "v", Visibility: "org", State: json.RawMessage(`{}`)}
		if err := store.Create(ctx, v, 2); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Create(ctx, &savedview.View{ID: uuid.NewString(), OrgID: otherOrg, Signal: "logs", Name: "v", Visibility: "org", State: json.RawMessage(`{}`)}, 2); !errors.Is(err, savedview.ErrLimit) {
		t.Errorf("limit: %v", err)
	}
	// Deleting an organization deletes its views.
	if _, err := pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, otherOrg); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM saved_views WHERE org_id = $1`, otherOrg).Scan(&n); err != nil || n != 0 {
		t.Errorf("views of a deleted org: %d %v", n, err)
	}
}
