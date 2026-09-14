package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/quota"
)

type fakeQueryLimitsStore struct {
	stored  *quota.StoredOrgQueryLimits
	actors  []quota.Actor
	orgIDs  []string
	changed int
}

func (f *fakeQueryLimitsStore) GetOrgQueryLimits(_ context.Context, orgID string) (quota.StoredOrgQueryLimits, bool, error) {
	if f.stored == nil {
		return quota.StoredOrgQueryLimits{}, false, nil
	}
	return *f.stored, true, nil
}

func (f *fakeQueryLimitsStore) PutOrgQueryLimits(_ context.Context, orgID string, l quota.OrgQueryLimits, a quota.Actor) error {
	f.orgIDs, f.actors = append(f.orgIDs, orgID), append(f.actors, a)
	if l.Empty() {
		f.stored = nil
		return nil
	}
	f.stored = &quota.StoredOrgQueryLimits{OrgQueryLimits: l, UpdatedAt: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC), UpdatedByEmail: a.Email}
	return nil
}

func newQueryLimitsServer(t *testing.T, p *auth.Principal, saas bool, env map[string]config.QueryLimits) (*Server, *fakeQueryLimitsStore) {
	t.Helper()
	s, _, st := newUsageServer(t, p)
	c, err := quota.ParseCatalog(`{"plans":[{"id":"free","name":"Free","limits":{"query":{"max_memory_usage":1073741824}}}]}`, "")
	if err != nil {
		t.Fatal(err)
	}
	ql := &fakeQueryLimitsStore{}
	d := *s.usage
	d.Catalog, d.SaaS, d.QueryLimits = c, saas, ql
	d.Query = config.Query{Defaults: config.QueryLimits{MaxMemoryUsage: 2 << 30, MaxRowsToRead: 2_000_000_000}, Tenants: env}
	d.QueryLimitsChanged = func(context.Context) { ql.changed++ }
	_ = st
	s.SetUsage(d)
	return s, ql
}

func TestQueryLimitsEndpoints(t *testing.T) {
	// Self-hosted: owners manage the organization layer; admins and viewers read.
	owner := usageMember(auth.RoleOwner)
	s, ql := newQueryLimitsServer(t, owner, false, nil)
	rec, body := usageDo(t, s, http.MethodGet, "/api/v1/usage/query-limits", "")
	if rec.Code != http.StatusOK || body["can_manage"] != true || body["organization"] != nil || body["environment"] != nil {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body)
	}
	eff := body["effective"].(map[string]any)
	src := body["sources"].(map[string]any)
	if eff["max_memory_usage"] != float64(1<<30) || src["max_memory_usage"] != "plan" || src["max_rows_to_read"] != "default" {
		t.Errorf("plan layer: %v %v", eff, src)
	}

	rec, body = usageDo(t, s, http.MethodPut, "/api/v1/usage/query-limits", `{"max_memory_usage": 4294967296, "max_rows_to_read": 0, "max_bytes_to_read": null}`)
	if rec.Code != http.StatusOK || ql.changed != 1 || len(ql.actors) != 1 || ql.actors[0].Email != owner.Email || ql.orgIDs[0] != owner.OrgID {
		t.Fatalf("PUT: %d %s (changed %d)", rec.Code, rec.Body, ql.changed)
	}
	eff, src = body["effective"].(map[string]any), body["sources"].(map[string]any)
	org := body["organization"].(map[string]any)
	if eff["max_memory_usage"] != float64(4<<30) || eff["max_rows_to_read"] != float64(0) || src["max_rows_to_read"] != "organization" ||
		src["max_bytes_to_read"] != "default" || org["max_bytes_to_read"] != nil || org["updated_by"] != owner.Email {
		t.Errorf("after PUT: %v %v %v", eff, src, org)
	}

	if rec, _ := usageDo(t, s, http.MethodPut, "/api/v1/usage/query-limits", `{"max_memory_usage": -1}`); rec.Code != http.StatusBadRequest {
		t.Errorf("negative: %d", rec.Code)
	}
	rec, body = usageDo(t, s, http.MethodDelete, "/api/v1/usage/query-limits", "")
	if rec.Code != http.StatusOK || body["organization"] != nil || ql.stored != nil {
		t.Errorf("DELETE: %d %s", rec.Code, rec.Body)
	}

	admin, _ := newQueryLimitsServer(t, usageMember(auth.RoleAdmin), false, nil)
	if rec, body := usageDo(t, admin, http.MethodGet, "/api/v1/usage/query-limits", ""); rec.Code != http.StatusOK || body["can_manage"] != false {
		t.Errorf("admin GET: %d %v", rec.Code, body)
	}
	if rec, _ := usageDo(t, admin, http.MethodPut, "/api/v1/usage/query-limits", `{"max_rows_to_read": 1}`); rec.Code != http.StatusForbidden {
		t.Errorf("admin PUT: %d", rec.Code)
	}

	// SaaS mode: only superadmins; the environment override wins and is reported.
	env := map[string]config.QueryLimits{"acme": {MaxMemoryUsage: 9, MaxRowsToRead: 8, MaxBytesToRead: 7}}
	saasOwner, _ := newQueryLimitsServer(t, owner, true, env)
	if rec, _ := usageDo(t, saasOwner, http.MethodPut, "/api/v1/usage/query-limits", `{"max_rows_to_read": 1}`); rec.Code != http.StatusForbidden {
		t.Errorf("SaaS owner PUT: %d", rec.Code)
	}
	ops := usageMember(auth.RoleViewer)
	ops.Email = "ops@openlog.test"
	saasOps, _ := newQueryLimitsServer(t, ops, true, env)
	rec, body = usageDo(t, saasOps, http.MethodPut, "/api/v1/usage/query-limits", `{"max_rows_to_read": 1}`)
	if rec.Code != http.StatusOK || body["can_manage"] != true {
		t.Fatalf("superadmin PUT: %d %s", rec.Code, rec.Body)
	}
	if eff := body["effective"].(map[string]any); eff["max_rows_to_read"] != float64(8) || body["sources"].(map[string]any)["max_rows_to_read"] != "environment" || body["environment"] == nil {
		t.Errorf("environment override: %v", body)
	}
}
