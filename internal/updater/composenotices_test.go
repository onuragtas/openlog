package updater

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// pendingDefinition is a recorded definition change of the openlog service, with the config hash the container
// carried when the bundle was installed.
func pendingDefinition(hash string) *Status {
	return &Status{ComposeChanges: &ComposeChanges{
		Version:     "0.9.1",
		InstalledAt: time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC),
		Services:    []PendingService{{Service: "openlog", Reason: "definition", ConfigHash: hash}},
	}}
}

func writeCompose(t *testing.T, f *composeFixture, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(filepath.Dir(f.envFile), "docker-compose.yml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A pending change that the running container already satisfies is not pending at all: the operator re-ran
// install-server.sh, docker said "Running" because there was nothing to change, and the notice used to stay
// forever because the config hash never moved. Found on the owner's server, stuck since 0.1.113.
func TestPendingDefinitionClearedWhenContainerAlreadyMatches(t *testing.T) {
	f := newComposeFixture(t)
	writeCompose(t, f, "services:\n  openlog:\n    volumes: [\"./clickhouse/a.xml:/etc/a.xml:ro\"]\n    ports: [\"8080:8080\"]\n")
	c := f.docker.containers[f.oldID]
	c.c.Mounts = []Mount{{
		Type:        "bind",
		Source:      filepath.Join(filepath.Dir(f.envFile), "clickhouse/a.xml"),
		Destination: "/etc/a.xml",
	}}

	st := pendingDefinition("abc") // the hash the container still has
	if n := f.engine.Notices(context.Background(), "0.9.0", st); len(n) != 0 || st.ComposeChanges != nil {
		t.Errorf("already applied: %+v %+v", n, st.ComposeChanges)
	}
}

func TestPendingDefinitionKeptWhileTheMountIsMissing(t *testing.T) {
	f := newComposeFixture(t)
	writeCompose(t, f, "services:\n  openlog:\n    volumes: [\"./clickhouse/a.xml:/etc/a.xml:ro\"]\n")

	st := pendingDefinition("abc")
	if n := f.engine.Notices(context.Background(), "0.9.0", st); len(n) != 1 || st.ComposeChanges == nil {
		t.Fatalf("not applied: %+v", n)
	}
	// And it goes when the container is recreated with it.
	c := f.docker.containers[f.oldID]
	c.c.Mounts = []Mount{{
		Type:        "bind",
		Source:      filepath.Join(filepath.Dir(f.envFile), "clickhouse/a.xml"),
		Destination: "/etc/a.xml",
	}}
	if n := f.engine.Notices(context.Background(), "0.9.0", st); len(n) != 0 || st.ComposeChanges != nil {
		t.Errorf("after recreate: %+v %+v", n, st.ComposeChanges)
	}
}

func TestPendingDefinitionKeptWhileAPortIsNotPublished(t *testing.T) {
	f := newComposeFixture(t)
	// The container of the fixture publishes 8080 and nothing else.
	writeCompose(t, f, "services:\n  openlog:\n    ports: [\"9000:9000\"]\n")

	st := pendingDefinition("abc")
	if n := f.engine.Notices(context.Background(), "0.9.0", st); len(n) != 1 {
		t.Fatalf("missing port: %+v", n)
	}
	writeCompose(t, f, "services:\n  openlog:\n    ports: [\"8080:8080\"]\n")
	if n := f.engine.Notices(context.Background(), "0.9.0", st); len(n) != 0 || st.ComposeChanges != nil {
		t.Errorf("published: %+v %+v", n, st.ComposeChanges)
	}
}

// What the file says but this cannot read -- a port range, a variable, no compose file at all -- falls back to
// the config hash rather than being guessed either way.
func TestPendingDefinitionFallsBackToTheConfigHash(t *testing.T) {
	f := newComposeFixture(t)
	writeCompose(t, f, "services:\n  openlog:\n    ports: [\"9000-9005:9000-9005\"]\n")

	st := pendingDefinition("abc")
	if n := f.engine.Notices(context.Background(), "0.9.0", st); len(n) != 1 {
		t.Fatalf("hash unchanged: %+v", n)
	}
	f.docker.containers[f.oldID].c.Config["Labels"].(map[string]any)["com.docker.compose.config-hash"] = "def"
	if n := f.engine.Notices(context.Background(), "0.9.0", st); len(n) != 0 || st.ComposeChanges != nil {
		t.Errorf("hash changed: %+v %+v", n, st.ComposeChanges)
	}

	// No compose file for the service either: the hash is all there is.
	f2 := newComposeFixture(t)
	writeCompose(t, f2, "services:\n  other:\n    image: x\n")
	st2 := pendingDefinition("abc")
	if n := f2.engine.Notices(context.Background(), "0.9.0", st2); len(n) != 1 {
		t.Fatalf("unknown service, hash unchanged: %+v", n)
	}
}

func TestVolumeTargetAndPublishedPort(t *testing.T) {
	// The .env of the installation, as the engine reads it.
	env := map[string]string{"DIR": "/srv/data"}
	expand := func(s string) (string, bool) {
		return interpolate(s, func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	}
	cases := []struct {
		in       any
		dst, src string
		ok       bool
	}{
		{"./a.xml:/etc/a.xml:ro", "/etc/a.xml", "./a.xml", true},
		{"data:/var/lib/data", "/var/lib/data", "data", true},
		{"/var/lib/data", "/var/lib/data", "", true},
		// A variable the .env defines is expanded, as compose expands it when the stack is started; one it
		// does not define (compose would read it from the shell) is not understood.
		{"${DIR}:/data", "/data", "/srv/data", true},
		// Unresolvable (compose would also look at the shell): the mount point still is, and that is what a
		// pending change is about. The source is then not compared.
		{"${OTHER:-./clickhouse/storage-none.xml}:/etc/s.xml:ro", "/etc/s.xml", "", true},
		{"${NOPE}:/data", "/data", "", true},
		{map[string]any{"type": "bind", "source": "/a", "target": "/b"}, "/b", "/a", true},
		{map[string]any{"type": "volume"}, "", "", false},
		{42, "", "", false},
	}
	for _, c := range cases {
		dst, src, ok := volumeTarget(c.in, expand)
		if ok != c.ok || (ok && (dst != c.dst || src != c.src)) {
			t.Errorf("volumeTarget(%v) = %q %q %v, want %q %q %v", c.in, dst, src, ok, c.dst, c.src, c.ok)
		}
	}

	ports := []struct {
		in   any
		want string
		ok   bool
	}{
		{"8080:8080", "8080/tcp", true},
		{"127.0.0.1:9000:9000", "9000/tcp", true},
		{"5514:5514/udp", "5514/udp", true},
		{9000, "9000/tcp", true},
		{"9000-9005:9000-9005", "", false},
		// The host port may be a variable; the container port may not.
		{"${PORT}:9000", "9000/tcp", true},
		{map[string]any{"target": 9000, "protocol": "udp"}, "9000/udp", true},
		{map[string]any{"published": "9000"}, "", false},
	}
	for _, c := range ports {
		got, ok := publishedPort(c.in, expand)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("publishedPort(%v) = %q %v, want %q %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// The owner's installation, as it stood on 2026-10-11: the ClickHouse service of the shipped compose file --
// three of its eight volumes and both of its ports are variables -- against the container that is actually
// running, which already has every mount the file asks for. The notice had been stuck since 0.1.113 because
// the config hash of a container that needs no change never changes.
func TestPendingDefinitionClearedForTheShippedClickHouseService(t *testing.T) {
	f := newComposeFixture(t)
	dir := filepath.Dir(f.envFile)
	writeCompose(t, f, `services:
  clickhouse:
    volumes:
      - ./clickhouse/openlog-cluster.xml:/etc/clickhouse-server/config.d/openlog-cluster.xml:ro
      - ./clickhouse/openlog-system-logs.xml:/etc/clickhouse-server/config.d/openlog-system-logs.xml:ro
      - ./clickhouse/openlog-reader.xml:/etc/clickhouse-server/users.d/openlog-reader.xml:ro
      - clickhouse-data:/var/lib/clickhouse
      - ${OPENLOG_CLICKHOUSE_STORAGE_CONFIG:-./clickhouse/storage-none.xml}:/etc/clickhouse-server/config.d/openlog-storage.xml:ro
      - ${OPENLOG_CLICKHOUSE_STORAGE_WARM_CONFIG:-./clickhouse/storage-none.xml}:/etc/clickhouse-server/config.d/openlog-storage-warm.xml:ro
      - ${OPENLOG_CLICKHOUSE_S3_CREDENTIALS_FILE:-./clickhouse/storage-none.xml}:/etc/clickhouse-server/config.d/zz-openlog-s3-credentials.xml:ro
      - clickhouse-warm:/var/lib/clickhouse-warm
    ports:
      - 127.0.0.1:${CLICKHOUSE_HTTP_HOST_PORT:-8123}:8123
      - 127.0.0.1:${CLICKHOUSE_NATIVE_HOST_PORT:-9000}:9000
`)
	bind := func(name, dst string) Mount {
		return Mount{Type: "bind", Source: filepath.Join(dir, "clickhouse", name), Destination: dst}
	}
	var ch Container
	roundTrip(map[string]any{
		"Id": "ch1", "Name": "/proj-clickhouse-1",
		"Config": map[string]any{"Image": "clickhouse/clickhouse-server:25.8", "Labels": map[string]string{
			labelProject: "proj", labelService: "clickhouse", "com.docker.compose.config-hash": "221c0f",
		}},
		"HostConfig": map[string]any{"PortBindings": map[string]any{
			"8123/tcp": []any{map[string]string{"HostIp": "127.0.0.1", "HostPort": "8123"}},
			"9000/tcp": []any{map[string]string{"HostIp": "127.0.0.1", "HostPort": "9000"}},
		}},
		"Mounts": []Mount{
			bind("openlog-cluster.xml", "/etc/clickhouse-server/config.d/openlog-cluster.xml"),
			bind("openlog-system-logs.xml", "/etc/clickhouse-server/config.d/openlog-system-logs.xml"),
			bind("openlog-reader.xml", "/etc/clickhouse-server/users.d/openlog-reader.xml"),
			bind("storage-none.xml", "/etc/clickhouse-server/config.d/openlog-storage.xml"),
			bind("storage-none.xml", "/etc/clickhouse-server/config.d/openlog-storage-warm.xml"),
			bind("storage-none.xml", "/etc/clickhouse-server/config.d/zz-openlog-s3-credentials.xml"),
			{Type: "volume", Name: "proj_clickhouse-data", Destination: "/var/lib/clickhouse"},
			{Type: "volume", Name: "proj_clickhouse-warm", Destination: "/var/lib/clickhouse-warm"},
		},
	}, &ch)
	f.docker.add(ch, true)

	st := &Status{ComposeChanges: &ComposeChanges{
		Version:     "0.1.113",
		InstalledAt: time.Date(2026, 10, 8, 21, 33, 47, 0, time.UTC),
		Services:    []PendingService{{Service: "clickhouse", Reason: "definition", ConfigHash: "221c0f"}},
	}}
	if n := f.engine.Notices(context.Background(), "0.9.0", st); len(n) != 0 || st.ComposeChanges != nil {
		t.Errorf("stuck notice: %+v %+v", n, st.ComposeChanges)
	}

	// And it is still reported when a mount really is missing.
	f.docker.containers["ch1"].c.Mounts = f.docker.containers["ch1"].c.Mounts[1:]
	st.ComposeChanges = &ComposeChanges{Version: "0.1.113", Services: []PendingService{{Service: "clickhouse", Reason: "definition", ConfigHash: "221c0f"}}}
	if n := f.engine.Notices(context.Background(), "0.9.0", st); len(n) != 1 {
		t.Errorf("missing mount: %+v", n)
	}
}
