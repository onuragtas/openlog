package phpfpm

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/fcgi"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/discovery"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/internal/testutil"
)

// Recorded from PHP-FPM 8.3 (`pm.status_path = /status`, requested with ?json).
const statusJSON = `{"pool":"www","process manager":"dynamic","start time":1758700000,"start since":3600,` +
	`"accepted conn":48211,"listen queue":3,"max listen queue":17,"listen queue len":511,"idle processes":4,` +
	`"active processes":6,"total processes":10,"max active processes":12,"max children reached":2,"slow requests":5}`

func TestParse(t *testing.T) {
	st, err := Parse([]byte(statusJSON))
	if err != nil {
		t.Fatal(err)
	}
	if st.Pool != "www" || st.ProcessManager != "dynamic" {
		t.Errorf("pool = %q, process manager = %q", st.Pool, st.ProcessManager)
	}
	if st.AcceptedConn != 48211 || st.ListenQueue != 3 || st.MaxChildrenReached != 2 || st.SlowRequests != 5 {
		t.Errorf("status = %+v", st)
	}
}

// A body that is not a status page must be rejected, so the collector tries the next path instead of
// recording nonsense. The 404 body of a pool without pm.status_path is the case that matters.
func TestParseRejectsOtherBodies(t *testing.T) {
	for _, body := range []string{"File not found.", "<html><body>404</body></html>", "", "{}", `{"pool":""}`, `[1,2]`} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("%q parsed as a status page", body)
		}
	}
}

func TestRecord(t *testing.T) {
	st, err := Parse([]byte(statusJSON))
	if err != nil {
		t.Fatal(err)
	}
	b := integrations.NewBatch(time.Now(), 0)
	Record(b, st)
	ps := testutil.Points(b)
	pool := map[string]string{"phpfpm.pool.name": "www"}

	testutil.Expect(t, ps, "phpfpm.uptime", "s", true, true, 3600, pool)
	testutil.Expect(t, ps, "phpfpm.connections.accepted", "{connections}", true, true, 48211, pool)
	testutil.Expect(t, ps, "phpfpm.requests.slow", "{requests}", true, true, 5, pool)
	testutil.Expect(t, ps, "phpfpm.max_children_reached", "{events}", true, true, 2, pool)
	testutil.Expect(t, ps, "phpfpm.listen_queue.current", "{requests}", false, false, 3, pool)
	testutil.Expect(t, ps, "phpfpm.listen_queue.max", "{requests}", false, false, 17, pool)
	testutil.Expect(t, ps, "phpfpm.listen_queue.limit", "{requests}", false, false, 511, pool)
	testutil.Expect(t, ps, "phpfpm.processes.max_active", "{processes}", false, false, 12, pool)
	// Idle and active are one metric split by state, so a pool's capacity is one query, not two.
	testutil.Expect(t, ps, "phpfpm.processes.current", "{processes}", true, false, 4,
		map[string]string{"phpfpm.pool.name": "www", "state": "idle"})
	testutil.Expect(t, ps, "phpfpm.processes.current", "{processes}", true, false, 6,
		map[string]string{"phpfpm.pool.name": "www", "state": "active"})

	if p := testutil.One(t, ps, "phpfpm.uptime", pool); p.Resource["phpfpm.process_manager"] != "dynamic" {
		t.Errorf("process manager resource attribute = %q", p.Resource["phpfpm.process_manager"])
	}
	// total processes is idle + active: a value the backend can add is not a series worth storing.
	if got := testutil.Find(ps, "phpfpm.processes.total", pool); len(got) != 0 {
		t.Errorf("total processes must not be emitted: %d points", len(got))
	}
}

// A unix socket has no backlog setting, so "listen queue len" is 0 — which must not be recorded as a
// limit of zero, because that reads as "no request may queue".
func TestRecordOmitsQueueLimitWhenZero(t *testing.T) {
	b := integrations.NewBatch(time.Now(), 0)
	Record(b, Status{Pool: "www", ListenQueueLen: 0, IdleProcesses: 1})
	if got := testutil.Find(testutil.Points(b), "phpfpm.listen_queue.limit", nil); len(got) != 0 {
		t.Errorf("a zero queue length must emit nothing: %d points", len(got))
	}
}

// fakePool serves a PHP-FPM-like status page over real FastCGI (net/http/fcgi is the server half of the
// protocol this package's client speaks), so the wire format is exercised rather than assumed.
func fakePool(t *testing.T, statusPath, body string) (sock string, asked func() []string) {
	t.Helper()
	sock = filepath.Join(t.TempDir(), "fpm.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var mu sync.Mutex
	var paths []string
	go func() {
		_ = fcgi.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			paths = append(paths, r.URL.Path)
			mu.Unlock()
			if statusPath == "" || r.URL.Path != statusPath {
				// What a pool without this status path answers.
				http.Error(w, "File not found.", http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(body))
		}))
	}()
	return sock, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
}

func collectorFor(t *testing.T, sock string) *collector {
	t.Helper()
	col, err := Integration{}.New(testutil.Instance(), integrations.Endpoint{Network: "unix", Address: sock, Display: "unix:" + sock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(col.Close)
	return col.(*collector)
}

// The status path is found by probing the usual values, and the one that answered is used directly
// afterwards: a pool that does not use /status costs one extra request once, not on every collection.
func TestCollectProbesAndRemembersTheStatusPath(t *testing.T) {
	sock, asked := fakePool(t, "/fpm-status", statusJSON)
	c := collectorFor(t, sock)

	b := integrations.NewBatch(time.Now(), 0)
	if err := c.Collect(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	testutil.Expect(t, testutil.Points(b), "phpfpm.connections.accepted", "{connections}", true, true, 48211,
		map[string]string{"phpfpm.pool.name": "www"})
	if c.path != "/fpm-status" {
		t.Errorf("remembered path = %q", c.path)
	}
	if first := asked(); len(first) != 2 || first[0] != "/status" || first[1] != "/fpm-status" {
		t.Errorf("probed %v, want /status then /fpm-status", first)
	}

	n := len(asked())
	if err := c.Collect(context.Background(), integrations.NewBatch(time.Now(), 0)); err != nil {
		t.Fatal(err)
	}
	if got := asked(); len(got)-n != 1 {
		t.Errorf("second collection asked %d paths, want 1 (%v)", len(got)-n, got[n:])
	}
}

// A reachable pool that serves no status page is a configuration answer, not a broken endpoint: reporting
// it as an error would put a red row on the integrations screen for something the operator must decide.
func TestPoolWithoutStatusPathNeedsConfiguration(t *testing.T) {
	sock, asked := fakePool(t, "", "")
	c := collectorFor(t, sock)

	err := c.Collect(context.Background(), integrations.NewBatch(time.Now(), 0))
	var se *integrations.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v (%T), want a status error", err, err)
	}
	if se.Status != discovery.StatusNeedsConfiguration {
		t.Errorf("status = %q, want %q", se.Status, discovery.StatusNeedsConfiguration)
	}
	if integrations.IsUnreachable(err) {
		t.Error("a pool that answered must not be reported unreachable")
	}
	if len(asked()) != len(StatusPaths) {
		t.Errorf("probed %d paths, want all %d", len(asked()), len(StatusPaths))
	}
}

func TestMissingSocketIsUnreachable(t *testing.T) {
	c := collectorFor(t, filepath.Join(t.TempDir(), "absent.sock"))
	err := c.Collect(context.Background(), integrations.NewBatch(time.Now(), 0))
	if !integrations.IsUnreachable(err) {
		t.Fatalf("err = %v, want unreachable", err)
	}
}

// An endpoint that is not FastCGI at all (an HTTP server on the probed port) must send the instance to the
// next candidate rather than fail the collection.
func TestNonFastCGIEndpointTriesNext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}), ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	col, err := Integration{}.New(testutil.Instance(),
		integrations.Endpoint{Network: "tcp", Address: ln.Addr().String(), Display: ln.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	defer col.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := col.Collect(ctx, integrations.NewBatch(time.Now(), 0)); err == nil {
		t.Fatal("an HTTP server must not pass as a PHP-FPM pool")
	}
}
