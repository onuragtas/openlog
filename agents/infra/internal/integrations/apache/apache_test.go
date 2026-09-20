package apache

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/internal/testutil"
)

// Recorded from httpd 2.4 with ExtendedStatus On.
const statusPage = `localhost
ServerVersion: Apache/2.4.58 (Unix)
ServerMPM: event
Server Built: Oct 23 2023 12:00:00
CurrentTime: Saturday, 13-Sep-2026 10:00:00 UTC
RestartTime: Saturday, 13-Sep-2026 08:00:00 UTC
ParentServerConfigGeneration: 1
ServerUptimeSeconds: 7200
Uptime: 7200
Total Accesses: 45231
Total kBytes: 102400
Total Duration: 91234
CPUUser: 12.5
CPUSystem: 3.25
CPUChildrenUser: 0.5
CPUChildrenSystem: .25
CPULoad: 2.17
Load1: 0.35
Load5: 0.4
Load15: 0.45
ReqPerSec: 6.3
BytesPerSec: 14563
BytesPerReq: 2318
BusyWorkers: 3
IdleWorkers: 22
ConnsTotal: 7
ConnsAsyncWriting: 1
ConnsAsyncKeepAlive: 4
ConnsAsyncClosing: 2
Scoreboard: __W_R.....K
`

func testCollector(t *testing.T, target string, autoEnable bool) *collector {
	t.Helper()
	inst := testutil.Instance()
	inst.Target.AutoEnable = autoEnable
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	c, err := Integration{}.New(inst, integrations.TCP(u.Hostname(), port))
	if err != nil {
		t.Fatal(err)
	}
	return c.(*collector)
}

func TestProbeAndCollect(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		if r.URL.Path == "/server-status" && r.URL.RawQuery == "auto" {
			_, _ = w.Write([]byte(statusPage))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	c := testCollector(t, srv.URL, true)
	defer c.Close()
	b := integrations.NewBatch(time.Unix(1_757_757_600, 0), 0)
	if err := c.Collect(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 || paths[0] != "/server-status?auto" {
		t.Fatalf("probe order: %v", paths)
	}
	ps := testutil.Points(b)
	testutil.Expect(t, ps, "apache.requests", "{requests}", true, true, 45231, nil)
	testutil.Expect(t, ps, "apache.traffic", "By", true, true, 102400*1024, nil)
	testutil.Expect(t, ps, "apache.uptime", "s", true, true, 7200, nil)
	testutil.Expect(t, ps, "apache.workers", "{workers}", true, false, 3, map[string]string{"state": "busy"})
	testutil.Expect(t, ps, "apache.workers", "{workers}", true, false, 22, map[string]string{"state": "idle"})
	testutil.Expect(t, ps, "apache.current_connections", "{connections}", true, false, 7, nil)
	testutil.Expect(t, ps, "apache.connections.async", "{connections}", true, false, 4, map[string]string{"state": "keep-alive"})
	testutil.Expect(t, ps, "apache.request.time", "ms", true, true, 91234, nil)
	// Scoreboard: 2 waiting (_), 1 sending (W), 1 reading (R), 5 open (.), 1 keepalive (K), 1 waiting more.
	testutil.Expect(t, ps, "apache.scoreboard", "{workers}", true, false, 3, map[string]string{"state": "waiting"})
	testutil.Expect(t, ps, "apache.scoreboard", "{workers}", true, false, 5, map[string]string{"state": "open"})
	if p := testutil.One(t, ps, "apache.cpu.load", nil); p.Double != 2.17 {
		t.Errorf("cpu load = %v", p.Double)
	}
	if p := testutil.One(t, ps, "apache.cpu.time", map[string]string{"level": "children", "mode": "system"}); p.Double != 0.25 {
		t.Errorf("children system cpu = %v", p.Double)
	}
	if p := testutil.One(t, ps, "apache.uptime", nil); p.Resource["apache.server.version"] != "Apache/2.4.58 (Unix)" {
		t.Errorf("resource = %v", p.Resource)
	}

	// The page found is remembered, so the next collection asks for it directly.
	paths = nil
	if err := c.Collect(context.Background(), integrations.NewBatch(time.Now(), 0)); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 {
		t.Errorf("second collection re-probed: %v", paths)
	}
}

func TestNoStatusPageIsNeedsConfiguration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>It works!</html>")) }))
	defer srv.Close()
	c := testCollector(t, srv.URL, true)
	defer c.Close()
	err := c.Collect(context.Background(), integrations.NewBatch(time.Now(), 0))
	var se *integrations.StatusError
	if !errors.As(err, &se) || se.Status != "needs_configuration" || !strings.Contains(se.Msg, "mod_status") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(Integration{}.Hint(testutil.Instance()), "SetHandler server-status") {
		t.Error("the hint must show how to enable mod_status")
	}
}

func TestParseRejectsOtherPages(t *testing.T) {
	if Parse("Total Accesses: 5\n") != nil { // without Uptime it is not the auto page
		t.Error("a page without Uptime must not parse")
	}
	if Parse("<html>nope</html>") != nil {
		t.Error("HTML must not parse")
	}
	st := Parse(statusPage)
	if st["ServerMPM"] != "event" || st["Scoreboard"] != "__W_R.....K" {
		t.Errorf("parsed = %v", st)
	}
}

func TestAutoEnableOff(t *testing.T) {
	inst := testutil.Instance()
	inst.Target.AutoEnable = false
	_, err := Integration{}.New(inst, integrations.TCP("127.0.0.1", 80))
	var se *integrations.StatusError
	if !errors.As(err, &se) || se.Status != "needs_configuration" {
		t.Fatalf("err = %v", err)
	}
}
