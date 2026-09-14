package docker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/containers"
	"github.com/onuragtas/openlog/agents/infra/internal/discovery"
	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
)

type fakeSource struct{ listErr, dockerErr error }

func (f fakeSource) List(context.Context, time.Duration) ([]containers.Container, error) {
	return nil, f.listErr
}

func (f fakeSource) DockerErr() error { return f.dockerErr }

func TestCollectUsesDockerState(t *testing.T) {
	denied := fmt.Errorf("containers: /run/docker.sock: %w", fs.ErrPermission)
	for _, tc := range []struct {
		name              string
		listErr, dockErr  error
		wantNil           bool
		wantStatus, wantS string
	}{
		{name: "docker answers", wantNil: true},
		// A CRI runtime (containerd, CRI-O) answered, Docker did not: the listing succeeds.
		{name: "cri only", dockErr: containers.ErrNoRuntime, wantS: "docker socket not found"},
		{name: "cri only, docker denied", dockErr: denied, wantStatus: discovery.StatusNeedsConfiguration, wantS: "permission denied"},
		{name: "cri only, docker failing", dockErr: errors.New("containers: /run/docker.sock: EOF"), wantS: "EOF"},
		{name: "nothing", listErr: containers.ErrNoRuntime, dockErr: containers.ErrNoRuntime, wantS: "docker socket not found"},
		{name: "docker denied, no cri", listErr: denied, dockErr: fmt.Errorf("x: %w", syscall.EACCES), wantStatus: discovery.StatusNeedsConfiguration},
	} {
		err := collector{src: fakeSource{tc.listErr, tc.dockErr}}.Collect(context.Background(), nil)
		if tc.wantNil {
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
			}
			continue
		}
		var se *integrations.StatusError
		switch {
		case err == nil:
			t.Errorf("%s: enabled, want an error", tc.name)
		case tc.wantStatus != "" && (!errors.As(err, &se) || se.Status != tc.wantStatus):
			t.Errorf("%s: %v, want status %s", tc.name, err, tc.wantStatus)
		case tc.wantStatus == "" && errors.As(err, &se):
			t.Errorf("%s: status %s, want a plain error", tc.name, se.Status)
		case !strings.Contains(err.Error(), tc.wantS):
			t.Errorf("%s: %v, want %q", tc.name, err, tc.wantS)
		}
	}
}

func TestCollectRealSource(t *testing.T) {
	root, err := os.MkdirTemp("", "dk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	src := containers.NewSource(hostfs.New(root), "/var/run/docker.sock")
	src.CRISockets = []string{"/run/containerd/containerd.sock"} // absent
	c, err := Integration{Source: src, Socket: "/var/run/docker.sock"}.New(nil, integrations.Endpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Collect(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "docker socket not found") {
		t.Errorf("no runtime: %v", err)
	}

	sock := filepath.Join(root, "run/docker.sock")
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/containers/json" {
			w.Write([]byte("[]"))
			return
		}
		http.NotFound(w, r)
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	time.Sleep(20 * time.Millisecond) // the source caches listings for 15 s
	src2 := containers.NewSource(hostfs.New(root), "/var/run/docker.sock")
	if err := (collector{src: src2}).Collect(context.Background(), nil); err != nil {
		t.Errorf("docker answers: %v", err)
	}

	if _, err := (Integration{}).New(nil, integrations.Endpoint{}); err == nil {
		t.Error("nil source must be not_available")
	}
}
