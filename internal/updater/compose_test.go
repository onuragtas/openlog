package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDocker is an in-memory Docker Engine.
type fakeDocker struct {
	mu          sync.Mutex
	containers  map[string]*fakeContainer
	images      map[string]*Image // by reference and by id
	nextID      int
	pulled      []string
	created     []ContainerSpec
	migrateExit int
	execCode    int
	pullErr     error
}

type fakeContainer struct {
	c       Container
	running bool
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{containers: map[string]*fakeContainer{}, images: map[string]*Image{}}
}

func (f *fakeDocker) addImage(ref, id, version string) {
	img := &Image{ID: id, Config: map[string]any{
		"Env":        []any{"PATH=/usr/local/bin:/usr/bin"},
		"Entrypoint": []any{"/usr/local/bin/openlog-allinone"},
		"Labels":     map[string]any{"org.opencontainers.image.version": version},
	}}
	f.images[ref], f.images[id] = img, img
}

func roundTrip(in, out any) {
	b, _ := json.Marshal(in)
	_ = json.Unmarshal(b, out)
}

func (f *fakeDocker) byName(name string) *fakeContainer {
	for _, c := range f.containers {
		if strings.TrimPrefix(c.c.Name, "/") == name {
			return c
		}
	}
	return nil
}

func (f *fakeDocker) add(c Container, running bool) {
	f.containers[c.ID] = &fakeContainer{c: c, running: running}
}

func (f *fakeDocker) ListContainers(_ context.Context, labels map[string]string) ([]ContainerSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ContainerSummary
	for _, fc := range f.containers {
		ls := map[string]string{}
		if m, ok := fc.c.Config["Labels"].(map[string]any); ok {
			for k, v := range m {
				ls[k], _ = v.(string)
			}
		}
		match := true
		for k, v := range labels {
			if ls[k] != v {
				match = false
			}
		}
		if !match {
			continue
		}
		state := "exited"
		if fc.running {
			state = "running"
		}
		img, _ := fc.c.Config["Image"].(string)
		out = append(out, ContainerSummary{ID: fc.c.ID, Names: []string{fc.c.Name}, Image: img, State: state, Labels: ls})
	}
	return out, nil
}

func (f *fakeDocker) InspectContainer(_ context.Context, id string) (*Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fc := f.containers[id]
	if fc == nil {
		fc = f.byName(id)
	}
	if fc == nil {
		return nil, &DockerError{Status: 404, Message: "no such container " + id}
	}
	var out Container
	roundTrip(fc.c, &out)
	out.State.Running = fc.running
	out.State.Status = map[bool]string{true: "running", false: "exited"}[fc.running]
	return &out, nil
}

func (f *fakeDocker) InspectImage(_ context.Context, ref string) (*Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if img := f.images[ref]; img != nil {
		return img, nil
	}
	return nil, &DockerError{Status: 404, Message: "no such image " + ref}
}

func (f *fakeDocker) PullImage(_ context.Context, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pullErr != nil {
		return f.pullErr
	}
	if f.images[ref] == nil {
		return fmt.Errorf("pull %s: manifest unknown", ref)
	}
	f.pulled = append(f.pulled, ref)
	return nil
}

func (f *fakeDocker) CreateContainer(_ context.Context, spec ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.byName(spec.Name) != nil {
		return "", &DockerError{Status: 409, Message: "name in use " + spec.Name}
	}
	ref, _ := spec.Config["Image"].(string)
	img := f.images[ref]
	if img == nil {
		return "", &DockerError{Status: 404, Message: "no such image " + ref}
	}
	f.nextID++
	id := fmt.Sprintf("%064x", 0xabc000+f.nextID)
	var c Container
	c.ID, c.Name, c.Image = id, "/"+spec.Name, img.ID
	roundTrip(spec.Config, &c.Config)
	roundTrip(spec.HostConfig, &c.HostConfig)
	roundTrip(spec.Networks, &c.NetworkSettings.Networks)
	f.add(c, false)
	f.created = append(f.created, spec)
	return id, nil
}

func (f *fakeDocker) StartContainer(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	fc := f.containers[id]
	if fc == nil {
		return &DockerError{Status: 404}
	}
	if ep := toStrings(fc.c.Config["Entrypoint"]); len(ep) > 0 && strings.HasSuffix(ep[0], "openlog-migrate") {
		fc.running = false // runs to completion immediately
		return nil
	}
	fc.running = true
	return nil
}

func (f *fakeDocker) StopContainer(_ context.Context, id string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fc := f.containers[id]; fc != nil {
		fc.running = false
		return nil
	}
	return &DockerError{Status: 404}
}

func (f *fakeDocker) RenameContainer(_ context.Context, id, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if other := f.byName(name); other != nil && other.c.ID != id {
		return &DockerError{Status: 409, Message: "name in use"}
	}
	f.containers[id].c.Name = "/" + name
	return nil
}

func (f *fakeDocker) RemoveContainer(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.containers, id)
	return nil
}

func (f *fakeDocker) WaitContainer(context.Context, string) (int, error) { return f.migrateExit, nil }

func (f *fakeDocker) ContainerLogs(context.Context, string, int) (string, error) {
	return "level=INFO msg=\"applying migration\"\n", nil
}

func (f *fakeDocker) Exec(_ context.Context, _ string, cmd []string, w io.Writer) (int, string, error) {
	if f.execCode != 0 {
		return f.execCode, "pg_dump: error: connection refused", nil
	}
	_, _ = fmt.Fprintf(w, "PGDMP %s", strings.Join(cmd, " "))
	return 0, "", nil
}

const (
	img090 = "reg.test/openlog@sha256:090"
	img091 = "reg.test/openlog@sha256:091"
	img092 = "reg.test/openlog@sha256:092" // never becomes ready
)

type composeFixture struct {
	docker  *fakeDocker
	engine  *ComposeEngine
	runner  *Runner
	store   *memStore
	audit   *recordAudit
	source  *fakeSource
	envFile string
	oldID   string
}

func newComposeFixture(t *testing.T) *composeFixture {
	t.Helper()
	dir := t.TempDir()
	fd := newFakeDocker()
	fd.addImage(img090, "sha256:img090", "0.9.0")
	fd.addImage(img091, "sha256:img091", "0.9.1")
	fd.addImage(img092, "sha256:img092", "0.9.2")
	versions := map[string]string{"sha256:img090": "0.9.0", "sha256:img091": "0.9.1", "sha256:img092": "0.9.2"}

	oldID := fmt.Sprintf("%064x", 0xc0ffee)
	var app Container
	roundTrip(map[string]any{
		"Id": oldID, "Name": "/proj-openlog-1", "Image": "sha256:img090",
		"Config": map[string]any{
			"Image": img090, "Hostname": oldID[:12], "StopTimeout": 40,
			"Env":        []string{"PATH=/usr/local/bin:/usr/bin", "OPENLOG_POSTGRES_DSN=postgres://openlog@postgres/openlog"},
			"Entrypoint": []string{"/usr/local/bin/openlog-allinone"},
			"Labels": map[string]string{
				labelProject: "proj", labelService: "openlog", labelImage: "sha256:img090",
				"com.docker.compose.config-hash": "abc", "org.opencontainers.image.version": "0.9.0",
			},
		},
		"HostConfig": map[string]any{
			"Binds": []string{"/srv/openlog:/data:rw"}, "NetworkMode": "proj_default",
			"PortBindings":  map[string]any{"8080/tcp": []any{map[string]string{"HostPort": "8080"}}},
			"RestartPolicy": map[string]any{"Name": "unless-stopped"},
		},
		"NetworkSettings": map[string]any{"Networks": map[string]any{
			"proj_default": map[string]any{"Aliases": []string{"proj-openlog-1", "openlog", oldID[:12]}, "IPAddress": "172.18.0.5"},
			"proj_extra":   map[string]any{"Aliases": []string{"openlog"}},
		}},
	}, &app)
	fd.add(app, true)
	var pg Container
	roundTrip(map[string]any{"Id": "pg1", "Name": "/proj-postgres-1", "Config": map[string]any{"Image": "postgres:16",
		"Labels": map[string]string{labelProject: "proj", labelService: "postgres"}}}, &pg)
	fd.add(pg, true)

	envFile := filepath.Join(dir, ".env")
	if err := os.WriteFile(envFile, []byte("OPENLOG_LOG_LEVEL=info\nOPENLOG_IMAGE=openlog:dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(func(k string) string {
		return map[string]string{
			"OPENLOG_UPDATER_MODE": "auto", "OPENLOG_UPDATER_COMPOSE_PROJECT": "proj",
			"OPENLOG_UPDATER_BACKUP_DIR": filepath.Join(dir, "backups"), "OPENLOG_UPDATER_BACKUP_KEEP": "2",
			"OPENLOG_UPDATER_HEALTH_TIMEOUT": "300ms", "OPENLOG_UPDATER_ENV_FILE": envFile,
		}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	health := func(_ context.Context, _ string) (string, bool, error) {
		fd.mu.Lock()
		defer fd.mu.Unlock()
		c := fd.byName("proj-openlog-1")
		if c == nil || !c.running {
			return "", false, errors.New("connection refused")
		}
		return versions[c.c.Image], c.c.Image != "sha256:img092", nil
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := &ComposeEngine{Cfg: cfg, Docker: fd, Log: log, Health: health, Poll: 10 * time.Millisecond}
	src := &fakeSource{
		channels: map[string][]string{"stable": {"0.9.0", "0.9.1"}},
		manifests: map[string]string{
			"0.9.1": manifestJSON("0.9.1", "stable", "0.9.0", img091),
			"0.9.2": manifestJSON("0.9.2", "stable", "0.9.1", img092),
		},
	}
	store, audit := &memStore{}, &recordAudit{}
	r := &Runner{Cfg: cfg, Engine: eng, Source: src, Store: store, Audit: audit, Log: log}
	return &composeFixture{docker: fd, engine: eng, runner: r, store: store, audit: audit, source: src, envFile: envFile, oldID: oldID}
}

func TestComposeUpdateAndRollback(t *testing.T) {
	f := newComposeFixture(t)
	ctx := context.Background()

	// N -> N+1 succeeds.
	if err := f.runner.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	st := f.store.st
	if st.State != StateSucceeded || st.CurrentVersion != "0.9.1" || st.PreviousVersion != "0.9.0" {
		t.Fatalf("status %+v", st)
	}
	var steps []string
	for _, s := range st.Steps {
		steps = append(steps, s.Name+"="+s.Status)
	}
	if want := "backup=ok pull=ok migrate=ok recreate=ok health=ok cleanup=ok contract-migrate=ok"; strings.Join(steps, " ") != want {
		t.Errorf("steps %v, want %s", steps, want)
	}
	if b, err := os.ReadFile(st.BackupFile); err != nil || !strings.HasPrefix(string(b), "PGDMP pg_dump -U openlog -d openlog -Fc") {
		t.Errorf("backup %s: %q %v", st.BackupFile, b, err)
	}
	if !slices.Equal(f.docker.pulled, []string{img091}) {
		t.Errorf("pulled %v", f.docker.pulled)
	}
	if f.docker.containers[f.oldID] != nil {
		t.Error("old container was not removed after a verified update")
	}
	// Migrate ran twice (expand before, contract after) with the app's env and network.
	var migrates, apps []ContainerSpec
	for _, c := range f.docker.created {
		if strings.Contains(c.Name, "updater-migrate") {
			migrates = append(migrates, c)
		} else {
			apps = append(apps, c)
		}
	}
	if len(migrates) != 2 || len(apps) != 1 {
		t.Fatalf("created migrate=%d app=%d", len(migrates), len(apps))
	}
	if env := toStrings(migrates[0].Config["Env"]); !slices.Equal(env, []string{"OPENLOG_POSTGRES_DSN=postgres://openlog@postgres/openlog"}) {
		t.Errorf("migrate env %v", env)
	}
	if migrates[0].HostConfig["NetworkMode"] != "proj_default" || migrates[0].Config["Image"] != img091 {
		t.Errorf("migrate spec %+v", migrates[0])
	}
	// The recreated container keeps compose settings and drops old-image defaults.
	app := apps[0]
	labels := app.Config["Labels"].(map[string]any)
	switch {
	case app.Name != "proj-openlog-1" || app.Config["Image"] != img091:
		t.Errorf("name/image %s %v", app.Name, app.Config["Image"])
	case labels[labelProject] != "proj" || labels["com.docker.compose.config-hash"] != "abc" || labels[labelImage] != "sha256:img091":
		t.Errorf("labels %v", labels)
	case labels["org.opencontainers.image.version"] != nil:
		t.Errorf("old image label kept: %v", labels)
	case app.Config["Hostname"] != nil || app.Config["Entrypoint"] != nil:
		t.Errorf("hostname/entrypoint from old container kept: %v", app.Config)
	case !slices.Equal(toStrings(app.Config["Env"]), []string{"OPENLOG_POSTGRES_DSN=postgres://openlog@postgres/openlog"}):
		t.Errorf("env %v", app.Config["Env"])
	case !slices.Equal(toStrings(app.HostConfig["Binds"]), []string{"/srv/openlog:/data:rw"}) || app.HostConfig["PortBindings"] == nil:
		t.Errorf("host config %v", app.HostConfig)
	case !slices.Equal(app.NetworkOrder, []string{"proj_default", "proj_extra"}):
		t.Errorf("network order %v", app.NetworkOrder)
	case !slices.Equal(toStrings(app.Networks["proj_default"]["Aliases"]), []string{"proj-openlog-1", "openlog"}) || app.Networks["proj_default"]["IPAddress"] != nil:
		t.Errorf("endpoint %v", app.Networks["proj_default"])
	}
	if b, _ := os.ReadFile(f.envFile); !strings.Contains(string(b), "OPENLOG_IMAGE="+img091+"\n") || !strings.Contains(string(b), "OPENLOG_LOG_LEVEL=info") {
		t.Errorf(".env not rewritten: %s", b)
	}
	if !slices.Equal(f.audit.actions, []string{"updater.update_started", "updater.update_succeeded"}) {
		t.Errorf("audit %v", f.audit.actions)
	}

	// Up to date: nothing happens.
	created := len(f.docker.created)
	if err := f.runner.RunOnce(ctx); err != nil || f.store.st.State != StateUpToDate || len(f.docker.created) != created {
		t.Fatalf("second run: %v %+v", err, f.store.st)
	}

	// N+2 never becomes ready: rolled back to N+1, remembered as failed.
	f.source.channels["stable"] = append(f.source.channels["stable"], "0.9.2")
	good := f.docker.byName("proj-openlog-1").c.ID
	if err := f.runner.RunOnce(ctx); err == nil {
		t.Fatal("expected failure")
	}
	st = f.store.st
	if st.State != StateRolledBack || !slices.Contains(st.FailedVersions, "0.9.2") || !strings.Contains(st.Error, "health") {
		t.Fatalf("status %+v", st)
	}
	cur := f.docker.byName("proj-openlog-1")
	if cur == nil || cur.c.ID != good || !cur.running || f.docker.byName("proj-openlog-1"+preUpdateSuffix) != nil {
		t.Fatalf("previous container not restored: %+v", cur)
	}
	if len(f.docker.containers) != 2 { // app + postgres
		t.Errorf("leftover containers: %d", len(f.docker.containers))
	}
	if st.History[0].Result != StateRolledBack || st.History[1].Result != StateSucceeded {
		t.Errorf("history %+v", st.History)
	}
	// The failed version is not retried.
	if err := f.runner.RunOnce(ctx); err != nil || f.store.st.State != StateUpToDate || !strings.Contains(f.store.st.Message, "failed before") {
		t.Fatalf("retry: %v %+v", err, f.store.st)
	}
}

func TestComposeFailuresBeforeRecreate(t *testing.T) {
	ctx := context.Background()
	t.Run("migrate", func(t *testing.T) {
		f := newComposeFixture(t)
		f.docker.migrateExit = 1
		if err := f.runner.RunOnce(ctx); err == nil || !strings.Contains(err.Error(), "migrate") {
			t.Fatalf("err %v", err)
		}
		if f.store.st.State != StateFailed {
			t.Errorf("state %s", f.store.st.State)
		}
		if c := f.docker.containers[f.oldID]; c == nil || !c.running || c.c.Config["Image"] != img090 {
			t.Error("running container was touched")
		}
	})
	t.Run("backup", func(t *testing.T) {
		f := newComposeFixture(t)
		f.docker.execCode = 1
		if err := f.runner.RunOnce(ctx); err == nil || !strings.Contains(err.Error(), "pg_dump exited 1") {
			t.Fatalf("err %v", err)
		}
		if len(f.docker.pulled) != 0 || f.store.st.State != StateFailed {
			t.Errorf("continued after failed backup: %+v", f.store.st)
		}
	})
	t.Run("pull", func(t *testing.T) {
		f := newComposeFixture(t)
		f.docker.pullErr = errors.New("unauthorized")
		if err := f.runner.RunOnce(ctx); err == nil || f.store.st.State != StateFailed {
			t.Fatalf("err %v state %s", err, f.store.st.State)
		}
	})
}

func TestComposeModesAndWindow(t *testing.T) {
	ctx := context.Background()
	f := newComposeFixture(t)
	f.runner.Cfg.Mode = ModeNotify
	if err := f.runner.RunOnce(ctx); err != nil || f.store.st.State != StateAvailable || f.store.st.TargetVersion != "0.9.1" {
		t.Fatalf("notify: %v %+v", err, f.store.st)
	}
	if len(f.docker.created) != 0 || !slices.Equal(f.audit.actions, []string{"updater.update_available"}) {
		t.Errorf("notify changed something: %v %v", f.docker.created, f.audit.actions)
	}
	f.runner.Cfg.Mode = ModeAuto
	f.runner.Cfg.MaintenanceWindows, _ = ParseWindows("sun 02:00-03:00")
	f.runner.Now = func() time.Time { return time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC) } // Wednesday
	if err := f.runner.RunOnce(ctx); err != nil || f.store.st.State != StateWaiting || len(f.docker.created) != 0 {
		t.Fatalf("window: %v %+v", err, f.store.st)
	}
	f.runner.Cfg.Mode = ModeOff
	if err := f.runner.RunOnce(ctx); err != nil || f.store.st.State != StateOff {
		t.Fatalf("off: %v %+v", err, f.store.st)
	}
	f.runner.Cfg.Mode = ModeAuto
	f.source.indexErr = errors.New("index: release: signature does not verify")
	if err := f.runner.RunOnce(ctx); err == nil || f.store.st.State != StateError || !strings.Contains(f.store.st.Error, "signature") {
		t.Fatalf("bad index: %v %+v", err, f.store.st)
	}
}

func TestComposeRecover(t *testing.T) {
	f := newComposeFixture(t)
	ctx := context.Background()
	// Simulate a crash after the new container was started but before it was verified.
	if _, err := f.engine.recreate(ctx, f.oldID, img091); err != nil {
		t.Fatal(err)
	}
	if f.docker.byName("proj-openlog-1"+preUpdateSuffix) == nil {
		t.Fatal("pre-update container missing")
	}
	if err := f.engine.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	c := f.docker.byName("proj-openlog-1")
	if c == nil || c.c.ID != f.oldID || !c.running || len(f.docker.containers) != 2 {
		t.Fatalf("not recovered: %+v (%d containers)", c, len(f.docker.containers))
	}
}

func TestBackupRetention(t *testing.T) {
	f := newComposeFixture(t)
	for i := 0; i < 4; i++ {
		now := time.Date(2026, 9, 13, 10, i, 0, 0, time.UTC)
		f.engine.Now = func() time.Time { return now }
		if _, err := f.engine.backup(context.Background(), "0.9.0"); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(f.engine.Cfg.BackupDir, "openlog-*.dump"))
	if len(files) != 2 || !strings.Contains(files[1], "T100300Z") {
		t.Errorf("kept %v", files)
	}
}

func TestRewriteEnvFileAppends(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	_ = os.WriteFile(p, []byte("A=1\n"), 0o644)
	if err := rewriteEnvFile(p, "OPENLOG_IMAGE", "x@sha256:1"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "A=1\n# set by openlog-updater\nOPENLOG_IMAGE=x@sha256:1\n" {
		t.Errorf("got %q", b)
	}
}
