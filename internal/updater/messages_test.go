package updater

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"testing"

	"github.com/onuragtas/openlog/internal/updatemsg"
	"github.com/onuragtas/openlog/internal/updatereq"
)

func wantCode(t *testing.T, st Status, code string, params map[string]string) {
	t.Helper()
	if st.MessageCode != code || !maps.Equal(st.MessageParams, params) {
		t.Fatalf("message code = %q %v, want %q %v (message %q)", st.MessageCode, st.MessageParams, code, params, st.Message)
	}
	if st.Message != updatemsg.Format(code, params) {
		t.Fatalf("message %q does not match code %s", st.Message, code)
	}
}

func TestStatusMessageCodes(t *testing.T) {
	ctx := context.Background()
	t.Run("notify", func(t *testing.T) {
		r, _, store, _, _ := requestRunner(ModeNotify, "")
		if err := r.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		wantCode(t, store.st, updatemsg.UpdateAvailableNotify, map[string]string{"version": "0.9.1"})
		if store.st.Message != "openlog 0.9.1 is available (OPENLOG_UPDATER_MODE=notify)" {
			t.Fatalf("English message changed: %q", store.st.Message)
		}
	})
	t.Run("waiting for window", func(t *testing.T) {
		r, _, store, _, _ := requestRunner(ModeAuto, "sat,sun 02:00-05:00")
		if err := r.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		wantCode(t, store.st, updatemsg.WaitingForWindow, map[string]string{"version": "0.9.1"})
	})
	t.Run("updated", func(t *testing.T) {
		r, _, store, _, _ := requestRunner(ModeAuto, "")
		if err := r.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		wantCode(t, store.st, updatemsg.Updated, map[string]string{"from": "0.9.0", "to": "0.9.1"})
		if store.st.Message != "updated 0.9.0 → 0.9.1" {
			t.Fatalf("English message changed: %q", store.st.Message)
		}
		// Up to date afterwards: the reason of SelectTarget carries a code too.
		r.Engine.(*applyEngine).version = "0.9.1"
		if err := r.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		wantCode(t, store.st, updatemsg.UpToDateNewest, map[string]string{"version": "0.9.1", "channel": "stable"})
	})
	t.Run("mode off", func(t *testing.T) {
		r, _, store, _, _ := requestRunner(ModeOff, "")
		if err := r.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		wantCode(t, store.st, updatemsg.ModeOff, map[string]string{})
	})
	t.Run("no eligible release", func(t *testing.T) {
		r, _, store, _, _ := requestRunner(ModeNotify, "")
		store.st, store.found = Status{FailedVersions: []string{"0.9.1"}}, true
		if err := r.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		wantCode(t, store.st, updatemsg.UpToDateNoEligible, map[string]string{"version": "0.9.0", "details": "0.9.1: failed before on this installation"})
	})
	for _, c := range []struct {
		state, code string
		params      map[string]string
	}{
		{StateRolledBack, updatemsg.RolledBack, map[string]string{"to": "0.9.1", "from": "0.9.0"}},
		{StateRollbackFailed, updatemsg.RollbackFailed, map[string]string{"to": "0.9.1", "from": "0.9.0"}},
		{StateFailed, updatemsg.FailedBeforeChange, map[string]string{"to": "0.9.1"}},
	} {
		t.Run(c.state, func(t *testing.T) {
			r, _, store, _, _ := requestRunner(ModeAuto, "")
			r.Engine = &failingEngine{version: "0.9.0", state: c.state}
			if err := r.RunOnce(ctx); err == nil {
				t.Fatal("expected an update error")
			}
			wantCode(t, store.st, c.code, c.params)
			if store.st.Error == "" {
				t.Fatal("error must stay set")
			}
		})
	}
	t.Run("error clears the code", func(t *testing.T) {
		r, _, store, _, _ := requestRunner(ModeNotify, "")
		if err := r.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		r.Source.(*fakeSource).indexErr = errors.New("index unreachable")
		if err := r.RunOnce(ctx); err == nil {
			t.Fatal("expected an error")
		}
		if store.st.State != StateError || store.st.Message != "" || store.st.MessageCode != "" || store.st.MessageParams != nil {
			t.Fatalf("status = %+v", store.st)
		}
	})
}

// failingEngine fails Apply and leaves state (failed, rolled_back, rollback_failed).
type failingEngine struct{ version, state string }

func (e *failingEngine) Name() string                                   { return "compose" }
func (e *failingEngine) Recover(context.Context) error                  { return nil }
func (e *failingEngine) CurrentVersion(context.Context) (string, error) { return e.version, nil }
func (e *failingEngine) Apply(_ context.Context, _ *Target, st *Status, _ func()) error {
	if e.state != StateFailed {
		st.State = e.state
	}
	return errStep("health", errors.New("container exited"))
}

// Request results are fixed messages the API can map back to codes (update_requests has text only).
func TestRequestMessageCodes(t *testing.T) {
	ctx := context.Background()
	check := func(t *testing.T, q *updatereq.MemQueue, id, code string, params map[string]string) {
		t.Helper()
		got := finished(t, q, id)
		c, p, ok := updatemsg.Parse(got.Message)
		if !ok || c != code || !maps.Equal(p, params) {
			t.Fatalf("request message %q = %q %v %v, want %q %v", got.Message, c, p, ok, code, params)
		}
	}
	t.Run("outside window", func(t *testing.T) {
		r, _, _, q, _ := requestRunner(ModeAuto, "sat,sun 02:00-05:00")
		req := enqueue(t, q, updatereq.Request{Action: updatereq.ActionApply, TargetVersion: "0.9.1"})
		if _, err := r.RunRequests(ctx); err != nil {
			t.Fatal(err)
		}
		check(t, q, req.ID, updatemsg.RequestOutsideWindow, map[string]string{})
	})
	t.Run("target changed", func(t *testing.T) {
		r, _, _, q, _ := requestRunner(ModeNotify, "")
		req := enqueue(t, q, updatereq.Request{Action: updatereq.ActionApply, TargetVersion: "0.9.2"})
		if _, err := r.RunRequests(ctx); err != nil {
			t.Fatal(err)
		}
		check(t, q, req.ID, updatemsg.RequestTargetChanged, map[string]string{"version": "0.9.1", "requested": "0.9.2"})
	})
	t.Run("mode off", func(t *testing.T) {
		r, _, _, q, _ := requestRunner(ModeOff, "")
		req := enqueue(t, q, updatereq.Request{Action: updatereq.ActionApply, TargetVersion: "0.9.1"})
		if _, err := r.RunRequests(ctx); err != nil {
			t.Fatal(err)
		}
		check(t, q, req.ID, updatemsg.RequestModeOff, map[string]string{})
	})
	t.Run("check done", func(t *testing.T) {
		r, _, _, q, _ := requestRunner(ModeNotify, "")
		req := enqueue(t, q, updatereq.Request{Action: updatereq.ActionCheck})
		if _, err := r.RunRequests(ctx); err != nil {
			t.Fatal(err)
		}
		check(t, q, req.ID, updatemsg.UpdateAvailableNotify, map[string]string{"version": "0.9.1"})
	})
	t.Run("rolled back", func(t *testing.T) {
		r, _, _, q, _ := requestRunner(ModeNotify, "")
		r.Engine = &failingEngine{version: "0.9.0", state: StateRolledBack}
		req := enqueue(t, q, updatereq.Request{Action: updatereq.ActionApply, TargetVersion: "0.9.1"})
		_, _ = r.RunRequests(ctx)
		check(t, q, req.ID, updatemsg.RolledBack, map[string]string{"to": "0.9.1", "from": "0.9.0", updatemsg.ParamError: "health: container exited"})
	})
}

// A status document of an older updater has no code and still decodes.
func TestStatusWithoutCodeDecodes(t *testing.T) {
	var st Status
	if err := json.Unmarshal([]byte(`{"engine":"compose","mode":"notify","state":"available","message":"openlog 0.9.1 is available"}`), &st); err != nil {
		t.Fatal(err)
	}
	if st.MessageCode != "" || st.MessageParams != nil || st.Message == "" {
		t.Fatalf("status = %+v", st)
	}
	b, _ := json.Marshal(Status{State: StateAvailable, Message: "x", MessageCode: updatemsg.UpdateAvailable, MessageParams: map[string]string{"version": "0.9.1"}})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["message_code"] != "update_available" || m["message_params"].(map[string]any)["version"] != "0.9.1" {
		t.Fatalf("json = %s", b)
	}
}
