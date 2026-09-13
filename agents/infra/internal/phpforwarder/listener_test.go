package phpforwarder

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/discovery"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

type discoveryService = discovery.Service

// shortDir returns a directory short enough for unix socket paths (t.TempDir is too long on macOS).
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "olphp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

type sink struct {
	mu    sync.Mutex
	spans int
}

func (s *sink) emit(td *tracepb.TracesData, n int) {
	s.mu.Lock()
	s.spans += n
	s.mu.Unlock()
}

func (s *sink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spans
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func sendUnix(t *testing.T, path string, b []byte) {
	t.Helper()
	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write(b); err != nil {
		t.Fatal(err)
	}
}

func TestUnixSocketPermissionsAndRestart(t *testing.T) {
	dir := filepath.Join(shortDir(t), "run")
	sock := filepath.Join(dir, "php.sock")
	gid := os.Getgid()
	var lookedUp []string
	s := &sink{}
	f := New(Options{Socket: sock, SocketGroup: "auto", FlushInterval: 20 * time.Millisecond, HostResource: hostResource(), Emit: s.emit,
		LookupGroup: func(name string) (*user.Group, error) {
			lookedUp = append(lookedUp, name)
			if name == "nginx" { // www-data does not exist on this "host"
				return &user.Group{Name: name, Gid: strconv.Itoa(gid)}, nil
			}
			return nil, user.UnknownGroupError(name)
		}})
	if err := f.Start(); err != nil {
		t.Fatal(err)
	}
	if di, err := os.Stat(dir); err != nil || di.Mode().Perm() != 0o755 {
		t.Errorf("socket dir mode = %v (%v), want 0755", di.Mode().Perm(), err)
	}
	fi, err := os.Lstat(sock)
	if err != nil || fi.Mode().Type() != fs.ModeSocket || fi.Mode().Perm() != 0o660 {
		t.Fatalf("socket mode = %v (%v), want socket 0660", fi.Mode(), err)
	}
	if st := fi.Sys().(*syscall.Stat_t); int(st.Gid) != gid || f.SocketGroup() != "nginx" {
		t.Errorf("socket gid = %d group %q, want %d nginx (lookups %v)", st.Gid, f.SocketGroup(), gid, lookedUp)
	}
	sendUnix(t, sock, msg(t, nil))
	waitFor(t, "spans over the unix socket", func() bool { return s.count() == 2 })

	// Oversized datagrams are rejected, not truncated into valid JSON (macOS caps unix datagrams at 2048 bytes).
	if runtime.GOOS == "linux" {
	big := make([]byte, MaxDatagramBytes+100)
	copy(big, msg(t, nil))
	for i := len(msg(t, nil)); i < len(big); i++ {
		big[i] = ' '
	}
	sendUnix(t, sock, big)
	waitFor(t, "oversized datagram counted", func() bool { return f.o.Stats.Snapshot().PHP.Messages["malformed"] == 1 })
	}

	f.Stop()
	if _, err := os.Lstat(sock); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("socket not removed on stop: %v", err)
	}

	// A stale socket left by a killed agent is replaced; a restart works.
	stale, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	stale.Close() // the file stays (no unlink for datagram sockets)
	if err := f.Start(); err != nil {
		t.Fatalf("restart over stale socket: %v", err)
	}
	sendUnix(t, sock, msg(t, func(m map[string]any) { m["trace_id"] = traceHex(7) }))
	waitFor(t, "spans after restart", func() bool { return s.count() == 4 })
	f.Stop()

	// A regular file is never deleted.
	if err := os.WriteFile(sock, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.Start(); err == nil {
		f.Stop()
		t.Error("start must fail when the socket path is a regular file")
	}
}

func TestSocketModeExplicitAndMissingGroup(t *testing.T) {
	sock := filepath.Join(shortDir(t), "php.sock")
	f := New(Options{Socket: sock, SocketGroup: "auto", SocketMode: 0o666,
		LookupGroup: func(name string) (*user.Group, error) { return nil, user.UnknownGroupError(name) }})
	if err := f.Start(); err != nil {
		t.Fatal(err)
	}
	defer f.Stop()
	if fi, _ := os.Lstat(sock); fi.Mode().Perm() != 0o666 {
		t.Errorf("mode = %v, want 0666", fi.Mode().Perm())
	}
	if f.o.Stats.Snapshot().PermissionDenied["php_forwarder"] != 1 {
		t.Error("missing socket group must be reported")
	}
}

func TestUDPListener(t *testing.T) {
	probe, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.LocalAddr().String()
	probe.Close()

	s := &sink{}
	f := New(Options{UDPListen: addr, FlushInterval: 20 * time.Millisecond, ReassemblyTimeout: 100 * time.Millisecond,
		HostResource: hostResource(), Emit: s.emit})
	if err := f.Start(); err != nil {
		t.Fatal(err)
	}
	defer f.Stop()
	c, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Split message over UDP: part 1 first, then part 0.
	p1 := msg(t, func(m map[string]any) { m["seq"], m["last"] = 1, true; m["spans"] = m["spans"].([]any)[1:] })
	p0 := msg(t, func(m map[string]any) { m["last"] = false; m["spans"] = m["spans"].([]any)[:1] })
	for _, b := range [][]byte{p1, p0} {
		if _, err := c.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "spans over UDP", func() bool { return s.count() == 2 })

	// A lone part times out and is exported as incomplete.
	if _, err := c.Write(msg(t, func(m map[string]any) { m["trace_id"], m["last"] = traceHex(3), false })); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "reassembly timeout", func() bool {
		return s.count() == 4 && f.o.Stats.Snapshot().PHP.ReassemblyTimeouts == 1
	})
}

func TestStopFlushesPending(t *testing.T) {
	sock := filepath.Join(shortDir(t), "php.sock")
	s := &sink{}
	f := New(Options{Socket: sock, SocketGroup: strconv.Itoa(os.Getgid()), ReassemblyTimeout: time.Minute, FlushInterval: time.Hour,
		HostResource: hostResource(), Emit: s.emit})
	if err := f.Start(); err != nil {
		t.Fatal(err)
	}
	sendUnix(t, sock, msg(t, func(m map[string]any) { m["last"] = false }))
	waitFor(t, "pending trace", func() bool { return f.o.Stats.Snapshot().PHP.PendingTraces == 1 })
	f.Stop()
	if s.count() != 2 {
		t.Errorf("stop flushed %d spans, want 2", s.count())
	}
}
