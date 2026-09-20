package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	cases := []struct {
		name  string
		in    Input
		field string
	}{
		{"no name", Input{Kind: KindCron, Cron: "* * * * *"}, "name"},
		{"bad cron", Input{Name: "x", Kind: KindCron, Cron: "99 * * * *"}, "cron"},
		{"unknown zone", Input{Name: "x", Kind: KindCron, Cron: "* * * * *", TimeZone: "Mars/Olympus"}, "time_zone"},
		{"interval too short", Input{Name: "x", Kind: KindInterval, IntervalSeconds: 30}, "interval_seconds"},
		{"grace too long", Input{Name: "x", Kind: KindCron, Cron: "* * * * *", GraceSeconds: 90000}, "grace_seconds"},
		{"unknown kind", Input{Name: "x", Kind: "webhook"}, "kind"},
		{"too many tags", Input{Name: "x", Kind: KindCron, Cron: "* * * * *", Tags: make([]string, 11)}, "tags"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			err := in.Validate()
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Field != tc.field {
				t.Fatalf("err = %v, want a validation error on %q", err, tc.field)
			}
		})
	}
}

// Each kind keeps only its own schedule, so a stored row never describes two.
func TestValidateClearsTheOtherKind(t *testing.T) {
	cron := Input{Name: "x", Kind: KindCron, Cron: "@daily", IntervalSeconds: 600}
	if err := cron.Validate(); err != nil {
		t.Fatal(err)
	}
	if cron.IntervalSeconds != 0 {
		t.Errorf("a cron monitor must not keep an interval: %+v", cron)
	}
	interval := Input{Name: "x", Kind: KindInterval, IntervalSeconds: 600, Cron: "@daily", TimeZone: "UTC"}
	if err := interval.Validate(); err != nil {
		t.Fatal(err)
	}
	if interval.Cron != "" || interval.TimeZone != "" {
		t.Errorf("an interval monitor must not keep a cron expression: %+v", interval)
	}
}

func TestNextExpected(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 30, 0, 0, time.UTC)

	cron := Input{Name: "nightly", Kind: KindCron, Cron: "0 3 * * *"}
	if err := cron.Validate(); err != nil {
		t.Fatal(err)
	}
	next, err := cron.NextExpected(now)
	if err != nil {
		t.Fatal(err)
	}
	if got := next.UTC().Format(time.RFC3339); got != "2026-03-11T03:00:00Z" {
		t.Errorf("cron next = %s", got)
	}

	// An interval monitor is measured from the last report, not from a fixed grid.
	interval := Input{Name: "heartbeat", Kind: KindInterval, IntervalSeconds: 600}
	if err := interval.Validate(); err != nil {
		t.Fatal(err)
	}
	next, err = interval.NextExpected(now)
	if err != nil {
		t.Fatal(err)
	}
	if !next.Equal(now.Add(10 * time.Minute)) {
		t.Errorf("interval next = %s, want %s", next, now.Add(10*time.Minute))
	}
}

func TestTokens(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		tok, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if NormalizeToken(tok) != tok {
			t.Fatalf("a fresh token must be canonical: %q", tok)
		}
		if seen[tok] {
			t.Fatalf("duplicate token %q", tok)
		}
		seen[tok] = true
	}
	// A token is accepted case-insensitively (a shell script may have upper-cased it) and rejected when it
	// is not a token at all, so the lookup is skipped instead of hitting the database.
	tok, _ := NewToken()
	if NormalizeToken(strings.ToUpper(tok)) != tok {
		t.Error("an upper-cased token must normalize back")
	}
	for _, bad := range []string{"", "olj_", "olj_short", "abc_" + strings.Repeat("a", 32), "olj_" + strings.Repeat("1", 32)} {
		if NormalizeToken(bad) != "" {
			t.Errorf("NormalizeToken(%q) must reject it", bad)
		}
	}
}

func TestParseEventAndStatus(t *testing.T) {
	cases := map[string]string{"": EventSuccess, "/": EventSuccess, "ok": EventSuccess, "done": EventSuccess,
		"start": EventStart, "begin": EventStart, "fail": EventFail, "error": EventFail, "FAIL": EventFail}
	for suffix, want := range cases {
		got, ok := ParseEvent(suffix)
		if !ok || got != want {
			t.Errorf("ParseEvent(%q) = %q, %v; want %q", suffix, got, ok, want)
		}
	}
	if _, ok := ParseEvent("explode"); ok {
		t.Error("an unknown suffix must be rejected")
	}
	// A non-zero exit code is a failure however the job spelled the event: `curl …/$?` is one line that
	// reports both at once.
	if got := (Ping{Event: EventSuccess, ExitCode: 2}).Status(); got != StatusFailure {
		t.Errorf("status = %q, want failure", got)
	}
	if got := (Ping{Event: EventSuccess}).Status(); got != StatusSuccess {
		t.Errorf("status = %q, want success", got)
	}
	if got := (Ping{Event: EventStart, ExitCode: 3}).Status(); got != StatusRunning {
		t.Errorf("a start ping is always running, got %q", got)
	}
}

func TestLate(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	m := Monitor{Input: Input{Enabled: true, GraceSeconds: 300}, State: State{ExpectedAt: now.Add(-time.Minute)}}
	if m.Late(now) {
		t.Error("a monitor inside its grace period is not late")
	}
	m.State.ExpectedAt = now.Add(-10 * time.Minute)
	if !m.Late(now) {
		t.Error("a monitor past its grace period is late")
	}
	m.Enabled = false
	if m.Late(now) {
		t.Error("a disabled monitor is never late")
	}
}

// ---- the sweeper ----

type fakeSweepStore struct {
	runs  []Run
	calls int
	err   error
}

func (f *fakeSweepStore) ClaimOverdue(_ context.Context, _ time.Time, limit int) ([]Run, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if len(f.runs) == 0 {
		return nil, nil
	}
	n := min(limit, len(f.runs))
	out := f.runs[:n]
	f.runs = f.runs[n:]
	return out, nil
}

type fakeSink struct{ rows []Run }

func (f *fakeSink) Add(rows []Run) { f.rows = append(f.rows, rows...) }

func TestSweepDrainsABacklog(t *testing.T) {
	store := &fakeSweepStore{runs: make([]Run, 5)}
	for i := range store.runs {
		store.runs[i] = Run{MonitorID: "m", Status: StatusMissed}
	}
	sink := &fakeSink{}
	s := NewSweeper(store, sink, SweeperOptions{MaxPerSweep: 2})
	if n := s.Sweep(context.Background()); n != 5 {
		t.Fatalf("swept %d runs, want 5", n)
	}
	if len(sink.rows) != 5 {
		t.Fatalf("sink got %d runs", len(sink.rows))
	}
	// Two full passes and one partial: a backlog drains in one sweep instead of one batch per tick, and the
	// partial pass ends it without an extra empty query.
	if store.calls != 3 {
		t.Errorf("ClaimOverdue called %d times, want 3", store.calls)
	}
}

func TestSweepStopsOnError(t *testing.T) {
	store := &fakeSweepStore{err: errors.New("database is down")}
	sink := &fakeSink{}
	s := NewSweeper(store, sink, SweeperOptions{})
	if n := s.Sweep(context.Background()); n != 0 {
		t.Fatalf("swept %d runs, want 0", n)
	}
	if len(sink.rows) != 0 {
		t.Error("a failed claim must write nothing")
	}
}

// ---- the metric mirror ----

func TestMetricRows(t *testing.T) {
	at := time.Date(2026, 3, 10, 3, 0, 0, 0, time.UTC)
	rows := metricRows([]Run{
		{MonitorID: "m1", TenantID: "t", Name: "backup", Status: StatusSuccess, FinishedAt: at,
			StartedAt: at.Add(-time.Minute), DurationMs: 60000, LateSeconds: 12},
		{MonitorID: "m2", TenantID: "t", Name: "sync", Status: StatusMissed, FinishedAt: at, LateSeconds: 600},
	})
	byName := map[string][]any{}
	for _, r := range rows {
		byName[r[1].(string)+"/"+r[13].(map[string]string)["job.id"]] = r
	}
	if got := byName[MetricSuccess+"/m1"][16].(float64); got != 1 {
		t.Errorf("success of a successful run = %v", got)
	}
	if got := byName[MetricSuccess+"/m2"][16].(float64); got != 0 {
		t.Errorf("success of a missed run = %v", got)
	}
	if got := byName[MetricMissed+"/m2"][16].(float64); got != 1 {
		t.Errorf("missed of a missed run = %v", got)
	}
	if got := byName[MetricLate+"/m2"][16].(float64); got != 600 {
		t.Errorf("late of a missed run = %v", got)
	}
	// A run openlog never saw start has no duration: a zero would drag every average down.
	if _, ok := byName[MetricDuration+"/m2"]; ok {
		t.Error("a missed run must not report a duration")
	}
	if got := byName[MetricDuration+"/m1"][16].(float64); got != 60000 {
		t.Errorf("duration = %v", got)
	}
}

func TestRunRowsClampAndDefault(t *testing.T) {
	at := time.Date(2026, 3, 10, 3, 0, 0, 0, time.UTC)
	rows := runRows([]Run{{MonitorID: "m", TenantID: "t", Name: "x", Status: StatusFailure, FinishedAt: at,
		ExitCode: 9000, Message: strings.Repeat("x", MaxMessageBytes+10)}})
	if got := rows[0][7].(int32); got != MaxExitCode {
		t.Errorf("exit code = %d, want it clamped to %d", got, MaxExitCode)
	}
	if got := len(rows[0][9].(string)); got != MaxMessageBytes {
		t.Errorf("message length = %d, want it truncated to %d", got, MaxMessageBytes)
	}
	// A run without a start writes the epoch rather than a zero time ClickHouse would reject.
	if got := rows[0][5].(time.Time); got.Unix() != 0 {
		t.Errorf("started_at = %s, want the epoch", got)
	}
}
