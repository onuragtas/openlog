package update

import (
	"archive/tar"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestApplyStagedUpgrade(t *testing.T) {
	f := newApplyFixture(t)
	f.stage("0.9.1", map[string]string{"rollback_floor": "0.9.0"}, nil)

	st := f.apply()
	if st == nil || st.Result != ApplySwitched || st.FromVersion != "0.9.0" || st.ToVersion != "0.9.1" || st.Error != "" {
		t.Fatalf("status %+v", st)
	}
	if f.current() != "0.9.1" {
		t.Fatalf("current %s", f.current())
	}
	if c := st.Candidate; c == nil || c.Version != "0.9.1" || c.Previous != "0.9.0" || c.Attempts != 1 || !c.SwitchedAt.Equal(f.now) {
		t.Fatalf("candidate %+v", st.Candidate)
	}
	if !slices.Equal(f.reconciles, []string{"0.9.1"}) {
		t.Errorf("reconcile %v, want the new version's", f.reconciles)
	}
	if len(f.selfTests) != 1 || !strings.Contains(f.selfTests[0], ".0.9.1.extract") {
		t.Errorf("self-tests %v", f.selfTests)
	}
	dir := filepath.Join(f.root, "versions", "0.9.1")
	for _, file := range []string{BinaryName, ManifestFile, SignatureFile, "LICENSE"} {
		if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
			t.Errorf("installed %s: %v", file, err)
		}
	}
	if !f.sys.trustedTree(dir) {
		t.Error("installed version is not a trusted tree")
	}
	if !slices.Equal(f.versions(), []string{"0.9.0", "0.9.1"}) {
		t.Errorf("versions %v", f.versions())
	}
	var onDisk ApplyStatus
	if err := f.sys.loadTrustedJSON(filepath.Join(f.root, ApplyStatusFile), &onDisk); err != nil || onDisk.InvocationID != "invocation-1" || onDisk.Current != "0.9.1" {
		t.Fatalf("status file %+v %v", onDisk, err)
	}

	// The next start (e.g. a crash before the agent took over the result) does not process the staged update again.
	f.now = f.now.Add(10 * time.Second)
	st = f.apply()
	if st.Result != "" || f.current() != "0.9.1" || st.Candidate == nil || st.Candidate.Attempts != 2 {
		t.Fatalf("second start: %+v", st)
	}
	if !slices.Equal(f.reconciles, []string{"0.9.1", ""}) || len(f.selfTests) != 1 {
		t.Errorf("reconciles %v self-tests %d", f.reconciles, len(f.selfTests))
	}

	// Confirmation by the agent: the candidate is settled and old versions are pruned.
	installVersion(t, f.root, "0.8.0", "exit 0", nil)
	f.saveAgent(State{Candidate: "0.9.1", Confirmed: true, ConfirmedVersion: "0.9.1", ConfirmedAt: f.now})
	st = f.apply()
	if st.Candidate != nil || st.Result != "" {
		t.Fatalf("after confirmation: %+v", st)
	}
	if !slices.Equal(f.versions(), []string{"0.9.0", "0.9.1"}) {
		t.Errorf("not pruned to current+previous: %v", f.versions())
	}
}

func TestApplyRejectsHostileStaging(t *testing.T) {
	outside := t.TempDir()
	cases := []struct {
		name   string
		mutate func(f *applyFixture, sf *stageFiles)
		setup  func(f *applyFixture)
		want   string
	}{
		{name: "bad signature", want: "signature", mutate: func(f *applyFixture, sf *stageFiles) {
			sf.sig = sign(sf.manifest, newKey(f.t))
		}},
		{name: "manifest changed after signing", want: "signature", mutate: func(f *applyFixture, sf *stageFiles) {
			sf.manifest = []byte(strings.Replace(string(sf.manifest), "example.com", "evil.example", 1))
		}},
		{name: "older version", want: "not newer", mutate: func(f *applyFixture, sf *stageFiles) {
			a := agentTarball(f.t, "0.8.5", "exit 0")
			sf.dir, sf.archive = "0.8.5", a
			sf.manifest = manifestFor(f.t, "0.8.5", "https://example.com", a, nil, nil)
			sf.sig = sign(sf.manifest, f.key)
			sf.state.Staged = "0.8.5"
		}},
		{name: "rollback below floor", want: "rollback_floor", mutate: func(f *applyFixture, sf *stageFiles) {
			a := agentTarball(f.t, "0.7.0", "exit 0")
			sf.dir, sf.archive = "0.7.0", a
			sf.manifest = manifestFor(f.t, "0.7.0", "https://example.com", a, nil, nil)
			sf.sig = sign(sf.manifest, f.key)
			sf.state.Staged, sf.state.Action = "0.7.0", ActionRollback
		}},
		{name: "wrong architecture", want: "no infra-agent linux/amd64", mutate: func(f *applyFixture, sf *stageFiles) {
			sf.manifest = manifestFor(f.t, "0.9.1", "https://example.com", sf.archive, nil, func(m map[string]any) {
				m["artifacts"].([]any)[0].(map[string]any)["arch"] = "arm64"
			})
			sf.sig = sign(sf.manifest, f.key)
		}},
		{name: "archive replaced", want: "sha256 mismatch", mutate: func(f *applyFixture, sf *stageFiles) {
			b := append([]byte{}, sf.archive...)
			b[len(b)/2] ^= 0xff
			sf.archive = b
		}},
		{name: "archive size differs", want: "size mismatch", mutate: func(f *applyFixture, sf *stageFiles) {
			sf.archive = append(append([]byte{}, sf.archive...), 0)
		}},
		{name: "path traversal in the signed archive", want: "contains ..", mutate: func(f *applyFixture, sf *stageFiles) {
			top := TopDir("0.9.1", "linux", "amd64")
			sf.archive = buildTarGz(f.t, []tarEntry{
				{name: top + "/", typ: tar.TypeDir, mode: 0o755},
				{name: top + "/" + BinaryName, typ: tar.TypeReg, mode: 0o755, body: "#!/bin/sh\n"},
				{name: top + "/../../evil", typ: tar.TypeReg, body: "x"},
			})
			sf.manifest = manifestFor(f.t, "0.9.1", "https://example.com", sf.archive, nil, nil)
			sf.sig = sign(sf.manifest, f.key)
		}},
		{name: "symlink in the signed archive", want: "symlink", mutate: func(f *applyFixture, sf *stageFiles) {
			top := TopDir("0.9.1", "linux", "amd64")
			sf.archive = buildTarGz(f.t, []tarEntry{
				{name: top + "/", typ: tar.TypeDir, mode: 0o755},
				{name: top + "/" + BinaryName, typ: tar.TypeSymlink, linkname: "/bin/sh"},
			})
			sf.manifest = manifestFor(f.t, "0.9.1", "https://example.com", sf.archive, nil, nil)
			sf.sig = sign(sf.manifest, f.key)
		}},
		{name: "archive symlink out of the state dir", want: "staged archive", mutate: func(f *applyFixture, sf *stageFiles) {
			target := filepath.Join(outside, "archive.tar.gz")
			os.WriteFile(target, sf.archive, 0o644)
			sf.archive = nil
			f.t.Cleanup(func() {})
			sf.state.Candidate = "0.9.1"
		}, setup: func(f *applyFixture) {
			os.Symlink(filepath.Join(outside, "archive.tar.gz"), filepath.Join(f.stateDir, UpdatesDir, "0.9.1", ArchiveFile))
		}},
		{name: "archive is a FIFO", want: "not a regular file", mutate: func(f *applyFixture, sf *stageFiles) {
			sf.archive = nil
		}, setup: func(f *applyFixture) {
			if err := syscall.Mkfifo(filepath.Join(f.stateDir, UpdatesDir, "0.9.1", ArchiveFile), 0o600); err != nil {
				f.t.Skip(err)
			}
		}},
		{name: "traversal in the staged version", want: "invalid staged version", mutate: func(f *applyFixture, sf *stageFiles) {
			sf.state.Staged = "../../../../etc"
		}},
		{name: "unknown action", want: "unknown staged update action", mutate: func(f *applyFixture, sf *stageFiles) {
			sf.state.Action = "exec"
		}},
		{name: "self-test fails", want: "cannot start", setup: func(f *applyFixture) {
			f.selfTestFn = func(string) error { return ruleErr(7, "self-test: cannot start") }
		}},
		{name: "updates disabled", want: "disabled", setup: func(f *applyFixture) { f.enabled = false }},
		{name: "no trusted keys", want: ErrNoTrustedKeys, setup: func(f *applyFixture) { f.noKeys = true }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newApplyFixture(t)
			var mutate func(*stageFiles)
			if c.mutate != nil {
				mutate = func(sf *stageFiles) { c.mutate(f, sf) }
			}
			sf := f.stage("0.9.1", nil, mutate)
			if c.setup != nil {
				c.setup(f)
			}
			st := f.apply()
			if st.Result != ApplyRejected || !strings.Contains(st.Error, c.want) {
				t.Fatalf("status %+v, want rejected with %q", st, c.want)
			}
			if st.ToVersion != truncate(sf.state.Staged, 64) {
				t.Errorf("to_version %q", st.ToVersion)
			}
			if f.current() != "0.9.0" || st.Candidate != nil {
				t.Fatalf("switched: current %s candidate %+v", f.current(), st.Candidate)
			}
			if !slices.Equal(f.versions(), []string{"0.9.0"}) {
				t.Errorf("leftovers in versions/: %v", f.versions())
			}
			if !slices.Equal(f.reconciles, []string{""}) {
				t.Errorf("reconcile %v, want in-process", f.reconciles)
			}
			if _, err := os.Stat(filepath.Join(f.root, "..", "evil")); err == nil {
				t.Error("traversal wrote outside the install root")
			}
			// Rejected once: the same staged update is not retried on the next start.
			if st := f.apply(); st.Result != "" {
				t.Errorf("retried: %+v", st)
			}
		})
	}
}

func TestApplyRollbackAfterFailedStarts(t *testing.T) {
	f := newApplyFixture(t)
	f.stage("0.9.1", nil, nil)
	if st := f.apply(); st.Result != ApplySwitched {
		t.Fatalf("switch: %+v", st)
	}
	for attempt := 2; attempt <= MaxStartAttempts; attempt++ {
		f.now = f.now.Add(5 * time.Second)
		if st := f.apply(); st.Result != "" || st.Candidate.Attempts != attempt || f.current() != "0.9.1" {
			t.Fatalf("attempt %d: %+v", attempt, st)
		}
	}
	f.now = f.now.Add(5 * time.Second)
	st := f.apply()
	if st.Result != ApplyRolledBack || st.FromVersion != "0.9.1" || st.ToVersion != "0.9.0" || !strings.Contains(st.Error, "start attempt 4") {
		t.Fatalf("status %+v", st)
	}
	if f.current() != "0.9.0" || st.Candidate != nil {
		t.Fatalf("current %s candidate %+v", f.current(), st.Candidate)
	}
	if last := f.reconciles[len(f.reconciles)-1]; last != "0.9.0" {
		t.Errorf("reconcile after rollback ran %q, want the previous version's", last)
	}
}

func TestApplyRollbackOnConfirmWindowAndRequest(t *testing.T) {
	t.Run("confirm window", func(t *testing.T) {
		f := newApplyFixture(t)
		f.stage("0.9.1", nil, nil)
		f.apply()
		f.now = f.now.Add(ConfirmWindow + time.Second)
		if st := f.apply(); st.Result != ApplyRolledBack || f.current() != "0.9.0" {
			t.Fatalf("status %+v current %s", st, f.current())
		}
	})
	t.Run("rollback requested by the candidate", func(t *testing.T) {
		f := newApplyFixture(t)
		f.stage("0.9.1", nil, nil)
		f.apply()
		f.saveAgent(State{Candidate: "0.9.1", RollbackRequest: "0.9.1", RollbackReason: "no export\nwithin 5m"})
		st := f.apply()
		if st.Result != ApplyRolledBack || f.current() != "0.9.0" || !strings.Contains(st.Error, "no export within 5m") {
			t.Fatalf("status %+v current %s", st, f.current())
		}
	})
	t.Run("rollback request for another version is ignored", func(t *testing.T) {
		f := newApplyFixture(t)
		f.stage("0.9.1", nil, nil)
		f.apply()
		f.saveAgent(State{Candidate: "0.9.1", RollbackRequest: "0.9.0"})
		if st := f.apply(); st.Result != "" || f.current() != "0.9.1" {
			t.Fatalf("status %+v current %s", st, f.current())
		}
	})
	t.Run("restart for a new unit is not a failed start", func(t *testing.T) {
		f := newApplyFixture(t)
		f.stage("0.9.1", nil, nil)
		first := f.apply()
		saveStatus(filepath.Join(f.root, ReconcileStatusFile), ReconcileStatus{InvocationID: first.InvocationID, RestartRequired: true, Context: ReconcileApply})
		if st := f.apply(); st.Candidate == nil || st.Candidate.Attempts != 1 {
			t.Fatalf("attempt counted: %+v", st.Candidate)
		}
	})
	t.Run("stale confirmation does not settle a new switch", func(t *testing.T) {
		f := newApplyFixture(t)
		f.stage("0.9.1", nil, nil)
		f.apply()
		f.saveAgent(State{ConfirmedVersion: "0.9.1", ConfirmedAt: f.now.Add(-time.Hour)})
		if st := f.apply(); st.Candidate == nil {
			t.Fatal("confirmation from before the switch accepted")
		}
	})
}

func TestApplyStateTampering(t *testing.T) {
	t.Run("forged apply status is ignored", func(t *testing.T) {
		f := newApplyFixture(t)
		installVersion(t, f.root, "0.1.0", "exit 0", nil)
		p := filepath.Join(f.root, ApplyStatusFile)
		saveStatus(p, ApplyStatus{Candidate: &RootCandidate{Version: "0.9.0", Previous: "0.1.0", Attempts: 99}})
		os.Chmod(p, 0o666) // writable by others: not written by root
		f.saveAgent(State{})
		if st := f.apply(); st.Candidate != nil || st.Result != "" || f.current() != "0.9.0" {
			t.Fatalf("forged candidate used: %+v current %s", st, f.current())
		}
	})
	for name, prev := range map[string]string{
		"previous outside versions": "../../../tmp",
		"previous not recorded":     "",
		"previous is untrusted":     "0.8.0",
		"previous has no binary":    "0.7.0",
	} {
		t.Run(name, func(t *testing.T) {
			f := newApplyFixture(t)
			installVersion(t, f.root, "0.8.0", "exit 0", nil)
			os.Chmod(filepath.Join(f.root, "versions", "0.8.0", BinaryName), 0o777)
			installVersion(t, f.root, "0.9.1", "exit 0", nil)
			SwitchCurrent(f.root, "0.9.1")
			saveStatus(filepath.Join(f.root, ApplyStatusFile), ApplyStatus{
				Candidate: &RootCandidate{Version: "0.9.1", Previous: prev, SwitchedAt: f.now, Attempts: MaxStartAttempts},
			})
			st := f.apply()
			if st.Result != ApplyRollbackFailed || f.current() != "0.9.1" || !strings.Contains(st.Error, "rollback impossible") {
				t.Fatalf("status %+v current %s", st, f.current())
			}
		})
	}
	t.Run("corrupt agent state", func(t *testing.T) {
		f := newApplyFixture(t)
		os.WriteFile(filepath.Join(f.stateDir, StateFile), []byte("{"), 0o600)
		if st := f.apply(); st == nil || st.Result != "" || f.current() != "0.9.0" {
			t.Fatalf("status %+v", st)
		}
	})
	t.Run("agent state is a FIFO", func(t *testing.T) {
		f := newApplyFixture(t)
		if err := syscall.Mkfifo(filepath.Join(f.stateDir, StateFile), 0o600); err != nil {
			t.Skip(err)
		}
		done := make(chan *ApplyStatus)
		go func() { done <- f.apply() }()
		select {
		case st := <-done:
			if st == nil || st.Result != "" {
				t.Fatalf("status %+v", st)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("apply blocked on a FIFO")
		}
	})
	t.Run("huge agent state", func(t *testing.T) {
		f := newApplyFixture(t)
		os.WriteFile(filepath.Join(f.stateDir, StateFile), make([]byte, maxStatusBytes+1), 0o600)
		if st := f.apply(); st == nil || st.Result != "" {
			t.Fatalf("status %+v", st)
		}
	})
}

func TestApplyMigratesLegacyLayout(t *testing.T) {
	f := newApplyFixture(t)
	installVersion(t, f.root, "0.8.0", "exit 0", nil)
	// Legacy: versions writable by the agent user (here: by everyone).
	for _, p := range []string{f.root, filepath.Join(f.root, "versions"), filepath.Join(f.root, "versions", "0.9.0"), filepath.Join(f.root, "versions", "0.8.0")} {
		os.Chmod(p, 0o777)
	}
	os.Chmod(filepath.Join(f.root, "versions", "0.9.0", BinaryName), 0o777)
	os.Symlink("/etc/passwd", filepath.Join(f.root, "versions", "0.9.0", "link"))
	os.WriteFile(filepath.Join(f.root, "versions", ".0.9.5.partial"), []byte("x"), 0o666)
	os.WriteFile(filepath.Join(f.root, ReconcileStatusFile), []byte(`{"restart_required":true}`), 0o666)
	os.Chmod(filepath.Join(f.root, ReconcileStatusFile), 0o666)
	before, _ := os.ReadFile(filepath.Join(f.root, "versions", "0.9.0", BinaryName))

	st := f.apply()
	if st == nil || len(st.Notes) != 2 {
		t.Fatalf("status %+v", st)
	}
	if !slices.Equal(f.versions(), []string{"0.9.0"}) || f.current() != "0.9.0" {
		t.Fatalf("versions %v current %s", f.versions(), f.current())
	}
	for _, p := range []string{f.root, filepath.Join(f.root, "versions")} {
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o755 {
			t.Errorf("%s mode %v", p, fi.Mode())
		}
	}
	dir := filepath.Join(f.root, "versions", "0.9.0")
	if !f.sys.trustedTree(dir) {
		t.Error("current version not migrated to a trusted tree")
	}
	if after, _ := os.ReadFile(filepath.Join(dir, BinaryName)); string(after) != string(before) {
		t.Error("binary content changed by the migration")
	}
	if _, err := os.Lstat(filepath.Join(dir, "link")); err == nil {
		t.Error("symlink copied")
	}
	if fi, _ := os.Stat(filepath.Join(dir, BinaryName)); fi.Mode().Perm() != 0o755 {
		t.Errorf("binary mode %v", fi.Mode())
	}
	if _, err := os.Stat(filepath.Join(f.root, ReconcileStatusFile)); err == nil {
		t.Error("status file writable by others kept")
	}
	// Idempotent.
	if st := f.apply(); len(st.Notes) != 0 {
		t.Errorf("second run notes %v", st.Notes)
	}
}

func TestApplyNoOp(t *testing.T) {
	f := newApplyFixture(t)
	if st := Apply(t.Context(), ApplyOptions{Sys: f.sys.Sys, Install: Install{Method: MethodDev, Reason: "not under"}}); st != nil {
		t.Errorf("dev install: %+v", st)
	}
	f.sys.IsRoot = func() bool { return false }
	if st := f.apply(); st != nil {
		t.Errorf("not root: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(f.root, ApplyStatusFile)); err == nil {
		t.Error("status written without root")
	}
}

func TestTrustedFile(t *testing.T) {
	sys := newFakeSys(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	os.WriteFile(p, []byte("x"), 0o640)
	os.Chmod(p, 0o640)
	if err := sys.TrustedFile(p); err != nil {
		t.Fatalf("trusted file refused: %v", err)
	}
	os.Symlink(p, filepath.Join(dir, "link.yaml"))
	if err := sys.TrustedFile(filepath.Join(dir, "link.yaml")); err != nil {
		t.Errorf("symlink to a trusted file refused: %v", err)
	}
	os.Chmod(p, 0o660)
	if err := sys.TrustedFile(p); err == nil {
		t.Error("group-writable file trusted")
	}
	os.Chmod(p, 0o640)
	os.Chmod(dir, 0o777)
	defer os.Chmod(dir, 0o755)
	if err := sys.TrustedFile(p); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Errorf("file in a world-writable directory trusted: %v", err)
	}
	sys.RootUID = os.Getuid() + 1
	os.Chmod(dir, 0o755)
	if err := sys.TrustedFile(p); err == nil {
		t.Error("file of another user trusted")
	}
}
