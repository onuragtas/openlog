package containers

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
	"github.com/onuragtas/openlog/agents/infra/internal/hostfs/hostfstest"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

const dockerList = `[
 {"Id":"bbbb000000000000000000000000000000000000000000000000000000000002","Names":["/cache"],"Image":"redis:7.2-alpine",
  "ImageID":"sha256:1111","Created":1704067200,"State":"running","Labels":{"com.docker.compose.project":"shop","big":"` + "xxxxxxxxxx" + `"},
  "Ports":[{"IP":"0.0.0.0","PrivatePort":6379,"PublicPort":6379,"Type":"tcp"},{"IP":"::","PrivatePort":6379,"PublicPort":6379,"Type":"tcp"},{"IP":"0.0.0.0","PrivatePort":6379,"PublicPort":6379,"Type":"tcp"}]},
 {"Id":"aaaa000000000000000000000000000000000000000000000000000000000001","Names":["/old"],"Image":"sha256:deadbeef","ImageID":"sha256:deadbeef",
  "Created":0,"State":"exited","Labels":null,"Ports":[]}
]`

const dockerInspect = `{"Id":"bbbb","RestartCount":3,"LogPath":"/var/lib/docker/containers/bbbb/bbbb-json.log",
 "State":{"Status":"running","StartedAt":"2026-09-14T09:00:00.5Z","FinishedAt":"0001-01-01T00:00:00Z","ExitCode":0,"Health":{"Status":"unhealthy"}},
 "HostConfig":{"LogConfig":{"Type":"json-file","Config":{}}},"Config":{"Tty":false}}`

func frame(stream byte, payload string) []byte {
	b := []byte{stream, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(b[4:], uint32(len(payload)))
	return append(b, payload...)
}

func TestParseInspectAndHealth(t *testing.T) {
	d, err := ParseInspect([]byte(dockerInspect))
	if err != nil {
		t.Fatal(err)
	}
	if d.RestartCount != 3 || !d.FinishedAt.IsZero() || d.StartedAt.Format(time.RFC3339Nano) != "2026-09-14T09:00:00.5Z" || d.Health != "unhealthy" {
		t.Errorf("details = %+v", d)
	}
	if d, _ := ParseInspect([]byte(`{"State":{"Health":{"Status":"none"}}}`)); d.Health != "" {
		t.Errorf("health none = %q", d.Health)
	}
	for status, want := range map[string]string{"Up 2 hours (healthy)": "healthy", "Up 1 second (health: starting)": "starting",
		"Up 5 minutes (unhealthy)": "unhealthy", "Exited (0) 3 days ago": ""} {
		if got := HealthFromStatus(status); got != want {
			t.Errorf("%q → %q", status, got)
		}
	}
	c := Container{State: "exited"}
	c.Apply(Details{ExitCode: 137, FinishedAt: time.Date(2026, 9, 14, 1, 2, 3, 0, time.UTC)})
	if c.ExitCode != 137 || c.FinishedAt != "2026-09-14T01:02:03Z" || !c.Inspected() {
		t.Errorf("apply = %+v", c)
	}
}

func TestAttributes(t *testing.T) {
	kv := func(attrs []*commonpb.KeyValue) map[string]string {
		m := map[string]string{}
		for _, a := range attrs {
			if arr := a.Value.GetArrayValue(); arr != nil {
				m[a.Key] = arr.Values[0].GetStringValue()
				continue
			}
			m[a.Key] = a.Value.GetStringValue()
		}
		return m
	}
	id := strings.Repeat("a", 64)
	if got := kv(Attributes(id, "containerd", nil)); !reflect.DeepEqual(got, map[string]string{"container.id": id, "container.runtime": "containerd"}) {
		t.Errorf("without metadata: %v", got)
	}
	meta := &Container{ID: id, Name: "shop-orders-1", Runtime: "docker", Image: "openlog-apmdemo/orders:1", Labels: map[string]string{
		"com.docker.compose.project": "shop", "com.docker.compose.service": "orders",
		"io.kubernetes.pod.name": "orders-7d9", "io.kubernetes.pod.namespace": "prod", "io.kubernetes.container.name": "app"}}
	want := map[string]string{"container.id": id, "container.name": "shop-orders-1", "container.image.name": "openlog-apmdemo/orders",
		"container.image.tags": "1", "container.runtime": "docker", "docker.compose.project": "shop", "docker.compose.service": "orders",
		"k8s.pod.name": "orders-7d9", "k8s.namespace.name": "prod", "k8s.container.name": "app"}
	if got := kv(Attributes(id, "", meta)); !reflect.DeepEqual(got, want) {
		t.Errorf("with metadata: %v", got)
	}
}

func TestFrameReaderAndTimestamps(t *testing.T) {
	raw := NewFrameReader(strings.NewReader("tty output\n"), true)
	if s, p, err := raw.Next(); err != nil || s != StreamStdout || string(p) != "tty output\n" {
		t.Errorf("raw = %d %q %v", s, p, err)
	}
	if _, _, err := raw.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("raw EOF = %v", err)
	}
	big := []byte{1, 0, 0, 0, 0xff, 0xff, 0xff, 0xff}
	if _, _, err := NewFrameReader(bytes.NewReader(big), false).Next(); !errors.Is(err, ErrFrameTooLarge) {
		t.Errorf("oversized frame = %v", err)
	}
	if _, _, err := NewFrameReader(bytes.NewReader(frame(1, "abc")[:9]), false).Next(); !errors.Is(err, io.EOF) {
		t.Errorf("truncated frame = %v", err)
	}
	ts, rest, ok := SplitTimestamp([]byte("2026-09-14T10:00:00.123456789Z GET / 200"))
	if !ok || string(rest) != "GET / 200" || ts.Nanosecond() != 123456789 {
		t.Errorf("split = %v %q %v", ts, rest, ok)
	}
	if _, rest, ok := SplitTimestamp([]byte("no timestamp here")); ok || string(rest) != "no timestamp here" {
		t.Error("line without timestamp")
	}
	if got := SinceParam(time.Unix(1757757600, 5)); got != "1757757600.000000005" || SinceParam(time.Time{}) != "0" {
		t.Errorf("since = %s", got)
	}
}

func TestParseDockerList(t *testing.T) {
	cs, err := ParseDockerList([]byte(strings.Replace(dockerList, "xxxxxxxxxx", strings.Repeat("é", 200), 1)))
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || cs[0].Name != "old" || cs[0].Created != "" || cs[0].Labels == nil || cs[0].Ports == nil {
		t.Fatalf("containers = %+v", cs)
	}
	c := cs[1]
	if c.Name != "cache" || c.Runtime != "docker" || c.Image != "redis:7.2-alpine" || c.State != "running" || c.Created != "2024-01-01T00:00:00Z" {
		t.Errorf("container = %+v", c)
	}
	if len(c.Ports) != 2 || c.Ports[0].IP != "0.0.0.0" || c.Ports[1].IP != "::" || c.Ports[0].Protocol != "tcp" {
		t.Errorf("ports = %+v", c.Ports)
	}
	if v := c.Labels["big"]; len(v) > MaxLabelValueBytes || !strings.HasPrefix(v, "é") || strings.ContainsRune(v, '�') {
		t.Errorf("label not truncated at a rune boundary: %d bytes", len(v))
	}
}

func TestImageName(t *testing.T) {
	cases := map[string]struct {
		name string
		tags []string
	}{
		"docker.io/library/nginx:1.25":  {"docker.io/library/nginx", []string{"1.25"}},
		"localhost:5000/app":            {"localhost:5000/app", nil},
		"localhost:5000/app:v2":         {"localhost:5000/app", []string{"v2"}},
		"redis@sha256:abcd":             {"redis", nil},
		"sha256:0123":                   {"sha256:0123", nil},
		"postgres":                      {"postgres", nil},
		"ghcr.io/org/img:1.0@sha256:ff": {"ghcr.io/org/img", []string{"1.0"}},
	}
	for in, want := range cases {
		n, tags := ImageName(in)
		if n != want.name || !reflect.DeepEqual(tags, want.tags) {
			t.Errorf("%s → %q %v", in, n, tags)
		}
	}
}

func TestFindCgroups(t *testing.T) {
	id1 := strings.Repeat("a", 64)
	id2 := strings.Repeat("b", 64)
	id3 := strings.Repeat("c", 64)
	fs := hostfstest.Build(t, map[string]string{
		"/sys/fs/cgroup/cgroup.controllers":                                                  "cpu io memory\n",
		"/sys/fs/cgroup/system.slice/docker-" + id1 + ".scope/cpu.stat":                      "usage_usec 1\n",
		"/sys/fs/cgroup/system.slice/docker-" + id1 + ".scope/init.scope/" + id3 + "/x":      "nested",
		"/sys/fs/cgroup/docker/" + id2 + "/cpu.stat":                                         "usage_usec 1\n",
		"/sys/fs/cgroup/kubepods.slice/kubepods-pod1.slice/cri-containerd-" + id3 + ".scope": "<dir>",
		"/sys/fs/cgroup/system.slice/ssh.service/cpu.stat":                                   "usage_usec 1\n",
	})
	got := FindCgroups(fs)
	if len(got) != 3 {
		t.Fatalf("cgroups = %+v", got)
	}
	if got[id1].Runtime != "docker" || got[id1].Path != "/sys/fs/cgroup/system.slice/docker-"+id1+".scope" {
		t.Errorf("id1 = %+v", got[id1])
	}
	if got[id2].Runtime != "docker" || got[id3].Runtime != "containerd" {
		t.Errorf("runtimes = %+v %+v", got[id2], got[id3])
	}
	if FindCgroups(hostfstest.Build(t, map[string]string{"/sys/fs/cgroup/cpu/x": "v1"})) != nil {
		t.Error("cgroup v1 must yield nil")
	}
}

// shortTempDir returns a directory whose paths fit the unix socket limit (104 bytes on macOS).
func shortTempDir(t *testing.T) string {
	dir, err := os.MkdirTemp("/tmp", "olc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func serveDocker(t *testing.T, sock string) {
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/containers/bbbb000000000000000000000000000000000000000000000000000000000002/json":
			_, _ = w.Write([]byte(dockerInspect))
			return
		case r.URL.Path == "/containers/bbbb000000000000000000000000000000000000000000000000000000000002/logs":
			// Two multiplexed frames: a stdout line and a stderr line split over two frames.
			_, _ = w.Write(frame(StreamStdout, "2026-09-14T10:00:00.000000001Z hello\n"))
			_, _ = w.Write(frame(StreamStderr, "2026-09-14T10:00:01Z oops"))
			_, _ = w.Write(frame(StreamStderr, "\n"))
			return
		}
		if r.URL.Path != "/containers/json" || r.URL.Query().Get("all") != "1" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(dockerList))
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
}

func TestSourceList(t *testing.T) {
	root := shortTempDir(t)
	// Socket only at /run/docker.sock: the /var/run path falls back to it.
	serveDocker(t, filepath.Join(root, "run/docker.sock"))
	src := NewSource(hostfs.New(root), "/var/run/docker.sock")
	cs, err := src.List(context.Background(), time.Minute)
	if err != nil || len(cs) != 2 {
		t.Fatalf("list = %v %v", cs, err)
	}
	fp := src.Fingerprint()
	if cached, _ := src.List(context.Background(), time.Minute); len(cached) != 2 || src.Fingerprint() != fp || fp == 0 {
		t.Error("cache/fingerprint")
	}
	// The running container was inspected (the exited one's inspect fails and keeps the list data).
	c := cs[1]
	if !c.Inspected() || c.RestartCount != 3 || c.StartedAt != "2026-09-14T09:00:00.5Z" || c.Health != "unhealthy" ||
		c.LogDriver != "json-file" || c.LogPath != "/var/lib/docker/containers/bbbb/bbbb-json.log" || c.Tty {
		t.Errorf("inspected container = %+v", c)
	}
	if cs[0].Inspected() {
		t.Errorf("exited container = %+v", cs[0])
	}

	body, err := src.Stream(context.Background(), "/containers/"+c.ID+"/logs?follow=1&stdout=1&stderr=1&timestamps=1")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	fr := NewFrameReader(body, false)
	var got []string
	for {
		stream, p, err := fr.Next()
		if err != nil {
			break
		}
		got = append(got, fmt.Sprintf("%d:%s", stream, p))
	}
	want := []string{"1:2026-09-14T10:00:00.000000001Z hello\n", "2:2026-09-14T10:00:01Z oops", "2:\n"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("frames = %q", got)
	}
	if _, err := src.Stream(context.Background(), "/containers/nope/logs"); err == nil {
		t.Error("stream of an unknown container must fail")
	}

	empty := NewSource(hostfs.New(shortTempDir(t)), "/var/run/docker.sock")
	if _, err := empty.List(context.Background(), 0); !errors.Is(err, ErrNoRuntime) {
		t.Errorf("no socket: %v", err)
	}
}

func TestSourcePermission(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses socket permissions")
	}
	root := shortTempDir(t)
	sock := filepath.Join(root, "var/run/docker.sock")
	serveDocker(t, sock)
	if err := os.Chmod(sock, 0); err != nil {
		t.Fatal(err)
	}
	denied := 0
	src := NewSource(hostfs.New(root), "/var/run/docker.sock")
	src.Permission = func() { denied++ }
	if _, err := src.List(context.Background(), 0); err == nil || errors.Is(err, ErrNoRuntime) || denied != 1 {
		t.Errorf("err = %v denied = %d", err, denied)
	}
}
