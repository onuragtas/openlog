package kafka

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/internal/testutil"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/jolokia"
)

// Recorded from a Kafka 3.7 broker's Jolokia answer, trimmed to the MBeans the collector reads.
const answer = `[
 {"status":200,"request":{"mbean":"kafka.server:type=BrokerTopicMetrics,name=MessagesInPerSec","attribute":"Count"},"value":1250000},
 {"status":200,"request":{"mbean":"kafka.server:type=BrokerTopicMetrics,name=BytesInPerSec","attribute":"Count"},"value":10485760},
 {"status":200,"request":{"mbean":"kafka.server:type=BrokerTopicMetrics,name=BytesOutPerSec","attribute":"Count"},"value":52428800},
 {"status":200,"request":{"mbean":"kafka.server:type=BrokerTopicMetrics,name=TotalFetchRequestsPerSec","attribute":"Count"},"value":98000},
 {"status":200,"request":{"mbean":"kafka.server:type=BrokerTopicMetrics,name=FailedProduceRequestsPerSec","attribute":"Count"},"value":4},
 {"status":200,"request":{"mbean":"kafka.server:type=ReplicaManager,name=PartitionCount","attribute":"Value"},"value":120},
 {"status":200,"request":{"mbean":"kafka.server:type=ReplicaManager,name=UnderReplicatedPartitions","attribute":"Value"},"value":2},
 {"status":200,"request":{"mbean":"kafka.controller:type=KafkaController,name=OfflinePartitionsCount","attribute":"Value"},"value":0},
 {"status":200,"request":{"mbean":"kafka.controller:type=KafkaController,name=ActiveControllerCount","attribute":"Value"},"value":1},
 {"status":200,"request":{"mbean":"kafka.server:type=KafkaRequestHandlerPool,name=RequestHandlerAvgIdlePercent","attribute":"OneMinuteRate"},"value":0.82},
 {"status":200,"request":{"mbean":"kafka.network:type=RequestChannel,name=RequestQueueSize","attribute":"Value"},"value":3},
 {"status":200,"request":{"mbean":"kafka.network:type=RequestMetrics,name=TotalTimeMs,request=*","attribute":"Mean"},
  "value":{"kafka.network:name=TotalTimeMs,request=Produce,type=RequestMetrics":{"Mean":2.4},
           "kafka.network:name=TotalTimeMs,request=Fetch,type=RequestMetrics":{"Mean":18.1}}},
 {"status":200,"request":{"mbean":"kafka.server:type=app-info","attribute":"version"},"value":"3.7.1"},
 {"status":200,"request":{"mbean":"kafka.server:type=KafkaServer,name=BrokerState","attribute":"Value"},"value":3},
 {"status":404,"error":"javax.management.InstanceNotFoundException",
  "request":{"mbean":"kafka.server:type=ReplicaManager,name=UnderMinIsrPartitionCount","attribute":"Value"}}
]`

func responses(t *testing.T) []jolokia.Response {
	t.Helper()
	var list []struct {
		Status  int    `json:"status"`
		Error   string `json:"error"`
		Value   any    `json:"value"`
		Request struct {
			MBean     string `json:"mbean"`
			Attribute string `json:"attribute"`
		} `json:"request"`
	}
	if err := json.Unmarshal([]byte(answer), &list); err != nil {
		t.Fatal(err)
	}
	out := make([]jolokia.Response, 0, len(list))
	for _, item := range list {
		out = append(out, jolokia.Response{Status: item.Status, Error: item.Error, Value: item.Value,
			MBean: item.Request.MBean, Attribute: item.Request.Attribute})
	}
	return out
}

func TestRecord(t *testing.T) {
	b := integrations.NewBatch(time.Now(), 0)
	if used := Record(b, responses(t)); used == 0 {
		t.Fatal("nothing was recorded")
	}
	res := map[string]string{}
	for _, kv := range b.ResourceAttrs() {
		res[kv.Key] = kv.Value.GetStringValue()
	}
	if res["kafka.version"] != "3.7.1" {
		t.Errorf("resource attributes = %v", res)
	}

	ps := testutil.Points(b)
	testutil.Expect(t, ps, "kafka.messages.in", "{messages}", true, true, 1250000, nil)
	testutil.Expect(t, ps, "kafka.network.io", "By", true, true, 10485760, map[string]string{"direction": "in"})
	testutil.Expect(t, ps, "kafka.network.io", "By", true, true, 52428800, map[string]string{"direction": "out"})
	testutil.Expect(t, ps, "kafka.request.count", "{requests}", true, true, 98000, map[string]string{"type": "fetch"})
	testutil.Expect(t, ps, "kafka.request.failed", "{requests}", true, true, 4, map[string]string{"type": "produce"})
	// The two numbers an operator watches for.
	testutil.Expect(t, ps, "kafka.partition.under_replicated", "{partitions}", true, false, 2, nil)
	testutil.Expect(t, ps, "kafka.partition.offline", "{partitions}", true, false, 0, nil)
	testutil.Expect(t, ps, "kafka.controller.active.count", "{controllers}", true, false, 1, nil)
	testutil.Expect(t, ps, "kafka.broker.state", "{state}", false, false, 3, nil)

	// The broker reports the idle share; busy is the same fact the way an operator reasons about it.
	if p := testutil.One(t, ps, "kafka.request.handler.busy", nil); p.Double < 0.17 || p.Double > 0.19 {
		t.Errorf("handler busy = %v, want about 0.18", p.Double)
	}
	// The pattern read: one series per request type.
	if p := testutil.One(t, ps, "kafka.request.time.avg", map[string]string{"type": "Fetch"}); p.Double != 18.1 {
		t.Errorf("fetch time = %v", p.Double)
	}
	// A failed read contributes nothing rather than a zero.
	if got := testutil.Find(ps, "kafka.partition.under_min_isr", nil); len(got) != 0 {
		t.Errorf("a failed read must not become a metric: %+v", got)
	}
}

// A Jolokia bridge in front of a JVM that is not a broker answers nothing useful, and the collector has to
// say so rather than report a broker with zero partitions.
func TestRecordWithoutKafkaMBeans(t *testing.T) {
	b := integrations.NewBatch(time.Now(), 0)
	used := Record(b, []jolokia.Response{
		{Status: 404, Error: "not found", MBean: "kafka.server:type=ReplicaManager,name=PartitionCount", Attribute: "Value"},
		{Status: 200, Value: 42.0, MBean: "java.lang:type=Threading", Attribute: "ThreadCount"},
	})
	if used != 0 {
		t.Fatalf("used = %d, want 0", used)
	}
}

func TestPropertyOf(t *testing.T) {
	name := "kafka.network:name=TotalTimeMs,request=Fetch,type=RequestMetrics"
	if got := propertyOf(name, "request"); got != "Fetch" {
		t.Errorf("request = %q", got)
	}
	if got := propertyOf(name, "missing"); got != "" {
		t.Errorf("a missing property must read as empty, got %q", got)
	}
}
