package javaagent

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

func TestAgentPaths(t *testing.T) {
	got := AgentPaths(
		[]string{"java", "-javaagent:/opt/a.jar=otel.x=1", "-jar", "app.jar", "-javaagent:/opt/a.jar"},
		[]string{`JAVA_TOOL_OPTIONS=-Xmx1g "-javaagent:/opt/my agents/b.jar" -Dx=y`, "JDK_JAVA_OPTIONS=-javaagent:/opt/c.jar", "OTHER=-javaagent:/nope.jar"})
	want := []string{"/opt/a.jar", "/opt/my agents/b.jar", "/opt/c.jar"}
	if !slices.Equal(got, want) {
		t.Fatalf("AgentPaths = %q, want %q", got, want)
	}
	if got := AgentPaths([]string{`-javaagent:C:\Program Files\openlog\openlog-javaagent.jar`}, nil); len(got) != 1 || got[0] != `C:\Program Files\openlog\openlog-javaagent.jar` {
		t.Fatalf("windows path = %q", got)
	}
}

func TestParseManifestMF(t *testing.T) {
	attrs := parseManifestMF([]byte("Manifest-Version: 1.0\r\nPremain-Class: io.opentelemetry.java\r\n agent.OpenTelemetryAgent\r\nOpenlog-Javaagent-Version: 1.2.3\r\n\r\nName: x\r\nOpenlog-Javaagent-Version: 9.9.9\r\n"))
	if attrs["Premain-Class"] != "io.opentelemetry.javaagent.OpenTelemetryAgent" || attrs[VersionAttribute] != "1.2.3" {
		t.Fatalf("attrs = %v", attrs)
	}
}

func TestInspectorLoadedVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The inspector runs with goos "linux" on POSIX paths; Windows paths are covered by the windows inspector test.
		t.Skip("Linux inspector paths; see the windows inspector test")
	}
	e := newEnv(t)
	t0 := time.Date(2026, 9, 15, 8, 0, 0, 0, time.UTC)
	st := &Status{Version: "1.1.0", Previous: "1.0.0",
		Switches:     []Switch{{"1.1.0", t0.Add(2 * time.Hour)}, {"1.0.0", t0}},
		LinkSwitches: []Switch{{"1.1.0", t0.Add(2 * time.Hour)}, {"1.0.0", t0}}}
	in := &inspector{goos: "linux", root: e.root, link: e.link, current: "1.1.0", status: st, cache: map[string]jarInfo{}}

	// A user's own jar next to the app: the version comes from its manifest (unchanged since the start).
	own := filepath.Join(e.base, "app", "openlog-javaagent-0.9.0.jar")
	os.MkdirAll(filepath.Dir(own), 0o755)
	os.WriteFile(own, testJar(t, "0.9.0", true), 0o644)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(own, old, old)

	procs := []Proc{
		{PID: 10, Exe: "/usr/bin/java", Args: []string{"java", "-javaagent:" + e.link, "-jar", "a.jar", "--db.password=secret"}, Start: t0.Add(time.Hour)},
		{PID: 11, Exe: "/usr/bin/java", Args: []string{"java", "-jar", "b.jar"}, Env: []string{"JAVA_TOOL_OPTIONS=-javaagent:" + e.link}, Start: t0.Add(3 * time.Hour)},
		{PID: 12, Exe: "/usr/bin/java", Args: []string{"java", "-javaagent:" + e.jarPath("1.0.0"), "-jar", "c.jar"}, Start: t0.Add(3 * time.Hour)},
		{PID: 13, Exe: "/usr/bin/java", Args: []string{"java", "-javaagent:" + e.link}, Start: t0.Add(3 * time.Hour),
			OpenJars: []string{"/srv/app.jar", e.jarPath("1.0.0") + " (deleted)"}},
		{PID: 14, Exe: "/usr/bin/java", Args: []string{"java", "-javaagent:openlog-javaagent-0.9.0.jar"}, Cwd: filepath.Dir(own), Start: time.Now()},
		{PID: 15, Exe: "/usr/bin/java", Args: []string{"java", "-javaagent:" + e.link}, Container: true, Start: t0},
		{PID: 16, Exe: "/usr/bin/java", Args: []string{"java", "-javaagent:/opt/newrelic/newrelic.jar"}, Start: t0},
		{PID: 17, Exe: "/usr/bin/java", Args: []string{"java", "-javaagent:" + e.link}, Start: t0.Add(-time.Hour)},
	}
	jvms := in.JVMs(procs)
	byPID := map[int]JVM{}
	for _, j := range jvms {
		byPID[j.PID] = j
	}
	if len(jvms) != 7 {
		t.Fatalf("jvms = %+v", jvms)
	}
	check := func(pid int, loaded string, managed, pending bool) {
		t.Helper()
		j := byPID[pid]
		if j.LoadedVersion != loaded || j.Managed != managed || j.RestartPending != pending {
			t.Errorf("pid %d = %+v, want loaded=%q managed=%v pending=%v", pid, j, loaded, managed, pending)
		}
	}
	check(10, "1.0.0", true, true)  // started between the switches
	check(11, "1.1.0", true, false) // started after the switch, agent from JAVA_TOOL_OPTIONS
	check(12, "1.0.0", true, false) // pinned to a version directory
	check(13, "1.0.0", true, true)  // open file wins over the start time
	check(14, "0.9.0", false, false)
	check(15, "", false, false)
	check(17, "", true, true) // older than the history: unknown, but it started before the switch
	if !byPID[15].Container {
		t.Error("container flag missing")
	}
	if c := byPID[10].Command; c == "" || slices.Contains([]string{c}, "secret") || !containsStr(c, "-javaagent:") {
		t.Errorf("command = %q", c)
	}
	if containsStr(byPID[10].Command, "secret") {
		t.Errorf("command not masked: %q", byPID[10].Command)
	}
}

func TestInspectorWindowsPaths(t *testing.T) {
	root, link := `C:\Program Files\openlog\java-agent`, `C:\Program Files\openlog\openlog-javaagent.jar`
	t0 := time.Date(2026, 9, 15, 8, 0, 0, 0, time.UTC)
	st := &Status{Version: "1.1.0", Switches: []Switch{{"1.1.0", t0}}, LinkSwitches: []Switch{{"1.0.0", t0.Add(-time.Hour)}}}
	in := &inspector{goos: "windows", root: root, link: link, current: "1.1.0", status: st}
	jvms := in.JVMs([]Proc{
		{PID: 1, Args: []string{`java.exe`, `-javaagent:c:/program files/openlog/openlog-javaagent.jar`}, Start: t0.Add(time.Minute)},
		{PID: 2, Args: []string{`javaw.exe`, `-javaagent:C:\Program Files\openlog\java-agent\current\openlog-javaagent.jar`}, Start: t0.Add(time.Minute)},
	})
	if len(jvms) != 2 || !jvms[0].Managed || jvms[0].LoadedVersion != "1.0.0" || !jvms[0].RestartPending ||
		!jvms[1].Managed || jvms[1].LoadedVersion != "1.1.0" || jvms[1].RestartPending {
		t.Fatalf("jvms = %+v", jvms)
	}
}

func containsStr(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (s[:len(sub)] == sub || containsStr(s[1:], sub)))
}
