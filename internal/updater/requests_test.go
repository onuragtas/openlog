package updater

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/updatereq"
)

// applyEngine records Apply calls.
type applyEngine struct {
	version string
	applied atomic.Int32
	target  atomic.Value
}

func (e *applyEngine) Name() string                                   { return "compose" }
func (e *applyEngine) Recover(context.Context) error                  { return nil }
func (e *applyEngine) CurrentVersion(context.Context) (string, error) { return e.version, nil }
func (e *applyEngine) Apply(_ context.Context, t *Target, st *Status, save func()) error {
	e.applied.Add(1)
	e.target.Store(t.Version.String())
	st.beginStep("pull", time.Now())
	st.endStep(nil, "", time.Now())
	save()
	return nil
}

func requestRunner(mode string, windows string) (*Runner, *applyEngine, *memStore, *updatereq.MemQueue, *recordAudit) {
	eng := &applyEngine{version: "0.9.0"}
	store := &memStore{}
	q := &updatereq.MemQueue{}
	audit := &recordAudit{}
	w, err := ParseWindows(windows)
	if err != nil {
		panic(err)
	}
	r := &Runner{
		Cfg:    Config{Mode: mode, Channel: "stable", Interval: time.Hour, RequestPoll: 10 * time.Millisecond, MaintenanceWindows: w},
		Engine: eng,
		Source: &fakeSource{channels: map[string][]string{"stable": {"0.9.0", "0.9.1"}}, manifests: map[string]string{
			"0.9.0": manifestJSON("0.9.0", "stable", "", "img@sha256:0"),
			"0.9.1": manifestJSON("0.9.1", "stable", "", "img@sha256:1"),
		}},
		Store: store, Audit: audit, Requests: q,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		// 12:00 UTC on a Wednesday: outside "sat,sun 02:00-05:00".
		Now: func() time.Time { return time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC) },
	}
	return r, eng, store, q, audit
}

func enqueue(t *testing.T, q *updatereq.MemQueue, r updatereq.Request) updatereq.Request {
	t.Helper()
	if err := q.Enqueue(context.Background(), &r, 0); err != nil {
		t.Fatal(err)
	}
	return r
}

func finished(t *testing.T, q *updatereq.MemQueue, id string) updatereq.Request {
	t.Helper()
	for _, r := range q.All() {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("request %s not found", id)
	return updatereq.Request{}
}

func TestApplyRequestInNotifyMode(t *testing.T) {
	r, eng, store, q, audit := requestRunner(ModeNotify, "")
	req := enqueue(t, q, updatereq.Request{Action: updatereq.ActionApply, TargetVersion: "0.9.1", RequestedByEmail: "admin@example.com"})
	n, err := r.RunRequests(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("RunRequests = %d, %v", n, err)
	}
	if eng.applied.Load() != 1 || eng.target.Load() != "0.9.1" {
		t.Fatalf("apply calls = %d target %v", eng.applied.Load(), eng.target.Load())
	}
	if store.st.State != StateSucceeded || store.st.CurrentVersion != "0.9.1" {
		t.Fatalf("status = %+v", store.st)
	}
	got := finished(t, q, req.ID)
	if got.State != updatereq.StateDone || !strings.Contains(got.Message, "0.9.0 → 0.9.1") || got.PickedAt == nil || got.FinishedAt == nil {
		t.Fatalf("request = %+v", got)
	}
	if len(audit.actions) != 2 || audit.actions[0] != "updater.update_started" || audit.actions[1] != "updater.update_succeeded" {
		t.Fatalf("audit = %v", audit.actions)
	}
}

func TestApplyRequestRejections(t *testing.T) {
	ctx := context.Background()
	t.Run("maintenance window", func(t *testing.T) {
		r, eng, store, q, _ := requestRunner(ModeAuto, "sat,sun 02:00-05:00")
		req := enqueue(t, q, updatereq.Request{Action: updatereq.ActionApply, TargetVersion: "0.9.1"})
		if _, err := r.RunRequests(ctx); err != nil {
			t.Fatal(err) // a rejection is not a run failure
		}
		got := finished(t, q, req.ID)
		if eng.applied.Load() != 0 || got.State != updatereq.StateFailed || !strings.Contains(got.Message, "maintenance window") || store.st.State != StateWaiting {
			t.Fatalf("applied=%d request=%+v status=%s", eng.applied.Load(), got, store.st.State)
		}
		// Confirmed outside the window: installed now.
		req = enqueue(t, q, updatereq.Request{Action: updatereq.ActionApply, TargetVersion: "0.9.1", IgnoreMaintenanceWindow: true})
		if _, err := r.RunRequests(ctx); err != nil {
			t.Fatal(err)
		}
		if eng.applied.Load() != 1 || finished(t, q, req.ID).State != updatereq.StateDone {
			t.Fatalf("ignore window: applied=%d request=%+v", eng.applied.Load(), finished(t, q, req.ID))
		}
	})
	t.Run("target changed", func(t *testing.T) {
		r, eng, store, q, _ := requestRunner(ModeNotify, "")
		req := enqueue(t, q, updatereq.Request{Action: updatereq.ActionApply, TargetVersion: "0.9.2"})
		if _, err := r.RunRequests(ctx); err != nil {
			t.Fatal(err)
		}
		got := finished(t, q, req.ID)
		if eng.applied.Load() != 0 || got.State != updatereq.StateFailed || !strings.Contains(got.Message, "now 0.9.1") || store.st.State != StateAvailable {
			t.Fatalf("applied=%d request=%+v status=%+v", eng.applied.Load(), got, store.st)
		}
	})
	t.Run("mode off", func(t *testing.T) {
		r, eng, _, q, _ := requestRunner(ModeOff, "")
		req := enqueue(t, q, updatereq.Request{Action: updatereq.ActionApply, TargetVersion: "0.9.1"})
		if _, err := r.RunRequests(ctx); err != nil {
			t.Fatal(err)
		}
		if got := finished(t, q, req.ID); eng.applied.Load() != 0 || got.State != updatereq.StateFailed || !strings.Contains(got.Message, "OPENLOG_UPDATER_MODE=off") {
			t.Fatalf("applied=%d request=%+v", eng.applied.Load(), got)
		}
	})
	t.Run("previously failed version may be retried", func(t *testing.T) {
		r, eng, store, q, _ := requestRunner(ModeNotify, "")
		store.st, store.found = Status{FailedVersions: []string{"0.9.1"}}, true
		// A regular check skips the failed version.
		if err := r.RunOnce(ctx); err != nil || store.st.State != StateUpToDate {
			t.Fatalf("check: %v %+v", err, store.st)
		}
		req := enqueue(t, q, updatereq.Request{Action: updatereq.ActionApply, TargetVersion: "0.9.1"})
		if _, err := r.RunRequests(ctx); err != nil {
			t.Fatal(err)
		}
		if eng.applied.Load() != 1 || finished(t, q, req.ID).State != updatereq.StateDone || len(store.st.FailedVersions) != 0 {
			t.Fatalf("applied=%d request=%+v status=%+v", eng.applied.Load(), finished(t, q, req.ID), store.st)
		}
	})
}

func TestCheckRequestDoesNotInstall(t *testing.T) {
	r, eng, store, q, _ := requestRunner(ModeNotify, "")
	req := enqueue(t, q, updatereq.Request{Action: updatereq.ActionCheck})
	if _, err := r.RunRequests(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := finished(t, q, req.ID)
	if eng.applied.Load() != 0 || store.st.State != StateAvailable || got.State != updatereq.StateDone || !strings.Contains(got.Message, "0.9.1 is available") {
		t.Fatalf("applied=%d status=%s request=%+v", eng.applied.Load(), store.st.State, got)
	}
}

// The loop picks up a request within the poll interval, long before OPENLOG_UPDATER_INTERVAL, and
// writes the heartbeat the API uses to show that an updater is listening.
func TestLoopPicksUpRequestsQuickly(t *testing.T) {
	r, eng, _, q, _ := requestRunner(ModeNotify, "")
	r.Now = nil
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Loop(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if hb, _ := q.Heartbeat(ctx); hb != nil {
			if hb.Engine != "compose" || hb.Mode != ModeNotify || !hb.Listening(time.Now()) {
				t.Fatalf("heartbeat = %+v", hb)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no heartbeat")
		}
		time.Sleep(5 * time.Millisecond)
	}
	req := enqueue(t, q, updatereq.Request{Action: updatereq.ActionApply, TargetVersion: "0.9.1"})
	for {
		if got := finished(t, q, req.ID); got.State == updatereq.StateDone {
			break
		} else if got.State == updatereq.StateFailed || time.Now().After(deadline) {
			t.Fatalf("request = %+v", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if eng.applied.Load() != 1 {
		t.Fatalf("apply calls = %d", eng.applied.Load())
	}
}

func TestLoopAbandonsRunningRequests(t *testing.T) {
	r, _, _, q, _ := requestRunner(ModeNotify, "")
	req := enqueue(t, q, updatereq.Request{Action: updatereq.ActionApply, TargetVersion: "0.9.1"})
	if _, err := q.Claim(context.Background()); err != nil { // a previous process claimed it and died
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.Loop(ctx) // one pass: abandon, run, return
	if got := finished(t, q, req.ID); got.State != updatereq.StateFailed || !strings.Contains(got.Message, "restarted") {
		t.Fatalf("request = %+v", got)
	}
}
