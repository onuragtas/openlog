package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type reconcileFixture struct {
	t        *testing.T
	sys      *fakeSys
	root     string
	stateDir string
	config   string
	method   string
	unit     string
	ctx      string
	inv      string
}

func newReconcileFixture(t *testing.T) *reconcileFixture {
	f := &reconcileFixture{t: t, sys: newFakeSys(t), root: t.TempDir(), method: MethodTarball, unit: "[Service]\nExecStart=/x\n", ctx: ReconcileApply, inv: "inv-1"}
	f.stateDir = filepath.Join(t.TempDir(), "state")
	f.config = filepath.Join(t.TempDir(), "etc", "config.yaml")
	os.MkdirAll(filepath.Dir(f.config), 0o755)
	os.WriteFile(f.config, []byte("license_key: k\n"), 0o644)
	installVersion(t, f.root, "0.9.1", "exit 0", nil)
	SwitchCurrent(f.root, "0.9.1")
	return f
}

func (f *reconcileFixture) run() (*ReconcileStatus, error) {
	f.t.Helper()
	return Reconcile(f.t.Context(), ReconcileOptions{
		Sys: f.sys.Sys, Install: Install{Method: f.method, InstallRoot: f.root, VersionDir: "0.9.1"},
		StateDir: f.stateDir, ConfigPath: f.config, Version: "0.9.1", Unit: []byte(f.unit),
		Context: f.ctx, InvocationID: f.inv, Now: func() time.Time { return time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC) },
	})
}

func TestReconcileTarball(t *testing.T) {
	f := newReconcileFixture(t)
	f.sys.write("/etc/systemd/system/"+UnitName, "[Service]\nExecStart=/old\n")

	st, err := f.run()
	if err != nil {
		t.Fatal(err)
	}
	if f.sys.read("/etc/systemd/system/"+UnitName) != f.unit || !st.UnitChanged || st.UnitPath != "/etc/systemd/system/"+UnitName {
		t.Fatalf("unit not installed: %+v", st)
	}
	if !f.sys.ran("systemctl daemon-reload") || !st.RestartRequired {
		t.Errorf("no daemon-reload / restart: runs %v status %+v", f.sys.runs, st)
	}
	if st.Docker != DockerAdded || !strings.Contains(f.sys.read("/etc/group"), "docker:x:999:alice,"+AgentUser) {
		t.Errorf("docker %q group %q", st.Docker, f.sys.read("/etc/group"))
	}
	if fi, _ := os.Stat(f.stateDir); fi == nil || fi.Mode().Perm() != 0o750 {
		t.Errorf("state dir %v", fi)
	}
	if fi, _ := os.Stat(f.config); fi.Mode().Perm() != 0o640 {
		t.Errorf("config mode %v", fi.Mode())
	}
	if b, _ := os.ReadFile(f.config); string(b) != "license_key: k\n" {
		t.Error("config rewritten")
	}
	if link, _ := os.Readlink(f.sys.path("/usr/bin/" + BinaryName)); link != filepath.Join(f.root, "current", BinaryName) {
		t.Errorf("bin link %q", link)
	}
	var onDisk ReconcileStatus
	if err := f.sys.loadTrustedJSON(filepath.Join(f.root, ReconcileStatusFile), &onDisk); err != nil || onDisk.InvocationID != "inv-1" || !onDisk.UnitChanged {
		t.Fatalf("status file %+v %v", onDisk, err)
	}
	if r := onDisk.Report(); r.Version != "0.9.1" || !r.UnitChanged || r.Docker != DockerAdded || r.Error != "" {
		t.Errorf("report %+v", r)
	}

	// Idempotent: nothing changes, no reload, no restart.
	f.sys.runs = nil
	f.inv = "inv-2"
	st, err = f.run()
	if err != nil || st.UnitChanged || st.RestartRequired || st.Docker != DockerMember || len(f.sys.runs) != 0 {
		t.Fatalf("second run: %+v %v runs %v", st, err, f.sys.runs)
	}
}

func TestReconcilePackageUnitPath(t *testing.T) {
	t.Run("deb manages the packaged unit", func(t *testing.T) {
		f := newReconcileFixture(t)
		f.method = MethodDeb
		f.sys.write("/usr/lib/systemd/system/"+UnitName, "old")
		st, err := f.run()
		if err != nil || f.sys.read("/usr/lib/systemd/system/"+UnitName) != f.unit || !st.UnitChanged {
			t.Fatalf("%+v %v", st, err)
		}
		if _, err := os.Stat(f.sys.path("/etc/systemd/system/" + UnitName)); err == nil {
			t.Error("unit written to /etc for a package install")
		}
	})
	t.Run("full override in /etc is left alone", func(t *testing.T) {
		f := newReconcileFixture(t)
		f.method = MethodRPM
		f.sys.write("/usr/lib/systemd/system/"+UnitName, "old")
		f.sys.write("/etc/systemd/system/"+UnitName, "admin")
		st, err := f.run()
		if err != nil || st.UnitChanged || f.sys.read("/usr/lib/systemd/system/"+UnitName) != "old" || f.sys.read("/etc/systemd/system/"+UnitName) != "admin" {
			t.Fatalf("%+v %v", st, err)
		}
		if !strings.Contains(strings.Join(st.Notes, " "), "overrides") {
			t.Errorf("notes %v", st.Notes)
		}
	})
	t.Run("no systemd", func(t *testing.T) {
		f := newReconcileFixture(t)
		os.RemoveAll(f.sys.path("/etc/systemd"))
		os.RemoveAll(f.sys.path("/run/systemd"))
		st, err := f.run()
		if err != nil || st.UnitChanged || f.sys.ran("systemctl") {
			t.Fatalf("%+v %v", st, err)
		}
	})
	t.Run("package context: docker group needs a restart", func(t *testing.T) {
		f := newReconcileFixture(t)
		f.ctx = ReconcilePackage
		os.RemoveAll(f.sys.path("/etc/systemd"))
		st, err := f.run()
		if err != nil || st.Docker != DockerAdded || !st.RestartRequired {
			t.Fatalf("%+v %v", st, err)
		}
	})
	t.Run("apply context: new groups apply to the agent about to start", func(t *testing.T) {
		f := newReconcileFixture(t)
		os.RemoveAll(f.sys.path("/etc/systemd"))
		st, err := f.run()
		if err != nil || st.Docker != DockerAdded || st.RestartRequired {
			t.Fatalf("%+v %v", st, err)
		}
	})
}

func TestReconcileRestartLoopGuard(t *testing.T) {
	f := newReconcileFixture(t)
	f.sys.write("/etc/systemd/system/"+UnitName, "old")
	if st, _ := f.run(); !st.RestartRequired {
		t.Fatal("first change did not require a restart")
	}
	// Something rewrites the old unit before the next start.
	f.sys.write("/etc/systemd/system/"+UnitName, "old")
	f.inv = "inv-2"
	st, _ := f.run()
	if !st.UnitChanged || st.RestartRequired {
		t.Fatalf("restarted again for the same unit: %+v", st)
	}
	// And the start after that one is quiet again.
	f.inv = "inv-3"
	if st, _ := f.run(); st.UnitChanged || st.RestartRequired {
		t.Fatalf("third run %+v", st)
	}
}

func TestReconcileDockerAccess(t *testing.T) {
	t.Run("opt-out env records the file", func(t *testing.T) {
		f := newReconcileFixture(t)
		f.sys.env[DockerAccessEnv] = "0"
		st, _ := f.run()
		if st.Docker != DockerOptOut || f.sys.ran("usermod") {
			t.Fatalf("%+v runs %v", st, f.sys.runs)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(f.config), DockerOptOutFile)); err != nil {
			t.Error("opt-out not recorded")
		}
		// Kept on later runs without the variable.
		delete(f.sys.env, DockerAccessEnv)
		if st, _ := f.run(); st.Docker != DockerOptOut || f.sys.ran("usermod") {
			t.Fatalf("opt-out file ignored: %+v", st)
		}
	})
	t.Run("no docker group", func(t *testing.T) {
		f := newReconcileFixture(t)
		f.sys.write("/etc/group", AgentUser+":x:1:\n")
		if st, _ := f.run(); st.Docker != DockerNoGroup || f.sys.ran("usermod") {
			t.Fatalf("%+v", st)
		}
	})
	t.Run("gpasswd when usermod is missing", func(t *testing.T) {
		f := newReconcileFixture(t)
		delete(f.sys.tools, "usermod")
		f.sys.tools["gpasswd"] = true
		if st, _ := f.run(); st.Docker != DockerAdded || !f.sys.ran("gpasswd -a "+AgentUser+" docker") {
			t.Fatalf("%+v runs %v", st, f.sys.runs)
		}
	})
	t.Run("usermod fails", func(t *testing.T) {
		f := newReconcileFixture(t)
		f.sys.fail = "usermod"
		st, err := f.run()
		if st.Docker != DockerFailed || err == nil || st.Report().Error == "" {
			t.Fatalf("%+v %v", st, err)
		}
	})
}

func TestReconcileCreatesAccount(t *testing.T) {
	f := newReconcileFixture(t)
	f.sys.write("/etc/passwd", "root:x:0:0::/root:/bin/sh\n")
	f.sys.write("/etc/group", "root:x:0:\n")
	st, err := f.run()
	if err != nil {
		t.Fatalf("%+v %v", st, err)
	}
	if !f.sys.ran("groupadd --system "+AgentUser) || !f.sys.ran("useradd --system --gid "+AgentUser+" --home-dir "+f.stateDir) {
		t.Errorf("runs %v", f.sys.runs)
	}
}

func TestReconcileNoOpOutsideLayout(t *testing.T) {
	f := newReconcileFixture(t)
	st, err := Reconcile(t.Context(), ReconcileOptions{Sys: f.sys.Sys, Install: Install{Method: MethodDev, Reason: "not under"}})
	if st != nil || err == nil {
		t.Fatalf("%+v %v", st, err)
	}
	if len(f.sys.runs) != 0 {
		t.Errorf("runs %v", f.sys.runs)
	}
}
