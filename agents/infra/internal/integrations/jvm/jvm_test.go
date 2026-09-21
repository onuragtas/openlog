package jvm

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/internal/testutil"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/jolokia"
)

// Recorded from a Temurin 21 JVM's Jolokia answer, trimmed to the attributes the collector reads.
const answer = `[
 {"status":200,"request":{"mbean":"java.lang:type=Memory","attribute":"HeapMemoryUsage"},
  "value":{"init":268435456,"used":1073741824,"committed":2147483648,"max":4294967296}},
 {"status":200,"request":{"mbean":"java.lang:type=Memory","attribute":"NonHeapMemoryUsage"},
  "value":{"init":7667712,"used":134217728,"committed":150994944,"max":-1}},
 {"status":200,"request":{"mbean":"java.lang:type=Threading","attribute":"ThreadCount"},"value":96},
 {"status":200,"request":{"mbean":"java.lang:type=Threading","attribute":"DaemonThreadCount"},"value":80},
 {"status":200,"request":{"mbean":"java.lang:type=Threading","attribute":"PeakThreadCount"},"value":120},
 {"status":200,"request":{"mbean":"java.lang:type=ClassLoading","attribute":"LoadedClassCount"},"value":14200},
 {"status":200,"request":{"mbean":"java.lang:type=ClassLoading","attribute":"UnloadedClassCount"},"value":31},
 {"status":200,"request":{"mbean":"java.lang:type=OperatingSystem","attribute":"ProcessCpuLoad"},"value":0.17},
 {"status":200,"request":{"mbean":"java.lang:type=OperatingSystem","attribute":"SystemCpuLoad"},"value":-1.0},
 {"status":200,"request":{"mbean":"java.lang:type=OperatingSystem","attribute":"OpenFileDescriptorCount"},"value":320},
 {"status":200,"request":{"mbean":"java.lang:type=Runtime","attribute":"Uptime"},"value":7200000},
 {"status":200,"request":{"mbean":"java.lang:type=Runtime","attribute":"VmVersion"},"value":"21.0.4+7-LTS"},
 {"status":200,"request":{"mbean":"java.lang:type=GarbageCollector,name=*","attribute":"CollectionCount"},
  "value":{"java.lang:name=G1 Young Generation,type=GarbageCollector":{"CollectionCount":4500},
           "java.lang:name=G1 Old Generation,type=GarbageCollector":{"CollectionCount":3}}},
 {"status":200,"request":{"mbean":"java.lang:type=GarbageCollector,name=*","attribute":"CollectionTime"},
  "value":{"java.lang:name=G1 Young Generation,type=GarbageCollector":{"CollectionTime":90000},
           "java.lang:name=G1 Old Generation,type=GarbageCollector":{"CollectionTime":800}}},
 {"status":200,"request":{"mbean":"java.lang:type=MemoryPool,name=*","attribute":"Usage"},
  "value":{"java.lang:name=G1 Eden Space,type=MemoryPool":{"Usage":{"used":536870912,"max":-1}},
           "java.lang:name=G1 Old Gen,type=MemoryPool":{"Usage":{"used":268435456,"max":4294967296}}}},
 {"status":404,"error":"javax.management.InstanceNotFoundException",
  "request":{"mbean":"java.lang:type=OperatingSystem","attribute":"MaxFileDescriptorCount"}}
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
	if res["jvm.version"] != "21.0.4+7-LTS" {
		t.Errorf("resource attributes = %v", res)
	}

	ps := testutil.Points(b)
	heap := map[string]string{"jvm.memory.type": "heap"}
	testutil.Expect(t, ps, "jvm.memory.used", "By", true, false, 1073741824, heap)
	testutil.Expect(t, ps, "jvm.memory.limit", "By", true, false, 4294967296, heap)
	// A non-heap pool reports max -1, which is "no limit" and not a limit of minus one byte.
	if got := testutil.Find(ps, "jvm.memory.limit", map[string]string{"jvm.memory.type": "non_heap"}); len(got) != 0 {
		t.Errorf("a max of -1 must not become a limit: %+v", got)
	}
	// The threads are partitioned by the daemon flag, so the two series sum to the total rather than
	// overlapping: 80 daemons and 16 others of the 96 the JVM reports.
	testutil.Expect(t, ps, "jvm.thread.count", "{threads}", true, false, 80, map[string]string{"jvm.thread.daemon": "true"})
	testutil.Expect(t, ps, "jvm.thread.count", "{threads}", true, false, 16, map[string]string{"jvm.thread.daemon": "false"})
	testutil.Expect(t, ps, "jvm.class.count", "{classes}", true, false, 14200, nil)
	testutil.Expect(t, ps, "jvm.uptime", "ms", true, true, 7200000, nil)
	testutil.Expect(t, ps, "jvm.file_descriptor.count", "{file_descriptors}", true, false, 320, nil)

	// The pattern reads: one series per collector and per pool.
	testutil.Expect(t, ps, "jvm.gc.collections", "{collections}", true, true, 4500, map[string]string{"jvm.gc.name": "G1 Young Generation"})
	testutil.Expect(t, ps, "jvm.gc.duration", "ms", true, true, 800, map[string]string{"jvm.gc.name": "G1 Old Generation"})
	testutil.Expect(t, ps, "jvm.memory.pool.used", "By", true, false, 536870912, map[string]string{"jvm.memory.pool.name": "G1 Eden Space"})
	testutil.Expect(t, ps, "jvm.memory.pool.limit", "By", true, false, 4294967296, map[string]string{"jvm.memory.pool.name": "G1 Old Gen"})

	if p := testutil.One(t, ps, "jvm.cpu.recent_utilization", nil); p.Double != 0.17 {
		t.Errorf("cpu = %v", p.Double)
	}
	// The platform MXBean reports -1 until it has two samples; that is "not measured yet", not a value.
	if got := testutil.Find(ps, "jvm.system.cpu.utilization", nil); len(got) != 0 {
		t.Errorf("a negative CPU share must not be recorded: %+v", got)
	}
	// A failed read (an attribute this JVM does not have) contributes nothing and costs nothing.
	if got := testutil.Find(ps, "jvm.file_descriptor.limit", nil); len(got) != 0 {
		t.Errorf("a failed read must not become a metric: %+v", got)
	}
}

func TestRecordWithNothingReadable(t *testing.T) {
	b := integrations.NewBatch(time.Now(), 0)
	used := Record(b, []jolokia.Response{
		{Status: 404, Error: "not found", MBean: "java.lang:type=Memory", Attribute: "HeapMemoryUsage"},
	})
	if used != 0 {
		t.Fatalf("used = %d, want 0", used)
	}
}

func TestPropertyOf(t *testing.T) {
	name := "java.lang:name=G1 Eden Space,type=MemoryPool"
	if got := propertyOf(name, "name"); got != "G1 Eden Space" {
		t.Errorf("name = %q", got)
	}
	if got := propertyOf(name, "type"); got != "MemoryPool" {
		t.Errorf("type = %q", got)
	}
	if got := propertyOf("nonsense", "name"); got != "" {
		t.Errorf("a name without properties must read as empty, got %q", got)
	}
}
