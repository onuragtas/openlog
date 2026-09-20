package rabbitmq

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/internal/testutil"
)

// Recorded from RabbitMQ 3.13, trimmed to the fields the collector reads.
const overviewJSON = `{
  "rabbitmq_version": "3.13.7",
  "cluster_name": "rabbit@broker-1",
  "object_totals": {"connections": 14, "channels": 28, "exchanges": 9, "queues": 3, "consumers": 6}
}`

const nodesJSON = `[{
  "name": "rabbit@broker-1", "type": "disc", "running": true, "uptime": 7200000,
  "mem_used": 134217728, "mem_limit": 1073741824, "mem_alarm": false,
  "disk_free": 21474836480, "disk_free_limit": 50000000, "disk_free_alarm": false,
  "fd_used": 60, "fd_total": 1048576, "sockets_used": 20, "sockets_total": 943629,
  "proc_used": 500, "proc_total": 1048576, "run_queue": 1, "partitions": []
}]`

const queuesJSON = `[
  {"name": "orders", "vhost": "/", "node": "rabbit@broker-1", "state": "running", "consumers": 4,
   "messages": 100, "messages_ready": 90, "messages_unacknowledged": 10,
   "message_stats": {"publish": 900, "deliver_get": 850, "ack": 840, "redeliver": 5}},
  {"name": "dead-letter", "vhost": "/", "node": "rabbit@broker-1", "state": "idle", "consumers": 0,
   "messages": 20, "messages_ready": 20, "messages_unacknowledged": 0}
]`

// server answers the management API; paths without an answer are 404.
func server(t *testing.T, answers map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := answers[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func collectorFor(t *testing.T, url string) *collector {
	t.Helper()
	inst := testutil.Instance()
	inst.Settings.Endpoint = url
	inst.Explicit = true
	c, err := Integration{}.New(inst, integrations.Endpoint{Network: "url", Address: url, Display: url})
	if err != nil {
		t.Fatal(err)
	}
	return c.(*collector)
}

// brokerPoints returns the points of the broker resource, which is the one naming neither a queue nor a node.
func brokerPoints(ps []testutil.Point) []testutil.Point {
	var out []testutil.Point
	for _, p := range ps {
		if p.Resource["rabbitmq.queue.name"] == "" && p.Resource["rabbitmq.node.name"] == "" {
			out = append(out, p)
		}
	}
	return out
}

func TestCollect(t *testing.T) {
	srv := server(t, map[string]string{
		"/api/overview": overviewJSON,
		"/api/nodes":    nodesJSON,
		"/api/queues":   queuesJSON,
	})
	c := collectorFor(t, srv.URL)
	defer c.Close()
	b := integrations.NewBatch(time.Now(), 0)
	if err := c.Collect(context.Background(), b); err != nil {
		t.Fatal(err)
	}

	res := map[string]string{}
	for _, kv := range b.ResourceAttrs() {
		res[kv.Key] = kv.Value.GetStringValue()
	}
	if res["rabbitmq.version"] != "3.13.7" || res["rabbitmq.cluster.name"] != "rabbit@broker-1" {
		t.Errorf("resource attributes = %v", res)
	}

	ps := testutil.Points(b)
	testutil.Expect(t, ps, "rabbitmq.connection.count", "{connections}", true, false, 14, nil)
	testutil.Expect(t, ps, "rabbitmq.channel.count", "{channels}", true, false, 28, nil)
	testutil.Expect(t, ps, "rabbitmq.exchange.count", "{exchanges}", true, false, 9, nil)
	testutil.Expect(t, ps, "rabbitmq.queue.count", "{queues}", true, false, 3, nil)
	// The broker's resource carries only the object counts: its message totals would double-count the queues.
	for _, name := range []string{"rabbitmq.message.published", "rabbitmq.message.current", "rabbitmq.consumer.count"} {
		if got := testutil.Find(brokerPoints(ps), name, nil); len(got) != 0 {
			t.Errorf("%s must be a queue metric, found %d broker points", name, len(got))
		}
	}

	// A node's own resource.
	node := map[string]string{"rabbitmq.node.name": "rabbit@broker-1"}
	testutil.Expect(t, ps, "rabbitmq.node.memory.used", "By", true, false, 134217728, node)
	testutil.Expect(t, ps, "rabbitmq.node.uptime", "s", true, true, 7200, node)
	if p := testutil.One(t, ps, "rabbitmq.node.up", node); p.Int != 1 || p.Attrs["type"] != "disc" {
		t.Errorf("node.up = %+v", p)
	}
	for _, kind := range []string{"memory", "disk"} {
		if p := testutil.One(t, ps, "rabbitmq.node.alarm", map[string]string{"kind": kind}); p.Int != 0 {
			t.Errorf("%s alarm = %d, want 0", kind, p.Int)
		}
	}

	// A queue's own resource.
	q := map[string]string{"rabbitmq.queue.name": "orders"}
	testutil.Expect(t, ps, "rabbitmq.consumer.count", "{consumers}", true, false, 4, q)
	testutil.Expect(t, ps, "rabbitmq.message.current", "{messages}", true, false, 90,
		map[string]string{"rabbitmq.queue.name": "orders", "state": "ready"})
	// deliver + deliver_get, because the API counts basic.deliver and basic.get separately.
	testutil.Expect(t, ps, "rabbitmq.message.delivered", "{messages}", true, true, 850, q)
	if p := testutil.One(t, ps, "rabbitmq.queue.state", q); p.Attrs["state"] != "running" {
		t.Errorf("queue state = %+v", p.Attrs)
	}
	// A queue without message_stats still reports its depth, and no counters.
	dl := map[string]string{"rabbitmq.queue.name": "dead-letter"}
	testutil.Expect(t, ps, "rabbitmq.message.current", "{messages}", true, false, 20,
		map[string]string{"rabbitmq.queue.name": "dead-letter", "state": "ready"})
	if got := testutil.Find(ps, "rabbitmq.message.published", dl); len(got) != 0 {
		t.Errorf("a queue that never published must have no publish counter: %d points", len(got))
	}
}

// A broker whose nodes or queues cannot be read is still worth its overview: the collection is partial.
func TestMissingListingsArePartial(t *testing.T) {
	srv := server(t, map[string]string{"/api/overview": overviewJSON})
	c := collectorFor(t, srv.URL)
	defer c.Close()
	b := integrations.NewBatch(time.Now(), 0)
	err := c.Collect(context.Background(), b)
	var pe *integrations.PartialError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want partial", err)
	}
	testutil.Expect(t, testutil.Points(b), "rabbitmq.queue.count", "{queues}", true, false, 3, nil)
}

func TestUnauthorizedIsNeedsConfiguration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := collectorFor(t, srv.URL)
	defer c.Close()
	err := c.Collect(context.Background(), integrations.NewBatch(time.Now(), 0))
	var se *integrations.StatusError
	if !errors.As(err, &se) || se.Status != "needs_configuration" {
		t.Fatalf("err = %v", err)
	}
}

func TestQueueLimit(t *testing.T) {
	queues := make([]queue, MaxQueues+3)
	for i := range queues {
		queues[i] = queue{Name: "q", VHost: "/"}
	}
	b := integrations.NewBatch(time.Now(), 0)
	if dropped := RecordQueues(b, queues); dropped != 3 {
		t.Fatalf("dropped = %d, want 3", dropped)
	}
}

func TestUnreachableEndpoint(t *testing.T) {
	srv := server(t, map[string]string{"/api/overview": overviewJSON})
	url := srv.URL
	srv.Close()
	c := collectorFor(t, url)
	defer c.Close()
	err := c.Collect(context.Background(), integrations.NewBatch(time.Now(), 0))
	if !integrations.IsUnreachable(err) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "connect") && !strings.Contains(err.Error(), "refused") {
		t.Logf("unreachable error: %v", err)
	}
}
