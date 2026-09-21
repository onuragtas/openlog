// Package jvm implements the Java runtime integration: the `java.lang` MBeans of any JVM that exposes
// Jolokia, emitting the OpenTelemetry `jvm.*` metric names (semantic-conventions §6.16).
//
// This is what makes "the JVM" an integration rather than a per-product one: heap, garbage collection,
// threads, class loading and CPU are the same MBeans in Kafka, Cassandra, Solr, Tomcat and an application
// nobody wrote an integration for. A product-specific collector (kafka) adds its own MBeans on top of
// these rather than repeating them.
package jvm

import (
	"context"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/jolokia"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
)

// Integration is the JVM integration.
type Integration struct{}

// ID implements integrations.Integration.
func (Integration) ID() string { return config.IntegrationJVM }

// Spec implements integrations.Integration.
func (Integration) Spec() integrations.EndpointSpec {
	return integrations.EndpointSpec{DefaultPort: jolokia.DefaultPort}
}

// Hint implements integrations.Integration.
func (Integration) Hint(*integrations.Instance) string {
	return `# A JVM's metrics live in its MBeans, and openlog reads them over Jolokia — one HTTP endpoint the
# JVM serves itself. Add the agent to the start command (download jolokia-agent-jvm.jar from jolokia.org):
#   java -javaagent:/opt/jolokia/jolokia-agent-jvm.jar=port=8778,host=127.0.0.1 -jar app.jar
# Bind it to 127.0.0.1: it exposes the JVM's MBeans, so it belongs to the machine, not the network.
# Then, if it is not on the usual port or path, set the endpoint in openlog (host → Integrations → JVM):
integrations:
  jvm:
    # endpoint: http://127.0.0.1:8778/jolokia
    # username / password when the Jolokia agent is configured with authentication`
}

// New implements integrations.Integration.
func (Integration) New(inst *integrations.Instance, ep integrations.Endpoint) (integrations.Collector, error) {
	client, err := jolokia.New(inst, ep)
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

// Reads are the java.lang MBeans every JVM has. They are read in one bulk request.
var Reads = []jolokia.Read{
	{MBean: "java.lang:type=Memory", Attribute: "HeapMemoryUsage"},
	{MBean: "java.lang:type=Memory", Attribute: "NonHeapMemoryUsage"},
	{MBean: "java.lang:type=Threading", Attribute: "ThreadCount"},
	{MBean: "java.lang:type=Threading", Attribute: "DaemonThreadCount"},
	{MBean: "java.lang:type=Threading", Attribute: "PeakThreadCount"},
	{MBean: "java.lang:type=ClassLoading", Attribute: "LoadedClassCount"},
	{MBean: "java.lang:type=ClassLoading", Attribute: "UnloadedClassCount"},
	{MBean: "java.lang:type=OperatingSystem", Attribute: "ProcessCpuLoad"},
	{MBean: "java.lang:type=OperatingSystem", Attribute: "SystemCpuLoad"},
	{MBean: "java.lang:type=OperatingSystem", Attribute: "OpenFileDescriptorCount"},
	{MBean: "java.lang:type=OperatingSystem", Attribute: "MaxFileDescriptorCount"},
	{MBean: "java.lang:type=Runtime", Attribute: "Uptime"},
	{MBean: "java.lang:type=Runtime", Attribute: "VmVersion"},
	// Patterns: every collector and every memory pool in one answer each.
	{MBean: "java.lang:type=GarbageCollector,name=*", Attribute: "CollectionCount"},
	{MBean: "java.lang:type=GarbageCollector,name=*", Attribute: "CollectionTime"},
	{MBean: "java.lang:type=MemoryPool,name=*", Attribute: "Usage"},
}

// Collect implements integrations.Collector.
func (c *collector) Collect(ctx context.Context, b *integrations.Batch) error {
	responses, err := c.client.ReadAll(ctx, Reads)
	if err != nil {
		return err
	}
	if n := Record(b, responses); n == 0 {
		// Jolokia answered but no java.lang MBean did: the endpoint is a Jolokia bridge in front of
		// something that is not a JVM, or its MBean access is restricted.
		return integrations.NeedsConfiguration("no java.lang MBean could be read; check the Jolokia agent's access policy", false)
	}
	return nil
}

// Record emits the metrics of a bulk read and returns how many attributes it could use.
func Record(b *integrations.Batch, responses []jolokia.Response) int {
	s := b.Resource()
	used := 0
	// The two thread counts are emitted together as one metric partitioned by the daemon flag: a total and
	// a subset under the same name would overlap, and summing the series would count the daemons twice.
	var threads, daemons int64
	var haveThreads, haveDaemons bool
	use := func(ok bool) bool {
		if ok {
			used++
		}
		return ok
	}
	for _, r := range responses {
		if !r.OK() {
			continue
		}
		switch {
		case r.MBean == "java.lang:type=Memory" && r.Attribute == "HeapMemoryUsage":
			used += recordMemory(s, r.Value, "heap")
		case r.MBean == "java.lang:type=Memory" && r.Attribute == "NonHeapMemoryUsage":
			used += recordMemory(s, r.Value, "non_heap")
		case r.MBean == "java.lang:type=Threading":
			if v, ok := jolokia.Number(r.Value); use(ok) {
				switch r.Attribute {
				case "ThreadCount":
					threads, haveThreads = int64(v), true
				case "DaemonThreadCount":
					daemons, haveDaemons = int64(v), true
				case "PeakThreadCount":
					s.SumInt("jvm.thread.peak", "{threads}", false, int64(v))
				}
			}
		case r.MBean == "java.lang:type=ClassLoading":
			if v, ok := jolokia.Number(r.Value); use(ok) {
				switch r.Attribute {
				case "LoadedClassCount":
					s.SumInt("jvm.class.count", "{classes}", false, int64(v))
				case "UnloadedClassCount":
					s.SumInt("jvm.class.unloaded", "{classes}", true, int64(v))
				}
			}
		case r.MBean == "java.lang:type=OperatingSystem":
			v, ok := jolokia.Number(r.Value)
			if !use(ok) {
				continue
			}
			switch r.Attribute {
			case "ProcessCpuLoad":
				// The platform MXBean reports -1 until it has two samples to compare; a negative share is
				// "not measured yet", not a value.
				if v >= 0 {
					s.GaugeDouble("jvm.cpu.recent_utilization", "1", v)
				}
			case "SystemCpuLoad":
				if v >= 0 {
					s.GaugeDouble("jvm.system.cpu.utilization", "1", v)
				}
			case "OpenFileDescriptorCount":
				s.SumInt("jvm.file_descriptor.count", "{file_descriptors}", false, int64(v))
			case "MaxFileDescriptorCount":
				s.SumInt("jvm.file_descriptor.limit", "{file_descriptors}", false, int64(v))
			}
		case r.MBean == "java.lang:type=Runtime" && r.Attribute == "Uptime":
			if v, ok := jolokia.Number(r.Value); use(ok) {
				s.SumInt("jvm.uptime", "ms", true, int64(v))
				b.SetStartTime(b.Now().Add(-time.Duration(v) * time.Millisecond))
			}
		case r.MBean == "java.lang:type=Runtime" && r.Attribute == "VmVersion":
			if v, ok := r.Value.(string); ok && v != "" {
				used++
				b.SetResourceAttr(otlputil.Str("jvm.version", v))
			}
		case strings.HasPrefix(r.MBean, "java.lang:type=GarbageCollector"):
			used += recordByName(s, r.Value, func(name string, value any) {
				v, ok := jolokia.Number(value)
				if !ok {
					return
				}
				attr := otlputil.Str("jvm.gc.name", name)
				switch r.Attribute {
				case "CollectionCount":
					s.SumInt("jvm.gc.collections", "{collections}", true, int64(v), attr)
				case "CollectionTime":
					s.SumInt("jvm.gc.duration", "ms", true, int64(v), attr)
				}
			})
		case strings.HasPrefix(r.MBean, "java.lang:type=MemoryPool"):
			used += recordByName(s, r.Value, func(name string, value any) {
				attr := otlputil.Str("jvm.memory.pool.name", name)
				if v, ok := jolokia.Field(value, "used"); ok {
					s.SumInt("jvm.memory.pool.used", "By", false, int64(v), attr)
				}
				if v, ok := jolokia.Field(value, "max"); ok && v >= 0 {
					// A pool with no maximum reports -1; a limit of -1 bytes is not a limit.
					s.SumInt("jvm.memory.pool.limit", "By", false, int64(v), attr)
				}
			})
		}
	}
	switch {
	case haveThreads && haveDaemons:
		s.SumInt("jvm.thread.count", "{threads}", false, daemons, otlputil.Str("jvm.thread.daemon", "true"))
		s.SumInt("jvm.thread.count", "{threads}", false, max(threads-daemons, 0), otlputil.Str("jvm.thread.daemon", "false"))
	case haveThreads:
		s.SumInt("jvm.thread.count", "{threads}", false, threads)
	}
	return used
}

func recordMemory(s *integrations.Scope, value any, typ string) int {
	attr := otlputil.Str("jvm.memory.type", typ)
	used := 0
	for _, f := range []struct{ field, metric string }{
		{"used", "jvm.memory.used"}, {"committed", "jvm.memory.committed"}, {"max", "jvm.memory.limit"},
	} {
		v, ok := jolokia.Field(value, f.field)
		if !ok || (f.field == "max" && v < 0) {
			continue
		}
		s.SumInt(f.metric, "By", false, int64(v), attr)
		used++
	}
	return used
}

// recordByName walks the answer of a pattern read, which is a map of object name → attribute value, and
// calls fn with the MBean's `name=` property.
func recordByName(s *integrations.Scope, value any, fn func(name string, value any)) int {
	byMBean, ok := value.(map[string]any)
	if !ok {
		return 0
	}
	used := 0
	for objectName, v := range byMBean {
		name := propertyOf(objectName, "name")
		if name == "" {
			continue
		}
		// A pattern read of one attribute answers `{"<objectname>": {"CollectionCount": 12}}`; unwrap the
		// single-attribute map so the caller sees the value itself.
		if m, isMap := v.(map[string]any); isMap && len(m) == 1 {
			for _, inner := range m {
				v = inner
			}
		}
		fn(name, v)
		used++
	}
	return used
}

// propertyOf reads a key of a JMX object name ("java.lang:type=MemoryPool,name=G1 Eden Space").
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
