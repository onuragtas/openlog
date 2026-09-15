package javaagent

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyInstallUpgradePrune(t *testing.T) {
	e := newEnv(t)
	e.stage("1.0.0", "")
	st := e.apply(ActionInstall, "1.0.0")
	if st == nil || st.Result != ResultApplied || st.Operation != OpInstall || st.Version != "1.0.0" || st.LinkState != LinkManaged {
		t.Fatalf("install = %+v", st)
	}
	if InstalledVersion(e.root) != "1.0.0" || e.linkTarget() != e.jarPath("1.0.0") {
		t.Fatalf("current %q, link %q", InstalledVersion(e.root), e.linkTarget())
	}
	if fi, err := os.Stat(e.jarPath("1.0.0")); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("jar: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(e.root, MarkerFile)); err != nil {
		t.Fatal("marker missing")
	}
	if loaded, err := LoadStatus(e.infra); err != nil || loaded.RequestID != st.RequestID {
		t.Fatalf("status file: %+v %v", loaded, err)
	}
	// The same request is handled once.
	if again := Apply(t.Context(), ApplyOptions{Sys: testSys(), StateDir: e.state, StatusDir: e.infra, Config: e.cfg}); again != nil {
		t.Fatalf("request handled twice: %+v", again)
	}

	old, _ := os.Open(e.link) // a running JVM keeps its open file across the switch
	defer old.Close()
	e.stage("1.1.0", "1.0.0")
	st = e.apply(ActionInstall, "1.1.0")
	if st.Result != ResultApplied || st.Operation != OpUpgrade || st.Previous != "1.0.0" || e.linkTarget() != e.jarPath("1.1.0") ||
		len(st.Switches) != 2 || len(st.LinkSwitches) != 2 || st.LinkSwitches[0].Version != "1.1.0" {
		t.Fatalf("upgrade = %+v", st)
	}
	if v := jarVersionOfFile(t, old); v != "1.0.0" {
		t.Fatalf("the open jar changed under the JVM: %q", v)
	}

	e.stage("1.2.0", "1.0.0")
	if st = e.apply(ActionInstall, "1.2.0", "1.0.0"); st.Result != ResultApplied {
		t.Fatalf("1.2.0 = %+v", st)
	}
	if _, err := os.Stat(e.jarPath("1.0.0")); err != nil {
		t.Fatal("a version in use was pruned")
	}
	e.stage("1.3.0", "1.0.0")
	if st = e.apply(ActionInstall, "1.3.0"); st.Result != ResultApplied {
		t.Fatalf("1.3.0 = %+v", st)
	}
	entries, _ := os.ReadDir(filepath.Join(e.root, "versions"))
	var names []string
	for _, d := range entries {
		names = append(names, d.Name())
	}
	if strings.Join(names, ",") != "1.2.0,1.3.0" {
		t.Fatalf("versions after prune = %v", names)
	}
}

func jarVersionOfFile(t *testing.T, f *os.File) string {
	t.Helper()
	fi, _ := f.Stat()
	b := make([]byte, fi.Size())
	if _, err := f.ReadAt(b, 0); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "copy.jar")
	os.WriteFile(p, b, 0o644)
	return JarVersion(p)
}

func TestApplyUnmanagedLinkAndRejections(t *testing.T) {
	e := newEnv(t)
	manual := testJar(t, "0.9.0", true)
	os.WriteFile(e.link, manual, 0o644)
	e.stage("1.0.0", "")
	st := e.apply(ActionInstall, "1.0.0")
	if st.Result != ResultApplied || st.LinkState != LinkUnmanaged || InstalledVersion(e.root) != "1.0.0" {
		t.Fatalf("unmanaged link = %+v", st)
	}
	if b, _ := os.ReadFile(e.link); !bytes.Equal(b, manual) {
		t.Fatal("a user's jar at link_path was changed")
	}
	// Uninstall leaves the user's file alone too.
	if st = e.apply(ActionUninstall, ""); st.Result != ResultUninstalled {
		t.Fatalf("uninstall = %+v", st)
	}
	if b, _ := os.ReadFile(e.link); !bytes.Equal(b, manual) {
		t.Fatal("uninstall removed a user's jar")
	}

	// An install root without the marker is never touched.
	os.Remove(e.link)
	os.MkdirAll(filepath.Join(e.root, "versions"), 0o755)
	e.stage("1.0.0", "")
	if st = e.apply(ActionInstall, "1.0.0"); st.Result != ResultRejected || !strings.Contains(st.Error, "not managed") {
		t.Fatalf("foreign root = %+v", st)
	}
	os.RemoveAll(e.root)

	// A tampered jar (sha256 mismatch) and a jar that is no Java agent are rejected before anything changes.
	jar := testJar(t, "1.0.0", true)
	e.stageJar("1.0.0", testJar(t, "1.0.0-tampered", true), testManifest(t, "1.0.0", "https://x", jar, ""))
	if st = e.apply(ActionInstall, "1.0.0"); st.Result != ResultRejected || InstalledVersion(e.root) != "" {
		t.Fatalf("tampered = %+v", st)
	}
	notAgent := testJar(t, "1.0.0", false)
	e.stageJar("1.0.0", notAgent, testManifest(t, "1.0.0", "https://x", notAgent, ""))
	if st = e.apply(ActionInstall, "1.0.0"); st.Result != ResultRejected || !strings.Contains(st.Error, "Premain-Class") {
		t.Fatalf("no agent = %+v", st)
	}
	wrong := testJar(t, "2.0.0", true)
	e.stageJar("1.0.0", wrong, testManifest(t, "1.0.0", "https://x", wrong, ""))
	if st = e.apply(ActionInstall, "1.0.0"); st.Result != ResultRejected || !strings.Contains(st.Error, "does not match") {
		t.Fatalf("wrong version = %+v", st)
	}
	if _, err := os.Lstat(e.link); err == nil {
		t.Fatal("link created by a rejected install")
	}

	// Downgrades below the rollback floor are refused.
	e.stage("1.1.0", "1.1.0")
	if st = e.apply(ActionInstall, "1.1.0"); st.Result != ResultApplied {
		t.Fatalf("1.1.0 = %+v", st)
	}
	e.stage("1.0.0", "")
	if st = e.apply(ActionInstall, "1.0.0"); st.Result != ResultRejected || !strings.Contains(st.Error, "rollback_floor") {
		t.Fatalf("downgrade = %+v", st)
	}
}

func TestApplyRollbackAndUninstall(t *testing.T) {
	e := newEnv(t)
	e.stage("1.0.0", "")
	e.apply(ActionInstall, "1.0.0")
	if st := e.apply(ActionRollback, "1.0.0"); st.Result != ResultFailed || InstalledVersion(e.root) != "1.0.0" {
		t.Fatalf("rollback without previous = %+v", st)
	}
	e.stage("1.1.0", "1.0.0")
	e.apply(ActionInstall, "1.1.0")
	st := e.apply(ActionRollback, "1.1.0")
	if st.Result != ResultRolledBack || st.Version != "1.0.0" || e.linkTarget() != e.jarPath("1.0.0") || InstalledVersion(e.root) != "1.0.0" {
		t.Fatalf("rollback = %+v", st)
	}
	if _, err := os.Stat(filepath.Join(e.root, "versions", "1.1.0")); err == nil {
		t.Fatal("rolled back version kept")
	}
	if st = e.apply(ActionUninstall, "", "1.0.0"); st.Result != ResultRejected || !strings.Contains(st.Error, "in use") {
		t.Fatalf("uninstall in use = %+v", st)
	}
	if st = e.apply(ActionUninstall, ""); st.Result != ResultUninstalled || st.LinkState != LinkMissing {
		t.Fatalf("uninstall = %+v", st)
	}
	if _, err := os.Lstat(e.link); err == nil {
		t.Fatal("link kept")
	}
	if _, err := os.Stat(e.root); err == nil {
		t.Fatal("install root kept")
	}
}

func TestApplyWindowsCopy(t *testing.T) {
	e := newEnv(t)
	e.goos = "windows"
	jar := e.stage("1.0.0", "")
	st := e.apply(ActionInstall, "1.0.0")
	fi, err := os.Lstat(e.link)
	if st.Result != ResultApplied || st.LinkState != LinkManaged || st.LinkSHA256 == "" || err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("copy install = %+v (%v)", st, err)
	}
	if b, _ := os.ReadFile(e.link); !bytes.Equal(b, jar) {
		t.Fatal("copy differs")
	}
	if LinkStateOf(e.link, e.root, st.LinkSHA256) != LinkManaged || LinkStateOf(e.link, e.root, "") != LinkUnmanaged {
		t.Fatal("copy classification")
	}
	jar2 := e.stage("1.1.0", "1.0.0")
	if st = e.apply(ActionInstall, "1.1.0"); st.Result != ResultApplied || st.LinkVersion != "1.1.0" {
		t.Fatalf("copy upgrade = %+v", st)
	}
	if b, _ := os.ReadFile(e.link); !bytes.Equal(b, jar2) {
		t.Fatal("copy not replaced")
	}
	if st = e.apply(ActionSwitch, "1.1.0"); st.Result != ResultApplied || st.LinkState != LinkManaged {
		t.Fatalf("switch = %+v", st)
	}
}
