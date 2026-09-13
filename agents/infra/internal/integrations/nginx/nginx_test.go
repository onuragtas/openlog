package nginx

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/discovery"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/internal/testutil"
)

// Recorded from nginx 1.27 (stub_status).
const fixture = "Active connections: 3 \nserver accepts handled requests\n 1024 1020 5061 \nReading: 0 Writing: 1 Waiting: 2 \n"

func TestParseAndRecord(t *testing.T) {
	st, err := Parse(fixture)
	if err != nil {
		t.Fatal(err)
	}
	b := integrations.NewBatch(time.Now(), 0)
	Record(b, st)
	ps := testutil.Points(b)
	testutil.Expect(t, ps, "nginx.requests", "{requests}", true, true, 5061, nil)
	testutil.Expect(t, ps, "nginx.connections_accepted", "{connections}", true, true, 1024, nil)
	testutil.Expect(t, ps, "nginx.connections_handled", "{connections}", true, true, 1020, nil)
	for state, v := range map[string]int64{"active": 3, "reading": 0, "writing": 1, "waiting": 2} {
		testutil.Expect(t, ps, "nginx.connections_current", "{connections}", true, false, v, map[string]string{"state": state})
	}
	for _, bad := range []string{"", "<html>Welcome to nginx!</html>", "Active connections: x\na\n1 2 3\nReading: 0 Writing: 1 Waiting: 2"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) accepted", bad)
		}
	}
}

func serve(t *testing.T, handler http.HandlerFunc) integrations.Endpoint {
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	host, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	return integrations.TCP(host, p)
}

func TestProbeFindsStubStatus(t *testing.T) {
	ep := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stub_status" {
			w.Write([]byte(fixture))
			return
		}
		http.NotFound(w, r)
	})
	inst := testutil.Instance()
	inst.Target.AutoEnable = true
	c, err := Integration{}.New(inst, ep)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	b := integrations.NewBatch(time.Now(), 0)
	if err := c.Collect(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if got := c.(*collector).url; got != "http://"+ep.Address+"/stub_status" {
		t.Errorf("url = %s", got)
	}
	if len(testutil.Points(b)) != 7 {
		t.Errorf("points = %d", len(testutil.Points(b)))
	}
}

func TestNoStubStatusAndAutoEnableOff(t *testing.T) {
	ep := serve(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>Welcome to nginx!</html>")) })
	inst := testutil.Instance()
	inst.Target.AutoEnable = true
	c, _ := Integration{}.New(inst, ep)
	err := c.Collect(context.Background(), integrations.NewBatch(time.Now(), 0))
	if !errors.Is(err, integrations.ErrTryNext) {
		t.Errorf("err = %v, want ErrTryNext", err)
	}

	inst.Target.AutoEnable = false
	_, err = Integration{}.New(inst, ep)
	var se *integrations.StatusError
	if !errors.As(err, &se) || se.Status != discovery.StatusNeedsConfiguration || !se.Static {
		t.Errorf("auto_enable off: %v", err)
	}
	if h := (Integration{}).Hint(inst); h == "" {
		t.Error("empty hint")
	}

	closed := integrations.TCP("127.0.0.1", 1)
	inst.Target.AutoEnable = true
	c, _ = Integration{}.New(inst, closed)
	if err := c.Collect(context.Background(), integrations.NewBatch(time.Now(), 0)); !errors.Is(err, integrations.ErrUnreachable) {
		t.Errorf("closed port: %v", err)
	}
}
