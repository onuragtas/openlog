package api

// Two api servers sharing one PostgreSQL enforce the public share link limits together (D-096). Skipped without a
// database:
//
//	OPENLOG_TEST_POSTGRES_DSN=postgres://openlog:openlog@127.0.0.1:55433/openlog?sslmode=disable \
//	  go test -count=1 -run TestShareRateLimitAcrossServers ./internal/api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/dashboard"
	"github.com/onuragtas/openlog/internal/dashboard/dashboardtest"
	"github.com/onuragtas/openlog/internal/ratelimit"
	"github.com/onuragtas/openlog/internal/store/postgres"
	"github.com/onuragtas/openlog/migrations"
)

func TestShareRateLimitAcrossServers(t *testing.T) {
	dsn := os.Getenv("OPENLOG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("OPENLOG_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := postgres.Open(ctx, postgres.Options{DSN: dsn, MaxConns: 10, Application: "openlog-apitest"})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
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
	// Unique counter keys per run: the limits are keyed by client IP and token.
	_, _ = pool.Exec(ctx, `DELETE FROM rate_limit_counters`)

	const (
		orgID   = "11111111-1111-1111-1111-111111111111"
		aliceID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	)
	authn := switchAuth{"alice": &auth.Principal{Kind: auth.KindSession, UserID: aliceID, Email: "alice@example.com", OrgID: orgID,
		OrgName: "Org", TenantID: "tenant-a", Role: auth.RoleAdmin}}
	store := dashboardtest.New() // the dashboards themselves are shared too (one database in production)
	store.Emails[aliceID] = "alice@example.com"
	store.Members[orgID] = []string{"alice@example.com"}
	store.Tenants[orgID] = "tenant-a"
	limits := shareLimits{perIP: 1000, perToken: 40, missesPerIP: 10}
	newServer := func() *Server {
		s := New(config.API{QueryTimeout: time.Second, MaxRows: 1000}, query.New(&recordingConn{}, "openlog", time.Second), authn, log, nil)
		s.SetDashboards(dashboard.NewManager(store))
		lim := ratelimit.New(ratelimit.Options{Store: ratelimit.PGStore{Pool: pool}, SyncInterval: 50 * time.Millisecond})
		go lim.Run(ctx)
		s.publicShares = &shareLimiter{limits: limits, l: lim}
		return s
	}
	servers := []*Server{newServer(), newServer()}
	do := func(s *Server, as, method, path, body, ip string) *httptest.ResponseRecorder {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, rd)
		req.RemoteAddr = ip + ":1234"
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if as != "" {
			req.Header.Set("X-Test-As", as)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	a := servers[0]
	rec := do(a, "alice", "POST", "/api/v1/dashboards", `{"name": "Shared", "pages": [{"name": "P", "widgets": [
	  {"title": "Notes", "visualization": "markdown", "layout": {"x": 0, "y": 0, "w": 6, "h": 3}, "markdown": "hi"}]}]}`, "10.0.0.1")
	if rec.Code != 201 {
		t.Fatalf("create dashboard: %d %s", rec.Code, rec.Body)
	}
	var d struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	if rec := do(a, "alice", "PUT", "/api/v1/dashboards/settings", `{"share_links_enabled": true}`, "10.0.0.1"); rec.Code != 200 {
		t.Fatalf("enable sharing: %d %s", rec.Code, rec.Body)
	}
	rec = do(a, "alice", "POST", "/api/v1/dashboards/"+d.ID+"/shares",
		`{"expires_at": "`+time.Now().Add(time.Hour).UTC().Format(time.RFC3339)+`", "range": "1h"}`, "10.0.0.1")
	if rec.Code != 201 {
		t.Fatalf("create share: %d %s", rec.Code, rec.Body)
	}
	var sh struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &sh)

	// Per link: 40 requests per minute in total, alternating between the two servers and many client IPs.
	ok := 0
	for i := 0; i < 120; i++ {
		ip := "198.51.100." + string(rune('0'+i%10))
		switch c := do(servers[i%2], "", "GET", "/api/v1/public/dashboards/"+sh.Token, "", ip).Code; c {
		case 200:
			ok++
		case 429:
		default:
			t.Fatalf("request %d: %d", i, c)
		}
	}
	if ok < limits.perToken || ok > limits.perToken+limits.perToken/8 {
		t.Fatalf("link served %d times across two servers with a limit of %d per minute", ok, limits.perToken)
	}

	// Failed lookups from one IP on one server block that IP on the other server after the counts are flushed.
	for i := 0; i < limits.missesPerIP; i++ {
		if c := do(servers[0], "", "GET", "/api/v1/public/dashboards/olds_unknown", "", "203.0.113.9").Code; c != 404 {
			t.Fatalf("miss %d: %d", i, c)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		c := do(servers[1], "", "GET", "/api/v1/public/dashboards/olds_unknown", "", "203.0.113.9").Code
		if c == 429 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("other server still answers %d after the IP exhausted its failed lookups", c)
		}
		time.Sleep(60 * time.Millisecond)
	}
}
