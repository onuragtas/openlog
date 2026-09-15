package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/updatemsg"
)

const updaterName = "proj-openlog-updater-1"

type handoverResult struct {
	proceed bool
	err     error
}

type selfUpdateFixture struct {
	*composeFixture
	updID   string
	exits   atomic.Int32
	results chan handoverResult
}

// newSelfUpdateFixture is the compose fixture plus the running openlog-updater container (0.9.0, restart policy
// unless-stopped) of an install-server.sh installation (bundle) whose .env pins OPENLOG_UPDATER_IMAGE.
func newSelfUpdateFixture(t *testing.T, bundle bool) *selfUpdateFixture {
	t.Helper()
	f := &selfUpdateFixture{composeFixture: newComposeFixture(t), updID: fmt.Sprintf("%064x", 0xdada), results: make(chan handoverResult, 4)}
	var upd Container
	roundTrip(map[string]any{
		"Id": f.updID, "Name": "/" + updaterName, "Image": "sha256:img090",
		"Config": map[string]any{
			"Image": img090, "Hostname": f.updID[:12], "User": "0:0",
			"Env":        []string{"PATH=/usr/local/bin:/usr/bin", "OPENLOG_UPDATER_MODE=auto"},
			"Entrypoint": []string{"/usr/local/bin/openlog-updater"},
			"Labels": map[string]string{labelProject: "proj", labelService: "openlog-updater",
				"com.docker.compose.container-number": "1", labelImage: "sha256:img090"},
		},
		"HostConfig": map[string]any{
			"Binds":         []string{"/var/run/docker.sock:/var/run/docker.sock", "/srv/backups:/backups"},
			"NetworkMode":   "proj_default",
			"RestartPolicy": map[string]any{"Name": "unless-stopped", "MaximumRetryCount": 0},
		},
		"NetworkSettings": map[string]any{"Networks": map[string]any{
			"proj_default": map[string]any{"Aliases": []string{updaterName, "openlog-updater"}},
		}},
	}, &upd)
	f.docker.add(upd, true)
	if bundle {
		if err := os.WriteFile(filepath.Join(filepath.Dir(f.envFile), bundleVersionFile), []byte("0.9.0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(f.envFile, []byte("OPENLOG_LOG_LEVEL=info\nOPENLOG_IMAGE=openlog:dev\nOPENLOG_UPDATER_IMAGE="+img090+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.engine.Cfg.SelfUpdate = SelfUpdateAuto
	f.engine.SelfVersion, f.engine.Hostname = "0.9.0", f.updID
	f.engine.Exit = func(int) { f.exits.Add(1) }
	f.runner.SelfVersion, f.runner.SelfUpdate = "0.9.0", true
	return f
}

// process returns the engine and runner of the updater process running in container id.
func (f *selfUpdateFixture) process(id, ver string) (*ComposeEngine, *Runner) {
	eng := *f.engine
	eng.Hostname, eng.SelfVersion = id, ver
	r := &Runner{Cfg: f.runner.Cfg, Engine: &eng, Source: f.source, Store: f.store, Audit: f.audit, Log: f.runner.Log,
		SelfVersion: ver, SelfUpdate: true}
	return &eng, r
}

func (f *selfUpdateFixture) stop(id string) {
	f.docker.mu.Lock()
	defer f.docker.mu.Unlock()
	if c := f.docker.containers[id]; c != nil {
		c.running = false
	}
}

// runNewUpdaters starts the process of every new updater container the old one starts (version ver); like
// openlog-updater, the process exits (the container stops) when Handover says it must not act.
func (f *selfUpdateFixture) runNewUpdaters(ver string) {
	f.docker.onStart = func(id string) {
		c, err := f.docker.InspectContainer(context.Background(), id)
		if err != nil || !strings.HasSuffix(containerName(c), handoverNewSuffix) {
			return
		}
		eng, r := f.process(id, ver)
		go func() {
			ok, err := eng.Handover(context.Background(), r.selfUpdateHooks(context.Background()))
			if !ok {
				f.stop(id)
			}
			f.results <- handoverResult{ok, err}
		}()
	}
}

func (f *selfUpdateFixture) result(t *testing.T) handoverResult {
	t.Helper()
	select {
	case r := <-f.results:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("the new updater process did not finish")
	}
	return handoverResult{}
}

// update installs 0.9.1 and runs the self-update like Loop does.
func (f *selfUpdateFixture) update(t *testing.T) (*Target, bool) {
	t.Helper()
	if err := f.runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	target := f.runner.selfUpdateTarget
	if target == nil || target.Version.String() != "0.9.1" {
		t.Fatalf("no self-update pending after the update: %+v", target)
	}
	return target, f.runner.selfUpdate(context.Background())
}

func (f *selfUpdateFixture) envValue(t *testing.T, key string) string {
	t.Helper()
	b, err := os.ReadFile(f.envFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, key+"="); ok {
			return v
		}
	}
	return ""
}

func (f *selfUpdateFixture) handoverFilesGone(t *testing.T) {
	t.Helper()
	for _, n := range []string{handoverFile, handoverReadyFile} {
		if _, err := os.Stat(f.engine.handoverPath(n)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind (%v)", n, err)
		}
	}
}

func (f *selfUpdateFixture) noHandoverContainers(t *testing.T) {
	t.Helper()
	f.docker.mu.Lock()
	defer f.docker.mu.Unlock()
	for _, c := range f.docker.containers {
		if strings.Contains(c.c.Name, "-self-update-") {
			t.Errorf("leftover container %s", c.c.Name)
		}
	}
}

func lastStep(st Status, name string) *StepRecord {
	for i := len(st.Steps) - 1; i >= 0; i-- {
		if st.Steps[i].Name == name {
			return &st.Steps[i]
		}
	}
	return nil
}

func noticeByCode(st Status, code string) *Notice {
	for i := range st.Notices {
		if st.Notices[i].Code == code {
			return &st.Notices[i]
		}
	}
	return nil
}

func TestSelfUpdateHandover(t *testing.T) {
	f := newSelfUpdateFixture(t, true)
	f.runNewUpdaters("0.9.1")
	if _, handedOver := f.update(t); !handedOver {
		t.Fatalf("not handed over: %+v", f.store.st.SelfUpdate)
	}
	if r := f.result(t); !r.proceed || r.err != nil {
		t.Fatalf("new updater: proceed=%v err=%v", r.proceed, r.err)
	}

	cur := f.docker.byName(updaterName)
	switch {
	case cur == nil || cur.c.ID == f.updID || !cur.running:
		t.Fatalf("canonical updater container = %+v", cur)
	case cur.c.Config["Image"] != img091:
		t.Errorf("image %v", cur.c.Config["Image"])
	case restartPolicyName(cur.c.HostConfig["RestartPolicy"]) != "unless-stopped":
		t.Errorf("restart policy %v", cur.c.HostConfig["RestartPolicy"])
	case !slices.Equal(toStrings(cur.c.Config["Entrypoint"]), []string{"/usr/local/bin/openlog-updater"}) || cur.c.Config["User"] != "0:0":
		t.Errorf("entrypoint/user %v %v", cur.c.Config["Entrypoint"], cur.c.Config["User"])
	case !slices.Equal(toStrings(cur.c.HostConfig["Binds"]), []string{"/var/run/docker.sock:/var/run/docker.sock", "/srv/backups:/backups"}):
		t.Errorf("binds %v", cur.c.HostConfig["Binds"])
	}
	labels, _ := cur.c.Config["Labels"].(map[string]any)
	if labels[labelProject] != "proj" || labels[labelService] != "openlog-updater" || labels["com.docker.compose.container-number"] != "1" || labels[labelImage] != "sha256:img091" {
		t.Errorf("labels %v", labels)
	}
	if f.docker.containers[f.updID] != nil {
		t.Error("the previous updater container was not removed")
	}
	f.noHandoverContainers(t)
	f.handoverFilesGone(t)
	// Created without a restart policy: after a host restart before the commit only the old updater comes back.
	for _, spec := range f.docker.created {
		if spec.Name == updaterName+handoverNewSuffix && restartPolicyName(spec.HostConfig["RestartPolicy"]) != "no" {
			t.Errorf("new updater created with restart policy %v", spec.HostConfig["RestartPolicy"])
		}
	}
	if v := f.envValue(t, "OPENLOG_UPDATER_IMAGE"); v != img091 {
		t.Errorf(".env OPENLOG_UPDATER_IMAGE=%s", v)
	}

	st := f.store.st
	if st.State != StateSucceeded || st.UpdaterVersion != "0.9.1" {
		t.Errorf("state %s updater_version %s", st.State, st.UpdaterVersion)
	}
	if rec := st.SelfUpdate; rec == nil || rec.State != SelfUpdateSucceeded || rec.FromVersion != "0.9.0" || rec.TargetVersion != "0.9.1" || rec.FinishedAt == nil {
		t.Errorf("self_update %+v", rec)
	}
	if s := st.Steps[len(st.Steps)-1]; s.Name != updatemsg.StepSelfUpdate || s.Status != StepOK || !strings.Contains(s.Detail, "0.9.0 → 0.9.1") {
		t.Errorf("last step %+v", s)
	}
	for _, n := range st.Notices {
		if isUpdaterNotice(n.Code) {
			t.Errorf("notice left after the self-update: %+v", n)
		}
	}
	if !slices.Contains(f.audit.actions, "updater.self_update_started") || !slices.Contains(f.audit.actions, "updater.self_update_succeeded") {
		t.Errorf("audit %v", f.audit.actions)
	}
	if f.exits.Load() != 0 {
		t.Error("watchdog fired")
	}
}

// failedKeepsOld asserts that a failed self-update left the old updater running under its name, removed everything
// of the handover and recorded the failure once.
func (f *selfUpdateFixture) failedKeepsOld(t *testing.T, errSubstrings ...string) {
	t.Helper()
	cur := f.docker.byName(updaterName)
	if cur == nil || cur.c.ID != f.updID || !cur.running || restartPolicyName(cur.c.HostConfig["RestartPolicy"]) != "unless-stopped" {
		t.Fatalf("the old updater is not kept: %+v", cur)
	}
	f.noHandoverContainers(t)
	f.handoverFilesGone(t)
	if v := f.envValue(t, "OPENLOG_UPDATER_IMAGE"); v != img090 {
		t.Errorf(".env not restored: OPENLOG_UPDATER_IMAGE=%s", v)
	}
	st := f.store.st
	rec := st.SelfUpdate
	if rec == nil || rec.State != SelfUpdateFailed || rec.TargetVersion != "0.9.1" {
		t.Fatalf("self_update %+v", rec)
	}
	matched := len(errSubstrings) == 0
	for _, s := range errSubstrings {
		matched = matched || strings.Contains(rec.Error, s)
	}
	if !matched {
		t.Errorf("error %q, want one of %q", rec.Error, errSubstrings)
	}
	if s := lastStep(st, updatemsg.StepSelfUpdate); s == nil || s.Status != StepFailed {
		t.Errorf("self-update step %+v", s)
	}
	if n := noticeByCode(st, updatemsg.UpdaterSelfUpdateFailed); n == nil || n.Params["version"] != "0.9.1" || n.Params["reason"] != rec.Error {
		t.Errorf("notices %+v", st.Notices)
	}
	if st.State != StateSucceeded {
		t.Errorf("the update itself must stay succeeded: %s", st.State)
	}
	if !slices.Contains(f.audit.actions, "updater.self_update_failed") {
		t.Errorf("audit %v", f.audit.actions)
	}
}

func TestSelfUpdateFailures(t *testing.T) {
	ctx := context.Background()
	t.Run("new container does not start", func(t *testing.T) {
		f := newSelfUpdateFixture(t, true)
		f.docker.startErr = func(c *Container) error {
			if strings.HasSuffix(c.Name, handoverNewSuffix) {
				return &DockerError{Status: 500, Message: "OCI runtime create failed"}
			}
			return nil
		}
		target, handedOver := f.update(t)
		if handedOver {
			t.Fatal("handed over")
		}
		f.failedKeepsOld(t, "OCI runtime create failed")
		// Not retried for the same version.
		created := len(f.docker.created)
		f.runner.selfUpdateTarget = target
		if f.runner.selfUpdate(ctx) || len(f.docker.created) != created {
			t.Fatal("the self-update was retried for the same version")
		}
	})
	t.Run("new container exits", func(t *testing.T) {
		f := newSelfUpdateFixture(t, true)
		f.docker.onStart = func(id string) {
			if c, _ := f.docker.InspectContainer(ctx, id); c != nil && strings.HasSuffix(containerName(c), handoverNewSuffix) {
				f.stop(id)
			}
		}
		if _, handedOver := f.update(t); handedOver {
			t.Fatal("handed over")
		}
		f.failedKeepsOld(t, "exited with code")
	})
	t.Run("self-test fails", func(t *testing.T) {
		f := newSelfUpdateFixture(t, true)
		f.runNewUpdaters("0.9.3") // the new container reports another version than the handover expects
		if _, handedOver := f.update(t); handedOver {
			t.Fatal("handed over")
		}
		f.failedKeepsOld(t, "self-test", "exited with code")
		if r := f.result(t); r.proceed || r.err == nil || !strings.Contains(r.err.Error(), "expects 0.9.1") {
			t.Fatalf("new updater: proceed=%v err=%v", r.proceed, r.err)
		}
	})
	t.Run("no self-test result in time", func(t *testing.T) {
		f := newSelfUpdateFixture(t, true)
		f.engine.HandoverTimeout = time.Second // the new container never reports
		if _, handedOver := f.update(t); handedOver {
			t.Fatal("handed over")
		}
		f.failedKeepsOld(t, "within 1s")
	})
	t.Run("new updater cannot commit", func(t *testing.T) {
		f := newSelfUpdateFixture(t, true)
		f.docker.policyErr = errors.New("update not permitted")
		f.runNewUpdaters("0.9.1")
		// The old updater released its name; the new one gives it back and exits; the old one reclaims it.
		if _, handedOver := f.update(t); handedOver {
			t.Fatal("handed over")
		}
		if r := f.result(t); r.proceed || r.err == nil || !strings.Contains(r.err.Error(), "restart policy") {
			t.Fatalf("new updater: proceed=%v err=%v", r.proceed, r.err)
		}
		f.failedKeepsOld(t, "stopped")
	})
}

// stage recreates the state of a handover interrupted by a crash: marker, .env rewrite and the (stopped) new container.
func (f *selfUpdateFixture) stage(t *testing.T) *handoverMarker {
	t.Helper()
	ctx := context.Background()
	self, err := f.docker.InspectContainer(ctx, f.updID)
	if err != nil {
		t.Fatal(err)
	}
	target := &Target{Version: mustVersion(t, "0.9.1"), Image: img091}
	m := &handoverMarker{TargetVersion: "0.9.1", FromVersion: "0.9.0", Image: img091, Name: updaterName, OldID: f.updID,
		NewName: updaterName + handoverNewSuffix, RestartPolicy: map[string]any{"Name": "unless-stopped"}, CreatedAt: time.Now().UTC(), TimeoutSeconds: 120}
	spec, err := f.engine.selfSpec(ctx, self, target, m.NewName)
	if err != nil {
		t.Fatal(err)
	}
	m.EnvKey, m.EnvPrevious = f.engine.updaterImageEnv(img091)
	if m.EnvKey == "" {
		t.Fatal("OPENLOG_UPDATER_IMAGE not planned")
	}
	if err := rewriteEnvFile(f.envFile, m.EnvKey, img091); err != nil {
		t.Fatal(err)
	}
	if m.NewID, err = f.docker.CreateContainer(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.writeHandoverFile(handoverFile, m); err != nil {
		t.Fatal(err)
	}
	f.store.st = Status{State: StateSucceeded, CurrentVersion: "0.9.1", Steps: []StepRecord{{Name: updatemsg.StepSelfUpdate, Status: StepRunning}},
		SelfUpdate: &SelfUpdateRecord{TargetVersion: "0.9.1", FromVersion: "0.9.0", State: SelfUpdateRunning}}
	f.store.found = true
	return m
}

func (f *selfUpdateFixture) rename(t *testing.T, id, name string) {
	t.Helper()
	if err := f.docker.RenameContainer(context.Background(), id, name); err != nil {
		t.Fatal(err)
	}
}

func TestSelfUpdateCrashRecovery(t *testing.T) {
	ctx := context.Background()
	t.Run("old updater restarts before the release", func(t *testing.T) {
		f := newSelfUpdateFixture(t, true)
		f.stage(t)
		eng, r := f.process(f.updID, "0.9.0")
		if ok, err := eng.Handover(ctx, r.selfUpdateHooks(ctx)); !ok || err != nil {
			t.Fatalf("proceed=%v err=%v", ok, err)
		}
		f.failedKeepsOld(t, "interrupted")
	})
	t.Run("host restart after the release", func(t *testing.T) {
		f := newSelfUpdateFixture(t, true)
		f.stage(t)
		f.rename(t, f.updID, updaterName+handoverOldSuffix) // released; the new container (restart policy no) is not restarted
		eng, r := f.process(f.updID, "0.9.0")
		if ok, err := eng.Handover(ctx, r.selfUpdateHooks(ctx)); !ok || err != nil {
			t.Fatalf("proceed=%v err=%v", ok, err)
		}
		f.failedKeepsOld(t, "stopped")
	})
	t.Run("new updater crashed holding the name before the commit", func(t *testing.T) {
		f := newSelfUpdateFixture(t, true)
		m := f.stage(t)
		f.rename(t, f.updID, updaterName+handoverOldSuffix)
		f.rename(t, m.NewID, updaterName) // restart policy still "no", not running
		eng, r := f.process(f.updID, "0.9.0")
		if ok, err := eng.Handover(ctx, r.selfUpdateHooks(ctx)); !ok || err != nil {
			t.Fatalf("proceed=%v err=%v", ok, err)
		}
		f.failedKeepsOld(t, "stopped")
	})
	t.Run("host restart after the commit", func(t *testing.T) {
		f := newSelfUpdateFixture(t, true)
		m := f.stage(t)
		f.rename(t, f.updID, updaterName+handoverOldSuffix)
		f.rename(t, m.NewID, updaterName)
		if err := f.docker.UpdateRestartPolicy(ctx, m.NewID, m.RestartPolicy); err != nil {
			t.Fatal(err)
		}
		// Both containers have a restart policy: the old one comes back first and must only wait.
		oldEng, oldRunner := f.process(f.updID, "0.9.0")
		wait := make(chan handoverResult, 1)
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		go func() {
			ok, err := oldEng.Handover(wctx, oldRunner.selfUpdateHooks(wctx))
			wait <- handoverResult{ok, err}
		}()
		time.Sleep(100 * time.Millisecond)
		if f.docker.containers[m.NewID] == nil || f.docker.byName(updaterName).c.ID != m.NewID {
			t.Fatal("the old updater touched the committed new updater")
		}
		if err := f.docker.StartContainer(ctx, m.NewID); err != nil {
			t.Fatal(err)
		}
		newEng, newRunner := f.process(m.NewID, "0.9.1")
		if ok, err := newEng.Handover(ctx, newRunner.selfUpdateHooks(ctx)); !ok || err != nil {
			t.Fatalf("new updater: proceed=%v err=%v", ok, err)
		}
		if r := <-wait; r.proceed || r.err != nil {
			t.Fatalf("old updater: proceed=%v err=%v (must stop without acting)", r.proceed, r.err)
		}
		if cur := f.docker.byName(updaterName); cur == nil || cur.c.ID != m.NewID || f.docker.containers[f.updID] != nil {
			t.Fatalf("containers after the takeover: %+v", cur)
		}
		f.noHandoverContainers(t)
		f.handoverFilesGone(t)
		if rec := f.store.st.SelfUpdate; rec == nil || rec.State != SelfUpdateSucceeded {
			t.Fatalf("self_update %+v", rec)
		}
		if v := f.envValue(t, "OPENLOG_UPDATER_IMAGE"); v != img091 {
			t.Errorf(".env OPENLOG_UPDATER_IMAGE=%s", v)
		}
	})
	t.Run("updater recreated by docker compose during the handover", func(t *testing.T) {
		f := newSelfUpdateFixture(t, true)
		m := f.stage(t)
		f.rename(t, f.updID, updaterName+handoverOldSuffix)
		self, _ := f.docker.InspectContainer(ctx, f.updID)
		third, err := f.docker.CreateContainer(ctx, ContainerSpec{Name: updaterName, Config: self.Config, HostConfig: self.HostConfig})
		if err != nil {
			t.Fatal(err)
		}
		_ = f.docker.StartContainer(ctx, third)
		eng, r := f.process(third, "0.9.0")
		if ok, err := eng.Handover(ctx, r.selfUpdateHooks(ctx)); !ok || err != nil {
			t.Fatalf("proceed=%v err=%v", ok, err)
		}
		if f.docker.containers[f.updID] != nil || f.docker.containers[m.NewID] != nil || f.docker.byName(updaterName).c.ID != third {
			t.Fatal("handover containers not removed")
		}
		f.handoverFilesGone(t)
		if rec := f.store.st.SelfUpdate; rec == nil || rec.State != SelfUpdateFailed || !strings.Contains(rec.Error, "recreated") {
			t.Fatalf("self_update %+v", rec)
		}
	})
	t.Run("no marker", func(t *testing.T) {
		f := newSelfUpdateFixture(t, true)
		if ok, err := f.engine.Handover(ctx, f.runner.selfUpdateHooks(ctx)); !ok || err != nil || f.store.saves != 0 {
			t.Fatalf("proceed=%v err=%v saves=%d", ok, err, f.store.saves)
		}
	})
}

func TestSelfUpdateSkips(t *testing.T) {
	ctx := context.Background()
	target := &Target{Version: mustVersion(t, "0.9.1"), Image: img091}
	for _, tc := range []struct {
		name   string
		setup  func(f *selfUpdateFixture, st *Status)
		reason string
	}{
		{"off", func(f *selfUpdateFixture, _ *Status) { f.engine.Cfg.SelfUpdate = SelfUpdateOff }, "OPENLOG_UPDATER_SELF_UPDATE=off"},
		{"auto without install-server.sh", func(f *selfUpdateFixture, _ *Status) {
			_ = os.Remove(filepath.Join(filepath.Dir(f.envFile), bundleVersionFile))
		}, "not an install-server.sh installation"},
		{"development build", func(f *selfUpdateFixture, _ *Status) { f.engine.SelfVersion = "0.0.0-dev" }, "development build"},
		{"not older", func(f *selfUpdateFixture, _ *Status) { f.engine.SelfVersion = "0.9.1" }, ""},
		{"already attempted", func(_ *selfUpdateFixture, st *Status) {
			st.SelfUpdate = &SelfUpdateRecord{TargetVersion: "0.9.1", State: SelfUpdateFailed}
		}, "already attempted"},
		{"unfinished handover", func(f *selfUpdateFixture, _ *Status) {
			_ = os.MkdirAll(f.engine.Cfg.BackupDir, 0o750)
			_ = os.WriteFile(f.engine.handoverPath(handoverFile), []byte("{}"), 0o640)
		}, "not finished"},
		{"image not pulled", func(f *selfUpdateFixture, _ *Status) { delete(f.docker.images, img091) }, "is not available"},
		{"own container unknown", func(f *selfUpdateFixture, _ *Status) { f.engine.Hostname = "not-a-container" }, "cannot identify"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSelfUpdateFixture(t, true)
			var st Status
			tc.setup(f, &st)
			c, reason := f.engine.selfUpdatePlan(ctx, target, st)
			if c != nil || !strings.Contains(reason, tc.reason) || (tc.reason == "" && reason != "") {
				t.Fatalf("plan = %v %q, want reason %q", c != nil, reason, tc.reason)
			}
			created := len(f.docker.created)
			if f.engine.SelfUpdate(ctx, target, st, f.runner.selfUpdateHooks(ctx)) || len(f.docker.created) != created || f.store.saves != 0 {
				t.Fatalf("skipped self-update changed something: created %d→%d saves %d", created, len(f.docker.created), f.store.saves)
			}
		})
	}
	t.Run("on without install-server.sh", func(t *testing.T) {
		f := newSelfUpdateFixture(t, false)
		f.engine.Cfg.SelfUpdate = SelfUpdateOn
		if c, reason := f.engine.selfUpdatePlan(ctx, target, Status{}); c == nil || c.ID != f.updID {
			t.Fatalf("plan = %v %q", c, reason)
		}
	})
	t.Run("never after a failed or rolled back update", func(t *testing.T) {
		f := newSelfUpdateFixture(t, true)
		f.docker.migrateExit = 1
		if err := f.runner.RunOnce(ctx); err == nil || f.runner.selfUpdateTarget != nil {
			t.Fatalf("failed update: err=%v pending=%v", err, f.runner.selfUpdateTarget)
		}
		g := newSelfUpdateFixture(t, true)
		g.source.manifests["0.9.1"] = manifestJSON("0.9.1", "stable", "0.9.0", img092) // never becomes ready
		if err := g.runner.RunOnce(ctx); err == nil || g.store.st.State != StateRolledBack || g.runner.selfUpdateTarget != nil {
			t.Fatalf("rolled back update: err=%v state=%s pending=%v", err, g.store.st.State, g.runner.selfUpdateTarget)
		}
		if g.runner.selfUpdate(ctx) || len(g.docker.created) == 0 {
			t.Fatal("unexpected self-update")
		}
	})
	t.Run("OPENLOG_UPDATER_IMAGE not pinned", func(t *testing.T) {
		f := newSelfUpdateFixture(t, true)
		for _, env := range []string{"OPENLOG_IMAGE=x\n", "OPENLOG_UPDATER_IMAGE=\n", "OPENLOG_UPDATER_IMAGE=${OPENLOG_IMAGE}\n", "OPENLOG_UPDATER_IMAGE=" + img091 + "\n"} {
			_ = os.WriteFile(f.envFile, []byte(env), 0o600)
			if k, _ := f.engine.updaterImageEnv(img091); k != "" {
				t.Errorf("%q: rewrites %s", env, k)
			}
		}
	})
}

func TestUpdaterNotices(t *testing.T) {
	f := newComposeFixture(t)
	r := f.runner
	r.SelfVersion = "0.9.0"
	want := func(st *Status, running, code string, params map[string]string) {
		t.Helper()
		n, ok := r.updaterNotice(st, running)
		if code == "" {
			if ok {
				t.Fatalf("running %s: unexpected notice %+v", running, n)
			}
			return
		}
		if !ok || n.Code != code || n.Message != updatemsg.Format(code, params) {
			t.Fatalf("running %s: notice %+v, want %s %v", running, n, code, params)
		}
		for k, v := range params {
			if n.Params[k] != v {
				t.Fatalf("params %v, want %v", n.Params, params)
			}
		}
	}
	outdated := map[string]string{"updater_version": "0.9.0", "running_version": "0.9.1"}
	want(&Status{}, "0.9.1", updatemsg.UpdaterOutdated, outdated)
	want(&Status{}, "0.9.0", "", nil)
	want(&Status{}, "0.0.0-dev+abc", "", nil)
	if err := os.WriteFile(filepath.Join(filepath.Dir(f.envFile), bundleVersionFile), []byte("0.9.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	want(&Status{}, "0.9.1", updatemsg.UpdaterOutdatedBundle, outdated)
	failed := &Status{SelfUpdate: &SelfUpdateRecord{TargetVersion: "0.9.1", State: SelfUpdateFailed, Error: "self-test: docker: 403"}}
	want(failed, "0.9.1", updatemsg.UpdaterSelfUpdateFailed, map[string]string{"version": "0.9.1", "reason": "self-test: docker: 403"})
	k8s := &Runner{Engine: &K8sEngine{}, SelfVersion: "0.9.0"}
	if n, ok := k8s.updaterNotice(&Status{}, "0.9.1"); !ok || n.Code != updatemsg.UpdaterOutdatedKubernetes {
		t.Fatalf("kubernetes notice %+v", n)
	}
	r.SelfVersion = "0.9.1"
	want(failed, "0.9.1", "", nil) // fixed by hand: no notice any more
	r.SelfVersion = "0.0.0-dev"
	want(&Status{}, "0.9.1", "", nil)

	// After an update without self-update the status carries the updater's version and the notice.
	g := newComposeFixture(t)
	g.runner.SelfVersion = "0.9.0"
	if err := g.runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := g.store.st
	if st.UpdaterVersion != "0.9.0" || noticeByCode(st, updatemsg.UpdaterOutdated) == nil || g.runner.selfUpdateTarget != nil {
		t.Fatalf("updater_version %q notices %+v pending %v", st.UpdaterVersion, st.Notices, g.runner.selfUpdateTarget)
	}
}
