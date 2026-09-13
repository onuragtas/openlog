package alert

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"
)

// memLeaseStore emulates the PostgreSQL lease queries with a fake clock. Every method is atomic (like a single
// statement with FOR UPDATE SKIP LOCKED).
type memLeaseStore struct {
	mu    sync.Mutex
	now   time.Time
	rules map[string]*memLease
	evals map[string]time.Time
}

type memLease struct {
	enabled bool
	owner   string
	until   time.Time
	next    time.Time
}

func newMemLeaseStore(n int) *memLeaseStore {
	s := &memLeaseStore{now: t0, rules: map[string]*memLease{}, evals: map[string]time.Time{}}
	for i := 0; i < n; i++ {
		s.rules[fmt.Sprintf("r%03d", i)] = &memLease{enabled: true, next: t0.Add(time.Duration(i) * time.Second)}
	}
	return s
}

func (s *memLeaseStore) advance(d time.Duration) {
	s.mu.Lock()
	s.now = s.now.Add(d)
	s.mu.Unlock()
}

func (s *memLeaseStore) lease(id string, l *memLease) Lease {
	return Lease{RuleID: id, OrgID: "org", NextEvalAt: l.next, LeaseUntil: l.until}
}

func (s *memLeaseStore) Heartbeat(_ context.Context, instance, _ string, ttl time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evals[instance] = s.now
	live := 0
	for _, seen := range s.evals {
		if s.now.Sub(seen) < ttl {
			live++
		}
	}
	return live, nil
}

func (s *memLeaseStore) Leave(_ context.Context, instance string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.evals, instance)
	for _, l := range s.rules {
		if l.owner == instance {
			l.owner, l.until = "", time.Time{}
		}
	}
	return nil
}

func (s *memLeaseStore) Renew(_ context.Context, instance string, ttl time.Duration) ([]Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Lease
	for id, l := range s.rules {
		if l.owner != instance {
			continue
		}
		if !l.enabled {
			l.owner, l.until = "", time.Time{}
			continue
		}
		l.until = s.now.Add(ttl)
		out = append(out, s.lease(id, l))
	}
	return out, nil
}

func (s *memLeaseStore) CountEnabled(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, l := range s.rules {
		if l.enabled {
			n++
		}
	}
	return n, nil
}

func (s *memLeaseStore) Claim(_ context.Context, instance string, n int, ttl time.Duration) ([]Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0)
	for id, l := range s.rules {
		if l.enabled && !l.until.After(s.now) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return s.rules[ids[i]].next.Before(s.rules[ids[j]].next) })
	var out []Lease
	for _, id := range ids {
		if len(out) == n {
			break
		}
		l := s.rules[id]
		l.owner, l.until = instance, s.now.Add(ttl)
		out = append(out, s.lease(id, l))
	}
	return out, nil
}

func (s *memLeaseStore) Release(_ context.Context, instance string, n int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for id, l := range s.rules {
		if l.owner == instance {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return s.rules[ids[i]].next.After(s.rules[ids[j]].next) })
	if len(ids) > n {
		ids = ids[:n]
	}
	for _, id := range ids {
		s.rules[id].owner, s.rules[id].until = "", time.Time{}
	}
	return ids, nil
}

// owners returns, per rule, the instances holding an unexpired lease (must be at most one) and per-instance counts.
func (s *memLeaseStore) owners() (map[string]int, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[string]int{}
	var unowned []string
	for id, l := range s.rules {
		if l.enabled && l.owner != "" && l.until.After(s.now) {
			counts[l.owner]++
		} else if l.enabled {
			unowned = append(unowned, id)
		}
	}
	return counts, unowned
}

// checkExclusive asserts that no rule is held (unexpired) by two managers according to their local views.
func checkExclusive(t *testing.T, s *memLeaseStore, ms map[string]*LeaseManager) {
	t.Helper()
	s.mu.Lock()
	now := s.now
	s.mu.Unlock()
	holder := map[string]string{}
	for name, m := range ms {
		for _, l := range m.Owned(now) {
			if other, dup := holder[l.RuleID]; dup {
				t.Fatalf("rule %s evaluated by %s and %s", l.RuleID, other, name)
			}
			holder[l.RuleID] = name
		}
	}
}

func TestLeaseShardingThreePodsAndPodDeath(t *testing.T) {
	ctx := context.Background()
	s := newMemLeaseStore(30)
	ttl := 30 * time.Second
	newMgr := func(name string) *LeaseManager {
		return NewLeaseManager(s, LeaseOptions{Instance: name, TTL: ttl, RenewInterval: 10 * time.Second})
	}
	ms := map[string]*LeaseManager{"a": newMgr("a"), "b": newMgr("b"), "c": newMgr("c")}
	round := func(names ...string) {
		for _, n := range names {
			if err := ms[n].Tick(ctx); err != nil {
				t.Fatal(err)
			}
			checkExclusive(t, s, ms)
		}
		s.advance(10 * time.Second)
	}

	// Pods start one after another: a claims everything first, then shares shrink as b and c join.
	for i := 0; i < 4; i++ {
		round("a", "b", "c")
	}
	counts, unowned := s.owners()
	if len(unowned) != 0 || counts["a"] != 10 || counts["b"] != 10 || counts["c"] != 10 {
		t.Fatalf("balanced shares: %v unowned %v", counts, unowned)
	}

	// c dies (no more ticks, no Leave). Its leases expire after the TTL and a and b take them over.
	for i := 0; i < 6; i++ {
		round("a", "b")
	}
	counts, unowned = s.owners()
	if len(unowned) != 0 || counts["c"] != 0 || counts["a"] != 15 || counts["b"] != 15 {
		t.Fatalf("after pod death: %v unowned %v", counts, unowned)
	}
	if got := len(ms["c"].Owned(s.now)); got != 0 {
		t.Fatalf("dead pod still believes it owns %d rules", got)
	}

	// A new pod joins: rules move without double ownership until shares are balanced again.
	ms["d"] = newMgr("d")
	for i := 0; i < 5; i++ {
		round("a", "b", "d")
	}
	counts, unowned = s.owners()
	if len(unowned) != 0 || counts["a"] != 10 || counts["b"] != 10 || counts["d"] != 10 {
		t.Fatalf("after join: %v unowned %v", counts, unowned)
	}

	// Graceful stop hands over at once (no TTL wait).
	if err := s.Leave(ctx, "d"); err != nil {
		t.Fatal(err)
	}
	delete(ms, "d")
	round("a", "b")
	round("a", "b")
	counts, unowned = s.owners()
	if len(unowned) != 0 || counts["a"]+counts["b"] != 30 {
		t.Fatalf("after graceful leave: %v unowned %v", counts, unowned)
	}

	// Disabled rules are released and not claimed.
	s.mu.Lock()
	for _, id := range []string{"r000", "r001", "r002"} {
		s.rules[id].enabled = false
	}
	s.mu.Unlock()
	round("a", "b")
	round("a", "b")
	counts, _ = s.owners()
	if counts["a"]+counts["b"] != 27 {
		t.Fatalf("disabled rules still owned: %v", counts)
	}
}

func TestLeaseSurvivesWithoutRenewal(t *testing.T) {
	s := newMemLeaseStore(4)
	m := NewLeaseManager(s, LeaseOptions{Instance: "a", TTL: 30 * time.Second})
	if err := m.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(m.Owned(s.now)); n != 4 {
		t.Fatalf("owned %d", n)
	}
	// Without a successful renewal the local view stops evaluating when the lease expires.
	if n := len(m.Owned(s.now.Add(31 * time.Second))); n != 0 {
		t.Fatalf("expired leases still reported: %d", n)
	}
}
