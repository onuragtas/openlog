package elasticsearch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/internal/testutil"
)

// Recorded from Elasticsearch 8.15, trimmed to the fields the collector reads.
const healthJSON = `{
  "cluster_name": "logs-prod", "status": "yellow", "timed_out": false,
  "number_of_nodes": 3, "number_of_data_nodes": 2,
  "active_primary_shards": 40, "active_shards": 75, "relocating_shards": 1,
  "initializing_shards": 2, "unassigned_shards": 5, "delayed_unassigned_shards": 0,
  "number_of_pending_tasks": 4, "task_max_waiting_in_queue_millis": 120
}`

// GET / : the node's identity, which is where the version and the distribution come from.
const rootJSON = `{"name":"es-data-1","cluster_name":"logs-prod","version":{"number":"8.15.2","distribution":null}}`

const nodeStatsJSON = `{
  "cluster_name": "logs-prod",
  "nodes": {
    "Abc123": {
      "name": "es-data-1", "roles": ["data", "master"],
      "indices": {
        "docs": {"count": 1500000, "deleted": 2400},
        "store": {"size_in_bytes": 53687091200},
        "indexing": {"index_total": 900000, "index_time_in_millis": 45000, "index_failed": 7},
        "search": {"query_total": 250000, "query_time_in_millis": 62000, "fetch_total": 240000, "fetch_time_in_millis": 8000},
        "merges": {"total": 1200, "total_time_in_millis": 300000, "total_docs": 80000},
        "translog": {"operations": 3400, "size_in_bytes": 10485760},
        "query_cache": {"memory_size_in_bytes": 4194304, "hit_count": 50000, "miss_count": 12000, "evictions": 30},
        "fielddata": {"memory_size_in_bytes": 1048576, "evictions": 2}
      },
      "jvm": {
        "mem": {"heap_used_in_bytes": 2147483648, "heap_max_in_bytes": 8589934592, "heap_used_percent": 25,
                "non_heap_used_in_bytes": 268435456},
        "threads": {"count": 96},
        "gc": {"collectors": {"young": {"collection_count": 4500, "collection_time_in_millis": 90000},
                              "old": {"collection_count": 3, "collection_time_in_millis": 800}}}
      },
      "process": {"open_file_descriptors": 820, "max_file_descriptors": 65535, "cpu": {"percent": 17}},
      "thread_pool": {
        "search": {"threads": 13, "queue": 2, "active": 1, "rejected": 4, "completed": 250000},
        "write": {"threads": 8, "queue": 0, "active": 0, "rejected": 0, "completed": 900000}
      },
      "fs": {"total": {"total_in_bytes": 536870912000, "free_in_bytes": 214748364800, "available_in_bytes": 214748364800}},
      "breakers": {"parent": {"tripped": 1, "estimated_size_in_bytes": 3221225472, "limit_size_in_bytes": 8160437862}}
    }
  }
}`

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

func TestCollect(t *testing.T) {
	srv := server(t, map[string]string{
		"/":                    rootJSON,
		"/_cluster/health":     healthJSON,
		"/_nodes/_local/stats": nodeStatsJSON,
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
	if res["elasticsearch.cluster.name"] != "logs-prod" || res["elasticsearch.version"] != "8.15.2" ||
		res["elasticsearch.distribution"] != "elasticsearch" {
		t.Errorf("resource attributes = %v", res)
	}

	ps := testutil.Points(b)
	// The health colour is a number so a chart and an alert can use it: green 0, yellow 1, red 2.
	if p := testutil.One(t, ps, "elasticsearch.cluster.health", nil); p.Int != 1 || p.Attrs["status"] != "yellow" {
		t.Errorf("cluster health = %+v", p)
	}
	testutil.Expect(t, ps, "elasticsearch.cluster.nodes", "{nodes}", true, false, 3, nil)
	testutil.Expect(t, ps, "elasticsearch.cluster.shards", "{shards}", true, false, 75, map[string]string{"state": "active"})
	testutil.Expect(t, ps, "elasticsearch.cluster.shards", "{shards}", true, false, 5, map[string]string{"state": "unassigned"})
	testutil.Expect(t, ps, "elasticsearch.cluster.pending_tasks", "{tasks}", true, false, 4, nil)

	node := map[string]string{"elasticsearch.node.name": "es-data-1"}
	testutil.Expect(t, ps, "elasticsearch.node.documents", "{documents}", true, false, 1500000,
		map[string]string{"elasticsearch.node.name": "es-data-1", "state": "active"})
	testutil.Expect(t, ps, "elasticsearch.node.disk.usage", "By", true, false, 53687091200, node)
	testutil.Expect(t, ps, "elasticsearch.node.operations.completed", "{operations}", true, true, 250000,
		map[string]string{"operation": "query"})
	testutil.Expect(t, ps, "elasticsearch.node.operations.time", "ms", true, true, 62000,
		map[string]string{"operation": "query"})
	testutil.Expect(t, ps, "elasticsearch.node.operations.failed", "{operations}", true, true, 7,
		map[string]string{"operation": "index"})
	testutil.Expect(t, ps, "elasticsearch.node.cache.count", "{hits}", true, true, 50000,
		map[string]string{"type": "hit"})
	testutil.Expect(t, ps, "elasticsearch.node.cache.evictions", "{evictions}", true, true, 2,
		map[string]string{"cache_name": "fielddata"})
	testutil.Expect(t, ps, "elasticsearch.node.translog.operations", "{operations}", true, false, 3400, node)

	testutil.Expect(t, ps, "jvm.memory.heap.used", "By", true, false, 2147483648, node)
	testutil.Expect(t, ps, "jvm.threads.count", "{threads}", true, false, 96, node)
	testutil.Expect(t, ps, "jvm.gc.collections.count", "{collections}", true, true, 4500,
		map[string]string{"name": "young"})
	testutil.Expect(t, ps, "jvm.gc.collections.elapsed", "ms", true, true, 800, map[string]string{"name": "old"})

	testutil.Expect(t, ps, "elasticsearch.node.thread_pool.tasks.rejected", "{tasks}", true, true, 4,
		map[string]string{"thread_pool_name": "search"})
	testutil.Expect(t, ps, "elasticsearch.node.thread_pool.tasks.queued", "{tasks}", true, false, 2,
		map[string]string{"thread_pool_name": "search"})
	testutil.Expect(t, ps, "elasticsearch.breaker.tripped", "{breaks}", true, true, 1,
		map[string]string{"circuit_breaker_name": "parent"})
	testutil.Expect(t, ps, "elasticsearch.node.fs.disk.free", "By", true, false, 214748364800, node)
	testutil.Expect(t, ps, "elasticsearch.node.cpu.usage", "%", false, false, 17, node)
}

// The cluster is reachable but the node statistics are not: the health is still worth storing.
func TestMissingNodeStatsIsPartial(t *testing.T) {
	srv := server(t, map[string]string{"/": rootJSON, "/_cluster/health": healthJSON})
	c := collectorFor(t, srv.URL)
	defer c.Close()
	b := integrations.NewBatch(time.Now(), 0)
	err := c.Collect(context.Background(), b)
	var pe *integrations.PartialError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want partial", err)
	}
	testutil.Expect(t, testutil.Points(b), "elasticsearch.cluster.nodes", "{nodes}", true, false, 3, nil)
}

// A secured cluster answers 401 until a monitoring user is configured.
// OpenSearch answers the same documents and names its distribution, which the resource must carry.
func TestOpenSearchDistribution(t *testing.T) {
	srv := server(t, map[string]string{
		"/":                    `{"cluster_name":"logs","version":{"number":"2.16.0","distribution":"opensearch"}}`,
		"/_cluster/health":     healthJSON,
		"/_nodes/_local/stats": nodeStatsJSON,
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
	if res["elasticsearch.distribution"] != "opensearch" || res["elasticsearch.version"] != "2.16.0" {
		t.Errorf("resource attributes = %v", res)
	}
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

// The transport port (9300) is never the REST API's, so discovery must not offer it as an endpoint.
func TestTransportPortIsSkipped(t *testing.T) {
	spec := Integration{}.Spec()
	if spec.DefaultPort != DefaultPort {
		t.Errorf("default port = %d", spec.DefaultPort)
	}
	if spec.SkipPort == nil || !spec.SkipPort(9300) || spec.SkipPort(9200) {
		t.Error("the transport port must be skipped and the REST port kept")
	}
}

func TestUnreachableEndpoint(t *testing.T) {
	srv := server(t, map[string]string{"/": rootJSON, "/_cluster/health": healthJSON})
	url := srv.URL
	srv.Close()
	c := collectorFor(t, url)
	defer c.Close()
	err := c.Collect(context.Background(), integrations.NewBatch(time.Now(), 0))
	if !integrations.IsUnreachable(err) {
		t.Fatalf("err = %v", err)
	}
}
