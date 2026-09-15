//go:build darwin

package update

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeLaunchd struct {
	mu           sync.Mutex
	calls        []string
	printsLoaded int // print succeeds (job listed) this many times
}

func (f *fakeLaunchd) run(_ context.Context, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(args, " "))
	if args[0] == "print" && f.printsLoaded > 0 {
		f.printsLoaded--
		return nil
	}
	if args[0] == "print" {
		return context.Canceled // any error: not listed
	}
	return nil
}

func withFakeLaunchd(t *testing.T, f *fakeLaunchd, timeout time.Duration) {
	t.Helper()
	oldCtl, oldTimeout := launchdCtl, launchdUnloadTimeout
	launchdCtl, launchdUnloadTimeout = f.run, timeout
	t.Cleanup(func() { launchdCtl, launchdUnloadTimeout = oldCtl, oldTimeout })
}

func TestUninstallServiceWaitsUntilUnloaded(t *testing.T) {
	f := &fakeLaunchd{printsLoaded: 3}
	withFakeLaunchd(t, f, 5*time.Second)
	if err := UninstallService(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.printsLoaded != 0 || f.calls[0] != "bootout system/"+LaunchdLabel {
		t.Errorf("calls %v", f.calls)
	}
}

func TestUninstallServiceFailsWhenStillLoaded(t *testing.T) {
	f := &fakeLaunchd{printsLoaded: 1 << 30}
	withFakeLaunchd(t, f, 600*time.Millisecond)
	err := UninstallService(context.Background())
	if err == nil || !strings.Contains(err.Error(), "still lists") {
		t.Fatalf("err = %v", err)
	}
	bootouts := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, "bootout") {
			bootouts++
		}
	}
	if bootouts != 2 {
		t.Errorf("bootouts = %d, want 2 (one retry): %v", bootouts, f.calls)
	}
}
