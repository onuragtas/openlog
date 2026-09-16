package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/auth/memstore"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/synthetics"
)

// fakeSyntheticStore is an in-memory synthetics.Store scoped by organization.
type fakeSyntheticStore struct {
	mu     sync.Mutex
	checks map[string]synthetics.Check // id -> check
	order  []string
	nextID int
	limit  bool     // Create returns ErrLimit
	audit  []string // action:id of every write
}

func newFakeSyntheticStore() *fakeSyntheticStore {
	return &fakeSyntheticStore{checks: map[string]synthetics.Check{}, nextID: 1}
}

func (f *fakeSyntheticStore) List(_ context.Context, orgID string) ([]synthetics.Check, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []synthetics.Check{}
	for _, id := range f.order {
		if c := f.checks[id]; c.OrgID == orgID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeSyntheticStore) Get(_ context.Context, orgID, id string) (*synthetics.Check, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.checks[id]
	if !ok || c.OrgID != orgID {
		return nil, synthetics.ErrNotFound
	}
	return &c, nil
}

func (f *fakeSyntheticStore) Create(_ context.Context, orgID string, in synthetics.Input, _ synthetics.Actor) (*synthetics.Check, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.limit {
		return nil, synthetics.ErrLimit
	}
	id := "check-" + string(rune('a'+f.nextID-1))
	f.nextID++
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	c := synthetics.Check{ID: id, OrgID: orgID, Input: in, CreatedAt: now, UpdatedAt: now,
		Status: []synthetics.LocationStatus{{Location: synthetics.LocationLocal, NextRunAt: now}}}
	f.checks[id] = c
	f.order = append(f.order, id)
	f.audit = append(f.audit, "create:"+id)
	return &c, nil
}

func (f *fakeSyntheticStore) Update(_ context.Context, orgID, id string, in synthetics.Input, _ synthetics.Actor) (*synthetics.Check, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.checks[id]
	if !ok || c.OrgID != orgID {
		return nil, synthetics.ErrNotFound
	}
	c.Input = in
	f.checks[id] = c
	f.audit = append(f.audit, "update:"+id)
	return &c, nil
}

func (f *fakeSyntheticStore) Delete(_ context.Context, orgID, id string, _ synthetics.Actor) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.checks[id]
	if !ok || c.OrgID != orgID {
		return synthetics.ErrNotFound
	}
	delete(f.checks, id)
	f.audit = append(f.audit, "delete:"+id)
	return nil
}

type syntheticsEnv struct {
	h     http.Handler
	store *fakeSyntheticStore
	conn  *recordingConn
	owner *client
}

func newSyntheticsEnv(t *testing.T) *syntheticsEnv {
	t.Helper()
	st := memstore.New()
	svc := auth.NewService(st, auth.Config{CookieSecure: true, LoginMaxFailures: 5}, quietLog())
	if _, err := svc.Bootstrap(context.Background(), auth.BootstrapSpec{TenantID: "tenant-a", OrgName: "Org A",
		OwnerEmail: "owner@example.com", OwnerPassword: ownerPassword}); err != nil {
		t.Fatal(err)
	}
	conn := &recordingConn{}
	s := New(config.API{QueryTimeout: time.Second, MaxRows: 1000}, query.New(conn, "openlog", time.Second), svc, quietLog(), nil)
	s.SetAccounts(svc)
	store := newFakeSyntheticStore()
	s.SetSynthetics(store)
	e := &syntheticsEnv{h: s.srv.Handler, store: store, conn: conn}
	e.owner = e.login(t, "owner@example.com", ownerPassword)
	return e
}

func (e *syntheticsEnv) login(t *testing.T, email, password string) *client {
	t.Helper()
	c := &client{t: t, h: e.h}
	rec := c.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": password})
	if rec.Code != http.StatusOK {
		t.Fatalf("login %s: %d %s", email, rec.Code, rec.Body)
	}
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == auth.DefaultCookieName {
			c.cookie = &http.Cookie{Name: ck.Name, Value: ck.Value}
		}
	}
	if c.cookie == nil {
		t.Fatal("no session cookie")
	}
	me := decode[meJSON](t, rec)
	if me.CSRFToken == nil || *me.CSRFToken == "" {
		t.Fatalf("login response without csrf_token: %s", rec.Body)
	}
	c.csrf = *me.CSRFToken
	return c
}

func validCheckBody() map[string]any {
	return map[string]any{"name": "Checkout health", "url": "https://shop.example.com/health"}
}

func TestSyntheticCheckCRUD(t *testing.T) {
	e := newSyntheticsEnv(t)

	rec := e.owner.do(http.MethodPost, "/api/v1/synthetics/checks", validCheckBody())
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	created := decode[syntheticCheckJSON](t, rec)
	// The server fills the defaults of the contract.
	if created.Method != "GET" || created.TimeoutMs != 10000 || created.IntervalSeconds != 300 || created.Type != "http" {
		t.Errorf("defaults: %+v", created)
	}
	if len(created.ExpectedStatus) != 1 || created.ExpectedStatus[0] != 200 {
		t.Errorf("expected_status %v", created.ExpectedStatus)
	}
	if len(created.Locations) != 1 || created.Locations[0] != "local" {
		t.Errorf("locations %v", created.Locations)
	}
	// Omitting "enabled" must not create a check that never runs.
	if !created.Enabled {
		t.Error("a created check is not enabled")
	}

	rec = e.owner.do(http.MethodGet, "/api/v1/synthetics/checks", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	list := decode[struct {
		Checks    []syntheticListItemJSON `json:"checks"`
		Locations []string                `json:"locations"`
	}](t, rec)
	if len(list.Checks) != 1 || list.Checks[0].ID != created.ID {
		t.Fatalf("list: %+v", list.Checks)
	}
	if len(list.Locations) == 0 || list.Locations[0] != "local" {
		t.Errorf("locations %v", list.Locations)
	}

	body := validCheckBody()
	body["name"] = "Checkout health v2"
	body["interval_seconds"] = 60
	rec = e.owner.do(http.MethodPut, "/api/v1/synthetics/checks/"+created.ID, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	if got := decode[syntheticCheckJSON](t, rec); got.Name != "Checkout health v2" || got.IntervalSeconds != 60 {
		t.Errorf("updated: %+v", got)
	}

	if rec = e.owner.do(http.MethodDelete, "/api/v1/synthetics/checks/"+created.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec = e.owner.do(http.MethodGet, "/api/v1/synthetics/checks/"+created.ID, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: %d %s", rec.Code, rec.Body)
	}
	if got := strings.Join(e.store.audit, " "); !strings.Contains(got, "create:") || !strings.Contains(got, "delete:") {
		t.Errorf("store writes: %s", got)
	}
}

func TestSyntheticCheckValidation(t *testing.T) {
	e := newSyntheticsEnv(t)
	cases := map[string]func(map[string]any){
		"missing name":     func(b map[string]any) { delete(b, "name") },
		"bad url":          func(b map[string]any) { b["url"] = "ftp://example.com" },
		"url credentials":  func(b map[string]any) { b["url"] = "https://user:pw@example.com/" },
		"bad method":       func(b map[string]any) { b["method"] = "TRACE" },
		"reserved header":  func(b map[string]any) { b["headers"] = map[string]string{"Host": "evil.example.com"} },
		"bad status":       func(b map[string]any) { b["expected_status"] = []int{99} },
		"timeout":          func(b map[string]any) { b["timeout_ms"] = 10 },
		"interval":         func(b map[string]any) { b["interval_seconds"] = 5 },
		"timeout interval": func(b map[string]any) { b["timeout_ms"] = 60000; b["interval_seconds"] = 30 },
		"assertion":        func(b map[string]any) { b["assertion_type"] = "contains" },
		"location":         func(b map[string]any) { b["locations"] = []string{"eu-west"} },
		"body on get":      func(b map[string]any) { b["body"] = "x" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			b := validCheckBody()
			mutate(b)
			rec := e.owner.do(http.MethodPost, "/api/v1/synthetics/checks", b)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("accepted: %d %s", rec.Code, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), "invalid_argument") {
				t.Errorf("error body %s", rec.Body)
			}
		})
	}
}

func TestSyntheticCheckLimit(t *testing.T) {
	e := newSyntheticsEnv(t)
	e.store.limit = true
	rec := e.owner.do(http.MethodPost, "/api/v1/synthetics/checks", validCheckBody())
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "failed_precondition") {
		t.Fatalf("limit: %d %s", rec.Code, rec.Body)
	}
}

func TestSyntheticCheckNotFound(t *testing.T) {
	e := newSyntheticsEnv(t)
	for _, path := range []string{"/api/v1/synthetics/checks/nope", "/api/v1/synthetics/checks/nope/results"} {
		if rec := e.owner.do(http.MethodGet, path, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
}

func TestSyntheticChecksRequireAuthentication(t *testing.T) {
	e := newSyntheticsEnv(t)
	anon := &client{t: t, h: e.h}
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/synthetics/checks"},
		{http.MethodPost, "/api/v1/synthetics/checks"},
	} {
		rec := anon.do(tc.method, tc.path, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, rec.Code, rec.Body)
		}
	}
}

// The results endpoint reads ClickHouse: every statement must carry the bound tenant predicate.
func TestSyntheticCheckResultsAreTenantScoped(t *testing.T) {
	e := newSyntheticsEnv(t)
	rec := e.owner.do(http.MethodPost, "/api/v1/synthetics/checks", validCheckBody())
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	id := decode[syntheticCheckJSON](t, rec).ID

	if rec = e.owner.do(http.MethodGet, "/api/v1/synthetics/checks/"+id+"/results", nil); rec.Code != http.StatusOK {
		t.Fatalf("results: %d %s", rec.Code, rec.Body)
	}
	res := decode[struct {
		Check    syntheticCheckJSON     `json:"check"`
		Summary  syntheticSummaryJSON   `json:"summary"`
		Failures []syntheticFailureJSON `json:"failures"`
	}](t, rec)
	if res.Check.ID != id || res.Failures == nil {
		t.Errorf("results: %+v", res)
	}

	e.conn.mu.Lock()
	statements := append([]string(nil), e.conn.sql...)
	e.conn.mu.Unlock()
	if len(statements) == 0 {
		t.Fatal("the results endpoint ran no query")
	}
	for _, sql := range statements {
		if strings.Contains(sql, "synthetic_runs") && !strings.Contains(sql, "tenant_id = {tenant_id:String}") {
			t.Errorf("statement without the tenant predicate: %s", sql)
		}
	}
}

// The list summarizes every check with one query, whatever the number of checks.
func TestSyntheticChecksListSummaryQueries(t *testing.T) {
	e := newSyntheticsEnv(t)
	for range 3 {
		if rec := e.owner.do(http.MethodPost, "/api/v1/synthetics/checks", validCheckBody()); rec.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", rec.Code, rec.Body)
		}
	}
	e.conn.mu.Lock()
	e.conn.sql = nil
	e.conn.mu.Unlock()

	if rec := e.owner.do(http.MethodGet, "/api/v1/synthetics/checks", nil); rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	e.conn.mu.Lock()
	n := len(e.conn.sql)
	e.conn.mu.Unlock()
	if n > 2 {
		t.Errorf("the list ran %d queries for 3 checks (want the totals plus the series)", n)
	}

	// summary=false skips ClickHouse entirely.
	e.conn.mu.Lock()
	e.conn.sql = nil
	e.conn.mu.Unlock()
	if rec := e.owner.do(http.MethodGet, "/api/v1/synthetics/checks?summary=false", nil); rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	e.conn.mu.Lock()
	n = len(e.conn.sql)
	e.conn.mu.Unlock()
	if n != 0 {
		t.Errorf("summary=false ran %d queries", n)
	}
}
