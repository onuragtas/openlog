package synthetics

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
	recorded []Result
	claims   int
}

func (f *fakeSchedule) Claim(_ context.Context, _ time.Time, limit int) ([]Due, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims++
	n := min(limit, len(f.due))
	out := append([]Due(nil), f.due[:n]...)
	f.due = f.due[n:]
	return out, nil
}

func (f *fakeSchedule) Record(_ context.Context, r Result) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recorded = append(f.recorded, r)
	return nil
}

func (f *fakeSchedule) records() []Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Result(nil), f.recorded...)
}

// fakeRunner records how many runs overlap, in total and per organization.
type fakeRunner struct {
	delay time.Duration

	mu            sync.Mutex
	cur, peak     int
	perTenant     map[string]int
	peakPerTenant map[string]int
	ran           []Due
}

func newFakeRunner(delay time.Duration) *fakeRunner {
	return &fakeRunner{delay: delay, perTenant: map[string]int{}, peakPerTenant: map[string]int{}}
}

func (f *fakeRunner) Run(_ context.Context, d Due) Result {
	f.mu.Lock()
	f.cur++
	f.perTenant[d.TenantID]++
	f.peak = max(f.peak, f.cur)
	f.peakPerTenant[d.TenantID] = max(f.peakPerTenant[d.TenantID], f.perTenant[d.TenantID])
	f.ran = append(f.ran, d)
	f.mu.Unlock()

	time.Sleep(f.delay)

	f.mu.Lock()
	f.cur--
	f.perTenant[d.TenantID]--
	f.mu.Unlock()
	return Result{CheckID: d.Check.ID, TenantID: d.TenantID, Location: d.Location, Name: d.Check.Name, Success: true}
}

func (f *fakeRunner) runs() []Due {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Due(nil), f.ran...)
}

type fakeSink struct {
	mu   sync.Mutex
	rows []Result
}

func (s *fakeSink) Add(rows []Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, rows...)
}

func (s *fakeSink) added() []Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Result(nil), s.rows...)
}

func dueRows(tenant string, n int) []Due {
	out := make([]Due, n)
	for i := range n {
		out[i] = Due{TenantID: tenant, Location: LocationLocal, Check: Check{
			ID: fmt.Sprintf("%s-c%d", tenant, i),
			Input: Input{Name: fmt.Sprintf("check %d", i), URL: "https://example.com/health", Method: "GET",
				ExpectedStatus: []int{200}, TimeoutMs: 1000, IntervalSeconds: 60, Locations: []string{LocationLocal}},
		}}
	}
	return out
}

func testRunner(store ScheduleStore, runner CheckRunner, sink ResultSink, o RunnerOptions) *Runner {
	o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewRunner(store, runner, sink, o)
}

func TestRunOnceRunsClaimedChecks(t *testing.T) {
	store := &fakeSchedule{due: dueRows("tenant-a", 5)}
	runner := newFakeRunner(0)
	sink := &fakeSink{}
	r := testRunner(store, runner, sink, RunnerOptions{})

	if n := r.RunOnce(context.Background()); n != 5 {
		t.Fatalf("RunOnce = %d, want 5", n)
	}
	if got := len(runner.runs()); got != 5 {
		t.Errorf("ran %d checks", got)
	}
	// Every run is recorded in PostgreSQL (the list view's status) and handed to the ClickHouse sink.
	if got := len(store.records()); got != 5 {
		t.Errorf("recorded %d results", got)
	}
	if got := len(sink.added()); got != 5 {
		t.Errorf("sink received %d results", got)
	}
}

func TestRunOnceWithNothingDue(t *testing.T) {
	store := &fakeSchedule{}
	runner := newFakeRunner(0)
	r := testRunner(store, runner, &fakeSink{}, RunnerOptions{})

	if n := r.RunOnce(context.Background()); n != 0 {
		t.Fatalf("RunOnce = %d, want 0", n)
	}
	if got := len(runner.runs()); got != 0 {
		t.Errorf("ran %d checks with nothing due", got)
	}
}

// One claim takes at most BatchSize rows; the rest stay due for the next round.
func TestClaimTakesOneBatch(t *testing.T) {
	store := &fakeSchedule{due: dueRows("tenant-a", 10)}
	runner := newFakeRunner(0)
	r := testRunner(store, runner, &fakeSink{}, RunnerOptions{MaxConcurrent: 2, BatchSize: 4})

	if n := r.RunOnce(context.Background()); n != 4 {
		t.Fatalf("first RunOnce = %d, want 4", n)
	}
	if n := r.RunOnce(context.Background()); n != 4 {
		t.Fatalf("second RunOnce = %d, want 4", n)
	}
	if n := r.RunOnce(context.Background()); n != 2 {
		t.Fatalf("third RunOnce = %d, want the remaining 2", n)
	}
}

// The leader model: claiming is what keeps a check to one runner. Two schedulers on the same store (a
// leadership handover where both believe they lead for a moment) must not run any check twice.
func TestTwoRunnersRunEachCheckOnce(t *testing.T) {
	store := &fakeSchedule{due: dueRows("tenant-a", 20)}
	runner := newFakeRunner(time.Millisecond)
	a := testRunner(store, runner, &fakeSink{}, RunnerOptions{MaxConcurrent: 8})
	b := testRunner(store, runner, &fakeSink{}, RunnerOptions{MaxConcurrent: 8})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a.RunOnce(context.Background()) }()
	go func() { defer wg.Done(); b.RunOnce(context.Background()) }()
	wg.Wait()

	runs := runner.runs()
	if len(runs) != 20 {
		t.Fatalf("ran %d checks, want 20", len(runs))
	}
	seen := map[string]int{}
	for _, d := range runs {
		seen[d.Check.ID]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("check %s ran %d times", id, n)
		}
	}
	if len(seen) != 20 {
		t.Errorf("%d distinct checks ran", len(seen))
	}
}

func TestGlobalConcurrencyCap(t *testing.T) {
	// Six organizations, so the per-tenant cap cannot be what limits the runs.
	var due []Due
	for i := range 6 {
		due = append(due, dueRows(fmt.Sprintf("tenant-%d", i), 2)...)
	}
	store := &fakeSchedule{due: due}
	runner := newFakeRunner(20 * time.Millisecond)
	r := testRunner(store, runner, &fakeSink{}, RunnerOptions{MaxConcurrent: 3, TenantMaxConcurrent: 3})

	if n := r.RunOnce(context.Background()); n != 12 {
		t.Fatalf("RunOnce = %d, want 12", n)
	}
	if runner.peak > 3 {
		t.Errorf("peak concurrency %d, cap 3", runner.peak)
	}
	if runner.peak < 2 {
		t.Errorf("peak concurrency %d: the runs did not overlap at all", runner.peak)
	}
}

func TestTenantConcurrencyCap(t *testing.T) {
	store := &fakeSchedule{due: append(dueRows("tenant-a", 8), dueRows("tenant-b", 8)...)}
	runner := newFakeRunner(20 * time.Millisecond)
	r := testRunner(store, runner, &fakeSink{}, RunnerOptions{MaxConcurrent: 16, TenantMaxConcurrent: 2})

	if n := r.RunOnce(context.Background()); n != 16 {
		t.Fatalf("RunOnce = %d, want 16", n)
	}
	for tenant, peak := range runner.peakPerTenant {
		if peak > 2 {
			t.Errorf("%s peaked at %d concurrent runs, cap 2", tenant, peak)
		}
	}
	// One organization at its cap must not stop the other from running.
	if runner.peak < 3 {
		t.Errorf("peak concurrency %d: the organizations did not run in parallel", runner.peak)
	}
}

// TenantMaxConcurrent is clamped to MaxConcurrent, so a misconfiguration cannot exceed the global cap.
func TestTenantCapClampedToGlobal(t *testing.T) {
	store := &fakeSchedule{due: dueRows("tenant-a", 6)}
	runner := newFakeRunner(20 * time.Millisecond)
	r := testRunner(store, runner, &fakeSink{}, RunnerOptions{MaxConcurrent: 2, TenantMaxConcurrent: 50})

	r.RunOnce(context.Background())
	if runner.peak > 2 {
		t.Errorf("peak concurrency %d, global cap 2", runner.peak)
	}
}

// A cancelled context (shutdown, lost leadership) stops the runs instead of starting new ones.
func TestRunOnceStopsOnCancelledContext(t *testing.T) {
	store := &fakeSchedule{due: dueRows("tenant-a", 5)}
	runner := newFakeRunner(0)
	r := testRunner(store, runner, &fakeSink{}, RunnerOptions{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.RunOnce(ctx)

	if got := len(runner.runs()); got != 0 {
		t.Errorf("ran %d checks after the context was cancelled", got)
	}
}

func TestRunStopsWhenContextIsDone(t *testing.T) {
	store := &fakeSchedule{due: dueRows("tenant-a", 3)}
	r := testRunner(store, newFakeRunner(0), &fakeSink{}, RunnerOptions{Tick: 10 * time.Millisecond})

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
