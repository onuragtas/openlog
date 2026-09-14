package quota

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/usage"
)

const testCatalog = `{
  "default": "free",
  "plans": [
    {"id": "free", "name": "Free", "limits": {"ingest_gb_month": 100, "hosts": 5, "users": 3,
      "retention_days": {"logs": 7, "traces": 3}, "ingest_bytes_per_second": 1000, "ingest_burst_bytes": 5000},
      "enforcement": {"hard_ingest_limit": true, "grace_percent": 10}},
    {"id": "pro", "name": "Pro", "limits": {"ingest_gb_month": 1000, "retention_days": {"logs": 30, "metrics": 90},
      "query": {"max_memory_usage": 4294967296}}, "billing": {"plan_ref": "price_pro"}},
    {"id": "enterprise", "name": "Enterprise", "limits": {}}
  ]
}`

func mustCatalog(t *testing.T) *Catalog {
	t.Helper()
	c, err := ParseCatalog(testCatalog, "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestParseCatalog(t *testing.T) {
	c := mustCatalog(t)
	if c.Default != "free" || len(c.Plans) != 3 {
		t.Fatalf("catalog %+v", c)
	}
	if p := c.Resolve("nope"); p.ID != "free" {
		t.Errorf("unknown plan resolves to %s", p.ID)
	}
	if p := c.Resolve("pro"); p.Limits.Query.MaxMemoryUsage != 4<<30 {
		t.Errorf("pro %+v", p)
	}
	if got := c.MaxRetentionDays(); got["logs"] != 30 || got["traces"] != 3 || got["metrics"] != 90 {
		t.Errorf("max retention %v", got)
	}
	// traces: free 3, pro and enterprise keep the schema default 7.
	if got := TableRetentionDays(c); got["logs"] != 30 || got["traces"] != 7 || got["metrics"] != 90 {
		t.Errorf("table retention %v", got)
	}

	empty, err := ParseCatalog("", "")
	if err != nil || empty.Default != UnlimitedPlanID || empty.Plans[0].Limits.IngestGBMonth != 0 {
		t.Errorf("empty catalog %+v %v", empty, err)
	}
	if got := TableRetentionDays(empty); got["logs"] != 14 || got["traces"] != 7 || got["metrics"] != 30 {
		t.Errorf("default table retention %v", got)
	}
	if c, err := ParseCatalog(testCatalog, "pro"); err != nil || c.Default != "pro" {
		t.Errorf("OPENLOG_DEFAULT_PLAN: %v %v", c, err)
	}

	for name, doc := range map[string]string{
		"bad id":          `{"plans":[{"id":"Free"}]}`,
		"duplicate":       `{"plans":[{"id":"a"},{"id":"a"}]}`,
		"no plans":        `{"plans":[]}`,
		"unknown field":   `{"plans":[{"id":"a","limits":{"gb":1}}]}`,
		"bad signal":      `{"plans":[{"id":"a","limits":{"retention_days":{"profiles":3}}}]}`,
		"negative":        `{"plans":[{"id":"a","limits":{"hosts":-1}}]}`,
		"unknown default": `{"default":"x","plans":[{"id":"a"}]}`,
	} {
		if _, err := ParseCatalog(doc, ""); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestOverrides(t *testing.T) {
	c := mustCatalog(t)
	gb, hard := 250.0, false
	o := Overrides{IngestGBMonth: &gb, HardIngestLimit: &hard, RetentionDays: map[string]int{"logs": 14}}
	p := c.EffectivePlan(OrgPlan{PlanID: "free", Overrides: o})
	if p.Limits.IngestGBMonth != 250 || p.Enforcement.HardIngestLimit || p.Limits.RetentionDays["logs"] != 14 || p.Limits.RetentionDays["traces"] != 3 {
		t.Errorf("effective %+v", p)
	}
	if orig, _ := c.Plan("free"); orig.Limits.RetentionDays["logs"] != 7 || orig.Limits.IngestGBMonth != 100 {
		t.Errorf("catalog plan modified: %+v", orig)
	}
	neg := -3.0
	if err := (Overrides{IngestGBMonth: &neg}).Validate(); err == nil {
		t.Error("negative override accepted")
	}
	if !(Overrides{}).Empty() || o.Empty() {
		t.Error("Empty")
	}
}

func TestEvaluate(t *testing.T) {
	c := mustCatalog(t)
	free, _ := c.Plan("free")
	limit := int64(100 * GiB)

	st := Evaluate(free, Usage{IngestBytes: limit / 2, ActiveHosts: 4, Users: 3}, true, 80)
	if st.Level != LevelExceeded || st.IngestBlocked {
		t.Errorf("users at limit: %+v", st)
	}
	byMetric := map[string]MetricStatus{}
	for _, m := range st.Metrics {
		byMetric[m.Metric] = m
	}
	if m := byMetric[MetricIngestBytes]; m.Level != LevelOK || m.Percent != 50 {
		t.Errorf("ingest %+v", m)
	}
	if m := byMetric[MetricHosts]; m.Level != LevelWarning || m.Percent != 80 {
		t.Errorf("hosts %+v", m)
	}
	if st.RateBytesPerSecond != 1000 || st.BurstBytes != 5000 || st.IngestLimitBytes != limit {
		t.Errorf("rates %+v", st)
	}

	// Hard block only in SaaS mode and only past limit + grace (10 %).
	over := Usage{IngestBytes: limit + limit/20}
	if st := Evaluate(free, over, true, 80); st.IngestBlocked || st.Level != LevelExceeded {
		t.Errorf("within grace: %+v", st)
	}
	over.IngestBytes = limit + limit/10
	if st := Evaluate(free, over, true, 80); !st.IngestBlocked {
		t.Errorf("past grace not blocked: %+v", st)
	}
	if st := Evaluate(free, over, false, 80); st.IngestBlocked {
		t.Error("self-hosted mode blocked ingest")
	}
	ent, _ := c.Plan("enterprise")
	if st := Evaluate(ent, Usage{IngestBytes: math.MaxInt64 / 2, ActiveHosts: 1e6, Users: 1e4}, true, 80); st.Level != LevelOK || st.IngestBlocked {
		t.Errorf("unlimited plan: %+v", st)
	}
	if WarnPercent([]int{100, 90, 75}) != 75 || WarnPercent([]int{100}) != DefaultWarnPercent {
		t.Error("WarnPercent")
	}
	if got := CrossedThresholds(MetricStatus{Used: 95, Limit: 100}, []int{80, 100}); len(got) != 1 || got[0] != 80 {
		t.Errorf("crossed %v", got)
	}
}

func TestTokenBucket(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	b := NewTokenBucket(100, 300, t0)
	if ok, _ := b.Take(250, t0); !ok {
		t.Fatal("first request rejected")
	}
	// 50 tokens left: admitted, bucket goes into debt (-450).
	if ok, _ := b.Take(500, t0); !ok {
		t.Fatal("request with tokens left rejected")
	}
	ok, wait := b.Take(1, t0)
	if ok || wait < 4*time.Second || wait > 5*time.Second {
		t.Fatalf("debt: ok=%v wait=%s", ok, wait)
	}
	if ok, _ := b.Take(1, t0.Add(wait)); !ok {
		t.Error("not admitted after the announced wait")
	}
	// The long-run rate stays bounded: over 100 s at most rate*100 + burst + one request.
	b = NewTokenBucket(100, 300, t0)
	admitted := 0.0
	for i := 0; i < 10000; i++ {
		now := t0.Add(time.Duration(i) * 10 * time.Millisecond)
		if ok, _ := b.Take(50, now); ok {
			admitted += 50
		}
	}
	if admitted > 100*100+300+50 {
		t.Errorf("admitted %.0f bytes in 100 s", admitted)
	}
	b.SetRate(0, 0, t0)
	if ok, _ := b.Take(1e12, t0); !ok {
		t.Error("rate 0 must not limit")
	}
}

type fakeStatus struct {
	st   map[string]IngestStatus
	pods int
	err  error
}

func (f *fakeStatus) LoadIngestStatuses(context.Context) (map[string]IngestStatus, error) {
	return f.st, f.err
}
func (f *fakeStatus) LiveIngestPods(context.Context) (int, error) { return f.pods, nil }

func TestIngestLimiter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	src := &fakeStatus{pods: 2, st: map[string]IngestStatus{
		"blocked": {TenantID: "blocked", PlanID: "free", Blocked: true, IngestBytes: 120 * GiB, IngestLimitBytes: 100 * GiB},
		"rated":   {TenantID: "rated", PlanID: "free", RateBytesPerSecond: 2000, BurstBytes: 2000},
	}}
	l := NewIngestLimiter(src, LimiterOptions{Now: func() time.Time { return now }, BlockedRetryAfter: time.Minute})
	if d := l.Allow("blocked", 10); !d.Allowed {
		t.Error("not loaded yet: must fail open")
	}
	if err := l.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := l.Allow("blocked", 10)
	if d.Allowed || d.Reason != ReasonQuotaExceeded || d.RetryAfter != time.Minute || !strings.Contains(d.Message, "free") {
		t.Errorf("blocked: %+v", d)
	}
	if d := l.Allow("other", 1<<30); !d.Allowed {
		t.Errorf("tenant without status: %+v", d)
	}
	// 2 pods: 1000 bytes/s and 1000 burst per pod.
	if d := l.Allow("rated", 1500); !d.Allowed {
		t.Fatalf("first: %+v", d)
	}
	d = l.Allow("rated", 10)
	if d.Allowed || d.Reason != ReasonRateLimited || d.RetryAfter < time.Second {
		t.Errorf("rate limited: %+v", d)
	}
	now = now.Add(2 * time.Second)
	if d := l.Allow("rated", 10); !d.Allowed {
		t.Errorf("after refill: %+v", d)
	}
	// PostgreSQL unreachable longer than MaxStale: fail open.
	src.err = errors.New("down")
	_ = l.Refresh(context.Background())
	now = now.Add(20 * time.Minute)
	if d := l.Allow("blocked", 10); !d.Allowed {
		t.Errorf("stale status still enforced: %+v", d)
	}
}

func TestPlanRetention(t *testing.T) {
	today := time.Date(2026, 9, 14, 3, 0, 0, 0, time.UTC)
	tenantDays := map[string]map[string]int{
		"free-a":  {"logs": 7, "traces": 3},
		"free-b":  {"logs": 7},
		"pro":     {"logs": 30}, // == table retention: nothing to delete
		"BAD'id":  {"logs": 1},  // invalid tenant ids are never put into SQL
		"metrics": {"metrics": 10},
	}
	tableDays := map[string]int{"logs": 30, "traces": 7, "metrics": 30}
	partitions := map[string][]string{
		"logs_local":        {"20260812", "20260813", "20260906", "20260907", "20260908", "20260914"},
		"spans_local":       {"20260906", "20260910", "20260911"},
		"trace_index_local": {"20260910"},
		"metrics_local":     {"20260903", "20260904", "20260905"},
	}
	tasks := PlanRetention(tenantDays, tableDays, partitions, today)
	got := map[string]RetentionTask{}
	for _, tk := range tasks {
		got[tk.Table+"/"+tk.PartitionID] = tk
		if strings.Contains(tk.SQL("openlog", "openlog"), "BAD") {
			t.Errorf("invalid tenant in SQL: %s", tk.SQL("openlog", "openlog"))
		}
	}
	// logs, 7 days: 2026-09-06 (+8 = 09-14 <= today) is due, 09-07 is not; 08-13 is past the table TTL (30+1).
	want := []string{"logs_local/20260906", "spans_local/20260910", "trace_index_local/20260910", "metrics_local/20260903"}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("missing task %s (tasks %v)", k, keys(got))
		}
	}
	for _, k := range []string{"logs_local/20260907", "logs_local/20260812", "logs_local/20260813", "logs_local/20260914", "spans_local/20260911", "metrics_local/20260904"} {
		if _, ok := got[k]; ok {
			t.Errorf("unexpected task %s", k)
		}
	}
	lg := got["logs_local/20260906"]
	if lg.Days != 7 || strings.Join(lg.Tenants, ",") != "free-a,free-b" || lg.Hash == "" {
		t.Errorf("logs task %+v", lg)
	}
	if sql := lg.SQL("openlog", "c1"); sql != "ALTER TABLE `openlog`.logs_local ON CLUSTER 'c1' DELETE IN PARTITION ID '20260906' WHERE tenant_id IN ('free-a', 'free-b')" {
		t.Errorf("sql %s", sql)
	}
	// spans 2026-09-06 is dropped by the 7-day table TTL already.
	if _, ok := got["spans_local/20260906"]; ok {
		t.Error("partition past table TTL planned")
	}
	if tasks[0].PartitionID > tasks[len(tasks)-1].PartitionID {
		t.Error("tasks not ordered oldest first")
	}
	// Same tenant set, same hash; a new tenant changes it.
	again := PlanRetention(map[string]map[string]int{"free-b": {"logs": 7}, "free-a": {"logs": 7}}, tableDays, map[string][]string{"logs_local": {"20260906"}}, today)
	more := PlanRetention(map[string]map[string]int{"free-b": {"logs": 7}, "free-a": {"logs": 7}, "free-c": {"logs": 7}}, tableDays, map[string][]string{"logs_local": {"20260906"}}, today)
	if again[0].Hash != lg.Hash || more[0].Hash == lg.Hash {
		t.Error("tenant set hash")
	}
}

func keys(m map[string]RetentionTask) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

type fakeEvalStore struct {
	orgs    []OrgPlan
	members map[string]int64
	saved   []StoredStatus
	claims  map[string]bool
	owners  []string
	done    map[string]int
}

func (f *fakeEvalStore) ListOrgPlans(context.Context) ([]OrgPlan, error) { return f.orgs, nil }
func (f *fakeEvalStore) MemberCounts(context.Context) (map[string]int64, error) {
	return f.members, nil
}
func (f *fakeEvalStore) SaveStatuses(_ context.Context, s []StoredStatus) error {
	f.saved = s
	return nil
}
func (f *fakeEvalStore) OwnerEmails(context.Context, string) ([]string, error) { return f.owners, nil }
func key(org string, p time.Time, m string, th int) string {
	return org + p.Format("2006-01") + m + string(rune('0'+th/10))
}
func (f *fakeEvalStore) ClaimNotification(_ context.Context, org string, p time.Time, m string, th int) (bool, error) {
	k := key(org, p, m, th)
	if f.claims[k] {
		return false, nil
	}
	f.claims[k] = true
	return true, nil
}
func (f *fakeEvalStore) CompleteNotification(_ context.Context, org string, p time.Time, m string, th, n int, sent bool) error {
	if !sent {
		delete(f.claims, key(org, p, m, th))
		return nil
	}
	f.done[key(org, p, m, th)] = n
	return nil
}

type fakeUsage map[string]usage.TenantPeriod

func (f fakeUsage) AllTenants(context.Context, time.Time, time.Time) (map[string]usage.TenantPeriod, error) {
	return f, nil
}

type fakeMailer struct{ sent []auth.Mail }

func (m *fakeMailer) Send(_ context.Context, mail auth.Mail) error {
	m.sent = append(m.sent, mail)
	return nil
}

func TestEvaluatorNotifiesOnce(t *testing.T) {
	c := mustCatalog(t)
	store := &fakeEvalStore{
		orgs:    []OrgPlan{{OrgID: "o1", TenantID: "t1", OrgName: "Acme", PlanID: "free", Assigned: true}, {OrgID: "o2", TenantID: "t2", OrgName: "Big"}},
		members: map[string]int64{"o1": 1, "o2": 2}, claims: map[string]bool{}, done: map[string]int{}, owners: []string{"owner@acme.test"},
	}
	u := fakeUsage{"t1": {IngestBytes: 85 * GiB, ActiveHosts: 1}}
	m := &fakeMailer{}
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	e := NewEvaluator(c, store, u, EvaluatorOptions{SaaS: true, Thresholds: []int{80, 100}, Mailer: m, PublicURL: "https://openlog.example",
		Now: func() time.Time { return now }})
	sts, err := e.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sts) != 2 || sts[0].Level != LevelWarning || sts[1].Level != LevelOK || sts[0].PeriodStart.Day() != 1 {
		t.Fatalf("statuses %+v", sts)
	}
	if len(m.sent) != 1 || !strings.Contains(m.sent[0].Subject, "80%") || !strings.Contains(m.sent[0].Text, "/settings/usage") || m.sent[0].To != "owner@acme.test" {
		t.Fatalf("mails %+v", m.sent)
	}
	if _, err := e.RunOnce(context.Background()); err != nil || len(m.sent) != 1 {
		t.Fatalf("second run sent again: %d", len(m.sent))
	}
	// Past 100 % + grace: one 100 % mail mentioning the rejection, status blocked.
	u["t1"] = usage.TenantPeriod{IngestBytes: 111 * GiB}
	sts, _ = e.RunOnce(context.Background())
	if !sts[0].IngestBlocked || len(m.sent) != 2 || !strings.Contains(m.sent[1].Subject, "100%") || !strings.Contains(m.sent[1].Text, "rejected") {
		t.Fatalf("100%%: blocked=%v mails=%d %+v", sts[0].IngestBlocked, len(m.sent), m.sent)
	}
}
