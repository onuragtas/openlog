package containers

import (
	"context"
	"errors"
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
)

const dockerList = `[
 {"Id":"bbbb000000000000000000000000000000000000000000000000000000000002","Names":["/cache"],"Image":"redis:7.2-alpine",
  "ImageID":"sha256:1111","Created":1704067200,"State":"running","Labels":{"com.docker.compose.project":"shop","big":"` + "xxxxxxxxxx" + `"},
  "Ports":[{"IP":"0.0.0.0","PrivatePort":6379,"PublicPort":6379,"Type":"tcp"},{"IP":"::","PrivatePort":6379,"PublicPort":6379,"Type":"tcp"},{"IP":"0.0.0.0","PrivatePort":6379,"PublicPort":6379,"Type":"tcp"}]},
 {"Id":"aaaa000000000000000000000000000000000000000000000000000000000001","Names":["/old"],"Image":"sha256:deadbeef","ImageID":"sha256:deadbeef",
  "Created":0,"State":"exited","Labels":null,"Ports":[]}
]`

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
