package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/quota"
	"github.com/onuragtas/openlog/internal/usage"
)

type usagePrincipalAuth struct{ p *auth.Principal }

func (a *usagePrincipalAuth) Authenticate(*http.Request) (*auth.Principal, error) {
	if a.p == nil {
		return nil, auth.ErrUnauthenticated
	}
	cp := *a.p
	return &cp, nil
}

type fakeUsageReader struct{ tenants []string }

func (f *fakeUsageReader) Totals(_ context.Context, tenant string, _, _ time.Time) (usage.Totals, error) {
	f.tenants = append(f.tenants, tenant)
	return usage.Totals{IngestBytes: 85 * quota.GiB, ActiveHosts: 2, Hosts: 3,
		Signals: []usage.SignalUsage{{Signal: "logs", Items: 10, Bytes: 100, IngestBytes: 85 * quota.GiB}}}, nil
}

func (f *fakeUsageReader) Daily(_ context.Context, tenant string, from, _ time.Time) ([]usage.Day, error) {
	f.tenants = append(f.tenants, tenant)
	return []usage.Day{{Day: from.Format(time.DateOnly), IngestBytes: 85 * quota.GiB, Signals: []usage.SignalUsage{{Signal: "logs", Items: 10}}}}, nil
}

func (f *fakeUsageReader) Top(_ context.Context, tenant, dim string, _, _ time.Time, limit int) ([]usage.TopEntry, error) {
	f.tenants = append(f.tenants, tenant)
	return []usage.TopEntry{{Key: dim + "-1", Bytes: 5, By: map[string]uint64{"logs": 5}}}, nil
}

func (f *fakeUsageReader) SignalBytesSince(_ context.Context, tenant string, since map[string]time.Time, _ time.Time) (map[string]uint64, error) {
	f.tenants = append(f.tenants, tenant)
	return map[string]uint64{"logs": 1000}, nil
}

func (f *fakeUsageReader) CompressionRatios(context.Context) (map[string]float64, error) {
	return map[string]float64{"logs": 0.1}, nil
}

type fakePlanStore struct {
	op     quota.OrgPlan
	status *quota.StoredStatus
	puts   []quota.Actor
}

func (f *fakePlanStore) FindOrgPlan(_ context.Context, ref string) (quota.OrgPlan, error) {
	if ref != f.op.OrgID && ref != f.op.TenantID {
		return quota.OrgPlan{}, quota.ErrOrgNotFound
	}
	return f.op, nil
}

func (f *fakePlanStore) PutOrgPlan(_ context.Context, op quota.OrgPlan, a quota.Actor) error {
	op.Assigned = true
	f.op = op
	f.puts = append(f.puts, a)
	return nil
}

func (f *fakePlanStore) GetStatus(context.Context, string) (quota.StoredStatus, bool, error) {
	if f.status == nil {
		return quota.StoredStatus{}, false, nil
	}
	return *f.status, true, nil
}

func (f *fakePlanStore) MemberCounts(context.Context) (map[string]int64, error) {
	return map[string]int64{f.op.OrgID: 2}, nil
}

func (f *fakePlanStore) FindOrgByBillingCustomer(context.Context, string, string) (quota.OrgPlan, error) {
	return quota.OrgPlan{}, quota.ErrOrgNotFound
}

func newUsageServer(t *testing.T, p *auth.Principal) (*Server, *fakeUsageReader, *fakePlanStore) {
	t.Helper()
	c, err := quota.ParseCatalog(`{"plans":[{"id":"free","name":"Free","limits":{"ingest_gb_month":100,"users":5,"retention_days":{"logs":7}},
		"enforcement":{"hard_ingest_limit":true}},{"id":"pro","name":"Pro","trial_days":14,"limits":{"ingest_gb_month":1000}}]}`, "")
	if err != nil {
		t.Fatal(err)
	}
	s := New(config.API{QueryTimeout: time.Second, MaxRows: 100}, query.New(nil, "openlog", time.Second), &usagePrincipalAuth{p},
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	s.now = func() time.Time { return time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC) }
	r := &fakeUsageReader{}
	st := &fakePlanStore{op: quota.OrgPlan{OrgID: "0f7b3c1e-5a2d-4c1b-9e8f-00000000000a", TenantID: "acme", OrgName: "Acme"}}
	s.SetUsage(UsageDeps{Reader: r, Catalog: c, Store: st, SaaS: true, Thresholds: []int{80, 100},
		Superadmin: func(e string) bool { return e == "ops@openlog.test" }})
	return s, r, st
}

func usageDo(t *testing.T, s *Server, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var out map[string]any
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec, out
}

func usageMember(role auth.Role) *auth.Principal {
	return &auth.Principal{Kind: auth.KindSession, UserID: "7c1e2d9a-3b4f-4e5a-8b6c-000000000001", Email: "user@acme.test", EmailVerified: true,
		OrgID: "0f7b3c1e-5a2d-4c1b-9e8f-00000000000a", OrgName: "Acme", TenantID: "acme", Role: role}
}

func TestUsageOverview(t *testing.T) {
	s, r, _ := newUsageServer(t, usageMember(auth.RoleViewer))
	rec, body := usageDo(t, s, http.MethodGet, "/api/v1/usage", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	if body["level"] != "warning" || body["ingest_blocked"] != false || body["can_manage_plan"] != false || body["saas_mode"] != true {
		t.Errorf("overview %v", body)
	}
	if p := body["plan"].(map[string]any); p["id"] != "free" {
		t.Errorf("plan %v", p)
	}
	period := body["period"].(map[string]any)
	if period["id"] != "2026-09" || !strings.HasPrefix(period["data_until"].(string), "2026-09-11T00:00:00") {
		t.Errorf("period %v", period)
	}
	// 85 GiB after 10 of 30 days, no complete previous days: linear projection 255 GiB.
	if proj := body["projection"].(map[string]any); math.Round(proj["ingest_percent"].(float64)) != 255 {
		t.Errorf("projection %v", proj)
	}
	stored := body["stored"].([]any)[1].(map[string]any)
	if stored["signal"] != "logs" || stored["retention_days"] != float64(7) || stored["compressed_bytes"] != float64(100) {
		t.Errorf("stored %v", stored)
	}
	var users map[string]any
	for _, l := range body["limits"].([]any) {
		if m := l.(map[string]any); m["metric"] == "users" {
			users = m
		}
	}
	if users["used"] != float64(2) || users["limit"] != float64(5) {
		t.Errorf("users %v", users)
	}
	for _, tn := range r.tenants {
		if tn != "acme" {
			t.Fatalf("query for tenant %q", tn)
		}
	}
	if rec, _ := usageDo(t, s, http.MethodGet, "/api/v1/usage?period=2026-13", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("bad period: %d", rec.Code)
	}
	if rec, body := usageDo(t, s, http.MethodGet, "/api/v1/usage/top?by=host&limit=5", ""); rec.Code != 200 || body["by"] != "host" {
		t.Errorf("top: %d %v", rec.Code, body)
	}
	if rec, _ := usageDo(t, s, http.MethodGet, "/api/v1/usage/top?by=pod", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("top by pod: %d", rec.Code)
	}
	if rec, body := usageDo(t, s, http.MethodGet, "/api/v1/usage/daily?period=previous", ""); rec.Code != 200 || len(body["days"].([]any)) != 1 {
		t.Errorf("daily: %d %v", rec.Code, body)
	}
	if rec, body := usageDo(t, s, http.MethodGet, "/api/v1/plans", ""); rec.Code != 200 || len(body["plans"].([]any)) != 2 || body["default"] != "free" {
		t.Errorf("plans: %d %v", rec.Code, body)
	} else {
		free, pro := body["plans"].([]any)[0].(map[string]any), body["plans"].([]any)[1].(map[string]any)
		if free["trial_days"] != float64(0) || free["trial_fallback_plan"] != "" || pro["trial_days"] != float64(14) || pro["trial_fallback_plan"] != "free" {
			t.Errorf("plan trial fields: free=%v pro=%v", free, pro)
		}
	}
}

func TestUsageStatusAndExport(t *testing.T) {
	s, _, st := newUsageServer(t, usageMember(auth.RoleViewer))
	if rec, body := usageDo(t, s, http.MethodGet, "/api/v1/usage/status", ""); rec.Code != 200 || body["level"] != "ok" {
		t.Errorf("status without evaluation: %d %v", rec.Code, body)
	}
	st.status = &quota.StoredStatus{Status: quota.Status{PlanID: "free", Level: quota.LevelExceeded, IngestBlocked: true,
		Metrics: []quota.MetricStatus{{Metric: "ingest_bytes", Used: 2, Limit: 1, Percent: 200, Level: quota.LevelExceeded}}},
		PeriodStart: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), EvaluatedAt: time.Now()}
	if rec, body := usageDo(t, s, http.MethodGet, "/api/v1/usage/status", ""); rec.Code != 200 || body["ingest_blocked"] != true || body["period_start"] != "2026-09-01" {
		t.Errorf("status: %d %v", rec.Code, body)
	}
	if rec, _ := usageDo(t, s, http.MethodGet, "/api/v1/usage/export", ""); rec.Code != http.StatusForbidden {
		t.Errorf("viewer export: %d", rec.Code)
	}

	s, _, _ = newUsageServer(t, usageMember(auth.RoleAdmin))
	rec, _ := usageDo(t, s, http.MethodGet, "/api/v1/usage/export?period=2026-08&format=csv", "")
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Disposition"), "openlog-usage-acme-2026-08.csv") ||
		!strings.HasPrefix(rec.Body.String(), "tenant_id,period,day,metric,value\nacme,2026-08,2026-08-01,logs.items,10") {
		t.Errorf("csv export: %d %q %s", rec.Code, rec.Header().Get("Content-Disposition"), rec.Body)
	}
	if rec, body := usageDo(t, s, http.MethodGet, "/api/v1/usage/export?format=json", ""); rec.Code != 200 || body["plan_id"] != "free" {
		t.Errorf("json export: %d %v", rec.Code, body)
	}
	if rec, _ := usageDo(t, s, http.MethodGet, "/api/v1/usage/export?format=xml", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("xml export: %d", rec.Code)
	}
	// No billing provider: webhooks are not found.
	if rec, _ := usageDo(t, s, http.MethodPost, "/api/v1/billing/webhooks/stripe", "{}"); rec.Code != http.StatusNotFound {
		t.Errorf("webhook without provider: %d", rec.Code)
	}
}

func TestAdminPlanOverride(t *testing.T) {
	owner := usageMember(auth.RoleOwner)
	s, _, _ := newUsageServer(t, owner)
	if rec, _ := usageDo(t, s, http.MethodPut, "/api/v1/admin/orgs/acme/plan", `{"plan_id":"pro"}`); rec.Code != http.StatusForbidden {
		t.Errorf("owner (not superadmin): %d", rec.Code)
	}

	ops := &auth.Principal{Kind: auth.KindSession, UserID: "7c1e2d9a-3b4f-4e5a-8b6c-000000000009", Email: "ops@openlog.test", EmailVerified: false}
	s, _, st := newUsageServer(t, ops)
	if rec, _ := usageDo(t, s, http.MethodGet, "/api/v1/admin/orgs/acme/plan", ""); rec.Code != http.StatusForbidden {
		t.Errorf("unverified superadmin: %d", rec.Code)
	}
	ops.EmailVerified = true
	s, _, st = newUsageServer(t, ops)
	rec, body := usageDo(t, s, http.MethodGet, "/api/v1/admin/orgs/acme/plan", "")
	if rec.Code != 200 || body["plan_id"] != "free" || body["assigned"] != false {
		t.Fatalf("get: %d %v", rec.Code, body)
	}
	if rec, _ := usageDo(t, s, http.MethodPut, "/api/v1/admin/orgs/acme/plan", `{"plan_id":"gold"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown plan: %d", rec.Code)
	}
	if rec, _ := usageDo(t, s, http.MethodPut, "/api/v1/admin/orgs/acme/plan", `{"plan_id":"pro","overrides":{"hosts":-1}}`); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid override: %d", rec.Code)
	}
	// Retention overrides cannot outlive the table TTL (longest plan retention: logs 14 days by default).
	if rec, _ := usageDo(t, s, http.MethodPut, "/api/v1/admin/orgs/acme/plan", `{"plan_id":"pro","overrides":{"retention_days":{"logs":60}}}`); rec.Code != http.StatusBadRequest {
		t.Errorf("retention override above table TTL: %d", rec.Code)
	}
	if rec, _ := usageDo(t, s, http.MethodGet, "/api/v1/admin/orgs/nope/plan", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown org: %d", rec.Code)
	}
	rec, body = usageDo(t, s, http.MethodPut, "/api/v1/admin/orgs/acme/plan",
		`{"plan_id":"pro","overrides":{"ingest_gb_month":2000,"retention_days":{"logs":10}},"billing":{"provider":"noop","customer_id":"cus_1"},"note":"deal #42"}`)
	if rec.Code != 200 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	eff := body["effective"].(map[string]any)["limits"].(map[string]any)
	if body["plan_id"] != "pro" || body["assigned"] != true || eff["ingest_gb_month"] != float64(2000) || body["note"] != "deal #42" {
		t.Errorf("put response %v", body)
	}
	if len(st.puts) != 1 || st.puts[0].Email != "ops@openlog.test" || st.op.BillingCustomerID != "cus_1" {
		t.Errorf("store %+v %+v", st.puts, st.op)
	}
}
