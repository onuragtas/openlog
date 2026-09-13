package updater

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// flakyEngine fails CurrentVersion a number of times, then reports version.
type flakyEngine struct {
	failures int32
	calls    atomic.Int32
	version  string
}

func (e *flakyEngine) Name() string                  { return "compose" }
func (e *flakyEngine) Recover(context.Context) error { return nil }
func (e *flakyEngine) Apply(context.Context, *Target, *Status, func()) error {
	return errors.New("not expected")
}
func (e *flakyEngine) CurrentVersion(context.Context) (string, error) {
	if e.calls.Add(1) <= e.failures {
		return "", errors.New(`Get "http://openlog:9464/readyz": dial tcp: lookup openlog: no such host`)
	}
	return e.version, nil
}

// A fresh `docker compose up` starts the updater before `openlog` resolves: the failed first run
// must be retried soon, not after the (1h) interval, so the status does not stay "error".
func TestLoopRetriesFailedRunBeforeInterval(t *testing.T) {
	eng := &flakyEngine{failures: 1, version: "0.9.0"}
	store := &memStore{}
	r := &Runner{
		Cfg:        Config{Mode: ModeNotify, Channel: "stable", Interval: time.Hour},
		Engine:     eng,
		Source:     &fakeSource{channels: map[string][]string{"stable": {"0.9.0"}}, manifests: map[string]string{"0.9.0": manifestJSON("0.9.0", "stable", "", "img@sha256:1")}},
		Store:      store,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		RetryDelay: 20 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Loop(ctx); close(done) }()

	deadline := time.Now().Add(5 * time.Second)
	for eng.calls.Load() < 2 {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("no retry after a failed run (calls=%d)", eng.calls.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	// After the successful retry the regular (1h) interval applies: no third run.
	time.Sleep(150 * time.Millisecond)
	calls := eng.calls.Load()
	cancel()
	<-done // the store is only read after Loop returned
	if calls != 2 {
		t.Errorf("runs = %d, want 2 (failed + retry)", calls)
	}
	if store.st.State != StateUpToDate || store.st.Error != "" {
		t.Fatalf("status after retry = %+v", store.st)
	}
}

func TestRetryDelayDefault(t *testing.T) {
	if d := (&Runner{Cfg: Config{Interval: time.Hour}}).retryDelay(); d != DefaultRetryDelay {
		t.Errorf("retry delay with 1h interval = %s", d)
	}
	if d := (&Runner{Cfg: Config{Interval: 10 * time.Second}}).retryDelay(); d != 10*time.Second {
		t.Errorf("retry delay with 10s interval = %s", d)
	}
}
