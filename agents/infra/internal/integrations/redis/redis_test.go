package redis

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/discovery"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/internal/testutil"
)

// Abbreviated INFO reply recorded from redis 7.2.
const infoFixture = "# Server\r\nredis_version:7.2.5\r\nredis_mode:standalone\r\nuptime_in_seconds:3600\r\n\r\n" +
	"# Clients\r\nconnected_clients:4\r\nblocked_clients:1\r\nclient_recent_max_input_buffer:20480\r\nclient_recent_max_output_buffer:0\r\n\r\n" +
	"# Memory\r\nused_memory:1048576\r\nused_memory_rss:4194304\r\nused_memory_peak:2097152\r\nused_memory_lua:31744\r\nmaxmemory:0\r\nmem_fragmentation_ratio:4.00\r\n\r\n" +
	"# Persistence\r\nrdb_changes_since_last_save:12\r\nlatest_fork_usec:250\r\n\r\n" +
	"# Stats\r\ntotal_connections_received:100\r\ntotal_commands_processed:2000\r\ninstantaneous_ops_per_sec:7\r\ntotal_net_input_bytes:5000\r\ntotal_net_output_bytes:9000\r\n" +
	"rejected_connections:0\r\nexpired_keys:3\r\nevicted_keys:0\r\nkeyspace_hits:80\r\nkeyspace_misses:20\r\n\r\n" +
	"# Replication\r\nrole:master\r\nconnected_slaves:0\r\nmaster_repl_offset:0\r\nrepl_backlog_first_byte_offset:0\r\n\r\n" +
	"# CPU\r\nused_cpu_sys:1.500000\r\nused_cpu_user:2.250000\r\nused_cpu_sys_children:0.000000\r\nused_cpu_user_children:0.000000\r\n\r\n" +
	"# Keyspace\r\ndb0:keys=10,expires=2,avg_ttl=5000\r\ndb3:keys=1,expires=0,avg_ttl=0\r\n"

func TestRecordInfo(t *testing.T) {
	now := time.Unix(10_000, 0)
	b := integrations.NewBatch(now, 0)
	if err := Record(b, ParseInfo(infoFixture)); err != nil {
		t.Fatal(err)
	}
	ps := testutil.Points(b)
	testutil.Expect(t, ps, "redis.clients.connected", "{client}", true, false, 4, nil)
	testutil.Expect(t, ps, "redis.clients.blocked", "{client}", true, false, 1, nil)
	testutil.Expect(t, ps, "redis.clients.max_input_buffer", "By", false, false, 20480, nil)
	testutil.Expect(t, ps, "redis.memory.used", "By", false, false, 1048576, nil)
	testutil.Expect(t, ps, "redis.keyspace.hits", "{hit}", true, true, 80, nil)
	testutil.Expect(t, ps, "redis.keyspace.misses", "{miss}", true, true, 20, nil)
	testutil.Expect(t, ps, "redis.commands.processed", "{command}", true, true, 2000, nil)
	testutil.Expect(t, ps, "redis.commands", "{ops}/s", false, false, 7, nil)
	testutil.Expect(t, ps, "redis.uptime", "s", true, true, 3600, nil)
	testutil.Expect(t, ps, "redis.keys.expired", "{event}", true, true, 3, nil)
	testutil.Expect(t, ps, "redis.rdb.changes_since_last_save", "{change}", true, false, 12, nil)
	testutil.Expect(t, ps, "redis.replication.offset", "By", false, false, 0, nil)
	testutil.Expect(t, ps, "redis.db.keys", "{key}", false, false, 10, map[string]string{"db": "0"})
	testutil.Expect(t, ps, "redis.db.keys", "{key}", false, false, 1, map[string]string{"db": "3"})
	testutil.Expect(t, ps, "redis.db.avg_ttl", "ms", false, false, 5000, map[string]string{"db": "0"})
	testutil.Expect(t, ps, "redis.role", "{role}", true, false, 1, map[string]string{"role": "primary"})
	if p := testutil.One(t, ps, "redis.memory.fragmentation_ratio", nil); p.Double != 4 || p.Metric.Unit != "1" {
		t.Errorf("fragmentation = %+v", p)
	}
	if p := testutil.One(t, ps, "redis.cpu.time", map[string]string{"state": "user"}); p.Double != 2.25 || !p.Monotonic {
		t.Errorf("cpu.time = %+v", p)
	}
	if len(testutil.Find(ps, "redis.cpu.time", nil)) != 4 {
		t.Error("cpu.time states")
	}
	p := testutil.One(t, ps, "redis.uptime", nil)
	if p.Resource["redis.version"] != "7.2.5" {
		t.Errorf("resource = %v", p.Resource)
	}
	sum := b.ResourceMetrics(nil, nil)[0].ScopeMetrics[0].Metrics
	for _, m := range sum {
		if s := m.GetSum(); s != nil && s.DataPoints[0].StartTimeUnixNano != uint64(now.Add(-time.Hour).UnixNano()) {
			t.Errorf("%s start time = %d", m.Name, s.DataPoints[0].StartTimeUnixNano)
		}
	}
	if err := Record(integrations.NewBatch(now, 0), map[string]string{}); err == nil {
		t.Error("INFO without uptime must fail")
	}
}

// fakeServer speaks enough RESP for AUTH and INFO; password "" disables auth.
func fakeServer(t *testing.T, user, password string) integrations.Endpoint {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go handle(conn, user, password)
		}
	}()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	p, _ := strconv.Atoi(port)
	return integrations.TCP("127.0.0.1", p)
}

func handle(conn net.Conn, user, password string) {
	defer conn.Close()
	rd := bufio.NewReader(conn)
	authed := password == ""
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			return
		}
		n, _ := strconv.Atoi(strings.TrimSpace(line[1:]))
		args := make([]string, n)
		for i := range args {
			rd.ReadString('\n')
			a, _ := rd.ReadString('\n')
			args[i] = strings.TrimRight(a, "\r\n")
		}
		switch strings.ToUpper(args[0]) {
		case "AUTH":
			u, p := "default", args[len(args)-1]
			if len(args) == 3 {
				u = args[1]
			}
			if password == "" {
				fmt.Fprint(conn, "-ERR AUTH <password> called without any password configured for the default user. Are you sure your configuration is correct?\r\n")
			} else if p == password && (user == "" || u == user) {
				authed = true
				fmt.Fprint(conn, "+OK\r\n")
			} else {
				fmt.Fprint(conn, "-WRONGPASS invalid username-password pair or user is disabled.\r\n")
			}
		case "INFO":
			if !authed {
				fmt.Fprint(conn, "-NOAUTH Authentication required.\r\n")
				continue
			}
			fmt.Fprintf(conn, "$%d\r\n%s\r\n", len(infoFixture), infoFixture)
		}
	}
}

func collect(t *testing.T, ep integrations.Endpoint, s config.InstanceSettings) (*integrations.Batch, error) {
	inst := testutil.Instance()
	inst.Settings = s
	c, err := Integration{}.New(inst, ep)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	b := integrations.NewBatch(time.Now(), 0)
	return b, c.Collect(context.Background(), b)
}

func TestAuthentication(t *testing.T) {
	t.Setenv("REDIS_TEST_PW", "s3cret")
	open := fakeServer(t, "", "")
	if b, err := collect(t, open, config.InstanceSettings{}); err != nil || b.Points() == 0 {
		t.Fatalf("no auth: %v", err)
	}

	protected := fakeServer(t, "openlog", "s3cret")
	_, err := collect(t, protected, config.InstanceSettings{})
	var se *integrations.StatusError
	if !errors.As(err, &se) || se.Status != discovery.StatusNeedsConfiguration {
		t.Errorf("missing password: %v", err)
	}

	_, err = collect(t, protected, config.InstanceSettings{Username: "openlog", Password: "wrong"})
	if err == nil || errors.As(err, &se) || !strings.Contains(err.Error(), "authentication failed: WRONGPASS") {
		t.Errorf("wrong password: %v", err)
	}

	if b, err := collect(t, protected, config.InstanceSettings{Username: "openlog", Password: "env:REDIS_TEST_PW"}); err != nil || b.Points() == 0 {
		t.Errorf("correct password: %v", err)
	}

	if _, err := collect(t, integrations.TCP("127.0.0.1", 1), config.InstanceSettings{}); !errors.Is(err, integrations.ErrUnreachable) {
		t.Errorf("closed port: %v", err)
	}
}
