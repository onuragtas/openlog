//go:build integration

// Integration tests of the alert PostgreSQL store against a real PostgreSQL 16.
//
//	go test -tags integration -count=1 ./internal/alert
//
// Without OPENLOG_TEST_POSTGRES_DSN the test starts the compose project openlog-alerttest
// (test/integration/alert, host port ALERTTEST_PORT or 55462) and removes it afterwards (ALERTTEST_KEEP=1 keeps it).
package alert_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/alert"
	"github.com/onuragtas/openlog/internal/alert/notify"
	"github.com/onuragtas/openlog/internal/alert/secrets"
	"github.com/onuragtas/openlog/internal/store/postgres"
	"github.com/onuragtas/openlog/migrations"
)

var pgPool *pgxpool.Pool

func alertCompose(args ...string) error {
	root, _ := filepath.Abs("../..")
	cmd := exec.Command("docker", append([]string{"compose", "-p", "openlog-alerttest", "-f",
		filepath.Join(root, "test/integration/alert/docker-compose.yml")}, args...)...)
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
		port := os.Getenv("ALERTTEST_PORT")
		if port == "" {
			port = "55462"
		}
		if err := alertCompose("up", "-d", "--wait"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		started = true
		dsn = "postgres://openlog:openlog@127.0.0.1:" + port + "/openlog?sslmode=disable"
	}
	code := func() int {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
		var err error
		if pgPool, err = postgres.Open(ctx, postgres.Options{DSN: dsn, MaxConns: 30, Application: "openlog-alerttest"}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer pgPool.Close()
		if err := postgres.WaitReady(ctx, pgPool, log); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		ms, err := postgres.LoadMigrations(migrations.PostgresFS(), "postgres")
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
	if started && os.Getenv("ALERTTEST_KEEP") != "1" {
		_ = alertCompose("down", "-v")
	}
	os.Exit(code)
}

var ctx = context.Background()

type fixture struct {
	store   *alert.PGStore
	orgID   string
	userID  string
	channel *alert.Channel
	keys    *secrets.Keyring
	actor   alert.Actor
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{store: alert.NewPGStore(pgPool)}
	sfx := uuid.NewString()[:8]
	if err := pgPool.QueryRow(ctx, `INSERT INTO organizations (tenant_id, name) VALUES ($1, $2) RETURNING id::text`, "t-"+sfx, "Org "+sfx).Scan(&f.orgID); err != nil {
		t.Fatal(err)
	}
	if err := pgPool.QueryRow(ctx, `INSERT INTO users (email, name) VALUES ($1, 'U') RETURNING id::text`, "u-"+sfx+"@example.com").Scan(&f.userID); err != nil {
		t.Fatal(err)
	}
	f.actor = alert.Actor{UserID: f.userID, Email: "u-" + sfx + "@example.com", IP: "192.0.2.1"}
	var err error
	if f.keys, err = secrets.NewKeyring(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), ""); err != nil {
		t.Fatal(err)
	}
	p, err := alert.PrepareChannel(alert.ChannelInput{Name: "hook", Type: notify.TypeWebhook, Secrets: map[string]string{"url": "https://hooks.example.com/x"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	stored, keyID, err := alert.EncryptSecrets(f.keys, f.orgID, id, p.Secrets)
	if err != nil {
		t.Fatal(err)
	}
	if f.channel, err = f.store.CreateChannel(ctx, f.orgID, id, p, stored, keyID, f.actor); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) rule(t *testing.T) *alert.RuleView {
	t.Helper()
	d, err := alert.RuleInput{Name: "High CPU", Type: alert.TypeMetricThreshold, IntervalSeconds: 60,
		Condition:  json.RawMessage(`{"metric":"system.cpu.utilization","window_seconds":60,"operator":"gt","threshold":0.9,"group_by":["host"]}`),
		ChannelIDs: []string{f.channel.ID}}.Validate()
	if err != nil {
		t.Fatal(err)
	}
	v, err := f.store.CreateRule(ctx, f.orgID, d, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// claimRule takes the lease of one specific rule for instance (releasing other free leases it grabbed).
func claimRule(t *testing.T, s *alert.PGStore, instance, ruleID string) alert.Lease {
	t.Helper()
	if _, err := pgPool.Exec(ctx, `UPDATE alert_rule_leases SET owner = $1, lease_until = now() + interval '30 seconds' WHERE rule_id = $2`, instance, ruleID); err != nil {
		t.Fatal(err)
	}
	ls, err := s.Renew(ctx, instance, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range ls {
		if l.RuleID == ruleID {
			return l
		}
	}
	t.Fatalf("lease of %s not held", ruleID)
	return alert.Lease{}
}

func TestCommitFencingIncidentsAndOutbox(t *testing.T) {
	f := newFixture(t)
	v := f.rule(t)
	rule, chans, err := f.store.LoadRule(ctx, v.ID)
	if err != nil || rule.TenantID == "" || rule.OrgName == "" || len(chans) != 1 || rule.Condition == nil {
		t.Fatalf("load rule: %+v %v %v", rule, chans, err)
	}
	lease := claimRule(t, f.store, "pod-a", v.ID)
	end := time.Now().UTC().Truncate(time.Second)
	breach := &alert.EvalResult{Samples: []alert.Sample{{Key: "host.id=h1", Labels: map[string]string{"host.id": "h1", "host.name": "web-1"}, Value: 0.97}}}
	plan := alert.BuildPlan(alert.PlanInput{Rule: rule, Channels: chans, States: map[string]alert.SeriesState{}, End: end, Result: breach, PublicURL: "http://ui"})
	plan.NextEvalAt = end.Add(time.Minute)
	if len(plan.Opens) != 1 || len(plan.Notifications) != 1 {
		t.Fatalf("plan %+v", plan)
	}

	// Fencing: another instance, and this instance after its lease expired, cannot commit.
	if err := f.store.Commit(ctx, "pod-b", plan); !errors.Is(err, alert.ErrLeaseLost) {
		t.Fatalf("foreign commit: %v", err)
	}
	if err := f.store.Commit(ctx, "pod-a", plan); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := f.store.Commit(ctx, "pod-a", plan); !errors.Is(err, alert.ErrStale) {
		t.Fatalf("second commit of the same window: %v", err)
	}
	incs, _, counts, err := f.store.ListIncidents(ctx, f.orgID, alert.IncidentFilter{})
	if err != nil || len(incs) != 1 || counts.Open != 1 || incs[0].Labels["host.name"] != "web-1" || len(incs[0].ChannelIDs) != 1 {
		t.Fatalf("incidents %+v %+v %v", incs, counts, err)
	}
	states, err := f.store.LoadSeries(ctx, v.ID)
	if err != nil || states["host.id=h1"].State != alert.StateFiring || states["host.id=h1"].Incident == nil {
		t.Fatalf("series %+v %v", states, err)
	}

	// A rule edit bumps the version: an evaluation of the old version is discarded.
	stale := alert.BuildPlan(alert.PlanInput{Rule: rule, Channels: chans, States: states, PrevEvalEnd: end, End: end.Add(time.Minute), Result: breach})
	d := rule.Definition
	d.Name = "High CPU (renamed)"
	if _, err := f.store.UpdateRule(ctx, f.orgID, v.ID, &d, 0, f.actor, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Commit(ctx, "pod-a", stale); !errors.Is(err, alert.ErrRuleChanged) {
		t.Fatalf("commit after rule change: %v", err)
	}
	// The name-only edit kept the incident open.
	if _, _, counts, _ := f.store.ListIncidents(ctx, f.orgID, alert.IncidentFilter{}); counts.Open != 1 {
		t.Fatalf("name change resolved incidents: %+v", counts)
	}

	// Lease expiry: the owner can no longer commit.
	if _, err := pgPool.Exec(ctx, `UPDATE alert_rule_leases SET lease_until = now() - interval '1 second' WHERE rule_id = $1`, v.ID); err != nil {
		t.Fatal(err)
	}
	rule, chans, _ = f.store.LoadRule(ctx, v.ID)
	late := alert.BuildPlan(alert.PlanInput{Rule: rule, Channels: chans, States: states, PrevEvalEnd: end, End: end.Add(2 * time.Minute), Result: breach})
	if err := f.store.Commit(ctx, "pod-a", late); !errors.Is(err, alert.ErrLeaseLost) {
		t.Fatalf("commit with expired lease: %v", err)
	}
	_ = lease

	// Outbox: the opened notification is claimed once even by concurrent dispatchers.
	var (
		mu      sync.Mutex
		claimed []string
		wg      sync.WaitGroup
	)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ds, err := f.store.ClaimDeliveries(ctx, fmt.Sprintf("disp-%d", i), 10, time.Minute)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, d := range ds {
				if d.OrgID == f.orgID {
					claimed = append(claimed, d.ID+"@"+fmt.Sprint(i))
					if d.Channel == nil || d.Incident == nil || d.Kind != alert.KindOpened {
						t.Errorf("delivery not enriched: %+v", d)
					}
				}
			}
		}(i)
	}
	wg.Wait()
	if len(claimed) != 1 {
		t.Fatalf("notification claimed %d times: %v", len(claimed), claimed)
	}
}

func TestDispatchResolveAndDeliveryLog(t *testing.T) {
	f := newFixture(t)
	v := f.rule(t)
	rule, chans, _ := f.store.LoadRule(ctx, v.ID)
	claimRule(t, f.store, "pod-a", v.ID)
	end := time.Now().UTC().Truncate(time.Second)
	breach := &alert.EvalResult{Samples: []alert.Sample{{Key: "host.id=h1", Labels: map[string]string{"host.id": "h1"}, Value: 0.97}}}
	plan := alert.BuildPlan(alert.PlanInput{Rule: rule, Channels: chans, States: map[string]alert.SeriesState{}, End: end, Result: breach})
	plan.NextEvalAt = end.Add(time.Minute)
	if err := f.store.Commit(ctx, "pod-a", plan); err != nil {
		t.Fatal(err)
	}
	sender := &recordingSender{}
	disp := alert.NewDispatcher(f.store, sender, f.keys, alert.DispatcherOptions{Instance: "disp", Workers: 2})
	drain := func() {
		for i := 0; i < 5; i++ {
			if _, err := disp.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	drain()
	incID := plan.Opens[0].ID
	// Manual resolve: series restarts, a resolve notification goes to the channel that got the opening.
	inc, err := f.store.ResolveIncident(ctx, f.orgID, incID, "fixed by restart", f.actor, "")
	if err != nil || inc.State != alert.IncidentResolved || inc.ResolveReason != alert.ReasonManual {
		t.Fatalf("resolve: %+v %v", inc, err)
	}
	if _, err := f.store.ResolveIncident(ctx, f.orgID, incID, "", f.actor, ""); err == nil {
		t.Fatal("second resolve accepted")
	}
	if states, _ := f.store.LoadSeries(ctx, v.ID); len(states) != 0 {
		t.Fatalf("series not reset: %+v", states)
	}
	drain()
	keys := sender.keys()
	want := []string{incID + ":opened:" + f.channel.ID, incID + ":resolved:" + f.channel.ID}
	sort.Strings(want)
	if fmt.Sprint(keys) != fmt.Sprint(want) {
		t.Fatalf("sent %v, want %v", keys, want)
	}
	_, events, deliveries, err := f.store.GetIncident(ctx, f.orgID, incID)
	if err != nil || len(deliveries) != 2 {
		t.Fatalf("incident detail: %d deliveries, %v", len(deliveries), err)
	}
	kinds := map[string]int{}
	for _, e := range events {
		kinds[e.Kind]++
	}
	if kinds[alert.EventOpened] != 1 || kinds[alert.EventResolved] != 1 || kinds[alert.EventNotificationDelivered] != 2 {
		t.Errorf("timeline %v", kinds)
	}
	for _, d := range deliveries {
		if d.Status != alert.StatusDelivered || len(d.AttemptLog) != 1 || !d.AttemptLog[0].Success {
			t.Errorf("delivery %+v", d)
		}
	}
	// Other organizations see nothing.
	other := newFixture(t)
	if _, _, _, err := other.store.GetIncident(ctx, other.orgID, incID); !errors.Is(err, alert.ErrNotFound) {
		t.Errorf("cross-org incident read: %v", err)
	}
	if _, _, err := other.store.GetRule(ctx, other.orgID, v.ID); !errors.Is(err, alert.ErrNotFound) {
		t.Errorf("cross-org rule read: %v", err)
	}

	// Disabling resolves open incidents and releases the lease on the next renewal.
	plan2 := alert.BuildPlan(alert.PlanInput{Rule: rule, Channels: chans, States: map[string]alert.SeriesState{}, End: end.Add(time.Minute),
		PrevEvalEnd: end, Result: breach})
	plan2.NextEvalAt = end.Add(2 * time.Minute)
	if err := f.store.Commit(ctx, "pod-a", plan2); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SetRuleEnabled(ctx, f.orgID, v.ID, false, f.actor, ""); err != nil {
		t.Fatal(err)
	}
	_, _, counts, _ := f.store.ListIncidents(ctx, f.orgID, alert.IncidentFilter{})
	if counts.Open != 0 {
		t.Fatalf("disable left open incidents: %+v", counts)
	}
	if ls, _ := f.store.Renew(ctx, "pod-a", 30*time.Second); containsLease(ls, v.ID) {
		t.Fatal("lease of disabled rule renewed")
	}
}

func containsLease(ls []alert.Lease, id string) bool {
	for _, l := range ls {
		if l.RuleID == id {
			return true
		}
	}
	return false
}

type recordingSender struct {
	mu   sync.Mutex
	sent []string
}

func (r *recordingSender) Send(_ context.Context, _ notify.Target, ev notify.Event, _ string) notify.Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, ev.IdempotencyKey)
	return notify.Result{StatusCode: 200}
}

func (r *recordingSender) keys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]string(nil), r.sent...)
	sort.Strings(out)
	return out
}

func TestLeaseClaimsAreExclusive(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 12; i++ {
		f.rule(t)
	}
	// Three pods claim concurrently many times: no rule is ever held by two unexpired owners.
	var wg sync.WaitGroup
	for _, pod := range []string{"p1", "p2", "p3"} {
		wg.Add(1)
		go func(pod string) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if _, err := f.store.Claim(ctx, pod, 5, 30*time.Second); err != nil {
					t.Error(err)
				}
				if _, err := f.store.Release(ctx, pod, 1); err != nil {
					t.Error(err)
				}
			}
		}(pod)
	}
	wg.Wait()
	var dup int
	if err := pgPool.QueryRow(ctx, `SELECT count(*) FROM (SELECT rule_id FROM alert_rule_leases WHERE owner <> '' AND lease_until > now()
		GROUP BY rule_id HAVING count(*) > 1) d`).Scan(&dup); err != nil || dup != 0 {
		t.Fatalf("duplicate owners %d %v", dup, err)
	}
	live, err := f.store.Heartbeat(ctx, "p1", "test", 30*time.Second)
	if err != nil || live < 1 {
		t.Fatalf("heartbeat live=%d %v", live, err)
	}
	if err := f.store.Leave(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	var held int
	_ = pgPool.QueryRow(ctx, `SELECT count(*) FROM alert_rule_leases WHERE owner = 'p1'`).Scan(&held)
	if held != 0 {
		t.Fatalf("leave kept %d leases", held)
	}
}

func TestSecretRotationAndMutes(t *testing.T) {
	f := newFixture(t)
	newKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	oldKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	ring, err := secrets.NewKeyring(newKey, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, pending, err := f.store.RotateSecrets(ctx, ring, true); err != nil || pending < 1 {
		t.Fatalf("check: pending=%d %v", pending, err)
	}
	rotated, failed, _, err := f.store.RotateSecrets(ctx, ring, false)
	if err != nil || rotated < 1 {
		t.Fatalf("rotate: %d %d %v", rotated, failed, err)
	}
	ch, err := f.store.GetChannel(ctx, f.orgID, f.channel.ID)
	if err != nil || ch.SecretsKeyID != ring.CurrentKeyID() {
		t.Fatalf("channel key %q, want %q (%v)", ch.SecretsKeyID, ring.CurrentKeyID(), err)
	}
	onlyNew, _ := secrets.NewKeyring(newKey, "")
	if sec, err := alert.DecryptSecrets(onlyNew, f.orgID, ch.ID, ch.Secrets); err != nil || sec["url"] != "https://hooks.example.com/x" {
		t.Fatalf("decrypt after rotation: %v %v", sec, err)
	}

	now := time.Now().UTC()
	m, err := f.store.CreateMute(ctx, f.orgID, &alert.ValidMute{Name: "maint", StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour),
		Matchers: []alert.MuteMatcher{{Label: "host.name", Op: "eq", Value: "web-1"}}}, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	active, err := f.store.ActiveMutes(ctx, f.orgID, now)
	if err != nil || len(active) != 1 || active[0].ID != m.ID || len(active[0].Matchers) != 1 {
		t.Fatalf("active mutes %+v %v", active, err)
	}
	if err := f.store.DeleteMute(ctx, f.orgID, m.ID, f.actor); err != nil {
		t.Fatal(err)
	}

	// Recurring mute (§5.2): every day 00:00-23:59 UTC, so it is active now; the stored window is the occurrence.
	parse := func(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) }
	vm, err := alert.MuteInput{Name: "nightly", Schedule: &alert.MuteScheduleInput{RRule: "FREQ=DAILY", StartTime: "00:00", EndTime: "23:59"}}.Validate(parse, now)
	if err != nil {
		t.Fatal(err)
	}
	rm, err := f.store.CreateMute(ctx, f.orgID, vm, f.actor)
	if err != nil || rm.Schedule == nil || rm.Schedule.RRule != "FREQ=DAILY" || len(rm.Schedule.Days) != 7 {
		t.Fatalf("recurring mute %+v %v", rm, err)
	}
	if active, err := f.store.ActiveMutes(ctx, f.orgID, now); err != nil || len(active) != 1 || active[0].ID != rm.ID {
		// 23:59-00:00 UTC is the one minute without an occurrence.
		if now.Hour() != 23 || now.Minute() != 59 {
			t.Fatalf("recurring active mutes %+v %v", active, err)
		}
	}
	// Pretend the stored window ended yesterday: the roll moves it to the current occurrence, once.
	if _, err := pgPool.Exec(ctx, `UPDATE alert_mutes SET starts_at = $2, ends_at = $3 WHERE id = $1`, rm.ID, now.Add(-48*time.Hour), now.Add(-47*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, err := f.store.RollRecurringMutes(ctx, now); err != nil || n != 1 {
		t.Fatalf("roll: %d %v", n, err)
	}
	if n, _ := f.store.RollRecurringMutes(ctx, now); n != 0 {
		t.Errorf("second roll moved %d mutes", n)
	}
	got, err := f.store.GetMute(ctx, f.orgID, rm.ID)
	wantStart, wantEnd, _ := rm.Schedule.Window(now)
	if err != nil || !got.StartsAt.Equal(wantStart) || !got.EndsAt.Equal(wantEnd) {
		t.Errorf("rolled window %s – %s, want %s – %s (%v)", got.StartsAt, got.EndsAt, wantStart, wantEnd, err)
	}
	if list, err := f.store.ListMutes(ctx, f.orgID, false); err != nil || len(list) != 1 {
		t.Errorf("list mutes %d %v", len(list), err)
	}
	var audits int
	_ = pgPool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE org_id = $1 AND action LIKE 'alert.%'`, f.orgID).Scan(&audits)
	if audits < 3 {
		t.Errorf("audit events %d", audits)
	}
}
