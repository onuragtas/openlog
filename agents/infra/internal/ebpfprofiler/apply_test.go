package ebpfprofiler

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The profiler is a Linux component (systemd unit, root-owned tree). The apply step is still exercised on
// macOS, where the ownership checks are real; Windows has neither the symlink nor the unit model.
func skipUnsupported(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the profiler is not built for windows")
	}
}

func TestApplyInstallUpgradePrune(t *testing.T) {
	skipUnsupported(t)
	e := newEnv(t)
	e.stage("1.0.0", "")
	st := e.apply(ActionInstall, "1.0.0")
	if st == nil || st.Result != ResultApplied || st.Operation != OpInstall || st.Version != "1.0.0" || st.Target != "1.0.0" {
		t.Fatalf("install = %+v", st)
	}
	if InstalledVersion(e.root) != "1.0.0" || e.currentTarget() != filepath.Join("versions", "1.0.0") {
		t.Fatalf("current = %q -> %q", InstalledVersion(e.root), e.currentTarget())
	}
	if fi, err := os.Lstat(e.binPath("1.0.0")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("profiler binary: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(e.root, MarkerFile)); err != nil {
		t.Fatal("marker missing")
	}
	// The unit of this version is installed where the host keeps packaged units, and systemd is told.
	unit, err := os.ReadFile(e.unitPath())
	if err != nil || !strings.Contains(string(unit), "profiler 1.0.0") {
		t.Fatalf("unit = %q %v", unit, err)
	}
	for _, cmd := range []string{"systemctl daemon-reload", "systemctl enable --now " + UnitName, "systemctl is-active --quiet " + UnitName} {
		if !e.ran(cmd) {
			t.Fatalf("%q not run; ran %v", cmd, e.runLog)
		}
	}
	if st.Unit != UnitName {
		t.Fatalf("unit not reported: %+v", st)
	}
	if loaded, err := LoadStatus(e.infra); err != nil || loaded.RequestID != st.RequestID || loaded.Version != "1.0.0" {
		t.Fatalf("status file: %+v %v", loaded, err)
	}
	if ManagedBy(e.sys(), e.root) != ManagedFleet {
		t.Fatalf("managed by = %q", ManagedBy(e.sys(), e.root))
	}

	// The same request is handled once.
	if again := Apply(context.Background(), ApplyOptions{Sys: e.sys(), StateDir: e.state, StatusDir: e.infra, Config: e.cfg}); again != nil {
		t.Fatalf("request handled twice: %+v", again)
	}

	e.stage("1.1.0", "")
	st = e.apply(ActionInstall, "1.1.0")
	if st.Result != ResultApplied || st.Operation != OpUpgrade || st.Version != "1.1.0" || st.Previous != "1.0.0" {
		t.Fatalf("upgrade = %+v", st)
	}
	if unit, _ := os.ReadFile(e.unitPath()); !strings.Contains(string(unit), "profiler 1.1.0") {
		t.Fatalf("the unit still belongs to the old version: %q", unit)
	}
	// The version that was replaced stays on disk: the health check comes minutes after the restart, and a
	// rollback then has to find it there instead of downloading it again.
	if got := versionDirs(t, e); got != "1.0.0,1.1.0" {
		t.Fatalf("versions after upgrade = %s", got)
	}
	// Only the one before that is pruned.
	e.stage("1.2.0", "")
	if st = e.apply(ActionInstall, "1.2.0"); st.Result != ResultApplied || st.Previous != "1.1.0" {
		t.Fatalf("1.2.0 = %+v", st)
	}
	if got := versionDirs(t, e); got != "1.1.0,1.2.0" {
		t.Fatalf("versions after prune = %s", got)
	}
}

func versionDirs(t *testing.T, e *env) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(e.root, "versions"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range entries {
		names = append(names, d.Name())
	}
	return strings.Join(names, ",")
}

func TestApplyRejections(t *testing.T) {
	skipUnsupported(t)
	e := newEnv(t)

	// An install root without the marker belongs to the package manager or to a person.
	if err := os.MkdirAll(filepath.Join(e.root, "versions"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.stage("1.0.0", "")
	if st := e.apply(ActionInstall, "1.0.0"); st.Result != ResultRejected || !strings.Contains(st.Error, "not installed by this agent") {
		t.Fatalf("foreign root = %+v", st)
	}
	if ManagedBy(e.sys(), e.root) != ManagedPackage {
		t.Fatalf("managed by = %q", ManagedBy(e.sys(), e.root))
	}
	if st := e.apply(ActionUninstall, ""); st.Result != ResultRejected {
		t.Fatalf("uninstall of a foreign root = %+v", st)
	}
	if _, err := os.Stat(e.root); err != nil {
		t.Fatal("a foreign install root was removed")
	}
	if err := os.RemoveAll(e.root); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, version, want string
		stage               func()
	}{
		{"unsigned", "1.0.0", "signature", func() {
			ar := testArchive(t, "1.0.0", e.goos, e.goarch, archiveOpts{})
			m := testArchiveManifest(t, "1.0.0", e.goos, e.goarch, "", ar)
			e.stageSigned("1.0.0", ar, m, signLine(m, newKey(t)))
		}},
		{"version mismatch", "1.0.0", "does not match", func() {
			ar := testArchive(t, "2.0.0", e.goos, e.goarch, archiveOpts{})
			e.stageRaw("1.0.0", ar, testArchiveManifest(t, "2.0.0", e.goos, e.goarch, "", ar))
		}},
		{"tampered archive", "1.0.0", "sha256", func() {
			good := testArchive(t, "1.0.0", e.goos, e.goarch, archiveOpts{})
			bad := slices.Clone(good)
			bad[len(bad)/2] ^= 0xff
			e.stageRaw("1.0.0", bad, testArchiveManifest(t, "1.0.0", e.goos, e.goarch, "", good))
		}},
		{"no profiler in the archive", "1.0.0", BinaryRel, func() {
			ar := testArchive(t, "1.0.0", e.goos, e.goarch, archiveOpts{noBinary: true})
			e.stageRaw("1.0.0", ar, testArchiveManifest(t, "1.0.0", e.goos, e.goarch, "", ar))
		}},
		{"no unit in the archive", "1.0.0", "systemd", func() {
			ar := testArchive(t, "1.0.0", e.goos, e.goarch, archiveOpts{noUnit: true})
			e.stageRaw("1.0.0", ar, testArchiveManifest(t, "1.0.0", e.goos, e.goarch, "", ar))
		}},
		{"other platform", "1.0.0", "artifact", func() {
			ar := testArchive(t, "1.0.0", "plan9", "mips", archiveOpts{})
			e.stageRaw("1.0.0", ar, testArchiveManifest(t, "1.0.0", "plan9", "mips", "", ar))
		}},
		{"invalid version", "not-a-version", "invalid version", func() {}},
		{"nothing staged", "3.0.0", "staged manifest", func() {}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.stage()
			st := e.apply(ActionInstall, c.version)
			if st.Result != ResultRejected || !strings.Contains(st.Error, c.want) {
				t.Fatalf("= %+v, want rejected containing %q", st, c.want)
			}
			if InstalledVersion(e.root) != "" {
				t.Fatalf("a rejected install left %q behind", InstalledVersion(e.root))
			}
			if len(e.runLog) != 0 {
				t.Fatalf("a rejected install touched systemd: %v", e.runLog)
			}
		})
	}
	if st := e.apply("frobnicate", "1.0.0"); st.Result != ResultRejected || !strings.Contains(st.Error, "unknown action") {
		t.Fatalf("unknown action = %+v", st)
	}

	// A downgrade below the installed version's rollback floor is refused.
	e.stage("1.1.0", "1.1.0")
	if st := e.apply(ActionInstall, "1.1.0"); st.Result != ResultApplied {
		t.Fatalf("1.1.0 = %+v", st)
	}
	e.stage("1.0.0", "")
	st := e.apply(ActionInstall, "1.0.0")
	if st.Result != ResultRejected || !strings.Contains(st.Error, "rollback_floor") {
		t.Fatalf("downgrade = %+v", st)
	}
	if InstalledVersion(e.root) != "1.1.0" {
		t.Fatal("a refused downgrade changed the installation")
	}
}

func TestApplyRollsBackWhenTheNewVersionDoesNotStart(t *testing.T) {
	skipUnsupported(t)
	e := newEnv(t)
	e.stage("1.0.0", "")
	if st := e.apply(ActionInstall, "1.0.0"); st.Result != ResultApplied {
		t.Fatalf("install = %+v", st)
	}
	e.stage("1.1.0", "")
	e.failActive = "1.1.0"
	st := e.apply(ActionInstall, "1.1.0")
	if st.Result != ResultRolledBack || st.Version != "1.0.0" || st.Target != "1.1.0" {
		t.Fatalf("rollback = %+v", st)
	}
	if !strings.Contains(st.Error, "is not running") {
		t.Fatalf("error does not say why: %q", st.Error)
	}
	if InstalledVersion(e.root) != "1.0.0" {
		t.Fatalf("current = %q after rollback", InstalledVersion(e.root))
	}
	if _, err := os.Stat(filepath.Join(e.root, "versions", "1.1.0")); err == nil {
		t.Fatal("the broken version was kept")
	}
	// The unit of the version that runs again is the one on disk.
	if unit, _ := os.ReadFile(e.unitPath()); !strings.Contains(string(unit), "profiler 1.0.0") {
		t.Fatalf("unit after rollback = %q", unit)
	}
}

func TestApplyFirstInstallFailureLeavesNothing(t *testing.T) {
	skipUnsupported(t)
	e := newEnv(t)
	e.stage("1.0.0", "")
	e.failActive = "1.0.0"
	st := e.apply(ActionInstall, "1.0.0")
	if st.Result != ResultFailed || st.Version != "" || !strings.Contains(st.Error, "is not running") {
		t.Fatalf("failed install = %+v", st)
	}
	if _, err := os.Stat(e.root); err == nil {
		t.Fatal("a failed first install left the root behind")
	}
	if !e.ran("systemctl disable --now " + UnitName) {
		t.Fatalf("the half-installed unit was not disabled: %v", e.runLog)
	}
}

func TestApplyLeavesAPackagedUnitAlone(t *testing.T) {
	skipUnsupported(t)
	e := newEnv(t)
	// install.sh installs the .deb/.rpm by default; that unit belongs to dpkg/rpm.
	if err := os.WriteFile(e.pkgUnitPath(), []byte("[Unit]\n# packaged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.stage("1.0.0", "")
	st := e.apply(ActionInstall, "1.0.0")
	if st.Result != ResultFailed || !strings.Contains(st.Error, "belongs to the package manager") {
		t.Fatalf("packaged unit = %+v", st)
	}
	if b, _ := os.ReadFile(e.pkgUnitPath()); !strings.Contains(string(b), "packaged") {
		t.Fatalf("the packaged unit was changed: %q", b)
	}
	if _, err := os.Stat(e.unitPath()); err == nil {
		t.Fatal("a second unit was installed next to the packaged one")
	}
	if _, err := os.Stat(e.root); err == nil {
		t.Fatal("the install root survived a failed installation")
	}
}

// An extracted tree that is not root-owned is refused: the agent user must never be able to put a file into
// the installation it later runs as root.
func TestApplyRefusesATreeThatIsNotRootOwned(t *testing.T) {
	skipUnsupported(t)
	if os.Getuid() == 0 {
		t.Skip("as root every file would pass the ownership check")
	}
	e := newEnv(t)
	e.uidShift = 1 // the extracted files belong to the test user, not to Sys.RootUID
	e.stage("1.0.0", "")
	st := e.apply(ActionInstall, "1.0.0")
	if st.Result != ResultRejected || !strings.Contains(st.Error, "root-owned") {
		t.Fatalf("foreign ownership = %+v", st)
	}
	if InstalledVersion(e.root) != "" {
		t.Fatalf("installed anyway: %q", InstalledVersion(e.root))
	}
	if _, err := os.Stat(e.unitPath()); err == nil {
		t.Fatal("a unit was installed for a tree that failed the ownership check")
	}
	if len(e.runLog) != 0 {
		t.Fatalf("systemd was told about it: %v", e.runLog)
	}
}

func TestApplyWithoutSystemd(t *testing.T) {
	skipUnsupported(t)
	e := newEnv(t)
	if err := os.RemoveAll(filepath.Join(e.base, "run", "systemd", "system")); err != nil {
		t.Fatal(err)
	}
	e.stage("1.0.0", "")
	st := e.apply(ActionInstall, "1.0.0")
	if st.Result != ResultApplied || st.Version != "1.0.0" {
		t.Fatalf("install without systemd = %+v", st)
	}
	if len(e.runLog) != 0 {
		t.Fatalf("systemctl called without systemd: %v", e.runLog)
	}
	if _, err := os.Stat(e.unitPath()); err != nil {
		t.Fatal("the unit was not installed")
	}
}

func TestApplyUninstall(t *testing.T) {
	skipUnsupported(t)
	e := newEnv(t)
	e.stage("1.0.0", "")
	if st := e.apply(ActionInstall, "1.0.0"); st.Result != ResultApplied {
		t.Fatalf("install = %+v", st)
	}
	st := e.apply(ActionUninstall, "")
	if st.Result != ResultUninstalled || st.Operation != OpUninstall || st.Version != "" {
		t.Fatalf("uninstall = %+v", st)
	}
	if _, err := os.Stat(e.root); err == nil {
		t.Fatal("the install root survived the uninstall")
	}
	if !e.ran("systemctl disable --now " + UnitName) {
		t.Fatalf("the unit was not disabled: %v", e.runLog)
	}
	if ManagedBy(e.sys(), e.root) != ManagedNone || InstalledVersion(e.root) != "" {
		t.Fatal("state after uninstall")
	}
	// Uninstalling again is a no-op, not a failure.
	if st := e.apply(ActionUninstall, ""); st.Result != ResultUninstalled {
		t.Fatalf("second uninstall = %+v", st)
	}
}

func TestApplyIgnoresWhatIsNotItsJob(t *testing.T) {
	skipUnsupported(t)
	e := newEnv(t)
	// No request at all.
	if st := Apply(context.Background(), ApplyOptions{Sys: e.sys(), StateDir: e.state, StatusDir: e.infra, Config: e.cfg}); st != nil {
		t.Fatalf("no request = %+v", st)
	}
	// Not root: the privileged step is the only one allowed to install.
	e.stage("1.0.0", "")
	sys := e.sys()
	sys.IsRoot = func() bool { return false }
	e.n++
	if err := os.MkdirAll(filepath.Join(e.state, StateSubdir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.state, StateSubdir, RequestFile), []byte(`{"id":"00000000000000ff","action":"install","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := Apply(context.Background(), ApplyOptions{Sys: sys, StateDir: e.state, StatusDir: e.infra, Config: e.cfg}); st != nil {
		t.Fatalf("unprivileged apply = %+v", st)
	}
	// A request id that is not a request id is ignored rather than acted on.
	if err := os.WriteFile(filepath.Join(e.state, StateSubdir, RequestFile), []byte(`{"id":"../../etc","action":"install","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := Apply(context.Background(), ApplyOptions{Sys: e.sys(), StateDir: e.state, StatusDir: e.infra, Config: e.cfg}); st != nil {
		t.Fatalf("bad id = %+v", st)
	}
	if InstalledVersion(e.root) != "" {
		t.Fatal("something was installed")
	}
}
