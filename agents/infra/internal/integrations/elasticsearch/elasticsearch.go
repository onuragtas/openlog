// Package elasticsearch implements the Elasticsearch (and OpenSearch) integration: the REST API's cluster
// health and node statistics, emitting the metrics of the OpenTelemetry Collector elasticsearchreceiver
// (semantic-conventions §6.14).
//
// Only the node the endpoint points at is read (`/_nodes/_local/stats`), not the whole cluster: an agent
// runs on every node, so asking each node about all the others would multiply the same numbers by the size
// of the cluster. The cluster health is small and cluster-wide, so it is read once per node and carries the
// cluster name, which is what makes the series of a cluster add up.
package elasticsearch

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/httpx"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
)

// DefaultPort is the REST API port; the transport port (9300) is never the API's.
const DefaultPort = 9200

// transportPorts are the cluster's internal ports, which discovery may find first.
var transportPorts = []int{9300, 9600}

// Integration is the Elasticsearch integration.
type Integration struct{}

// ID implements integrations.Integration.
func (Integration) ID() string { return config.IntegrationElasticsearch }

// Spec implements integrations.Integration.
func (Integration) Spec() integrations.EndpointSpec {
	return integrations.EndpointSpec{DefaultPort: DefaultPort, SkipPort: func(p int) bool { return p == 9300 }}
}

// Hint implements integrations.Integration.
func (Integration) Hint(*integrations.Instance) string {
	return `# Elasticsearch (and OpenSearch) publish their metrics on the REST API. With security enabled the agent
# needs a read-only user holding the built-in monitoring role:
#   POST /_security/user/openlog {"password":"<password>","roles":["monitoring_user"]}
# Then enter the user and password in openlog (host → Integrations → Elasticsearch), or in config.yaml:
integrations:
  elasticsearch:
    username: openlog
    password: env:OPENLOG_ELASTICSEARCH_PASSWORD
    # endpoint: https://127.0.0.1:9200
    # tls: { enabled: true, ca_file: /etc/elasticsearch/certs/http_ca.crt }`
}

// New implements integrations.Integration.
func (Integration) New(inst *integrations.Instance, ep integrations.Endpoint) (integrations.Collector, error) {
	base, err := httpx.BaseURL(inst, ep, DefaultPort, transportPorts...)
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

// root is GET /: the node's identity. The node statistics carry no version, and the distribution is what
// tells OpenSearch from Elasticsearch, so the root document is read with every collection — it is a few
// hundred bytes and an upgrade or a distribution change must not need an agent restart to be seen.
type root struct {
	ClusterName string `json:"cluster_name"`
	Version     struct {
		Number       string `json:"number"`
		Distribution string `json:"distribution"` // "opensearch" on OpenSearch; absent on Elasticsearch
	} `json:"version"`
}

// health is GET /_cluster/health.
type health struct {
	ClusterName                 string `json:"cluster_name"`
	Status                      string `json:"status"`
	NumberOfNodes               int64  `json:"number_of_nodes"`
	NumberOfDataNodes           int64  `json:"number_of_data_nodes"`
	ActivePrimaryShards         int64  `json:"active_primary_shards"`
	ActiveShards                int64  `json:"active_shards"`
	RelocatingShards            int64  `json:"relocating_shards"`
	InitializingShards          int64  `json:"initializing_shards"`
	UnassignedShards            int64  `json:"unassigned_shards"`
	DelayedUnassignedShards     int64  `json:"delayed_unassigned_shards"`
	NumberOfPendingTasks        int64  `json:"number_of_pending_tasks"`
	TaskMaxWaitingInQueueMillis int64  `json:"task_max_waiting_in_queue_millis"`
}

// nodeStats is GET /_nodes/_local/stats: the node the agent's host runs.
type nodeStats struct {
	ClusterName string `json:"cluster_name"`
	Nodes       map[string]struct {
		Name    string   `json:"name"`
		Roles   []string `json:"roles"`
		Indices struct {
			Docs struct {
				Count   int64 `json:"count"`
				Deleted int64 `json:"deleted"`
			} `json:"docs"`
			Store struct {
				SizeInBytes int64 `json:"size_in_bytes"`
			} `json:"store"`
			Indexing struct {
				IndexTotal        int64 `json:"index_total"`
				IndexTimeInMillis int64 `json:"index_time_in_millis"`
				IndexFailed       int64 `json:"index_failed"`
			} `json:"indexing"`
			Search struct {
				QueryTotal        int64 `json:"query_total"`
				QueryTimeInMillis int64 `json:"query_time_in_millis"`
				FetchTotal        int64 `json:"fetch_total"`
				FetchTimeInMillis int64 `json:"fetch_time_in_millis"`
			} `json:"search"`
			Merges struct {
				Total             int64 `json:"total"`
				TotalTimeInMillis int64 `json:"total_time_in_millis"`
				TotalDocs         int64 `json:"total_docs"`
			} `json:"merges"`
			Translog struct {
				Operations  int64 `json:"operations"`
				SizeInBytes int64 `json:"size_in_bytes"`
			} `json:"translog"`
			QueryCache struct {
				MemorySizeInBytes int64 `json:"memory_size_in_bytes"`
				HitCount          int64 `json:"hit_count"`
				MissCount         int64 `json:"miss_count"`
				Evictions         int64 `json:"evictions"`
			} `json:"query_cache"`
			Fielddata struct {
				MemorySizeInBytes int64 `json:"memory_size_in_bytes"`
				Evictions         int64 `json:"evictions"`
			} `json:"fielddata"`
		} `json:"indices"`
		JVM struct {
			Mem struct {
				HeapUsedInBytes    int64 `json:"heap_used_in_bytes"`
				HeapMaxInBytes     int64 `json:"heap_max_in_bytes"`
				HeapUsedPercent    int64 `json:"heap_used_percent"`
				NonHeapUsedInBytes int64 `json:"non_heap_used_in_bytes"`
			} `json:"mem"`
			Threads struct {
				Count int64 `json:"count"`
			} `json:"threads"`
			GC struct {
				Collectors map[string]struct {
					CollectionCount        int64 `json:"collection_count"`
					CollectionTimeInMillis int64 `json:"collection_time_in_millis"`
				} `json:"collectors"`
			} `json:"gc"`
		} `json:"jvm"`
		Process struct {
			OpenFileDescriptors int64 `json:"open_file_descriptors"`
			MaxFileDescriptors  int64 `json:"max_file_descriptors"`
			CPU                 struct {
				Percent int64 `json:"percent"`
			} `json:"cpu"`
		} `json:"process"`
		ThreadPool map[string]struct {
			Threads   int64 `json:"threads"`
			Queue     int64 `json:"queue"`
			Active    int64 `json:"active"`
			Rejected  int64 `json:"rejected"`
			Completed int64 `json:"completed"`
		} `json:"thread_pool"`
		FS struct {
			Total struct {
				TotalInBytes     int64 `json:"total_in_bytes"`
				FreeInBytes      int64 `json:"free_in_bytes"`
				AvailableInBytes int64 `json:"available_in_bytes"`
			} `json:"total"`
		} `json:"fs"`
		Breakers map[string]struct {
			Tripped              int64 `json:"tripped"`
			EstimatedSizeInBytes int64 `json:"estimated_size_in_bytes"`
			LimitSizeInBytes     int64 `json:"limit_size_in_bytes"`
		} `json:"breakers"`
	} `json:"nodes"`
}

// clusterStatus maps the health colour to a number, so a chart and an alert can use it: 0 green, 1 yellow, 2 red.
var clusterStatus = map[string]int64{"green": 0, "yellow": 1, "red": 2}

// MaxThreadPools bounds the thread pools stored per node; a node has about 20 and each is four series.
const MaxThreadPools = 32

// Collect implements integrations.Collector.
func (c *collector) Collect(ctx context.Context, b *integrations.Batch) error {
	var r root
	if err := httpx.GetJSON(ctx, c.client, c.inst, c.base+"/", &r, 0); err != nil {
		return err
	}
	if r.Version.Number != "" {
		b.SetResourceAttr(otlputil.Str("elasticsearch.version", r.Version.Number))
	}
	distribution := r.Version.Distribution
	if distribution == "" {
		distribution = "elasticsearch"
	}
	b.SetResourceAttr(otlputil.Str("elasticsearch.distribution", distribution))

	var h health
	if err := httpx.GetJSON(ctx, c.client, c.inst, c.base+"/_cluster/health", &h, 0); err != nil {
		return err
	}
	if h.ClusterName != "" {
		b.SetResourceAttr(otlputil.Str("elasticsearch.cluster.name", h.ClusterName))
	}
	RecordHealth(b, h)

	var ns nodeStats
	if err := httpx.GetJSON(ctx, c.client, c.inst, c.base+"/_nodes/_local/stats", &ns, 0); err != nil {
		return integrations.Partial(fmt.Errorf("node stats: %w", err))
	}
	RecordNodes(b, ns)
	return nil
}

// RecordHealth emits the cluster-wide metrics.
func RecordHealth(b *integrations.Batch, h health) {
	s := b.Resource()
	if v, ok := clusterStatus[strings.ToLower(h.Status)]; ok {
		s.GaugeInt("elasticsearch.cluster.health", "{status}", v, otlputil.Str("status", strings.ToLower(h.Status)))
	}
	s.SumInt("elasticsearch.cluster.nodes", "{nodes}", false, h.NumberOfNodes)
	s.SumInt("elasticsearch.cluster.data_nodes", "{nodes}", false, h.NumberOfDataNodes)
	s.SumInt("elasticsearch.cluster.shards", "{shards}", false, h.ActiveShards, otlputil.Str("state", "active"))
	s.SumInt("elasticsearch.cluster.shards", "{shards}", false, h.ActivePrimaryShards, otlputil.Str("state", "active_primary"))
	s.SumInt("elasticsearch.cluster.shards", "{shards}", false, h.RelocatingShards, otlputil.Str("state", "relocating"))
	s.SumInt("elasticsearch.cluster.shards", "{shards}", false, h.InitializingShards, otlputil.Str("state", "initializing"))
	s.SumInt("elasticsearch.cluster.shards", "{shards}", false, h.UnassignedShards, otlputil.Str("state", "unassigned"))
	s.SumInt("elasticsearch.cluster.shards", "{shards}", false, h.DelayedUnassignedShards, otlputil.Str("state", "delayed_unassigned"))
	s.SumInt("elasticsearch.cluster.pending_tasks", "{tasks}", false, h.NumberOfPendingTasks)
	s.GaugeInt("elasticsearch.cluster.in_flight_fetch", "ms", h.TaskMaxWaitingInQueueMillis)
}

// RecordNodes emits the metrics of the local node.
func RecordNodes(b *integrations.Batch, ns nodeStats) {
	for id, n := range ns.Nodes {
		name := n.Name
		if name == "" {
			name = id
		}
		s := b.Resource(otlputil.Str("elasticsearch.node.name", name))
		idx := n.Indices
		s.SumInt("elasticsearch.node.documents", "{documents}", false, idx.Docs.Count, otlputil.Str("state", "active"))
		s.SumInt("elasticsearch.node.documents", "{documents}", false, idx.Docs.Deleted, otlputil.Str("state", "deleted"))
		s.SumInt("elasticsearch.node.disk.usage", "By", false, idx.Store.SizeInBytes)
		s.SumInt("elasticsearch.node.operations.completed", "{operations}", true, idx.Indexing.IndexTotal, otlputil.Str("operation", "index"))
		s.SumInt("elasticsearch.node.operations.completed", "{operations}", true, idx.Search.QueryTotal, otlputil.Str("operation", "query"))
		s.SumInt("elasticsearch.node.operations.completed", "{operations}", true, idx.Search.FetchTotal, otlputil.Str("operation", "fetch"))
		s.SumInt("elasticsearch.node.operations.completed", "{operations}", true, idx.Merges.Total, otlputil.Str("operation", "merge"))
		s.SumInt("elasticsearch.node.operations.time", "ms", true, idx.Indexing.IndexTimeInMillis, otlputil.Str("operation", "index"))
		s.SumInt("elasticsearch.node.operations.time", "ms", true, idx.Search.QueryTimeInMillis, otlputil.Str("operation", "query"))
		s.SumInt("elasticsearch.node.operations.time", "ms", true, idx.Search.FetchTimeInMillis, otlputil.Str("operation", "fetch"))
		s.SumInt("elasticsearch.node.operations.time", "ms", true, idx.Merges.TotalTimeInMillis, otlputil.Str("operation", "merge"))
		s.SumInt("elasticsearch.node.operations.failed", "{operations}", true, idx.Indexing.IndexFailed, otlputil.Str("operation", "index"))
		s.SumInt("elasticsearch.node.translog.operations", "{operations}", false, idx.Translog.Operations)
		s.SumInt("elasticsearch.node.translog.size", "By", false, idx.Translog.SizeInBytes)
		s.SumInt("elasticsearch.node.cache.memory.usage", "By", false, idx.QueryCache.MemorySizeInBytes, otlputil.Str("cache_name", "query"))
		s.SumInt("elasticsearch.node.cache.memory.usage", "By", false, idx.Fielddata.MemorySizeInBytes, otlputil.Str("cache_name", "fielddata"))
		s.SumInt("elasticsearch.node.cache.evictions", "{evictions}", true, idx.QueryCache.Evictions, otlputil.Str("cache_name", "query"))
		s.SumInt("elasticsearch.node.cache.evictions", "{evictions}", true, idx.Fielddata.Evictions, otlputil.Str("cache_name", "fielddata"))
		s.SumInt("elasticsearch.node.cache.count", "{hits}", true, idx.QueryCache.HitCount, otlputil.Str("type", "hit"))
		s.SumInt("elasticsearch.node.cache.count", "{hits}", true, idx.QueryCache.MissCount, otlputil.Str("type", "miss"))

		s.SumInt("jvm.memory.heap.used", "By", false, n.JVM.Mem.HeapUsedInBytes)
		s.SumInt("jvm.memory.heap.max", "By", false, n.JVM.Mem.HeapMaxInBytes)
		s.GaugeInt("jvm.memory.heap.utilization", "%", n.JVM.Mem.HeapUsedPercent)
		s.SumInt("jvm.memory.nonheap.used", "By", false, n.JVM.Mem.NonHeapUsedInBytes)
		s.SumInt("jvm.threads.count", "{threads}", false, n.JVM.Threads.Count)
		for _, name := range integrations.SortedKeys(n.JVM.GC.Collectors) {
			g := n.JVM.GC.Collectors[name]
			s.SumInt("jvm.gc.collections.count", "{collections}", true, g.CollectionCount, otlputil.Str("name", name))
			s.SumInt("jvm.gc.collections.elapsed", "ms", true, g.CollectionTimeInMillis, otlputil.Str("name", name))
		}
		s.SumInt("elasticsearch.node.open_files", "{files}", false, n.Process.OpenFileDescriptors)
		s.GaugeInt("elasticsearch.node.cpu.usage", "%", n.Process.CPU.Percent)
		s.SumInt("elasticsearch.node.fs.disk.free", "By", false, n.FS.Total.AvailableInBytes)
		s.SumInt("elasticsearch.node.fs.disk.total", "By", false, n.FS.Total.TotalInBytes)

		pools := integrations.SortedKeys(n.ThreadPool)
		for i, pool := range pools {
			if i >= MaxThreadPools {
				break
			}
			tp := n.ThreadPool[pool]
			attr := otlputil.Str("thread_pool_name", pool)
			s.SumInt("elasticsearch.node.thread_pool.threads", "{threads}", false, tp.Threads, attr)
			s.SumInt("elasticsearch.node.thread_pool.tasks.queued", "{tasks}", false, tp.Queue, attr)
			s.SumInt("elasticsearch.node.thread_pool.threads.active", "{threads}", false, tp.Active, attr)
			s.SumInt("elasticsearch.node.thread_pool.tasks.rejected", "{tasks}", true, tp.Rejected, attr)
			s.SumInt("elasticsearch.node.thread_pool.tasks.finished", "{tasks}", true, tp.Completed, attr)
		}
		for _, name := range integrations.SortedKeys(n.Breakers) {
			br := n.Breakers[name]
			attr := otlputil.Str("circuit_breaker_name", name)
			s.SumInt("elasticsearch.breaker.tripped", "{breaks}", true, br.Tripped, attr)
			s.SumInt("elasticsearch.breaker.memory.estimated", "By", false, br.EstimatedSizeInBytes, attr)
			s.SumInt("elasticsearch.breaker.memory.limit", "By", false, br.LimitSizeInBytes, attr)
		}
	}
}
