package alert

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

// fakeCondition returns one series whose value is a function of the window end.
type fakeCondition struct {
	MetricCondition
	value func(end time.Time) float64
	calls *int
	mu    *sync.Mutex
}

func (f fakeCondition) Evaluate(_ context.Context, sc *query.Scope, end time.Time, _ Limits) (*EvalResult, error) {
	f.mu.Lock()
	*f.calls++
	f.mu.Unlock()
	if sc.Tenant() != "tenant-a" {
		panic("wrong tenant scope: " + sc.Tenant())
	}
	v := f.value(end)
	if math.IsNaN(v) {
		return &EvalResult{}, nil
	}
	return &EvalResult{Samples: []Sample{{Key: "host.id=h1", Labels: map[string]string{"host.id": "h1", "host.name": "web-1"}, Value: v}}}, nil
}

// memEvalStore is an EvalStore over memLeaseStore with the same fencing rules as the PostgreSQL store.
type memEvalStore struct {
	leases        *memLeaseStore
	mu            sync.Mutex
	rule          *Rule
	lastEnd       map[string]time.Time
	states        map[string]map[string]SeriesState
	incidents     map[string]*Incident
	notifications map[string]Notification // by idempotency key
	commits       int
}

func (s *memEvalStore) LoadRule(_ context.Context, id string) (*Rule, []ChannelRef, error) {
	r := *s.rule
	r.ID = id
	return &r, testChannels, nil
}

func (s *memEvalStore) LoadSeries(_ context.Context, id string) (map[string]SeriesState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]SeriesState{}
	for k, v := range s.states[id] {
		if v.IncidentID != "" {
			if inc := s.incidents[v.IncidentID]; inc != nil {
				c := *inc
				v.Incident = &c
			}
		}
		out[k] = v
	}
	return out, nil
}

// LoadRouting: this store has no routing rules, so incidents reach the channels of their rule (§5.6).
func (s *memEvalStore) LoadRouting(context.Context, string) (*Routing, error) { return nil, nil }

func (s *memEvalStore) Commit(_ context.Context, instance string, p *Plan) error {
	s.leases.mu.Lock()
	defer s.leases.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.leases.rules[p.RuleID]
	if l == nil || l.owner != instance || !l.until.After(s.leases.now) {
		return ErrLeaseLost
	}
	if last, ok := s.lastEnd[p.RuleID]; ok && !p.EvalEnd.After(last) {
		return ErrStale
	}
	s.lastEnd[p.RuleID] = p.EvalEnd
	l.next = p.NextEvalAt
	s.commits++
	if s.states[p.RuleID] == nil {
		s.states[p.RuleID] = map[string]SeriesState{}
	}
	for _, inc := range p.Opens {
		for _, other := range s.incidents {
			if other.RuleID == inc.RuleID && other.SeriesKey == inc.SeriesKey && other.State != IncidentResolved {
				panic("second open incident for one series")
			}
		}
		c := inc
		s.incidents[inc.ID] = &c
	}
	for _, r := range p.Resolves {
		s.incidents[r.ID].State = IncidentResolved
	}
	for _, k := range p.SeriesDeletes {
		delete(s.states[p.RuleID], k)
	}
	for _, st := range p.SeriesUpserts {
		st.Incident = nil
		s.states[p.RuleID][st.Key] = st
	}
	for _, n := range p.Notifications {
		if _, dup := s.notifications[n.IdempotencyKey]; !dup {
			s.notifications[n.IdempotencyKey] = n
		}
	}
	return nil
}

func TestEvaluatorsShareRulesAndSurvivePodDeath(t *testing.T) {
	ctx := context.Background()
	leaseStore := newMemLeaseStore(1)
	start := t0
	cond := mustParse(t, TypeMetricThreshold, `{"metric":"m","window_seconds":60,"operator":"gt","threshold":0.9,"group_by":["host"]}`).(MetricCondition)
	calls, mu := 0, &sync.Mutex{}
	// Breach from minute 3 to minute 12, then recovery.
	value := func(end time.Time) float64 {
		m := end.Sub(start).Minutes()
		if m >= 3 && m < 12 {
			return 0.97
		}
		return 0.3
	}
	rule := testRule(t, func(in *RuleInput) { in.ForSeconds = 60 })
	rule.Condition = fakeCondition{MetricCondition: cond, value: value, calls: &calls, mu: mu}
	store := &memEvalStore{leases: leaseStore, rule: rule, lastEnd: map[string]time.Time{}, states: map[string]map[string]SeriesState{},
		incidents: map[string]*Incident{}, notifications: map[string]Notification{}}
	db := query.New(nil, "openlog", time.Second)

	type pod struct {
		lm *LeaseManager
		ev *Evaluator
	}
	pods := map[string]*pod{}
	for _, name := range []string{"a", "b", "c"} {
		lm := NewLeaseManager(leaseStore, LeaseOptions{Instance: name, TTL: 30 * time.Second})
		ev := NewEvaluator(store, lm, db, EvaluatorOptions{Instance: name, Delay: 15 * time.Second, Now: leaseStore.clockNow})
		pods[name] = &pod{lm, ev}
	}
	owner := func() string {
		leaseStore.mu.Lock()
		defer leaseStore.mu.Unlock()
		return leaseStore.rules["r000"].owner
	}
	killed := ""
	// Simulate 20 minutes in 5 s steps: every pod renews every 10 s and evaluates due rules every step.
	for step := 0; step < 240; step++ {
		for name, p := range pods {
			if name == killed {
				continue
			}
			if step%2 == 0 {
				if err := p.lm.Tick(ctx); err != nil {
					t.Fatal(err)
				}
			}
			p.ev.RunOnce(ctx)
		}
		// Kill the lease owner once the incident is open (minute 5).
		if killed == "" && leaseStore.clockNow().Sub(start) >= 5*time.Minute {
			store.mu.Lock()
			open := len(store.incidents)
			store.mu.Unlock()
			if open != 1 {
				t.Fatalf("incident not open before pod kill (incidents=%d)", open)
			}
			killed = owner()
			t.Logf("killed lease owner %s at %s", killed, leaseStore.clockNow().Sub(start))
		}
		leaseStore.advance(5 * time.Second)
	}
	if killed == "" || owner() == killed {
		t.Fatalf("rule not taken over (owner %q, killed %q)", owner(), killed)
	}
	// A zombie (the killed pod waking up with its old lease view) cannot commit.
	zombie := pods[killed]
	if res := zombie.ev.Evaluate(ctx, Lease{RuleID: "r000", OrgID: "org-1", NextEvalAt: leaseStore.clockNow()}); res != "lease_lost" {
		t.Fatalf("zombie evaluation result %q, want lease_lost", res)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.incidents) != 1 {
		t.Fatalf("incidents = %d, want exactly 1", len(store.incidents))
	}
	var opened, resolved int
	for _, n := range store.notifications {
		switch n.Kind {
		case KindOpened:
			opened++
		case KindResolved:
			resolved++
		}
	}
	for _, inc := range store.incidents {
		if inc.State != IncidentResolved {
			t.Fatalf("incident not resolved after recovery: %+v", inc)
		}
	}
	if opened != 2 || resolved != 2 {
		t.Fatalf("notifications opened=%d resolved=%d, want 2/2 (one per channel)", opened, resolved)
	}
	// One commit per window end at most: ~20 one-minute windows.
	if store.commits > 21 || store.commits < 15 {
		t.Errorf("commits = %d", store.commits)
	}
}

func (s *memLeaseStore) clockNow() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now
}

func TestAlignmentAndPhase(t *testing.T) {
	interval := time.Minute
	phase := Phase("rule-1", interval)
	if phase < 0 || phase >= interval || phase != Phase("rule-1", interval) {
		t.Fatalf("phase %v", phase)
	}
	now := time.Date(2026, 9, 13, 10, 5, 37, 0, time.UTC)
	end := AlignedEnd(now, interval, 15*time.Second, 7*time.Second)
	if !end.Equal(time.Date(2026, 9, 13, 10, 5, 7, 0, time.UTC)) {
		t.Fatalf("aligned end %v", end)
	}
	// Every pod computes the same end within the same interval.
	if !AlignedEnd(now.Add(20*time.Second), interval, 15*time.Second, 7*time.Second).Equal(end) {
		t.Fatal("alignment differs within the interval")
	}
}

func TestTenantBudget(t *testing.T) {
	now := t0
	b := newTenantBudget(2, 3, func() time.Time { return now })
	if !b.acquire("a") || !b.acquire("a") || b.acquire("a") {
		t.Fatal("concurrency limit")
	}
	if !b.acquire("b") {
		t.Fatal("tenants share a budget")
	}
	b.release("a")
	b.release("a")
	if !b.acquire("a") || b.acquire("a") {
		t.Fatal("per-minute token bucket (3/min)")
	}
	b.release("a")
	now = now.Add(time.Minute)
	if !b.acquire("a") {
		t.Fatal("tokens not refilled")
	}
}

var _ = json.Marshal
