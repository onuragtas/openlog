package billing

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/quota"
	"github.com/onuragtas/openlog/internal/usage"
)

type memPushStore struct {
	mu   sync.Mutex
	recs map[string]*struct {
		status   string
		attempts int
	}
}

func (m *memPushStore) ClaimPush(_ context.Context, r PushRecord, maxAttempts int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.recs[r.IdempotencyKey]
	if cur == nil {
		m.recs[r.IdempotencyKey] = &struct {
			status   string
			attempts int
		}{"pending", 1}
		return true, nil
	}
	if cur.status != "failed" || cur.attempts >= maxAttempts {
		return false, nil
	}
	cur.status, cur.attempts = "pending", cur.attempts+1
	return true, nil
}

func (m *memPushStore) FinishPush(_ context.Context, key string, err error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.recs[key].status = "failed"
	} else {
		m.recs[key].status = "pushed"
	}
	return nil
}

type fakeOrgs struct{ orgs []quota.OrgPlan }

func (f fakeOrgs) ListOrgPlans(context.Context) ([]quota.OrgPlan, error) { return f.orgs, nil }
func (f fakeOrgs) MemberCounts(context.Context) (map[string]int64, error) {
	return map[string]int64{"o1": 4}, nil
}

type fakeDay map[string]usage.DayTotals

func (f fakeDay) DayAllTenants(context.Context, time.Time) (map[string]usage.DayTotals, error) {
	return f, nil
}

type recordingProvider struct {
	Noop
	mu      sync.Mutex
	records []UsageRecord
	fail    bool
}

func (p *recordingProvider) PushUsage(_ context.Context, r UsageRecord) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail {
		return errors.New("provider down")
	}
	p.records = append(p.records, r)
	return nil
}

func TestPushJobIdempotent(t *testing.T) {
	prov := &recordingProvider{}
	store := &memPushStore{recs: map[string]*struct {
		status   string
		attempts int
	}{}}
	job := &PushJob{Provider: prov, Store: store, Usage: fakeDay{"t1": {IngestBytes: 3 * quota.GiB, Hosts: 7}},
		Orgs: fakeOrgs{orgs: []quota.OrgPlan{
			{OrgID: "o1", TenantID: "t1", BillingProvider: "noop", BillingCustomerID: "cus_1", BillingSubscriptionID: "sub_1"},
			{OrgID: "o2", TenantID: "t2"}, // not connected
			{OrgID: "o3", TenantID: "t3", BillingProvider: "other", BillingCustomerID: "x"},
		}}}
	day := time.Date(2026, 9, 13, 15, 0, 0, 0, time.UTC)

	prov.fail = true
	if p, f, err := job.PushDay(context.Background(), day); err != nil || p != 0 || f != 3 {
		t.Fatalf("failing provider: pushed %d failed %d %v", p, f, err)
	}
	prov.fail = false
	p, f, err := job.PushDay(context.Background(), day)
	if err != nil || p != 3 || f != 0 {
		t.Fatalf("retry: pushed %d failed %d %v", p, f, err)
	}
	if p, _, _ := job.PushDay(context.Background(), day); p != 0 {
		t.Fatalf("pushed again: %d", p)
	}
	got := map[string]UsageRecord{}
	for _, r := range prov.records {
		got[r.Metric] = r
	}
	if got[MetricIngestGB].Quantity != 3 || got[MetricHosts].Quantity != 7 || got[MetricUsers].Quantity != 4 ||
		got[MetricHosts].IdempotencyKey != "openlog:o1:2026-09-13:hosts" || got[MetricUsers].SubscriptionID != "sub_1" {
		t.Errorf("records %+v", got)
	}
}

type memPlanStore struct {
	op  quota.OrgPlan
	put *quota.OrgPlan
}

func (m *memPlanStore) FindOrgByBillingCustomer(_ context.Context, provider, customer string) (quota.OrgPlan, error) {
	if provider == m.op.BillingProvider && customer == m.op.BillingCustomerID {
		return m.op, nil
	}
	return quota.OrgPlan{}, quota.ErrOrgNotFound
}

func (m *memPlanStore) PutOrgPlan(_ context.Context, op quota.OrgPlan, _ quota.Actor) error {
	m.put = &op
	return nil
}

func TestApplyEvent(t *testing.T) {
	c, err := quota.ParseCatalog(`{"plans":[{"id":"free"},{"id":"pro","billing":{"plan_ref":"price_pro"}}]}`, "")
	if err != nil {
		t.Fatal(err)
	}
	st := &memPlanStore{op: quota.OrgPlan{OrgID: "o1", PlanID: "free", Assigned: true, BillingProvider: "p", BillingCustomerID: "cus"}}
	changed, err := ApplyEvent(context.Background(), st, c, "p", Event{Type: EventSubscriptionUpdated, CustomerID: "cus", SubscriptionID: "sub", PlanRef: "price_pro"})
	if err != nil || !changed || st.put.PlanID != "pro" || st.put.BillingSubscriptionID != "sub" {
		t.Fatalf("upgrade: %v %v %+v", changed, err, st.put)
	}
	st.op, st.put = *st.put, nil
	changed, err = ApplyEvent(context.Background(), st, c, "p", Event{Type: EventSubscriptionCanceled, CustomerID: "cus"})
	if err != nil || !changed || st.put.PlanID != "free" || st.put.BillingSubscriptionID != "" {
		t.Fatalf("cancel: %v %v %+v", changed, err, st.put)
	}
	if _, err := ApplyEvent(context.Background(), st, c, "p", Event{Type: EventSubscriptionUpdated, CustomerID: "cus", PlanRef: "unknown"}); err == nil {
		t.Error("unknown plan ref accepted")
	}
	if changed, err := ApplyEvent(context.Background(), st, c, "p", Event{Type: EventSubscriptionUpdated, CustomerID: "nobody", PlanRef: "price_pro"}); err != nil || changed {
		t.Error("unknown customer changed something")
	}
	if changed, _ := ApplyEvent(context.Background(), st, c, "p", Event{Type: EventInvoicePaid, CustomerID: "cus"}); changed {
		t.Error("invoice event changed the plan")
	}
	if p, err := New("none"); p != nil || err != nil {
		t.Error("none provider")
	}
	if _, err := New("stripe"); err == nil {
		t.Error("unknown provider accepted")
	}
}
