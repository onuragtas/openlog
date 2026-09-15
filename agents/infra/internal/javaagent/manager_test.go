package javaagent

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
)

// fleet serves jars and builds the java_agent section of sync responses.
type fleet struct {
	t    *testing.T
	e    *env
	srv  *httptest.Server
	mu   sync.Mutex
	jars map[string][]byte
}

func newFleet(e *env) *fleet {
	f := &fleet{t: e.t, e: e, jars: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		b, ok := f.jars[strings.TrimPrefix(r.URL.Path, "/")]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	e.t.Cleanup(f.srv.Close)
	return f
}

func (f *fleet) remote(mode, version, floor string) json.RawMessage {
	f.t.Helper()
	r := Remote{Mode: mode, Version: "agent", TargetVersion: version, Reason: "offer"}
	if version != "" {
		jar := testJar(f.t, version, true)
		name := "openlog-javaagent-" + version + ".jar"
		f.mu.Lock()
		f.jars[name] = jar
		f.mu.Unlock()
		m := testManifest(f.t, version, f.srv.URL, jar, floor)
		r.Manifest, r.Signature, r.DownloadURL = base64.StdEncoding.EncodeToString(m), string(signLine(m, f.e.key)), f.srv.URL+"/"+name
	}
	b, _ := json.Marshal(r)
	return b
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func (e *env) manager(c *clock, procs *[]Proc, inProcess bool, restart func()) *Manager {
	return NewManager(Options{Config: e.cfg, StateDir: e.state, StatusDir: e.infra, AgentVersion: "0.9.0", OS: e.goos,
		Trusted: []ed25519.PublicKey{e.key.pub}, Capable: true, InProcess: inProcess, Sys: testSys(), Restart: restart,
		ListProcs: func() ([]Proc, error) { return *procs, nil }, Now: c.now, InventoryEvery: time.Minute, Tick: time.Second})
}

func TestManagerInProcessLifecycle(t *testing.T) {
	e := newEnv(t)
	f := newFleet(e)
	c := &clock{t: time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)}
	procs := []Proc{}
	m := e.manager(c, &procs, true, nil)
	ctx := context.Background()

	m.Evaluate(ctx)
	// Locally only the infra agent's own manifest is available: without it the agent waits for the fleet (no failure,
	// which would block the fleet's offer for 1 h).
	if r := m.Report(); r.Status != StatusNotFound || r.Mode != config.JavaAgentModeAuto || r.Source != SourceLocal || r.TargetVersion != "0.9.0" ||
		r.Update != nil {
		t.Fatalf("initial report = %+v", r)
	}
	// A package's embedded manifest lists no java-agent jar: still waiting.
	embedded, _ := json.Marshal(map[string]any{"schema": 1, "product": "openlog", "version": "0.9.0", "channel": "stable", "artifacts": []any{}})
	m.o.OwnManifest = func() ([]byte, []byte, error) { return embedded, signLine(embedded, e.key), nil }
	m.Evaluate(ctx)
	if r := m.Report(); r.Status != StatusNotFound || r.Update != nil {
		t.Fatalf("embedded manifest report = %+v", r)
	}

	m.SetRemote(f.remote("auto", "1.0.0", ""))
	m.Evaluate(ctx)
	r := m.Report()
	if r.Source != SourceRemote || r.CurrentVersion != "1.0.0" || r.Status != StatusInstalled || r.LinkState != LinkManaged ||
		!r.Managed || r.Update == nil || r.Update.State != StateConfirming || r.Update.Operation != OpInstall {
		t.Fatalf("after install = %+v %+v", r, r.Update)
	}
	if e.linkTarget() != e.jarPath("1.0.0") {
		t.Fatal("link not switched")
	}
	c.add(2 * time.Minute)
	m.Evaluate(ctx)
	if r = m.Report(); r.Update.State != StateApplied {
		t.Fatalf("verification = %+v", r.Update)
	}

	// An application starts with the managed link, then 1.1.0 is installed: it must be restarted.
	procs = []Proc{{PID: 4242, Exe: "/usr/bin/java", Args: []string{"java", "-jar", "shop.jar"},
		Env: []string{"JAVA_TOOL_OPTIONS=-javaagent:" + e.link}, Start: c.now()}}
	c.add(time.Minute)
	m.SetRemote(f.remote("auto", "1.1.0", "1.0.0"))
	m.Evaluate(ctx)
	c.add(2 * time.Minute)
	m.Evaluate(ctx)
	r = m.Report()
	if r.CurrentVersion != "1.1.0" || r.Status != StatusRestartPending || len(r.JVMs) != 1 || !r.JVMs[0].RestartPending ||
		r.JVMs[0].LoadedVersion != "1.0.0" || !strings.Contains(r.Detail, "restart") {
		t.Fatalf("restart pending = %+v", r)
	}
	if _, err := os.Stat(e.jarPath("1.0.0")); err != nil {
		t.Fatal("the version a JVM runs was pruned")
	}

	// mode off keeps the jar while a JVM uses it, and removes it once the JVM is gone.
	m.SetRemote(f.remote("off", "", ""))
	m.Evaluate(ctx)
	if r = m.Report(); r.CurrentVersion != "1.1.0" || r.Status != StatusError || !strings.Contains(r.Detail, "running JVMs") {
		t.Fatalf("off while in use = %+v", r)
	}
	procs = nil
	c.add(2 * time.Minute)
	m.Evaluate(ctx)
	if r = m.Report(); r.CurrentVersion != "" || r.Status != StatusNotFound || r.Update.State != StateUninstalled {
		t.Fatalf("off = %+v %+v", r, r.Update)
	}
	if _, err := os.Lstat(e.link); err == nil {
		t.Fatal("link kept after removal")
	}
}

func TestManagerUnmanagedAndManual(t *testing.T) {
	e := newEnv(t)
	e.cfg.Mode = config.JavaAgentModeManual
	os.WriteFile(e.link, testJar(t, "0.8.0", true), 0o644)
	c := &clock{t: time.Now()}
	procs := []Proc{{PID: 7, Exe: "/usr/bin/java", Args: []string{"java", "-javaagent:" + e.link, "-jar", "x.jar"}, Start: time.Now()}}
	m := e.manager(c, &procs, true, nil)
	m.Evaluate(context.Background())
	r := m.Report()
	if r.Status != StatusUnmanaged || !strings.Contains(r.Detail, "left alone") || len(r.JVMs) != 1 || r.JVMs[0].Managed ||
		r.JVMs[0].LoadedVersion != "0.8.0" || r.JVMs[0].RestartPending || r.Update != nil {
		t.Fatalf("unmanaged = %+v", r)
	}
}

func TestManagerHandsOverToApply(t *testing.T) {
	e := newEnv(t)
	f := newFleet(e)
	c := &clock{t: time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)}
	procs := []Proc{}
	restarted := false
	m := e.manager(c, &procs, false, func() { restarted = true })
	m.SetRemote(f.remote("auto", "1.0.0", ""))
	m.Evaluate(context.Background())
	if !restarted || m.Report().Status != StatusStaged {
		t.Fatalf("hand over: restarted=%v %+v", restarted, m.Report())
	}
	// The next start: "-apply" as root, then the agent takes over the result.
	st := Apply(context.Background(), ApplyOptions{Sys: testSys(), StateDir: e.state, StatusDir: e.infra, Config: e.cfg,
		Trusted: []ed25519.PublicKey{e.key.pub}, OS: e.goos, Now: c.now})
	if st == nil || st.Result != ResultApplied {
		t.Fatalf("apply = %+v", st)
	}
	m2 := e.manager(c, &procs, false, nil)
	m2.Startup()
	m2.Evaluate(context.Background())
	if r := m2.Report(); r.CurrentVersion != "1.0.0" || r.Update.State != StateConfirming || r.Status != StatusInstalled {
		t.Fatalf("after restart = %+v %+v", r, r.Update)
	}
	if _, err := os.Stat(e.state + "/" + StateSubdir + "/" + StagedDir); err == nil {
		t.Fatal("staged files kept")
	}
}
