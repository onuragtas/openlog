package memcached

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/internal/testutil"
)

// Recorded from memcached 1.6.
const statsAnswer = `STAT pid 1
STAT uptime 7200
STAT time 1757757600
STAT version 1.6.21
STAT curr_connections 12
STAT total_connections 480
STAT cmd_get 5000
STAT cmd_set 1200
STAT cmd_flush 2
STAT cmd_touch 7
STAT get_hits 4200
STAT get_misses 800
STAT delete_hits 10
STAT delete_misses 5
STAT incr_hits 3
STAT incr_misses 1
STAT decr_hits 0
STAT decr_misses 0
STAT bytes_read 1048576
STAT bytes_written 20971520
STAT bytes 4096000
STAT curr_items 900
STAT evictions 17
STAT threads 4
STAT rusage_user 1.5
STAT rusage_system 0.25
STAT limit_maxbytes 67108864
END
`

// fakeServer answers one stats command per connection.
func fakeServer(t *testing.T, answer string) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				rd := bufio.NewReader(conn)
				for {
					line, err := rd.ReadString('\n')
					if err != nil {
						return
					}
					if strings.TrimSpace(line) == "stats" {
						_, _ = conn.Write([]byte(strings.ReplaceAll(answer, "\n", "\r\n")))
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln
}

func collectorFor(t *testing.T, addr string) *collector {
	t.Helper()
	inst := testutil.Instance()
	host, portStr, _ := net.SplitHostPort(addr)
	port := 0
	_, _ = fmtSscan(portStr, &port)
	c, err := Integration{}.New(inst, integrations.TCP(host, port))
	if err != nil {
		t.Fatal(err)
	}
	return c.(*collector)
}

func fmtSscan(s string, v *int) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("not a number")
		}
		n = n*10 + int(r-'0')
	}
	*v = n
	return 1, nil
}

func TestCollect(t *testing.T) {
	ln := fakeServer(t, statsAnswer)
	c := collectorFor(t, ln.Addr().String())
	defer c.Close()
	b := integrations.NewBatch(time.Unix(1_757_757_600, 0), 0)
	if err := c.Collect(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	ps := testutil.Points(b)
	testutil.Expect(t, ps, "memcached.uptime", "s", true, true, 7200, nil)
	testutil.Expect(t, ps, "memcached.connections.current", "{connections}", true, false, 12, nil)
	testutil.Expect(t, ps, "memcached.connections.total", "{connections}", true, true, 480, nil)
	testutil.Expect(t, ps, "memcached.commands", "{commands}", true, true, 5000, map[string]string{"command": "get"})
	testutil.Expect(t, ps, "memcached.network", "By", true, true, 20971520, map[string]string{"direction": "sent"})
	testutil.Expect(t, ps, "memcached.operations", "{operations}", true, true, 4200, map[string]string{"operation": "get", "type": "hit"})
	testutil.Expect(t, ps, "memcached.operations", "{operations}", true, true, 800, map[string]string{"operation": "get", "type": "miss"})
	testutil.Expect(t, ps, "memcached.current_items", "{items}", true, false, 900, nil)
	testutil.Expect(t, ps, "memcached.evictions", "{evictions}", true, true, 17, nil)
	testutil.Expect(t, ps, "memcached.bytes", "By", true, false, 4096000, nil)
	if p := testutil.One(t, ps, "memcached.operation_hit_ratio", map[string]string{"operation": "get"}); p.Double != 84 {
		t.Errorf("hit ratio = %v, want 84", p.Double)
	}
	if p := testutil.One(t, ps, "memcached.cpu.usage", map[string]string{"state": "user"}); p.Double != 1.5 || !p.Monotonic {
		t.Errorf("cpu user = %+v", p)
	}
	// Counters are cumulative from the server's start, so the batch carries that start time.
	for _, rm := range b.ResourceMetrics(nil, nil) {
		for _, m := range rm.ScopeMetrics[0].Metrics {
			if m.Name == "memcached.connections.total" && m.GetSum().DataPoints[0].StartTimeUnixNano == 0 {
				t.Error("cumulative sums must carry a start time")
			}
		}
	}
}

func TestSASLRefusalIsNeedsConfiguration(t *testing.T) {
	ln := fakeServer(t, "CLIENT_ERROR unauthenticated\n")
	c := collectorFor(t, ln.Addr().String())
	defer c.Close()
	err := c.Collect(context.Background(), integrations.NewBatch(time.Now(), 0))
	var se *integrations.StatusError
	if !errors.As(err, &se) || se.Status != "needs_configuration" || !strings.Contains(se.Msg, "SASL") {
		t.Fatalf("err = %v", err)
	}
}

func TestUnreachableEndpoint(t *testing.T) {
	ln := fakeServer(t, statsAnswer)
	addr := ln.Addr().String()
	ln.Close()
	c := collectorFor(t, addr)
	err := c.Collect(context.Background(), integrations.NewBatch(time.Now(), 0))
	if !integrations.IsUnreachable(err) {
		t.Fatalf("err = %v", err)
	}
}
