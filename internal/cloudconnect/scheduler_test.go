package cloudconnect

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// fakeSchedule is an in-memory ScheduleStore. Claim hands out every due row exactly once, like the SQL claim
// that selects with FOR UPDATE SKIP LOCKED and advances next_run_at in the same statement.
type fakeSchedule struct {
	mu       sync.Mutex
	due      []Due
	recorded []recordedRun
}

type recordedRun struct {
	connectionID string
	run          Run
	backoff      time.Duration
}

func (f *fakeSchedule) Claim(_ context.Context, _ time.Time, limit int) ([]Due, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := min(limit, len(f.due))
	out := append([]Due(nil), f.due[:n]...)
	f.due = f.due[n:]
	return out, nil
}

func (f *fakeSchedule) Record(_ context.Context, connectionID string, r Run, backoff time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recorded = append(f.recorded, recordedRun{connectionID: connectionID, run: r, backoff: backoff})
	return nil
}

func (f *fakeSchedule) records() []recordedRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRun(nil), f.recorded...)
}

// fakePoller records how many polls overlap, in total and per organization and connection.
type fakePoller struct {
	delay  time.Duration
	status string

	mu                sync.Mutex
	cur, peak         int
	perTenant         map[string]int
	peakPerTenant     map[string]int
	perConnection     map[string]int
	peakPerConnection map[string]int
	polled            []Due
}

func newFakePoller(delay time.Duration) *fakePoller {
	return &fakePoller{delay: delay, status: StatusOK, perTenant: map[string]int{}, peakPerTenant: map[string]int{},
		perConnection: map[string]int{}, peakPerConnection: map[string]int{}}
}

func (f *fakePoller) Collect(_ context.Context, d Due) Run {
	f.mu.Lock()
	f.cur++
	f.perTenant[d.TenantID]++
	f.perConnection[d.Connection.ID]++
	f.peak = max(f.peak, f.cur)
	f.peakPerTenant[d.TenantID] = max(f.peakPerTenant[d.TenantID], f.perTenant[d.TenantID])
	f.peakPerConnection[d.Connection.ID] = max(f.peakPerConnection[d.Connection.ID], f.perConnection[d.Connection.ID])
	f.polled = append(f.polled, d)
	f.mu.Unlock()

	time.Sleep(f.delay)

	f.mu.Lock()
	f.cur--
	f.perTenant[d.TenantID]--
	f.perConnection[d.Connection.ID]--
	f.mu.Unlock()
	return Run{Scope: d.Scope, Status: f.status, Metrics: 3, APICalls: 2}
}

func (f *fakePoller) polls() []Due {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Due(nil), f.polled...)
}

// dueScopes builds n due scopes of one connection.
func dueScopes(tenant, connID string, n int) []Due {
	out := make([]Due, n)
	for i := range n {
		out[i] = Due{TenantID: tenant, Scope: fmt.Sprintf("region-%d", i), CredentialsEnc: "enc",
			Connection: Connection{ID: connID, OrgID: "org-" + tenant, Input: Input{
				Name: connID, Provider: ProviderAWS, IngestMode: IngestPoll, Enabled: true,
				Services: []string{"rds"}, PollIntervalSeconds: 300, MaxMetricsPerPoll: 100, MaxAPICallsPerPoll: 100,
			}}}
	}
	return out
}

func testRunner(store ScheduleStore, poller PollRunner, o RunnerOptions) *Runner {
	o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewRunner(store, poller, o)
}

func TestRunOncePollsClaimedScopes(t *testing.T) {
	store := &fakeSchedule{due: dueScopes("tenant-a", "conn-1", 5)}
	poller := newFakePoller(0)
	r := testRunner(store, poller, RunnerOptions{})

	if n := r.RunOnce(context.Background()); n != 5 {
		t.Fatalf("RunOnce = %d, want 5", n)
	}
	if got := len(poller.polls()); got != 5 {
		t.Errorf("polled %d scopes", got)
	}
	// Every poll is recorded on its schedule row, which is what the connection list shows.
	if got := len(store.records()); got != 5 {
		t.Errorf("recorded %d runs", got)
	}
}

// One claim takes at most BatchSize rows; the rest stay due for the next round.
func TestClaimTakesOneBatch(t *testing.T) {
	store := &fakeSchedule{due: dueScopes("tenant-a", "conn-1", 10)}
	r := testRunner(store, newFakePoller(0), RunnerOptions{MaxConcurrent: 2, BatchSize: 4})

	for i, want := range []int{4, 4, 2} {
		if n := r.RunOnce(context.Background()); n != want {
			t.Fatalf("RunOnce #%d = %d, want %d", i+1, n, want)
		}
	}
}

// The leader model: claiming is what keeps a scope to one poller. Two schedulers on the same store (a
// leadership handover where both believe they lead) must not poll any scope twice — a duplicate poll is a
// duplicate bill at the provider.
func TestTwoRunnersPollEachScopeOnce(t *testing.T) {
	store := &fakeSchedule{due: dueScopes("tenant-a", "conn-1", 20)}
	poller := newFakePoller(time.Millisecond)
	a := testRunner(store, poller, RunnerOptions{MaxConcurrent: 8, TenantMaxConcurrent: 8, ConnectionMaxConcurrent: 8})
	b := testRunner(store, poller, RunnerOptions{MaxConcurrent: 8, TenantMaxConcurrent: 8, ConnectionMaxConcurrent: 8})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a.RunOnce(context.Background()) }()
	go func() { defer wg.Done(); b.RunOnce(context.Background()) }()
	wg.Wait()

	polls := poller.polls()
	if len(polls) != 20 {
		t.Fatalf("polled %d scopes, want 20", len(polls))
	}
	seen := map[string]int{}
	for _, d := range polls {
		seen[d.Scope]++
	}
	for scope, n := range seen {
		if n != 1 {
			t.Errorf("scope %s polled %d times", scope, n)
		}
	}
}

func TestGlobalConcurrencyCap(t *testing.T) {
	var due []Due
	for i := range 6 {
		due = append(due, dueScopes(fmt.Sprintf("tenant-%d", i), fmt.Sprintf("conn-%d", i), 2)...)
	}
	store := &fakeSchedule{due: due}
	poller := newFakePoller(20 * time.Millisecond)
	r := testRunner(store, poller, RunnerOptions{MaxConcurrent: 3, TenantMaxConcurrent: 3, ConnectionMaxConcurrent: 3})

	if n := r.RunOnce(context.Background()); n != 12 {
		t.Fatalf("RunOnce = %d, want 12", n)
	}
	if poller.peak > 3 {
		t.Errorf("peak concurrency %d, cap 3", poller.peak)
	}
	if poller.peak < 2 {
		t.Errorf("peak concurrency %d: the polls did not overlap at all", poller.peak)
	}
}

func TestTenantConcurrencyCap(t *testing.T) {
	store := &fakeSchedule{due: append(dueScopes("tenant-a", "conn-a", 8), dueScopes("tenant-b", "conn-b", 8)...)}
	poller := newFakePoller(20 * time.Millisecond)
	r := testRunner(store, poller, RunnerOptions{MaxConcurrent: 16, TenantMaxConcurrent: 2, ConnectionMaxConcurrent: 2})

	if n := r.RunOnce(context.Background()); n != 16 {
		t.Fatalf("RunOnce = %d, want 16", n)
	}
	for tenant, peak := range poller.peakPerTenant {
		if peak > 2 {
			t.Errorf("%s peaked at %d concurrent polls, cap 2", tenant, peak)
		}
	}
	// One organization at its cap must not stop the other from polling.
	if poller.peak < 3 {
		t.Errorf("peak concurrency %d: the organizations did not poll in parallel", poller.peak)
	}
}

// One connection covering many regions must not burst requests at a single cloud account.
func TestConnectionConcurrencyCap(t *testing.T) {
	store := &fakeSchedule{due: append(dueScopes("tenant-a", "conn-a", 8), dueScopes("tenant-a", "conn-b", 8)...)}
	poller := newFakePoller(20 * time.Millisecond)
	r := testRunner(store, poller, RunnerOptions{MaxConcurrent: 16, TenantMaxConcurrent: 8, ConnectionMaxConcurrent: 2})

	if n := r.RunOnce(context.Background()); n != 16 {
		t.Fatalf("RunOnce = %d, want 16", n)
	}
	for conn, peak := range poller.peakPerConnection {
		if peak > 2 {
			t.Errorf("connection %s peaked at %d concurrent polls, cap 2", conn, peak)
		}
	}
	// The two connections of the same organization still run in parallel.
	if poller.peak < 3 {
		t.Errorf("peak concurrency %d: the connections did not poll in parallel", poller.peak)
	}
}

// The per-connection cap is clamped to the tenant cap, which is clamped to the global one.
func TestConcurrencyCapsAreClamped(t *testing.T) {
	r := NewRunner(&fakeSchedule{}, newFakePoller(0), RunnerOptions{
		MaxConcurrent: 2, TenantMaxConcurrent: 50, ConnectionMaxConcurrent: 50,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if r.o.TenantMaxConcurrent != 2 || r.o.ConnectionMaxConcurrent != 2 {
		t.Errorf("caps = %d/%d, want both clamped to 2", r.o.TenantMaxConcurrent, r.o.ConnectionMaxConcurrent)
	}
}

// A cancelled context (shutdown, lost leadership) stops the polls instead of starting new ones.
func TestRunOnceStopsOnCancelledContext(t *testing.T) {
	store := &fakeSchedule{due: dueScopes("tenant-a", "conn-1", 5)}
	poller := newFakePoller(0)
	r := testRunner(store, poller, RunnerOptions{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.RunOnce(ctx)

	if got := len(poller.polls()); got != 0 {
		t.Errorf("polled %d scopes after the context was cancelled", got)
	}
}

func TestRunStopsWhenContextIsDone(t *testing.T) {
	store := &fakeSchedule{due: dueScopes("tenant-a", "conn-1", 3)}
	r := testRunner(store, newFakePoller(0), RunnerOptions{Tick: 10 * time.Millisecond})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return when the context was done")
	}
}

// A scope that keeps failing is backed off, so a broken region stops costing a request every interval. The
// first failure still polls at the normal interval: a single blip should not delay recovery.
func TestBackoffGrowsWithRepeatedFailures(t *testing.T) {
	record := func(consecutive int, status string) time.Duration {
		due := dueScopes("tenant-a", "conn-1", 1)
		due[0].Connection.Status = []ScopeStatus{{Scope: due[0].Scope, ConsecutiveErrors: consecutive}}
		store := &fakeSchedule{due: due}
		poller := newFakePoller(0)
		poller.status = status
		testRunner(store, poller, RunnerOptions{}).RunOnce(context.Background())
		recs := store.records()
		if len(recs) != 1 {
			t.Fatalf("recorded %d runs", len(recs))
		}
		return recs[0].backoff
	}

	if d := record(0, StatusError); d != 0 {
		t.Errorf("the first failure was backed off by %s, want the normal interval", d)
	}
	// The second consecutive failure starts the ramp (the claim carries the count from before this run).
	if d := record(1, StatusError); d <= 0 {
		t.Errorf("a repeated failure was recorded without a backoff (%s)", d)
	}
	if d := record(4, StatusError); d < record(2, StatusError) {
		t.Error("the backoff does not grow with the number of failures")
	}
	if d := record(3, StatusOK); d != 0 {
		t.Errorf("a successful poll was backed off by %s", d)
	}
	// A partial poll collected something real, so it keeps its normal schedule.
	if d := record(3, StatusPartial); d != 0 {
		t.Errorf("a partial poll was backed off by %s", d)
	}
}

func TestErrorBackoffGrowsAndIsCapped(t *testing.T) {
	interval := 5 * time.Minute
	if d := errorBackoff(1, interval); d != 0 {
		t.Errorf("the first failure backed off by %s, want 0 (the normal interval)", d)
	}
	if d := errorBackoff(2, interval); d != 10*time.Minute {
		t.Errorf("errorBackoff(2) = %s, want 10m", d)
	}
	if d := errorBackoff(3, interval); d != 20*time.Minute {
		t.Errorf("errorBackoff(3) = %s, want 20m", d)
	}
	for _, n := range []int{5, 10, 100} {
		if d := errorBackoff(n, interval); d != MaxErrorBackoff {
			t.Errorf("errorBackoff(%d) = %s, want the cap %s", n, d, MaxErrorBackoff)
		}
	}
}
