package updatereq

import (
	"context"
	"errors"
	"testing"
	"time"
)

// queueContract exercises the semantics shared by MemQueue and PGQueue. advance moves the queue's
// clock (MemQueue) or ages the stored rows (PGQueue).
func queueContract(t *testing.T, q interface {
	Queue
	Poller
}, advance func(d time.Duration)) {
	t.Helper()
	ctx := context.Background()

	if r, err := q.Latest(ctx); err != nil || r != nil {
		t.Fatalf("empty Latest = %+v, %v", r, err)
	}
	check := Request{Action: ActionCheck, RequestedByEmail: "admin@example.com"}
	if err := q.Enqueue(ctx, &check, MinGap); err != nil {
		t.Fatal(err)
	}
	if check.ID == "" || check.State != StatePending || check.RequestedAt.IsZero() {
		t.Fatalf("enqueued = %+v", check)
	}
	// Rate limit per action, installation-wide.
	var tooSoon *TooSoonError
	if err := q.Enqueue(ctx, &Request{Action: ActionCheck}, MinGap); !errors.As(err, &tooSoon) || tooSoon.RetryAfter <= 0 || tooSoon.RetryAfter > MinGap {
		t.Fatalf("second check = %v", err)
	}
	apply := Request{Action: ActionApply, TargetVersion: "0.9.1", IgnoreMaintenanceWindow: true}
	if err := q.Enqueue(ctx, &apply, MinGap); err != nil {
		t.Fatalf("apply after check: %v", err)
	}
	if latest, err := q.Latest(ctx); err != nil || latest.ID != apply.ID || !latest.IgnoreMaintenanceWindow || latest.TargetVersion != "0.9.1" {
		t.Fatalf("Latest = %+v, %v", latest, err)
	}
	advance(MinGap + time.Second)
	// An open apply blocks another one.
	if err := q.Enqueue(ctx, &Request{Action: ActionApply, TargetVersion: "0.9.1"}, MinGap); !errors.Is(err, ErrBusy) {
		t.Fatalf("second apply = %v", err)
	}

	// Oldest first.
	got, err := q.Claim(ctx)
	if err != nil || got == nil || got.ID != check.ID || got.State != StateRunning || got.PickedAt == nil {
		t.Fatalf("claim 1 = %+v, %v", got, err)
	}
	if err := q.Finish(ctx, got.ID, StateDone, "up to date"); err != nil {
		t.Fatal(err)
	}
	got, err = q.Claim(ctx)
	if err != nil || got == nil || got.ID != apply.ID {
		t.Fatalf("claim 2 = %+v, %v", got, err)
	}
	if got, err := q.Claim(ctx); err != nil || got != nil {
		t.Fatalf("claim 3 = %+v, %v", got, err)
	}
	// Running apply still blocks; a restarted updater abandons it.
	if err := q.Enqueue(ctx, &Request{Action: ActionApply, TargetVersion: "0.9.1"}, MinGap); !errors.Is(err, ErrBusy) {
		t.Fatalf("apply while running = %v", err)
	}
	if err := q.Abandon(ctx, "interrupted"); err != nil {
		t.Fatal(err)
	}
	if latest, _ := q.Latest(ctx); latest.State != StateFailed || latest.Message != "interrupted" || latest.FinishedAt == nil {
		t.Fatalf("abandoned = %+v", latest)
	}

	// Nobody picks up a pending request: it expires and no longer blocks.
	stale := Request{Action: ActionApply, TargetVersion: "0.9.2"}
	if err := q.Enqueue(ctx, &stale, MinGap); err != nil {
		t.Fatal(err)
	}
	advance(PendingTTL + time.Minute)
	if err := q.Enqueue(ctx, &Request{Action: ActionCheck}, MinGap); err != nil {
		t.Fatal(err)
	}
	got, err = q.Claim(ctx)
	if err != nil || got == nil || got.Action != ActionCheck {
		t.Fatalf("claim after expiry = %+v, %v", got, err)
	}
	if err := q.Enqueue(ctx, &Request{Action: ActionApply, TargetVersion: "0.9.2"}, MinGap); err != nil {
		t.Fatalf("apply after expiry: %v", err)
	}

	if hb, err := q.Heartbeat(ctx); err != nil || hb != nil {
		t.Fatalf("empty heartbeat = %+v, %v", hb, err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := q.PutHeartbeat(ctx, Heartbeat{Engine: "compose", Mode: "notify", PollSeconds: 10, PolledAt: now}); err != nil {
		t.Fatal(err)
	}
	if hb, err := q.Heartbeat(ctx); err != nil || hb == nil || hb.Engine != "compose" || !hb.PolledAt.Equal(now) {
		t.Fatalf("heartbeat = %+v, %v", hb, err)
	}
}

func TestMemQueue(t *testing.T) {
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	q := &MemQueue{Now: func() time.Time { return now }}
	queueContract(t, q, func(d time.Duration) { now = now.Add(d) })
	var expired int
	for _, r := range q.All() {
		if r.State == StateExpired {
			expired++
			if r.Message != expiredMessage {
				t.Errorf("expired message = %q", r.Message)
			}
		}
	}
	if expired != 1 {
		t.Errorf("expired = %d", expired)
	}
}

func TestHeartbeatListening(t *testing.T) {
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	var nilHB *Heartbeat
	if nilHB.Listening(now) {
		t.Error("nil heartbeat listening")
	}
	hb := &Heartbeat{PollSeconds: 10, PolledAt: now.Add(-80 * time.Second)}
	if !hb.Listening(now) {
		t.Error("80s old heartbeat (30s refresh) not listening")
	}
	hb.PolledAt = now.Add(-2 * time.Minute)
	if hb.Listening(now) {
		t.Error("2m old heartbeat listening")
	}
	hb.PollSeconds = 60 // slow poll: 3 polls
	if !hb.Listening(now) {
		t.Error("2m old heartbeat with 60s poll not listening")
	}
}

func TestClip(t *testing.T) {
	long := string(make([]byte, 1999)) + "é" + "x"
	if got := clip(long); len(got) != 1999 {
		t.Errorf("clip splits a rune: len %d", len(got))
	}
}
