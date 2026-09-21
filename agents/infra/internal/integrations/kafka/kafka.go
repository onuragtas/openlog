// Package kafka implements the Apache Kafka broker integration: the broker's own MBeans read over Jolokia,
// emitting the metric names of the OpenTelemetry Collector's JMX receiver for Kafka
// (semantic-conventions §6.17).
//
// The JVM metrics of the same process are not repeated here: a Kafka broker is a JVM, the `jvm` integration
// already reads java.lang, and both bind to the same Jolokia endpoint. What this adds is what makes the
// process a *broker* — throughput, partitions, the controller, and the two numbers an operator watches for:
// under-replicated partitions and offline partitions.
package kafka

import (
	"context"
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/jolokia"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// KafkaPorts are the broker's own listener ports, which are never the Jolokia bridge's.
var KafkaPorts = []int{9092, 9093, 9094}

// Integration is the Kafka integration.
type Integration struct{}

// ID implements integrations.Integration.
func (Integration) ID() string { return config.IntegrationKafka }

// Spec implements integrations.Integration.
func (Integration) Spec() integrations.EndpointSpec {
	return integrations.EndpointSpec{DefaultPort: jolokia.DefaultPort}
}

// Hint implements integrations.Integration.
func (Integration) Hint(*integrations.Instance) string {
	return `# Kafka publishes its metrics as JMX MBeans, and openlog reads them over Jolokia — one HTTP endpoint
# the broker serves itself. Add the agent to the broker's JVM options and restart it:
#   KAFKA_OPTS="-javaagent:/opt/jolokia/jolokia-agent-jvm.jar=port=8778,host=127.0.0.1"
# Bind it to 127.0.0.1: it exposes the broker's MBeans, so it belongs to the machine, not the network.
# Then, if it is not on the usual port or path, set the endpoint in openlog (host → Integrations → Kafka):
integrations:
  kafka:
    # endpoint: http://127.0.0.1:8778/jolokia`
}

// New implements integrations.Integration.
func (Integration) New(inst *integrations.Instance, ep integrations.Endpoint) (integrations.Collector, error) {
	// Discovery finds the broker's listener port; the Jolokia bridge is elsewhere, so a discovered 9092
	// becomes the default Jolokia port unless an endpoint says otherwise.
	client, err := jolokia.New(inst, ep, KafkaPorts...)
	if err != nil {
		return nil, err
	}
	return &collector{inst: inst, client: client}, nil
}

type collector struct {
	inst   *integrations.Instance
	client *jolokia.Client
}

func (c *collector) Close() { c.client.Close() }

// Reads are the broker MBeans, read in one bulk request.
var Reads = []jolokia.Read{
	// Throughput. The Count attribute is the monotonic total behind the *Rate ones, which is what a metric
	// store wants: a rate computed by the broker over its own window cannot be re-aggregated.
	{MBean: "kafka.server:type=BrokerTopicMetrics,name=MessagesInPerSec", Attribute: "Count"},
	{MBean: "kafka.server:type=BrokerTopicMetrics,name=BytesInPerSec", Attribute: "Count"},
	{MBean: "kafka.server:type=BrokerTopicMetrics,name=BytesOutPerSec", Attribute: "Count"},
	{MBean: "kafka.server:type=BrokerTopicMetrics,name=TotalFetchRequestsPerSec", Attribute: "Count"},
	{MBean: "kafka.server:type=BrokerTopicMetrics,name=TotalProduceRequestsPerSec", Attribute: "Count"},
	{MBean: "kafka.server:type=BrokerTopicMetrics,name=FailedFetchRequestsPerSec", Attribute: "Count"},
	{MBean: "kafka.server:type=BrokerTopicMetrics,name=FailedProduceRequestsPerSec", Attribute: "Count"},
	// Replication: the two numbers that say a cluster is in trouble.
	{MBean: "kafka.server:type=ReplicaManager,name=PartitionCount", Attribute: "Value"},
	{MBean: "kafka.server:type=ReplicaManager,name=LeaderCount", Attribute: "Value"},
	{MBean: "kafka.server:type=ReplicaManager,name=UnderReplicatedPartitions", Attribute: "Value"},
	{MBean: "kafka.server:type=ReplicaManager,name=UnderMinIsrPartitionCount", Attribute: "Value"},
	{MBean: "kafka.server:type=ReplicaManager,name=IsrShrinksPerSec", Attribute: "Count"},
	{MBean: "kafka.server:type=ReplicaManager,name=IsrExpandsPerSec", Attribute: "Count"},
	{MBean: "kafka.controller:type=KafkaController,name=OfflinePartitionsCount", Attribute: "Value"},
	{MBean: "kafka.controller:type=KafkaController,name=ActiveControllerCount", Attribute: "Value"},
	{MBean: "kafka.controller:type=KafkaController,name=GlobalTopicCount", Attribute: "Value"},
	{MBean: "kafka.controller:type=KafkaController,name=GlobalPartitionCount", Attribute: "Value"},
	{MBean: "kafka.controller:type=ControllerStats,name=LeaderElectionRateAndTimeMs", Attribute: "Count"},
	{MBean: "kafka.controller:type=ControllerStats,name=UncleanLeaderElectionsPerSec", Attribute: "Count"},
	// Request handling: the idle share is the broker's own headroom figure.
	{MBean: "kafka.server:type=KafkaRequestHandlerPool,name=RequestHandlerAvgIdlePercent", Attribute: "OneMinuteRate"},
	{MBean: "kafka.network:type=SocketServer,name=NetworkProcessorAvgIdlePercent", Attribute: "Value"},
	{MBean: "kafka.network:type=RequestChannel,name=RequestQueueSize", Attribute: "Value"},
	{MBean: "kafka.network:type=RequestChannel,name=ResponseQueueSize", Attribute: "Value"},
	// Per-request latency, as a pattern so every request type comes back in one answer.
	{MBean: "kafka.network:type=RequestMetrics,name=TotalTimeMs,request=*", Attribute: "Mean"},
	{MBean: "kafka.network:type=RequestMetrics,name=RequestsPerSec,request=*,version=*", Attribute: "Count"},
	// Logs and purgatory.
	{MBean: "kafka.log:type=LogFlushStats,name=LogFlushRateAndTimeMs", Attribute: "Count"},
	{MBean: "kafka.server:type=DelayedOperationPurgatory,delayedOperation=Produce,name=PurgatorySize", Attribute: "Value"},
	{MBean: "kafka.server:type=DelayedOperationPurgatory,delayedOperation=Fetch,name=PurgatorySize", Attribute: "Value"},
	// Identity.
	{MBean: "kafka.server:type=app-info", Attribute: "version"},
	{MBean: "kafka.server:type=KafkaServer,name=BrokerState", Attribute: "Value"},
}

// Collect implements integrations.Collector.
func (c *collector) Collect(ctx context.Context, b *integrations.Batch) error {
	responses, err := c.client.ReadAll(ctx, Reads)
	if err != nil {
		return err
	}
	if n := Record(b, responses); n == 0 {
		// Jolokia answered, but no kafka.* MBean did: the bridge is in front of a JVM that is not a broker.
		return integrations.NeedsConfiguration("no Kafka MBean could be read; the Jolokia endpoint belongs to another JVM, or its access policy hides kafka.*", false)
	}
	return nil
}

// simple maps an MBean to one metric; the value is whatever attribute was read.
var simple = map[string]struct {
	metric    string
	unit      string
	monotonic bool
	attrs     []*commonpb.KeyValue
}{
	"kafka.server:type=BrokerTopicMetrics,name=MessagesInPerSec":                              {"kafka.messages.in", "{messages}", true, nil},
	"kafka.server:type=BrokerTopicMetrics,name=TotalFetchRequestsPerSec":                      {"kafka.request.count", "{requests}", true, kv("type", "fetch")},
	"kafka.server:type=BrokerTopicMetrics,name=TotalProduceRequestsPerSec":                    {"kafka.request.count", "{requests}", true, kv("type", "produce")},
	"kafka.server:type=BrokerTopicMetrics,name=FailedFetchRequestsPerSec":                     {"kafka.request.failed", "{requests}", true, kv("type", "fetch")},
	"kafka.server:type=BrokerTopicMetrics,name=FailedProduceRequestsPerSec":                   {"kafka.request.failed", "{requests}", true, kv("type", "produce")},
	"kafka.server:type=ReplicaManager,name=PartitionCount":                                    {"kafka.partition.count", "{partitions}", false, nil},
	"kafka.server:type=ReplicaManager,name=LeaderCount":                                       {"kafka.leader.count", "{partitions}", false, nil},
	"kafka.server:type=ReplicaManager,name=UnderReplicatedPartitions":                         {"kafka.partition.under_replicated", "{partitions}", false, nil},
	"kafka.server:type=ReplicaManager,name=UnderMinIsrPartitionCount":                         {"kafka.partition.under_min_isr", "{partitions}", false, nil},
	"kafka.server:type=ReplicaManager,name=IsrShrinksPerSec":                                  {"kafka.isr.operation.count", "{operations}", true, kv("operation", "shrink")},
	"kafka.server:type=ReplicaManager,name=IsrExpandsPerSec":                                  {"kafka.isr.operation.count", "{operations}", true, kv("operation", "expand")},
	"kafka.controller:type=KafkaController,name=OfflinePartitionsCount":                       {"kafka.partition.offline", "{partitions}", false, nil},
	"kafka.controller:type=KafkaController,name=ActiveControllerCount":                        {"kafka.controller.active.count", "{controllers}", false, nil},
	"kafka.controller:type=KafkaController,name=GlobalTopicCount":                             {"kafka.topic.count", "{topics}", false, nil},
	"kafka.controller:type=KafkaController,name=GlobalPartitionCount":                         {"kafka.partition.global", "{partitions}", false, nil},
	"kafka.controller:type=ControllerStats,name=LeaderElectionRateAndTimeMs":                  {"kafka.leader.election.count", "{elections}", true, nil},
	"kafka.controller:type=ControllerStats,name=UncleanLeaderElectionsPerSec":                 {"kafka.leader.election.unclean", "{elections}", true, nil},
	"kafka.network:type=RequestChannel,name=RequestQueueSize":                                 {"kafka.request.queue", "{requests}", false, nil},
	"kafka.network:type=RequestChannel,name=ResponseQueueSize":                                {"kafka.response.queue", "{responses}", false, nil},
	"kafka.log:type=LogFlushStats,name=LogFlushRateAndTimeMs":                                 {"kafka.logs.flush.count", "{flushes}", true, nil},
	"kafka.server:type=DelayedOperationPurgatory,delayedOperation=Produce,name=PurgatorySize": {"kafka.purgatory.size", "{operations}", false, kv("operation", "produce")},
	"kafka.server:type=DelayedOperationPurgatory,delayedOperation=Fetch,name=PurgatorySize":   {"kafka.purgatory.size", "{operations}", false, kv("operation", "fetch")},
}

func kv(k, v string) []*commonpb.KeyValue { return []*commonpb.KeyValue{otlputil.Str(k, v)} }

// Record emits the metrics of a bulk read and returns how many attributes it could use.
func Record(b *integrations.Batch, responses []jolokia.Response) int {
	s := b.Resource()
	used := 0
	for _, r := range responses {
		if !r.OK() {
			continue
		}
		if spec, ok := simple[r.MBean]; ok {
			if v, ok := jolokia.Number(r.Value); ok {
				s.SumInt(spec.metric, spec.unit, spec.monotonic, int64(v), spec.attrs...)
				used++
			}
			continue
		}
		switch {
		case r.MBean == "kafka.server:type=BrokerTopicMetrics,name=BytesInPerSec":
			if v, ok := jolokia.Number(r.Value); ok {
				s.SumInt("kafka.network.io", "By", true, int64(v), otlputil.Str("direction", "in"))
				used++
			}
		case r.MBean == "kafka.server:type=BrokerTopicMetrics,name=BytesOutPerSec":
			if v, ok := jolokia.Number(r.Value); ok {
				s.SumInt("kafka.network.io", "By", true, int64(v), otlputil.Str("direction", "out"))
				used++
			}
		case r.MBean == "kafka.server:type=KafkaRequestHandlerPool,name=RequestHandlerAvgIdlePercent":
			if v, ok := jolokia.Number(r.Value); ok {
				// The broker reports the idle share of its request handler threads (0–1); busy is what an
				// operator reasons about, and one minus it is the same fact the right way round.
				s.GaugeDouble("kafka.request.handler.busy", "1", clamp(1-v))
				used++
			}
		case r.MBean == "kafka.network:type=SocketServer,name=NetworkProcessorAvgIdlePercent":
			if v, ok := jolokia.Number(r.Value); ok {
				s.GaugeDouble("kafka.network.processor.busy", "1", clamp(1-v))
				used++
			}
		case strings.HasPrefix(r.MBean, "kafka.network:type=RequestMetrics,name=TotalTimeMs"):
			used += byProperty(r.Value, "request", func(request string, value any) {
				if v, ok := jolokia.Number(value); ok {
					s.GaugeDouble("kafka.request.time.avg", "ms", v, otlputil.Str("type", request))
				}
			})
		case strings.HasPrefix(r.MBean, "kafka.network:type=RequestMetrics,name=RequestsPerSec"):
			used += byProperty(r.Value, "request", func(request string, value any) {
				if v, ok := jolokia.Number(value); ok {
					s.SumInt("kafka.request.total", "{requests}", true, int64(v), otlputil.Str("type", request))
				}
			})
		case r.MBean == "kafka.server:type=app-info":
			if v, ok := r.Value.(string); ok && v != "" {
				b.SetResourceAttr(otlputil.Str("kafka.version", v))
				used++
			}
		case r.MBean == "kafka.server:type=KafkaServer,name=BrokerState":
			if v, ok := jolokia.Number(r.Value); ok {
				// 3 is RUNNING; the number is kept as it is so a state nobody documented is still visible.
				s.GaugeInt("kafka.broker.state", "{state}", int64(v))
				used++
			}
		}
	}
	return used
}

// byProperty walks the answer of a pattern read and calls fn with the named object-name property.
func byProperty(value any, key string, fn func(value string, attr any)) int {
	byMBean, ok := value.(map[string]any)
	if !ok {
		return 0
	}
	used := 0
	// A request type appears once per protocol version; the versions are summed into the type, because
	// "how slow is Fetch" is the question, not "how slow is Fetch v12".
	for objectName, v := range byMBean {
		prop := propertyOf(objectName, key)
		if prop == "" {
			continue
		}
		if m, isMap := v.(map[string]any); isMap && len(m) == 1 {
			for _, inner := range m {
				v = inner
			}
		}
		fn(prop, v)
		used++
	}
	return used
}

// propertyOf reads a key of a JMX object name.
func propertyOf(objectName, key string) string {
	_, props, ok := strings.Cut(objectName, ":")
	if !ok {
		return ""
	}
	for _, part := range strings.Split(props, ",") {
		k, v, ok := strings.Cut(part, "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), key) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func clamp(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	}
	return v
}
