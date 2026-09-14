package containers

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"

	"google.golang.org/protobuf/encoding/protowire"
)

func pbStr(b []byte, num protowire.Number, s string) []byte {
	return protowire.AppendString(protowire.AppendTag(b, num, protowire.BytesType), s)
}

func pbMsg(b []byte, num protowire.Number, msg []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(b, num, protowire.BytesType), msg)
}

func pbVarint(b []byte, num protowire.Number, v uint64) []byte {
	return protowire.AppendVarint(protowire.AppendTag(b, num, protowire.VarintType), v)
}

func pbMap(b []byte, num protowire.Number, k, v string) []byte {
	return pbMsg(b, num, pbStr(pbStr(nil, 1, k), 2, v))
}

var (
	criRunningID = "cccc000000000000000000000000000000000000000000000000000000000003"
	criExitedID  = "dddd000000000000000000000000000000000000000000000000000000000004"
)

func criListResponse() []byte {
	running := pbStr(nil, 1, criRunningID)
	running = pbStr(running, 2, "sandbox1")
	running = pbMsg(running, 3, pbVarint(pbStr(nil, 1, "app"), 2, 2))
	running = pbMsg(running, 4, pbStr(nil, 1, "sha256:aaaa"))
	running = pbStr(running, 5, "sha256:aaaa")
	running = pbVarint(running, 6, 1)
	running = pbVarint(running, 7, uint64(time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC).UnixNano()))
	running = pbMap(running, 8, "io.kubernetes.pod.name", "web-7d9")
	running = pbMap(running, 8, "io.kubernetes.pod.namespace", "prod")
	running = pbMap(running, 8, "io.kubernetes.container.name", "app")
	running = pbMap(running, 9, criRestartAnnotation, "2")

	exited := pbStr(nil, 1, criExitedID)
	exited = pbMsg(exited, 3, pbStr(nil, 1, "migrate"))
	exited = pbMsg(exited, 4, pbStr(pbStr(nil, 1, "sha256:bbbb"), 18, "busybox:1.36"))
	exited = pbVarint(exited, 6, 2)
	exited = pbStr(exited, 10, "sha256:bbbb")
	exited = pbVarint(exited, 99, 7) // unknown fields are skipped

	return pbMsg(pbMsg(nil, 1, exited), 1, running)
}

func criStatusResponse(id string) []byte {
	var st []byte
	switch id {
	case criRunningID:
		st = pbVarint(st, 3, 1)
		st = pbVarint(st, 5, uint64(time.Date(2026, 9, 14, 9, 0, 1, 500, time.UTC).UnixNano()))
		st = pbMsg(st, 8, pbStr(nil, 1, "docker.io/library/nginx:1.25"))
		st = pbStr(st, 15, "/var/log/pods/prod_web-7d9_uid/app/2.log")
	case criExitedID:
		st = pbVarint(st, 3, 2)
		st = pbVarint(st, 5, uint64(time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC).UnixNano()))
		st = pbVarint(st, 6, uint64(time.Date(2026, 9, 14, 8, 0, 5, 0, time.UTC).UnixNano()))
		st = pbVarint(st, 7, 137)
		st = pbStr(st, 15, "app/0.log") // relative: dropped
	default:
		return nil
	}
	return pbMap(pbMsg(nil, 1, st), 2, "info", "{}")
}

type fakeCRI struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeCRI) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// serveCRI serves a fake runtime.v1 RuntimeService over unencrypted HTTP/2 on a unix socket.
// unimplemented answers every call with gRPC status UNIMPLEMENTED (trailers-only).
func serveCRI(t *testing.T, sock, runtimeName string, unimplemented bool) *fakeCRI {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	f := &fakeCRI{}
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	srv := &http.Server{Protocols: &p, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		method := strings.TrimPrefix(r.URL.Path, criService)
		f.mu.Lock()
		f.calls = append(f.calls, method)
		f.mu.Unlock()
		if r.ProtoMajor != 2 || r.Header.Get("Content-Type") != "application/grpc" || len(body) < 5 {
			http.Error(w, "not grpc", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/grpc")
		if unimplemented {
			w.Header().Set("Grpc-Status", "12")
			w.Header().Set("Grpc-Message", "unknown%20service%20runtime.v1.RuntimeService")
			w.WriteHeader(http.StatusOK)
			return
		}
		var resp []byte
		switch method {
		case "Version":
			resp = pbStr(pbStr(nil, 1, "0.1.0"), 2, runtimeName)
		case "ListContainers":
			resp = criListResponse()
		case "ContainerStatus":
			var id string
			_ = protoFields(body[5:], func(num protowire.Number, _ protowire.Type, v []byte, _ uint64) {
				if num == 1 {
					id = string(v)
				}
			})
			if resp = criStatusResponse(id); resp == nil {
				w.Header().Set("Trailer", "Grpc-Status, Grpc-Message")
				w.WriteHeader(http.StatusOK)
				w.Header().Set("Grpc-Status", "5")
				w.Header().Set("Grpc-Message", "not found")
				return
			}
		}
		w.Header().Set("Trailer", "Grpc-Status")
		w.WriteHeader(http.StatusOK)
		hdr := make([]byte, 5)
		binary.BigEndian.PutUint32(hdr[1:], uint32(len(resp)))
		_, _ = w.Write(append(hdr, resp...))
		w.Header().Set("Grpc-Status", "0")
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return f
}

func TestParseCRIListAndStatus(t *testing.T) {
	cs, err := ParseCRIList(criListResponse(), "containerd")
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 {
		t.Fatalf("containers = %+v", cs)
	}
	r, x := cs[0], cs[1]
	if r.ID != criRunningID || r.Name != "app" || r.Runtime != "containerd" || r.State != "running" || r.Image != "sha256:aaaa" ||
		r.ImageID != "sha256:aaaa" || r.RestartCount != 2 || r.Created != "2026-09-14T09:00:00Z" || r.Labels["io.kubernetes.pod.name"] != "web-7d9" || r.Ports == nil {
		t.Errorf("running = %+v", r)
	}
	if x.Name != "migrate" || x.State != "exited" || x.Image != "busybox:1.36" || x.ImageID != "sha256:bbbb" || len(x.Labels) != 0 {
		t.Errorf("exited = %+v", x)
	}
	st, err := ParseCRIStatus(criStatusResponse(criExitedID))
	if err != nil || st.ExitCode != 137 || st.LogPath != "" || st.LogDriver != LogDriverCRI || st.FinishedAt.IsZero() {
		t.Errorf("status = %+v %v", st, err)
	}
	if _, err := ParseCRIList([]byte{0x0a, 0x05, 0x01}, "containerd"); err == nil {
		t.Error("truncated message must fail")
	}
	for name, want := range map[string]string{"cri-o": "cri-o", "containerd": "containerd", "": "containerd"} {
		if got := criRuntimeName(pbStr(nil, 2, name), "/run/containerd/containerd.sock"); got != want {
			t.Errorf("runtime %q → %q", name, got)
		}
	}
	if got := criRuntimeName(nil, "/var/run/crio/crio.sock"); got != "cri-o" {
		t.Errorf("crio socket → %q", got)
	}
}

func TestSourceCRI(t *testing.T) {
	root := shortTempDir(t)
	// crio.sock is configured under /var/run and served at /run; Docker's containerd has no CRI service.
	f := serveCRI(t, filepath.Join(root, "run/crio/crio.sock"), "cri-o", false)
	dockerd := serveCRI(t, filepath.Join(root, "run/containerd/containerd.sock"), "", true)
	src := NewSource(hostfs.New(root), "/var/run/docker.sock")
	src.CRISockets = DefaultCRISockets
	cs, err := src.List(context.Background(), 0)
	if err != nil || len(cs) != 2 {
		t.Fatalf("list = %+v %v", cs, err)
	}
	if !errors.Is(src.DockerErr(), ErrNoRuntime) {
		t.Errorf("docker err = %v", src.DockerErr())
	}
	r, x := cs[0], cs[1]
	if r.ID != criRunningID || r.Runtime != "cri-o" || r.Image != "docker.io/library/nginx:1.25" || !r.Inspected() || r.RestartCount != 2 ||
		r.StartedAt != "2026-09-14T09:00:01.0000005Z" || r.LogPath != "/var/log/pods/prod_web-7d9_uid/app/2.log" || r.LogDriver != LogDriverCRI || r.ExitCode != 0 {
		t.Errorf("running = %+v", r)
	}
	if x.ExitCode != 137 || x.FinishedAt != "2026-09-14T08:00:05Z" || x.LogPath != "" {
		t.Errorf("exited = %+v", x)
	}
	// Details are cached: a second listing only lists (the runtime name is cached too).
	n := len(f.called())
	if _, err := src.List(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if calls := f.called()[n:]; len(calls) != 1 || calls[0] != "ListContainers" {
		t.Errorf("second listing calls = %v", calls)
	}
	if calls := dockerd.called(); len(calls) != 1 {
		t.Errorf("socket without CRI must be skipped after the first call: %v", calls)
	}

	// Docker and CRI together: union, Docker first.
	serveDocker(t, filepath.Join(root, "run/docker.sock"))
	cs, err = src.List(context.Background(), 0)
	if err != nil || len(cs) != 4 || src.DockerErr() != nil {
		t.Fatalf("merged = %d %v %v", len(cs), err, src.DockerErr())
	}
	runtimes := map[string]int{}
	for _, c := range cs {
		runtimes[c.Runtime]++
	}
	if runtimes["docker"] != 2 || runtimes["cri-o"] != 2 {
		t.Errorf("runtimes = %v", runtimes)
	}
}

func TestSourceCRIPermission(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses socket permissions")
	}
	root := shortTempDir(t)
	sock := filepath.Join(root, "run/containerd/containerd.sock")
	serveCRI(t, sock, "containerd", false)
	if err := os.Chmod(sock, 0); err != nil {
		t.Fatal(err)
	}
	denied := 0
	src := NewSource(hostfs.New(root), "/var/run/docker.sock")
	src.CRISockets = []string{"/run/containerd/containerd.sock"}
	src.Permission = func() { denied++ }
	if _, err := src.List(context.Background(), 0); err == nil || errors.Is(err, ErrNoRuntime) || denied != 1 {
		t.Errorf("err = %v denied = %d", err, denied)
	}
	// Docker working: the CRI permission error is ignored.
	serveDocker(t, filepath.Join(root, "run/docker.sock"))
	if cs, err := src.List(context.Background(), 0); err != nil || len(cs) != 2 || denied != 1 {
		t.Errorf("docker + denied CRI = %d %v %d", len(cs), err, denied)
	}
}
