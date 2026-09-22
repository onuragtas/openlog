package haproxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/internal/testutil"
)

// Recorded from HAProxy 2.8 (`show stat`), trimmed to the columns the collector reads. The header is a
// comment line, and every row ends with the trailing comma the format has.
const statsCSV = `# pxname,svname,qcur,scur,slim,stot,bin,bout,dreq,dresp,ereq,econ,eresp,wretr,wredis,status,act,bck,chkfail,rate,req_rate,req_tot,hrsp_1xx,hrsp_2xx,hrsp_3xx,hrsp_4xx,hrsp_5xx,hrsp_other,qtime,ctime,rtime,ttime,conn_tot,conn_rate,dcon,type,
http-in,FRONTEND,,12,2000,48000,10485760,52428800,3,0,1,,,,,OPEN,,,,25,24,47000,0,44000,1000,1500,500,0,,,,,50000,26,4,0,
web,web-1,0,6,100,24000,5242880,26214400,,0,,2,7,3,1,UP 2/3,1,0,5,12,,,0,22000,500,700,250,0,1,2,15,18,,,,2,
web,BACKEND,1,12,200,48000,10485760,52428800,0,0,,4,9,5,2,UP,2,1,,24,,47000,0,44000,1000,1400,500,0,2,3,18,23,,,,1,
`

func TestParseCSV(t *testing.T) {
	rows, err := ParseCSV([]byte(statsCSV))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	if rows[0]["pxname"] != "http-in" || rows[0]["svname"] != "FRONTEND" || rows[0]["type"] != "0" {
		t.Errorf("first row = %v", rows[0])
	}
}

// A page that is not the CSV export must send the collector to the next candidate rather than fail.
func TestParseCSVRejectsHTML(t *testing.T) {
	_, err := ParseCSV([]byte("<html><body>HAProxy statistics</body></html>"))
	if err == nil {
		t.Fatal("HTML must not parse as statistics")
	}
	if !errors.Is(err, integrations.ErrTryNext) {
		t.Fatalf("err = %v, want try-next", err)
	}
}

func TestRecord(t *testing.T) {
	rows, err := ParseCSV([]byte(statsCSV))
	if err != nil {
		t.Fatal(err)
	}
	b := integrations.NewBatch(time.Now(), 0)
	if dropped := Record(b, rows); dropped != 0 {
		t.Fatalf("dropped = %d", dropped)
	}
	ps := testutil.Points(b)

	fe := map[string]string{"haproxy.proxy.name": "http-in", "haproxy.service.name": "FRONTEND"}
	testutil.Expect(t, ps, "haproxy.sessions.count", "{sessions}", true, true, 48000, fe)
	testutil.Expect(t, ps, "haproxy.sessions.current", "{sessions}", false, false, 12, fe)
	testutil.Expect(t, ps, "haproxy.requests.total", "{requests}", true, true, 47000, fe)
	testutil.Expect(t, ps, "haproxy.connections.total", "{connections}", true, true, 50000, fe)
	testutil.Expect(t, ps, "haproxy.bytes", "By", true, true, 10485760,
		map[string]string{"haproxy.proxy.name": "http-in", "direction": "received"})
	testutil.Expect(t, ps, "haproxy.responses.count", "{responses}", true, true, 44000,
		map[string]string{"haproxy.proxy.name": "http-in", "status_code": "2xx"})
	if p := testutil.One(t, ps, "haproxy.status", fe); p.Attrs["state"] != "open" {
		t.Errorf("frontend status = %v", p.Attrs)
	}
	// A frontend has no queue or backend timings, so those columns are empty and must emit nothing.
	if got := testutil.Find(ps, "haproxy.queue.time", fe); len(got) != 0 {
		t.Errorf("an empty column must not become a point: %d", len(got))
	}

	// A server row keeps its own resource, and "UP 2/3" is the state "up".
	srv := map[string]string{"haproxy.proxy.name": "web", "haproxy.service.name": "web-1"}
	if p := testutil.One(t, ps, "haproxy.status", srv); p.Attrs["state"] != "up" {
		t.Errorf("server status = %v", p.Attrs)
	}
	testutil.Expect(t, ps, "haproxy.health_check.failures", "{checks}", true, true, 5, srv)
	testutil.Expect(t, ps, "haproxy.response.time", "ms", false, false, 15, srv)
	for _, p := range testutil.Find(ps, "haproxy.status", srv) {
		if p.Resource["haproxy.proxy.type"] != "server" {
			t.Errorf("server row type = %q", p.Resource["haproxy.proxy.type"])
		}
	}

	be := map[string]string{"haproxy.proxy.name": "web", "haproxy.service.name": "BACKEND"}
	testutil.Expect(t, ps, "haproxy.servers", "{servers}", false, false, 2,
		map[string]string{"haproxy.service.name": "BACKEND", "state": "active"})
	testutil.Expect(t, ps, "haproxy.servers", "{servers}", false, false, 1,
		map[string]string{"haproxy.service.name": "BACKEND", "state": "backup"})
	testutil.Expect(t, ps, "haproxy.queue.current", "{requests}", false, false, 1, be)
	testutil.Expect(t, ps, "haproxy.server.retries", "{retries}", true, true, 5, be)
}

func TestProxyLimit(t *testing.T) {
	rows := make([]Row, MaxProxies+2)
	for i := range rows {
		rows[i] = Row{"pxname": "px", "svname": "FRONTEND", "type": "0", "stot": "1"}
	}
	if dropped := Record(integrations.NewBatch(time.Now(), 0), rows); dropped != 2 {
		t.Fatalf("dropped = %d, want 2", dropped)
	}
}

// The stats page is found by probing the usual paths, and the one that answered is remembered.
func TestCollectOverHTTPProbesPaths(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.RequestURI())
		if r.URL.RequestURI() != "/stats;csv" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(statsCSV))
	}))
	defer srv.Close()

	inst := testutil.Instance()
	inst.Settings.Endpoint = srv.URL
	inst.Explicit = true
	col, err := Integration{}.New(inst, integrations.Endpoint{Network: "url", Address: srv.URL, Display: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	c := col.(*collector)
	defer c.Close()

	b := integrations.NewBatch(time.Now(), 0)
	if err := c.Collect(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	testutil.Expect(t, testutil.Points(b), "haproxy.sessions.count", "{sessions}", true, true, 48000,
		map[string]string{"haproxy.proxy.name": "http-in"})
	if c.url != srv.URL+"/stats;csv" {
		t.Errorf("remembered url = %q", c.url)
	}
	// The second collection goes straight to the page that answered.
	n := len(asked)
	if err := c.Collect(context.Background(), integrations.NewBatch(time.Now(), 0)); err != nil {
		t.Fatal(err)
	}
	if len(asked)-n != 1 {
		t.Errorf("second collection asked %d paths, want 1 (%v)", len(asked)-n, asked[n:])
	}
}

// The runtime socket answers the same CSV to `show stat`.
func TestCollectOverRuntimeSocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "admin.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				// Answer without waiting for the command first. Windows AF_UNIX did not deliver the
				// collector's request to this read before its own 3 s read deadline expired
				// ("read unix @->…admin.sock: i/o timeout"), so the reply was never written and the test
				// failed on a runner, not on the code. haproxy answers a command; what is asserted here is
				// the parsing of the answer, and that does not need the command to be read first.
				_, _ = conn.Write([]byte(statsCSV))
				buf := make([]byte, 64)
				_, _ = conn.Read(buf)
			}()
		}
	}()

	inst := testutil.Instance()
	col, err := Integration{}.New(inst, integrations.Endpoint{Network: "unix", Address: sock, Display: "unix:" + sock})
	if err != nil {
		t.Fatal(err)
	}
	c := col.(*collector)
	defer c.Close()
	b := integrations.NewBatch(time.Now(), 0)
	if err := c.Collect(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	testutil.Expect(t, testutil.Points(b), "haproxy.requests.total", "{requests}", true, true, 47000,
		map[string]string{"haproxy.proxy.name": "http-in"})
}

func TestMissingSocketIsUnreachable(t *testing.T) {
	inst := testutil.Instance()
	sock := filepath.Join(t.TempDir(), "absent.sock")
	col, err := Integration{}.New(inst, integrations.Endpoint{Network: "unix", Address: sock, Display: "unix:" + sock})
	if err != nil {
		t.Fatal(err)
	}
	defer col.Close()
	if _, err := os.Stat(sock); err == nil {
		t.Fatal("the socket must not exist")
	}
	err = col.Collect(context.Background(), integrations.NewBatch(time.Now(), 0))
	if !integrations.IsUnreachable(err) {
		t.Fatalf("err = %v", err)
	}
}
