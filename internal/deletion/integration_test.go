//go:build integration

// Integration tests of account and organization deletion against PostgreSQL 16 and a single-node ClickHouse cluster
// with the full schema (throwaway containers, e.g.):
//
//	docker run -d --name pg -e POSTGRES_USER=openlog -e POSTGRES_PASSWORD=openlog -e POSTGRES_DB=openlog -p 127.0.0.1:55471:5432 postgres:16-alpine
//	docker run -d --name ch -e CLICKHOUSE_USER=openlog -e CLICKHOUSE_PASSWORD=openlog -e CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=0 \
//	  -v $PWD/deploy/compose/clickhouse/openlog-cluster.xml:/etc/clickhouse-server/config.d/openlog-cluster.xml:ro -p 127.0.0.1:19030:9000 clickhouse/clickhouse-server:25.8
//	OPENLOG_TEST_POSTGRES_DSN=postgres://openlog:openlog@127.0.0.1:55471/openlog?sslmode=disable OPENLOG_TEST_CLICKHOUSE_ADDR=127.0.0.1:19030 \
//	  go test -tags integration -count=1 ./internal/deletion
package deletion_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/deletion"
	"github.com/onuragtas/openlog/internal/migrate"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
	"github.com/onuragtas/openlog/internal/store/postgres"
	"github.com/onuragtas/openlog/internal/tenant"
	"github.com/onuragtas/openlog/migrations"
	"github.com/onuragtas/openlog/schema"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

var (
	pgOnce sync.Once
	pgPool *pgxpool.Pool
	pgErr  error
)

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("OPENLOG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("OPENLOG_TEST_POSTGRES_DSN is not set")
	}
	pgOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if pgPool, pgErr = postgres.Open(ctx, postgres.Options{DSN: dsn, Application: "deletion-test"}); pgErr != nil {
			return
		}
		if pgErr = postgres.WaitReady(ctx, pgPool, quiet); pgErr != nil {
			return
		}
		ms, err := postgres.LoadMigrations(migrations.Postgres, "postgres")
		if err != nil {
			pgErr = err
			return
		}
		pgErr = postgres.Migrate(ctx, pgPool, ms, quiet)
	})
	if pgErr != nil {
		t.Fatal(pgErr)
	}
	return pgPool
}

var (
	chOnce sync.Once
	chConn clickhouse.Conn
	chErr  error
)

func chconn(t *testing.T) clickhouse.Conn {
	t.Helper()
	addr := os.Getenv("OPENLOG_TEST_CLICKHOUSE_ADDR")
	if addr == "" {
		t.Skip("OPENLOG_TEST_CLICKHOUSE_ADDR is not set")
	}
	chOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if chConn, chErr = clickhouse.OpenRetry(ctx, clickhouse.Options{Addr: []string{addr}, Database: "default", User: "openlog", Password: "openlog"}, nil); chErr != nil {
			return
		}
		ms, err := migrate.Load(schema.ClickHouseFS(), "clickhouse", "openlog")
		if err != nil {
			chErr = err
			return
		}
		chErr = migrate.Run(ctx, chConn, ms, quiet)
	})
	if chErr != nil {
		t.Fatal(chErr)
	}
	return chConn
}

func suffix() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func randHash() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b
}

type fixture struct {
	t    *testing.T
	pool *pgxpool.Pool
	ctx  context.Context
}

func (f fixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, sql, args...); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
}

func (f fixture) id(sql string, args ...any) string {
	f.t.Helper()
	var id string
	if err := f.pool.QueryRow(f.ctx, sql, args...).Scan(&id); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
	return id
}

func (f fixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(f.ctx, sql, args...).Scan(&n); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (f fixture) user(email string) string {
	return f.id(`INSERT INTO users (email, name, password_hash, email_verified_at) VALUES ($1, 'Test', 'x', now()) RETURNING id::text`, email)
}

func (f fixture) org(tenantID, name string) string {
	return f.id(`INSERT INTO organizations (tenant_id, name) VALUES ($1, $2) RETURNING id::text`, tenantID, name)
}

func (f fixture) member(orgID, userID, role string) {
	f.exec(`INSERT INTO memberships (org_id, user_id, role) VALUES ($1, $2, $3)`, orgID, userID, role)
}

func TestDeleteUserPseudonymizes(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f := fixture{t, p, ctx}
	s := suffix()
	orgA, orgB := f.org("dsr-a-"+s, "Org A "+s), f.org("dsr-b-"+s, "Org B "+s)
	owner1, owner2 := f.user("o1-"+s+"@example.com"), f.user("o2-"+s+"@example.com")
	victimEmail := "victim-" + s + "@example.com"
	victim := f.user(victimEmail)
	soleOwner := f.user("sole-" + s + "@example.com")
	f.member(orgA, owner1, "owner")
	f.member(orgA, owner2, "owner")
	f.member(orgA, victim, "admin")
	f.member(orgB, soleOwner, "owner")
	f.member(orgB, victim, "member")

	f.exec(`INSERT INTO sessions (user_id, token_hash, csrf_token, expires_at, ip, user_agent) VALUES ($1, $2, 'c', now() + interval '1 day', '10.1.1.1', 'ua')`, victim, randHash())
	f.exec(`INSERT INTO api_keys (org_id, name, key_prefix, key_hash, created_by) VALUES ($1, 'k', 'ola_', $2, $3)`, orgA, randHash(), victim)
	f.exec(`INSERT INTO invitations (org_id, email, role, token_hash, expires_at) VALUES ($1, $2, 'viewer', $3, now() + interval '1 day')`, orgB, victimEmail, randHash())
	f.exec(`INSERT INTO audit_log (org_id, actor_user_id, actor_email, action, target_type, target_id, details, ip)
		VALUES ($1, $2::uuid, $3, 'member.role_change', 'user', $4, jsonb_build_object('email', $3::text, 'user_id', $4::text), '10.1.1.1')`, orgA, victim, victimEmail, victim)
	f.exec(`INSERT INTO audit_log (org_id, actor_user_id, actor_email, action, target_type, target_id, details)
		VALUES ($1, $2, 'o1@example.com', 'member.invite', 'user', $3, jsonb_build_object('email', $4::text))`, orgA, owner1, victim, victimEmail)
	dash := f.id(`INSERT INTO dashboards (org_id, name, created_by) VALUES ($1, 'D', $2) RETURNING id::text`, orgA, victim)
	f.exec(`INSERT INTO dashboard_reports (id, org_id, dashboard_id, frequency, hour, recipients, time_range) VALUES (gen_random_uuid(), $1, $2, 'daily', 8, $3, '24h')`,
		orgA, dash, []string{victimEmail})

	st := deletion.PGStore{Pool: p}
	var sole *deletion.SoleOwnerError
	if _, err := st.DeleteUser(ctx, soleOwner, deletion.InitiatorSelf, time.Now()); !errors.As(err, &sole) || len(sole.Orgs) != 1 || sole.Orgs[0].ID != orgB {
		t.Fatalf("sole owner: %v", err)
	}
	res, err := st.DeleteUser(ctx, victim, deletion.InitiatorSelf, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Email != victimEmail || !strings.HasPrefix(res.Pseudonym, "deleted-user-") || res.Certificate.ID == "" {
		t.Fatalf("result %+v", res)
	}
	if n := f.count(`SELECT count(*) FROM users WHERE id::text = $1`, victim); n != 0 {
		t.Fatal("user row remains")
	}
	for name, sql := range map[string]string{
		"sessions":         `SELECT count(*) FROM sessions WHERE user_id::text = $1`,
		"memberships":      `SELECT count(*) FROM memberships WHERE user_id::text = $1`,
		"api keys":         `SELECT count(*) FROM api_keys WHERE created_by::text = $1`,
		"audit by user id": `SELECT count(*) FROM audit_log WHERE actor_user_id::text = $1 OR target_id = $1 OR strpos(details::text, $1) > 0`,
	} {
		if n := f.count(sql, victim); n != 0 {
			t.Errorf("%s: %d rows left", name, n)
		}
	}
	if n := f.count(`SELECT count(*) FROM audit_log WHERE strpos(actor_email || details::text || ip, $1) > 0`, victimEmail); n != 0 {
		t.Errorf("audit rows still contain the address: %d", n)
	}
	if n := f.count(`SELECT count(*) FROM audit_log WHERE actor_email = $1 AND ip = ''`, res.Pseudonym); n != 1 {
		t.Errorf("pseudonymized actor rows: %d", n)
	}
	if n := f.count(`SELECT count(*) FROM audit_log WHERE target_id = $1 AND org_id::text = $2`, res.Pseudonym, orgA); n != 2 {
		t.Errorf("pseudonymized target rows: %d", n)
	}
	if n := f.count(`SELECT count(*) FROM invitations WHERE email = $1`, victimEmail); n != 0 {
		t.Errorf("invitations left: %d", n)
	}
	if n := f.count(`SELECT count(*) FROM dashboard_reports WHERE org_id::text = $1 AND enabled = false AND cardinality(recipients) = 0`, orgA); n != 1 {
		t.Errorf("report recipient not removed")
	}
	if n := f.count(`SELECT count(*) FROM deletion_certificates WHERE subject_type = 'user' AND subject_hash = $1 AND verified`, deletion.SubjectHash(victim)); n != 1 {
		t.Errorf("certificate: %d", n)
	}
}

func TestScheduleAndCancelOrgDeletion(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f := fixture{t, p, ctx}
	s := suffix()
	org := f.org("dsr-s-"+s, "Sched "+s)
	owner := f.user("owner-s-" + s + "@example.com")
	f.member(org, owner, "owner")
	keyHash := randHash()
	f.exec(`INSERT INTO license_keys (org_id, name, key_prefix, key_hash) VALUES ($1, 'k', 'olk_', $2)`, org, keyHash)
	oldHash := randHash()
	f.exec(`INSERT INTO license_keys (org_id, name, key_prefix, key_hash, revoked_at) VALUES ($1, 'old', 'olk_', $2, now())`, org, oldHash)

	store := postgres.NewStore(p)
	if _, err := store.LookupLicenseKey(ctx, [][]byte{keyHash}); err != nil {
		t.Fatalf("key before: %v", err)
	}
	st := deletion.PGStore{Pool: p}
	d, n, err := st.ScheduleOrg(ctx, deletion.ScheduleInput{OrgID: org, Initiator: deletion.InitiatorOwner, ActorUserID: owner,
		ActorEmail: "owner@example.com", Grace: time.Hour, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != deletion.StatusScheduled || n.OrgName != "Sched "+s || len(n.Owners) != 1 || !d.PurgeAfter.After(time.Now()) {
		t.Fatalf("scheduled %+v %+v", d, n)
	}
	if _, _, err := st.ScheduleOrg(ctx, deletion.ScheduleInput{OrgID: org, Initiator: deletion.InitiatorOwner, Grace: time.Hour, Now: time.Now()}); !errors.Is(err, deletion.ErrAlreadyScheduled) {
		t.Fatalf("second schedule: %v", err)
	}
	if _, err := store.GetMembership(ctx, org, owner); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("membership of a scheduled organization: %v", err)
	}
	if ms, _ := store.ListMemberships(ctx, owner); len(ms) != 0 {
		t.Fatalf("memberships %v", ms)
	}
	// D-115: the key revoked by the scheduling answers org_deleted (also through a later hash candidate); the key a
	// user revoked before stays an unknown key.
	if _, err := store.LookupLicenseKey(ctx, [][]byte{randHash(), keyHash}); !errors.Is(err, tenant.ErrOrgDeleted) {
		t.Fatalf("key of a scheduled organization: %v", err)
	}
	if _, err := store.LookupLicenseKey(ctx, [][]byte{oldHash}); !errors.Is(err, tenant.ErrUnknownKey) || errors.Is(err, tenant.ErrOrgDeleted) {
		t.Fatalf("key revoked before the scheduling: %v", err)
	}
	pending, err := st.PendingForOwner(ctx, owner)
	if err != nil || len(pending) != 1 || pending[0].ID != d.ID {
		t.Fatalf("pending %v %v", pending, err)
	}
	// Not due yet: the job does nothing.
	if due, _ := st.Due(ctx, time.Now(), 100); containsDeletion(due, d.ID) {
		t.Fatal("due before the grace period ended")
	}
	if _, _, err := st.CancelOrg(ctx, d.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetMembership(ctx, org, owner); err != nil {
		t.Fatalf("membership after cancel: %v", err)
	}
	if _, err := store.LookupLicenseKey(ctx, [][]byte{keyHash}); err != nil {
		t.Fatalf("key after cancel: %v", err)
	}
	if n := f.count(`SELECT count(*) FROM license_key_tombstones WHERE org_deletion_id::text = $1`, d.ID); n != 0 {
		t.Fatalf("tombstones left after cancel: %d", n)
	}
	if n := f.count(`SELECT count(*) FROM license_keys WHERE org_id::text = $1 AND revoked_at IS NOT NULL`, org); n != 1 {
		t.Fatalf("the key revoked before the deletion must stay revoked: %d", n)
	}
	if _, _, err := st.CancelOrg(ctx, d.ID, time.Now()); !errors.Is(err, deletion.ErrNotCancellable) {
		t.Fatalf("second cancel: %v", err)
	}
}

// TestOrgDeletedKeyAfterPurge covers the PostgreSQL side only (no ClickHouse): the tombstone survives the purge with a
// retention, and a new active key with the same hash wins over it (D-115).
func TestOrgDeletedKeyAfterPurge(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	f := fixture{t, p, ctx}
	s := suffix()
	org := f.org("dsr-t-"+s, "Tomb "+s)
	keyHash := randHash()
	f.exec(`INSERT INTO license_keys (org_id, name, key_prefix, key_hash) VALUES ($1, 'k', 'olk_', $2)`, org, keyHash)
	f.exec(`INSERT INTO license_key_tombstones (key_hash, reason, created_at, expires_at) VALUES ($1, 'org_deleted', now(), now() - interval '1 day')`, randHash())

	store := postgres.NewStore(p)
	st := deletion.PGStore{Pool: p}
	d, _, err := st.ScheduleOrg(ctx, deletion.ScheduleInput{OrgID: org, Initiator: deletion.InitiatorOperator, Reason: "test", Grace: 0, Now: time.Now().Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.Start(ctx, d.ID, time.Now()); err != nil || !ok {
		t.Fatalf("start %v %v", ok, err)
	}
	now := time.Now()
	if _, err := st.PurgeOrg(ctx, d, nil, true, now); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT count(*) FROM organizations WHERE id::text = $1`, org); n != 0 {
		t.Fatal("organization remains")
	}
	if _, err := store.LookupLicenseKey(ctx, [][]byte{keyHash}); !errors.Is(err, tenant.ErrOrgDeleted) {
		t.Fatalf("key after purge: %v", err)
	}
	if _, err := store.LookupLicenseKey(ctx, [][]byte{randHash()}); !errors.Is(err, tenant.ErrUnknownKey) || errors.Is(err, tenant.ErrOrgDeleted) {
		t.Fatalf("unknown key: %v", err)
	}
	if n := f.count(`SELECT count(*) FROM license_key_tombstones WHERE org_deletion_id::text = $1 AND expires_at > now() + interval '300 days'`, d.ID); n != 1 {
		t.Fatalf("tombstone with retention: %d", n)
	}
	if n := f.count(`SELECT count(*) FROM license_key_tombstones WHERE expires_at < now()`); n != 0 {
		t.Fatalf("expired tombstones not removed: %d", n)
	}
	// The same key hash becomes active again in another organization (a re-used custom key): the active key wins.
	other := f.org("dsr-t2-"+s, "Tomb2 "+s)
	f.exec(`INSERT INTO license_keys (org_id, name, key_prefix, key_hash) VALUES ($1, 'k', 'olk_', $2)`, other, keyHash)
	if info, err := store.LookupLicenseKey(ctx, [][]byte{keyHash}); err != nil || info.TenantID != "dsr-t2-"+s {
		t.Fatalf("re-used key: %+v %v", info, err)
	}
	// Revoked there by a user: the old tombstone still matches the hash, but the key row now belongs to an organization
	// that is not deleted, so the lookup answers an unknown key (401), not org_deleted.
	f.exec(`UPDATE license_keys SET revoked_at = now() WHERE org_id::text = $1`, other)
	if _, err := store.LookupLicenseKey(ctx, [][]byte{keyHash}); !errors.Is(err, tenant.ErrUnknownKey) {
		t.Fatalf("revoked re-used key: %v", err)
	}
}

func containsDeletion(ds []deletion.OrgDeletion, id string) bool {
	for _, d := range ds {
		if d.ID == id {
			return true
		}
	}
	return false
}

type recordingMailer struct {
	mu   sync.Mutex
	sent []auth.Mail
}

func (m *recordingMailer) Send(_ context.Context, mail auth.Mail) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, mail)
	return nil
}

func TestOrgHardDeletion(t *testing.T) {
	p := pool(t)
	conn := chconn(t)
	ctx := context.Background()
	f := fixture{t, p, ctx}
	s := suffix()
	tenantID, otherTenant := "dsr-h-"+s, "dsr-keep-"+s
	org, other := f.org(tenantID, "Hard "+s), f.org(otherTenant, "Keep "+s)
	owner := f.user("owner-h-" + s + "@example.com")
	shared := f.user("shared-h-" + s + "@example.com")
	f.member(org, owner, "owner")
	f.member(org, shared, "member")
	f.member(other, shared, "owner")
	f.exec(`INSERT INTO license_keys (org_id, name, key_prefix, key_hash) VALUES ($1, 'k', 'olk_', $2)`, org, randHash())
	f.exec(`INSERT INTO audit_log (org_id, actor_email, action) VALUES ($1, 'x@example.com', 'org.create')`, org)
	f.exec(`INSERT INTO update_requests (org_id, action, requested_by_email) VALUES ($1, 'check', 'x@example.com')`, org)

	now := time.Now().UTC()
	for _, tid := range []string{tenantID, otherTenant} {
		for _, day := range []time.Time{now, now.Add(-24 * time.Hour)} {
			for _, sql := range []string{
				"INSERT INTO openlog.logs_local (tenant_id, timestamp, body) VALUES (?, ?, 'hello')",
				"INSERT INTO openlog.metrics_local (tenant_id, timestamp) VALUES (?, ?)",
				"INSERT INTO openlog.spans_local (tenant_id, timestamp) VALUES (?, ?)",
			} {
				if err := conn.Exec(ctx, sql, tid, day); err != nil {
					t.Fatalf("%s: %v", sql, err)
				}
			}
		}
	}

	st := deletion.PGStore{Pool: p}
	d, _, err := st.ScheduleOrg(ctx, deletion.ScheduleInput{OrgID: org, Initiator: deletion.InitiatorOperator, Reason: "test", Grace: 0, Now: time.Now().Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	mailer := &recordingMailer{}
	job := &deletion.Job{Store: st, Purger: &deletion.Purger{Conn: conn, Database: "openlog", Cluster: "openlog", Resubmit: 5 * time.Second},
		Mailer: mailer, Log: quiet}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if _, err := job.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		cur, err := st.Get(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if cur.Status == deletion.StatusCompleted {
			d = cur
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("deletion did not complete: %+v", cur)
		}
		time.Sleep(time.Second)
	}
	if d.OrgID != "" || d.RequestedByEmail != "" || d.Reason != "" || d.CertificateID == "" {
		t.Fatalf("completed deletion keeps personal fields: %+v", d)
	}
	certs, err := st.ListCertificates(ctx, deletion.SubjectHash(tenantID), 10)
	if err != nil || len(certs) != 1 {
		t.Fatalf("certificates %v %v", certs, err)
	}
	c := certs[0]
	if !c.Verified || c.ClickHouseRows["logs_local"] != 2 || c.ClickHouseRows["metrics_local"] != 2 || c.ClickHouseRows["spans_local"] != 2 ||
		c.PostgresRows["memberships"] != 2 || c.PostgresRows["license_keys"] != 1 || c.PostgresRows["users_deleted"] != 1 {
		t.Fatalf("certificate %+v", c)
	}
	// Every ClickHouse table is empty for the tenant; the other tenant keeps its rows.
	purger := &deletion.Purger{Conn: conn, Database: "openlog", Cluster: "openlog"}
	tables, err := purger.Tables(ctx)
	if err != nil || len(tables) < 10 {
		t.Fatalf("tables %v %v", tables, err)
	}
	for _, tid := range []string{tenantID, otherTenant} {
		parts, err := purger.Partitions(ctx, tid, tables)
		if err != nil {
			t.Fatal(err)
		}
		var total uint64
		for _, ps := range parts {
			for _, n := range ps {
				total += n
			}
		}
		if tid == tenantID && total != 0 {
			t.Fatalf("rows left for the deleted tenant: %v", parts)
		}
		if tid == otherTenant && parts["logs_local"] == nil {
			t.Fatalf("other tenant lost rows: %v", parts)
		}
	}
	if n := f.count(`SELECT count(*) FROM organizations WHERE id::text = $1`, org); n != 0 {
		t.Fatal("organization remains")
	}
	if n := f.count(`SELECT count(*) FROM users WHERE id::text = $1`, owner); n != 0 {
		t.Fatal("account without organization remains")
	}
	if n := f.count(`SELECT count(*) FROM users WHERE id::text = $1`, shared); n != 1 {
		t.Fatal("account with another organization was deleted")
	}
	if n := f.count(`SELECT count(*) FROM update_requests WHERE requested_by_email = 'x@example.com' AND org_id IS NULL`); n != 0 {
		t.Fatal("update requests of the organization remain")
	}
	_ = mailer
}
