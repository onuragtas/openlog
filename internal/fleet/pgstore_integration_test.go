//go:build integration

// Integration tests of PGStore against a real PostgreSQL 16.
//
//	go test -tags integration -count=1 ./internal/fleet
//
// Without OPENLOG_TEST_POSTGRES_DSN the test starts the compose project openlog-fleettest
// (test/integration/fleet, host port FLEETTEST_PORT or 55442) and removes it afterwards
// (FLEETTEST_KEEP=1 keeps it).
package fleet_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/fleet"
	"github.com/onuragtas/openlog/internal/store/postgres"
	"github.com/onuragtas/openlog/migrations"
)

var pgPool *pgxpool.Pool

func fleetCompose(args ...string) error {
	root, _ := filepath.Abs("../..")
	cmd := exec.Command("docker", append([]string{"compose", "-p", "openlog-fleettest", "-f",
		filepath.Join(root, "test/integration/fleet/docker-compose.yml")}, args...)...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose %v: %v\n%s", args, err, out.String())
	}
	return nil
}

func TestMain(m *testing.M) {
	dsn := os.Getenv("OPENLOG_TEST_POSTGRES_DSN")
	started := false
	if dsn == "" {
		port := os.Getenv("FLEETTEST_PORT")
		if port == "" {
			port = "55442"
		}
		if err := fleetCompose("up", "-d", "--wait"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		started = true
		dsn = "postgres://openlog:openlog@127.0.0.1:" + port + "/openlog?sslmode=disable"
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	code := func() int {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		var err error
		pgPool, err = postgres.Open(ctx, postgres.Options{DSN: dsn, Application: "openlog-fleettest"})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer pgPool.Close()
		if err := postgres.WaitReady(ctx, pgPool, log); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		ms, err := postgres.LoadMigrations(migrations.Postgres, "postgres")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := postgres.Migrate(ctx, pgPool, ms, log); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return m.Run()
	}()
	if started && os.Getenv("FLEETTEST_KEEP") == "" {
		_ = fleetCompose("down", "-v")
	}
	os.Exit(code)
}

// newOrg creates an organization and a user and returns (org id, tenant id, user id).
func newOrg(t *testing.T) (string, string, string) {
	t.Helper()
	ctx := context.Background()
	tenant := "t" + uuid.NewString()[:8]
	var orgID, userID string
	if err := pgPool.QueryRow(ctx, `INSERT INTO organizations (tenant_id, name) VALUES ($1, $1) RETURNING id::text`, tenant).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	if err := pgPool.QueryRow(ctx, `INSERT INTO users (email) VALUES ($1) RETURNING id::text`, tenant+"@example.com").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	return orgID, tenant, userID
}

func TestPGStorePolicyAndOverrides(t *testing.T) {
	ctx := context.Background()
	st := fleet.NewPGStore(pgPool)
	org, _, user := newOrg(t)

	sp, err := st.GetPolicy(ctx, org)
	if err != nil || !sp.IsDefault || sp.Mode != fleet.ModeAuto || len(sp.Waves) != 3 {
		t.Fatalf("default policy = %+v, %v", sp, err)
	}
	p := fleet.DefaultPolicy()
	v := "0.4.0"
	p.Mode, p.Target, p.PinnedVersion, p.Waves = fleet.ModeNotify, fleet.TargetPinned, &v, []int{5, 25, 100}
	p.MaintenanceWindows = []fleet.Window{{Days: []string{"sat"}, Start: "01:00", End: "04:00"}}
	if err := st.PutPolicy(ctx, org, p, user, time.Now()); err != nil {
		t.Fatal(err)
	}
	sp, err = st.GetPolicy(ctx, org)
	if err != nil || sp.IsDefault || sp.Mode != fleet.ModeNotify || *sp.PinnedVersion != "0.4.0" || fmt.Sprint(sp.Waves) != "[5 25 100]" ||
		sp.MaintenanceWindows[0].Days[0] != "sat" || sp.UpdatedByEmail == "" {
		t.Fatalf("stored policy = %+v, %v", sp, err)
	}
	// The database rejects a pinned target without a version even if the API is bypassed.
	p.PinnedVersion = nil
	if err := st.PutPolicy(ctx, org, p, user, time.Now()); err == nil {
		t.Error("pinned policy without version stored")
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := st.PutOverride(ctx, org, fleet.Override{HostID: "h1", Action: fleet.OverridePin, Version: "0.3.1", UpdatedAt: now}, user); err != nil {
		t.Fatal(err)
	}
	if err := st.PutOverride(ctx, org, fleet.Override{HostID: "h1", Action: fleet.OverrideHold, UpdatedAt: now}, user); err != nil {
		t.Fatal(err)
	}
	ovs, err := st.ListOverrides(ctx, org)
	if err != nil || len(ovs) != 1 || ovs["h1"].Action != fleet.OverrideHold || ovs["h1"].Version != "" {
		t.Fatalf("overrides = %+v, %v", ovs, err)
	}
	if deleted, err := st.DeleteOverride(ctx, org, "h1"); err != nil || !deleted {
		t.Fatalf("delete = %v %v", deleted, err)
	}
	if deleted, _ := st.DeleteOverride(ctx, org, "h1"); deleted {
		t.Error("second delete reported a row")
	}
}

func TestPGStoreHostsBatchUpsert(t *testing.T) {
	ctx := context.Background()
	st := fleet.NewPGStore(pgPool)
	org, tenant, _ := newOrg(t)
	t0 := time.Now().UTC().Truncate(time.Second)
	var recs []fleet.HostRecord
	for i := range 250 {
		recs = append(recs, fleet.HostRecord{TenantID: tenant, SyncAt: t0, Report: fleet.HostReport{
			HostID: fmt.Sprintf("h%03d", i), HostName: fmt.Sprintf("web-%03d", i), Version: "0.3.0", OS: "linux", Arch: "amd64",
			InstallMethod: "tarball", UpdateCapable: i%10 != 0, UpdateState: fleet.StateIdle}})
	}
	// Reports of an unknown tenant are skipped, not failing the batch.
	recs = append(recs, fleet.HostRecord{TenantID: "no-such-tenant", SyncAt: t0, Report: fleet.HostReport{HostID: "x"}})
	if err := st.UpsertHosts(ctx, recs); err != nil {
		t.Fatal(err)
	}

	rollout := uuid.NewString()
	t1 := t0.Add(time.Minute)
	upd := []fleet.HostRecord{
		{TenantID: tenant, SyncAt: t1, RolloutID: rollout, Report: fleet.HostReport{HostID: "h001", HostName: "web-001", Version: "0.3.0",
			UpdateCapable: true, UpdateState: fleet.StateDownloading, UpdateTo: "0.4.0", UpdateChangedAt: t1}},
	}
	if err := st.UpsertHosts(ctx, upd); err != nil {
		t.Fatal(err)
	}
	// A later sync without an offer keeps the rollout id; an older sync does not move last_sync_at back.
	upd[0].RolloutID, upd[0].SyncAt = "", t0
	upd[0].Report.UpdateState, upd[0].Report.Version = fleet.StateSucceeded, "0.4.0"
	if err := st.UpsertHosts(ctx, upd); err != nil {
		t.Fatal(err)
	}
	h, err := st.GetHost(ctx, org, "h001")
	if err != nil {
		t.Fatal(err)
	}
	if h.RolloutID != rollout || h.Version != "0.4.0" || h.UpdateState != fleet.StateSucceeded || !h.LastSyncAt.Equal(t1) || !h.FirstSeenAt.Equal(t0) {
		t.Fatalf("host = %+v", h)
	}
	if _, err := st.GetHost(ctx, org, "x"); !errors.Is(err, fleet.ErrNotFound) {
		t.Errorf("unknown tenant host stored: %v", err)
	}

	// Pagination, filters and LIKE escaping.
	var all []fleet.Host
	cursor := ""
	for {
		page, next, err := st.ListHosts(ctx, org, fleet.HostFilter{Limit: 100, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page...)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(all) != 250 || all[0].HostName != "web-000" || all[249].HostName != "web-249" {
		t.Fatalf("paged %d hosts", len(all))
	}
	if page, _, _ := st.ListHosts(ctx, org, fleet.HostFilter{Limit: 10, Version: "0.4.0"}); len(page) != 1 {
		t.Errorf("version filter = %d", len(page))
	}
	if page, _, _ := st.ListHosts(ctx, org, fleet.HostFilter{Limit: 10, State: "succeeded"}); len(page) != 1 {
		t.Errorf("state filter = %d", len(page))
	}
	if page, _, _ := st.ListHosts(ctx, org, fleet.HostFilter{Limit: 500, Query: "WEB-01"}); len(page) != 10 {
		t.Errorf("q filter = %d", len(page))
	}
	if page, _, _ := st.ListHosts(ctx, org, fleet.HostFilter{Limit: 500, Query: "%"}); len(page) != 0 {
		t.Errorf("%% matched %d hosts (LIKE not escaped)", len(page))
	}
	if _, _, err := st.ListHosts(ctx, org, fleet.HostFilter{Limit: 10, Cursor: "!!"}); err == nil {
		t.Error("bad cursor accepted")
	}

	groups, err := st.HostGroups(ctx, org, t0.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	total, recent := 0, 0
	for _, g := range groups {
		total += g.Hosts
		recent += g.RecentlySeen
	}
	if total != 250 || recent != 1 {
		t.Errorf("groups: total %d recent %d (%+v)", total, recent, groups)
	}
	active, _ := st.AllHosts(ctx, org, t0.Add(30*time.Second))
	if len(active) != 1 {
		t.Errorf("AllHosts since = %d", len(active))
	}
	orgs, _ := st.FleetOrgs(ctx)
	found := false
	for _, o := range orgs {
		found = found || o == org
	}
	if !found {
		t.Error("org missing from FleetOrgs")
	}
}

func TestPGStoreRollouts(t *testing.T) {
	ctx := context.Background()
	st := fleet.NewPGStore(pgPool)
	org, tenant, user := newOrg(t)
	now := time.Now().UTC().Truncate(time.Microsecond)

	r1 := &fleet.Rollout{OrgID: org, Action: fleet.ActionUpgrade, FromVersion: "0.3.0", ToVersion: "0.4.0", Waves: []int{10, 50, 100},
		WaveStartedAt: now, WaveSoakMinutes: 60, HaltFailureRate: 0.05, State: fleet.RolloutActive, CreatedAt: now}
	if err := st.CreateRollout(ctx, r1, ""); err != nil {
		t.Fatal(err)
	}
	r1.Counters = fleet.Counters{Pending: 7, Attempted: 3, Succeeded: 2, Failed: 1}
	r1.CurrentWave, r1.State, r1.StateReason, r1.UpdatedAt = 1, fleet.RolloutHalted, "failure rate", now
	if err := st.UpdateRollout(ctx, r1, fleet.RolloutActive); err != nil {
		t.Fatal(err)
	}
	// Conditional write: the state is no longer active.
	if err := st.UpdateRollout(ctx, r1, fleet.RolloutActive); !errors.Is(err, fleet.ErrConflict) {
		t.Fatalf("stale update err = %v", err)
	}
	got, err := st.GetRollout(ctx, org, r1.ID)
	if err != nil || got.State != fleet.RolloutHalted || got.Counters != r1.Counters || got.CurrentWave != 1 || got.Targets == nil {
		t.Fatalf("rollout = %+v, %v", got, err)
	}

	later := now.Add(time.Minute)
	r2 := &fleet.Rollout{OrgID: org, Action: fleet.ActionRollback, FromVersion: "0.4.0", ToVersion: "0.3.0", Waves: []int{100},
		WaveStartedAt: later, WaveSoakMinutes: 0, HaltFailureRate: 0.1, State: fleet.RolloutActive, CreatedBy: user, CreatedAt: later}
	if err := st.CreateRollout(ctx, r2, "superseded by rollback"); err != nil {
		t.Fatal(err)
	}
	old, _ := st.GetRollout(ctx, org, r1.ID)
	if old.State != fleet.RolloutSuperseded || old.EndedAt == nil || old.StateReason != "superseded by rollback" {
		t.Fatalf("old rollout = %+v", old)
	}
	cur, err := st.CurrentRollout(ctx, org)
	if err != nil || cur.ID != r2.ID || cur.CreatedByEmail == "" {
		t.Fatalf("current = %+v, %v", cur, err)
	}
	r3 := &fleet.Rollout{OrgID: org, Action: fleet.ActionUpgrade, Targets: map[string]string{"0.3": "0.3.2"}, Waves: []int{100},
		WaveStartedAt: later, State: fleet.RolloutActive, CreatedAt: later.Add(time.Second)}
	if err := st.CreateRollout(ctx, r3, "superseded"); err != nil {
		t.Fatal(err)
	}
	list, _ := st.ListRollouts(ctx, org, 10)
	if len(list) != 3 || list[0].ID != r3.ID || list[0].ToVersion != "" || list[0].Targets["0.3"] != "0.3.2" {
		t.Fatalf("list = %+v", list)
	}

	// One open rollout per organization even under concurrent creation.
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := &fleet.Rollout{OrgID: org, Action: fleet.ActionUpgrade, ToVersion: fmt.Sprintf("0.5.%d", i), Waves: []int{100},
				WaveStartedAt: later, State: fleet.RolloutActive, CreatedAt: later.Add(time.Duration(2+i) * time.Second)}
			errs <- st.CreateRollout(ctx, r, "superseded")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, fleet.ErrConflict) {
			t.Errorf("concurrent create: %v", err)
		}
	}
	var open int
	if err := pgPool.QueryRow(ctx, `SELECT count(*) FROM agent_rollouts WHERE org_id = $1 AND state IN ('active','paused','halted')`, org).Scan(&open); err != nil || open != 1 {
		t.Fatalf("open rollouts = %d, %v", open, err)
	}

	st2, err := st.LoadOrgState(ctx, tenant)
	if err != nil || st2.OrgID != org || st2.Rollout == nil || st2.Policy.Mode != fleet.ModeAuto {
		t.Fatalf("org state = %+v, %v", st2, err)
	}
	if _, err := st.LoadOrgState(ctx, "no-such-tenant"); !errors.Is(err, fleet.ErrNotFound) {
		t.Errorf("unknown tenant err = %v", err)
	}

	if err := st.AddAudit(ctx, fleet.AuditEntry{OrgID: org, ActorEmail: "openlog-controller", Action: "fleet.rollout.create", TargetType: "rollout",
		TargetID: r3.ID, Details: map[string]any{"to_version": "0.3.2"}}); err != nil {
		t.Fatal(err)
	}
}
