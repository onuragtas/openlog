package quota

import (
	"context"
	"errors"
	"testing"

	"github.com/onuragtas/openlog/internal/config"
)

func ptr(v int64) *int64 { return &v }

func TestResolveQueryLimitsPrecedence(t *testing.T) {
	defaults := config.QueryLimits{MaxMemoryUsage: 2 << 30, MaxRowsToRead: 2_000_000_000, MaxBytesToRead: 0}
	plan := QueryLimits{MaxMemoryUsage: 4 << 30} // rows/bytes not set by the plan

	got, src := ResolveQueryLimits(defaults, QueryLimits{}, OrgQueryLimits{}, nil)
	if got != defaults || src != (QueryLimitSources{"default", "default", "default"}) {
		t.Errorf("defaults only: %+v %+v", got, src)
	}

	got, src = ResolveQueryLimits(defaults, plan, OrgQueryLimits{}, nil)
	if got.MaxMemoryUsage != 4<<30 || got.MaxRowsToRead != 2_000_000_000 || src.MaxMemoryUsage != "plan" || src.MaxRowsToRead != "default" {
		t.Errorf("plan: %+v %+v", got, src)
	}

	// The organization setting wins over the plan; 0 is an explicit "not set by openlog".
	org := OrgQueryLimits{MaxMemoryUsage: ptr(1 << 30), MaxBytesToRead: ptr(0)}
	got, src = ResolveQueryLimits(defaults, plan, org, nil)
	if got.MaxMemoryUsage != 1<<30 || got.MaxBytesToRead != 0 || got.MaxRowsToRead != 2_000_000_000 ||
		src != (QueryLimitSources{"organization", "default", "organization"}) {
		t.Errorf("organization: %+v %+v", got, src)
	}

	// OPENLOG_QUERY_TENANT_LIMITS replaces every layer.
	env := config.QueryLimits{MaxMemoryUsage: 8 << 30, MaxRowsToRead: 5, MaxBytesToRead: 7}
	got, src = ResolveQueryLimits(defaults, plan, org, &env)
	if got != env || src != (QueryLimitSources{"environment", "environment", "environment"}) {
		t.Errorf("environment: %+v %+v", got, src)
	}
}

func TestOrgQueryLimitsValidate(t *testing.T) {
	if err := (OrgQueryLimits{MaxRowsToRead: ptr(0), MaxMemoryUsage: ptr(1 << 40)}).Validate(); err != nil {
		t.Error(err)
	}
	if err := (OrgQueryLimits{MaxRowsToRead: ptr(-1)}).Validate(); err == nil {
		t.Error("negative value accepted")
	}
	if !(OrgQueryLimits{}).Empty() || (OrgQueryLimits{MaxBytesToRead: ptr(0)}).Empty() {
		t.Error("Empty")
	}
}

type fakeQueryLimitsStore struct {
	orgs []OrgPlan
	org  map[string]OrgQueryLimits
	err  error
}

func (f *fakeQueryLimitsStore) ListOrgPlans(context.Context) ([]OrgPlan, error) { return f.orgs, f.err }
func (f *fakeQueryLimitsStore) ListOrgQueryLimits(context.Context) (map[string]OrgQueryLimits, error) {
	return f.org, f.err
}

func TestQueryLimitsResolver(t *testing.T) {
	cat, err := ParseCatalog(`{"plans":[{"id":"free","limits":{"query":{"max_memory_usage":1073741824}}},{"id":"pro"}],"default":"free"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	st := &fakeQueryLimitsStore{
		orgs: []OrgPlan{{TenantID: "a", PlanID: "free", Assigned: true}, {TenantID: "b", PlanID: "pro", Assigned: true}, {TenantID: "c", PlanID: "free", Assigned: true}},
		org:  map[string]OrgQueryLimits{"c": {MaxMemoryUsage: ptr(3 << 30)}},
	}
	q := config.Query{Defaults: config.QueryLimits{MaxMemoryUsage: 2 << 30, MaxRowsToRead: 100},
		Tenants: map[string]config.QueryLimits{"env": {MaxMemoryUsage: 9}}}
	r := &QueryLimitsResolver{Query: q, Catalog: cat, Store: st}

	// Before the first load: environment limits.
	if l := r.Limits("a"); l.MaxMemoryUsage != 2<<30 {
		t.Errorf("before load: %+v", l)
	}
	if err := r.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	for tenant, want := range map[string]int64{"a": 1 << 30, "b": 2 << 30, "c": 3 << 30, "env": 9, "unknown": 2 << 30} {
		if l := r.Limits(tenant); l.MaxMemoryUsage != want || l.MaxRowsToRead != 100 && tenant != "env" {
			t.Errorf("%s: %+v, want max_memory_usage %d", tenant, l, want)
		}
	}
	// A failing reload keeps the previous layers.
	st.err = errors.New("postgres down")
	if err := r.Load(context.Background()); err == nil {
		t.Error("load error not reported")
	}
	if l := r.Limits("c"); l.MaxMemoryUsage != 3<<30 {
		t.Errorf("after failed reload: %+v", l)
	}
}
