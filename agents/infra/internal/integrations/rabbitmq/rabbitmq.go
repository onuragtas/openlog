// Package rabbitmq implements the RabbitMQ integration: the management plugin's HTTP API, emitting the
// metrics of the OpenTelemetry Collector rabbitmqreceiver plus the node health the receiver leaves out
// (semantic-conventions §6.13).
package rabbitmq

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/httpx"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// ManagementPort is where the management plugin listens; discovery finds the AMQP port (5672), so the
// integration moves to this one unless an endpoint says otherwise.
const ManagementPort = 15672

// AMQPPorts are the service's own ports, which are never the API's.
var AMQPPorts = []int{5672, 5671}

// MaxQueues bounds the queues one collection stores: a queue is a set of metric series, and a broker used
// for per-request reply queues has thousands of short-lived ones.
const MaxQueues = 500

// Integration is the RabbitMQ integration.
type Integration struct{}

// ID implements integrations.Integration.
func (Integration) ID() string { return config.IntegrationRabbitMQ }

// Spec implements integrations.Integration.
func (Integration) Spec() integrations.EndpointSpec {
	return integrations.EndpointSpec{DefaultPort: ManagementPort}
}

// Hint implements integrations.Integration.
func (Integration) Hint(*integrations.Instance) string {
	return `# RabbitMQ publishes its metrics through the management plugin's HTTP API. Enable it and create a
# read-only monitoring user (the agent never changes the broker):
#   rabbitmq-plugins enable rabbitmq_management
#   rabbitmqctl add_user openlog '<password>'
#   rabbitmqctl set_user_tags openlog monitoring
#   rabbitmqctl set_permissions -p / openlog "" "" ".*"
# Then enter the user and password in openlog (host → Integrations → RabbitMQ), or in config.yaml:
integrations:
  rabbitmq:
    username: openlog
    password: env:OPENLOG_RABBITMQ_PASSWORD   # or file:/etc/openlog-infra-agent/rabbitmq.password
    # endpoint: http://127.0.0.1:15672        # only when the management API is elsewhere`
}

// New implements integrations.Integration.
func (Integration) New(inst *integrations.Instance, ep integrations.Endpoint) (integrations.Collector, error) {
	base, err := httpx.BaseURL(inst, ep, ManagementPort, AMQPPorts...)
	if err != nil {
		return nil, err
	}
	client, err := httpx.Client(inst)
	if err != nil {
		return nil, err
	}
	return &collector{inst: inst, base: strings.TrimSuffix(base, "/"), client: client}, nil
}

type collector struct {
	inst   *integrations.Instance
	base   string
	client *http.Client
}

func (c *collector) Close() { c.client.CloseIdleConnections() }

// overview is GET /api/overview: the broker's identity and its message rates.
type overview struct {
	RabbitMQVersion string `json:"rabbitmq_version"`
	ClusterName     string `json:"cluster_name"`
	Node            string `json:"node"`
	ObjectTotals    struct {
		Connections int64 `json:"connections"`
		Channels    int64 `json:"channels"`
		Exchanges   int64 `json:"exchanges"`
		Queues      int64 `json:"queues"`
		Consumers   int64 `json:"consumers"`
	} `json:"object_totals"`
}

type messageStats struct {
	Publish          int64 `json:"publish"`
	Deliver          int64 `json:"deliver"`
	DeliverGet       int64 `json:"deliver_get"`
	Ack              int64 `json:"ack"`
	Redeliver        int64 `json:"redeliver"`
	DropUnroutable   int64 `json:"drop_unroutable"`
	ReturnUnroutable int64 `json:"return_unroutable"`
}

// node is one entry of GET /api/nodes: the health of the Erlang node behind the broker.
type node struct {
	Name            string   `json:"name"`
	Type            string   `json:"type"`
	Running         bool     `json:"running"`
	MemUsed         int64    `json:"mem_used"`
	MemLimit        int64    `json:"mem_limit"`
	MemAlarm        bool     `json:"mem_alarm"`
	DiskFree        int64    `json:"disk_free"`
	DiskFreeLimit   int64    `json:"disk_free_limit"`
	DiskFreeAlarm   bool     `json:"disk_free_alarm"`
	FDUsed          int64    `json:"fd_used"`
	FDTotal         int64    `json:"fd_total"`
	SocketsUsed     int64    `json:"sockets_used"`
	SocketsTotal    int64    `json:"sockets_total"`
	ProcUsed        int64    `json:"proc_used"`
	ProcTotal       int64    `json:"proc_total"`
	Uptime          int64    `json:"uptime"` // milliseconds
	RunQueue        int64    `json:"run_queue"`
	GCNum           int64    `json:"gc_num"`
	ContextSwitches int64    `json:"context_switches"`
	Partitions      []string `json:"partitions"`
}

// queue is one entry of GET /api/queues.
type queue struct {
	Name                   string       `json:"name"`
	VHost                  string       `json:"vhost"`
	Node                   string       `json:"node"`
	State                  string       `json:"state"`
	Consumers              int64        `json:"consumers"`
	Messages               int64        `json:"messages"`
	MessagesReady          int64        `json:"messages_ready"`
	MessagesUnacknowledged int64        `json:"messages_unacknowledged"`
	MessageStats           messageStats `json:"message_stats"`
}

// Collect implements integrations.Collector.
func (c *collector) Collect(ctx context.Context, b *integrations.Batch) error {
	var ov overview
	if err := httpx.GetJSON(ctx, c.client, c.inst, c.base+"/api/overview", &ov, 0); err != nil {
		return err
	}
	if ov.RabbitMQVersion != "" {
		b.SetResourceAttr(otlputil.Str("rabbitmq.version", ov.RabbitMQVersion))
	}
	if ov.ClusterName != "" {
		b.SetResourceAttr(otlputil.Str("rabbitmq.cluster.name", ov.ClusterName))
	}
	RecordOverview(b, ov)

	var partial []string
	var nodes []node
	if err := httpx.GetJSON(ctx, c.client, c.inst, c.base+"/api/nodes", &nodes, 0); err != nil {
		partial = append(partial, "nodes: "+err.Error())
	} else {
		RecordNodes(b, nodes)
	}
	var queues []queue
	// The columns keep the answer small on a broker with many queues: without them RabbitMQ serializes
	// every queue's full state, which is megabytes and seconds of broker CPU.
	cols := "name,vhost,node,state,consumers,messages,messages_ready,messages_unacknowledged,message_stats.publish," +
		"message_stats.deliver,message_stats.deliver_get,message_stats.ack,message_stats.redeliver,message_stats.drop_unroutable"
	url := fmt.Sprintf("%s/api/queues?disable_stats=false&page=1&page_size=%d&columns=%s", c.base, MaxQueues, cols)
	if err := httpx.GetJSON(ctx, c.client, c.inst, url, &queues, 0); err != nil {
		// Older brokers do not paginate /api/queues; the plain listing is the fallback.
		if err2 := httpx.GetJSON(ctx, c.client, c.inst, c.base+"/api/queues?columns="+cols, &queues, 0); err2 != nil {
			partial = append(partial, "queues: "+err2.Error())
		}
	}
	if dropped := RecordQueues(b, queues); dropped > 0 {
		partial = append(partial, fmt.Sprintf("%d queues beyond the limit of %d were not stored", dropped, MaxQueues))
	}
	if len(partial) > 0 {
		return integrations.Partial(fmt.Errorf("%s", strings.Join(partial, "; ")))
	}
	return nil
}

// RecordOverview emits the broker-wide object counts.
//
// Only counts the queue listing cannot produce are emitted here: the message and consumer metrics are the
// queue's (rabbitmqreceiver names them per queue), and emitting the broker's totals under the same names
// would make every sum over an instance count each message twice — once in its queue and once in the total.
func RecordOverview(b *integrations.Batch, ov overview) {
	s := b.Resource()
	s.SumInt("rabbitmq.connection.count", "{connections}", false, ov.ObjectTotals.Connections)
	s.SumInt("rabbitmq.channel.count", "{channels}", false, ov.ObjectTotals.Channels)
	s.SumInt("rabbitmq.exchange.count", "{exchanges}", false, ov.ObjectTotals.Exchanges)
	s.SumInt("rabbitmq.queue.count", "{queues}", false, ov.ObjectTotals.Queues)
}

func recordMessageStats(s *integrations.Scope, m messageStats, attrs ...*commonpb.KeyValue) {
	add := func(name string, v int64) {
		if v > 0 {
			s.SumInt(name, "{messages}", true, v, attrs...)
		}
	}
	add("rabbitmq.message.published", m.Publish)
	add("rabbitmq.message.delivered", m.Deliver+m.DeliverGet)
	add("rabbitmq.message.acknowledged", m.Ack)
	add("rabbitmq.message.redelivered", m.Redeliver)
	add("rabbitmq.message.dropped", m.DropUnroutable+m.ReturnUnroutable)
}

// RecordNodes emits one resource per broker node.
func RecordNodes(b *integrations.Batch, nodes []node) {
	for _, n := range nodes {
		s := b.Resource(otlputil.Str("rabbitmq.node.name", n.Name))
		up := int64(0)
		if n.Running {
			up = 1
		}
		s.GaugeInt("rabbitmq.node.up", "{status}", up, otlputil.Str("type", n.Type))
		s.SumInt("rabbitmq.node.memory.used", "By", false, n.MemUsed)
		s.SumInt("rabbitmq.node.memory.limit", "By", false, n.MemLimit)
		s.SumInt("rabbitmq.node.disk.free", "By", false, n.DiskFree)
		s.SumInt("rabbitmq.node.disk.free_limit", "By", false, n.DiskFreeLimit)
		s.SumInt("rabbitmq.node.file_descriptors.used", "{file_descriptors}", false, n.FDUsed)
		s.SumInt("rabbitmq.node.file_descriptors.limit", "{file_descriptors}", false, n.FDTotal)
		s.SumInt("rabbitmq.node.sockets.used", "{sockets}", false, n.SocketsUsed)
		s.SumInt("rabbitmq.node.sockets.limit", "{sockets}", false, n.SocketsTotal)
		s.SumInt("rabbitmq.node.processes.used", "{processes}", false, n.ProcUsed)
		s.SumInt("rabbitmq.node.processes.limit", "{processes}", false, n.ProcTotal)
		s.GaugeInt("rabbitmq.node.run_queue", "{processes}", n.RunQueue)
		if n.Uptime > 0 {
			s.SumInt("rabbitmq.node.uptime", "s", true, n.Uptime/1000)
		}
		// An alarm is why a broker stops accepting publishes; it belongs next to the numbers that caused it.
		s.GaugeInt("rabbitmq.node.alarm", "{status}", boolInt(n.MemAlarm), otlputil.Str("kind", "memory"))
		s.GaugeInt("rabbitmq.node.alarm", "{status}", boolInt(n.DiskFreeAlarm), otlputil.Str("kind", "disk"))
		s.GaugeInt("rabbitmq.node.partitions", "{partitions}", int64(len(n.Partitions)))
	}
}

// RecordQueues emits one resource per queue and returns how many were dropped by MaxQueues.
func RecordQueues(b *integrations.Batch, queues []queue) (dropped int) {
	for i, q := range queues {
		if i >= MaxQueues {
			return len(queues) - MaxQueues
		}
		s := b.Resource(
			otlputil.Str("rabbitmq.queue.name", q.Name),
			otlputil.Str("rabbitmq.vhost.name", q.VHost),
			otlputil.Str("rabbitmq.node.name", q.Node),
		)
		s.SumInt("rabbitmq.consumer.count", "{consumers}", false, q.Consumers)
		s.SumInt("rabbitmq.message.current", "{messages}", false, q.MessagesReady, otlputil.Str("state", "ready"))
		s.SumInt("rabbitmq.message.current", "{messages}", false, q.MessagesUnacknowledged, otlputil.Str("state", "unacknowledged"))
		recordMessageStats(s, q.MessageStats)
		if q.State != "" {
			s.GaugeInt("rabbitmq.queue.state", "{status}", 1, otlputil.Str("state", q.State))
		}
	}
	return 0
}

func boolInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}
