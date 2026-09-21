package mongodb

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/internal/testutil"
)

// Recorded from MongoDB 7.0's serverStatus, trimmed to the fields the collector reads. The numeric types
// are the ones the server actually sends — int32, int64 and double mixed in the same document — because
// reading them as one type is the mistake this collector has to avoid.
func serverStatus() bson.M {
	return bson.M{
		"version": "7.0.14",
		"process": "mongod",
		"uptime":  float64(7200),
		"connections": bson.M{
			"current": int32(12), "available": int32(51188), "active": int32(3),
		},
		"mem": bson.M{"resident": int32(512), "virtual": int32(2048)},
		"opcounters": bson.M{
			"insert": int64(1200), "query": int64(45000), "update": int64(800),
			"delete": int64(30), "getmore": int64(910), "command": int64(60000),
		},
		"metrics": bson.M{
			"document": bson.M{"inserted": int64(1200), "returned": int64(98000), "updated": int64(800), "deleted": int64(30)},
			"cursor":   bson.M{"open": bson.M{"total": int32(4), "noTimeout": int32(1)}, "timedOut": int64(7)},
		},
		"opLatencies": bson.M{
			"reads":    bson.M{"latency": int64(1_500_000), "ops": int64(45000)},
			"writes":   bson.M{"latency": int64(400_000), "ops": int64(2030)},
			"commands": bson.M{"latency": int64(900_000), "ops": int64(60000)},
		},
		"network":    bson.M{"bytesIn": int64(10_485_760), "bytesOut": int64(52_428_800), "numRequests": int64(107_000)},
		"globalLock": bson.M{"totalTime": int64(7_200_000_000)},
		"wiredTiger": bson.M{"cache": bson.M{
			"pages requested from the cache": int64(1_000_000),
			"pages read into cache":          int64(25_000),
		}},
		"logicalSessionRecordCache": bson.M{"activeSessionsCount": int32(9)},
		"asserts":                   bson.M{"regular": int64(0), "warning": int64(2)},
	}
}

func TestRecordServerStatus(t *testing.T) {
	b := integrations.NewBatch(time.Now(), 0)
	RecordServerStatus(b, serverStatus())

	res := map[string]string{}
	for _, kv := range b.ResourceAttrs() {
		res[kv.Key] = kv.Value.GetStringValue()
	}
	if res["db.version"] != "7.0.14" || res["mongodb.process"] != "mongod" {
		t.Errorf("resource attributes = %v", res)
	}

	ps := testutil.Points(b)
	testutil.Expect(t, ps, "mongodb.uptime", "s", true, true, 7200, nil)
	testutil.Expect(t, ps, "mongodb.connection.count", "{connections}", true, false, 12, map[string]string{"type": "current"})
	// mem.* is mebibytes on the wire and bytes in the metric, like every other size in openlog.
	testutil.Expect(t, ps, "mongodb.memory.usage", "By", true, false, 512*1024*1024, map[string]string{"type": "resident"})
	testutil.Expect(t, ps, "mongodb.operation.count", "{operations}", true, true, 45000, map[string]string{"operation": "query"})
	testutil.Expect(t, ps, "mongodb.document.operation.count", "{documents}", true, true, 98000, map[string]string{"operation": "query"})
	testutil.Expect(t, ps, "mongodb.operation.latency.time", "us", true, true, 1_500_000, map[string]string{"operation": "read"})
	testutil.Expect(t, ps, "mongodb.network.io.receive", "By", true, true, 10_485_760, nil)
	// globalLock.totalTime is microseconds; the metric is milliseconds.
	testutil.Expect(t, ps, "mongodb.global_lock.time", "ms", true, true, 7_200_000, nil)
	testutil.Expect(t, ps, "mongodb.cursor.count", "{cursors}", true, false, 4, map[string]string{"type": "open"})
	testutil.Expect(t, ps, "mongodb.cursor.timeout.count", "{cursors}", true, true, 7, nil)
	// The cache hit count is what the server does not report directly: requested minus read in.
	testutil.Expect(t, ps, "mongodb.cache.operations", "{operations}", true, true, 975_000, map[string]string{"type": "hit"})
	testutil.Expect(t, ps, "mongodb.cache.operations", "{operations}", true, true, 25_000, map[string]string{"type": "miss"})
	testutil.Expect(t, ps, "mongodb.session.count", "{sessions}", true, false, 9, nil)
	testutil.Expect(t, ps, "mongodb.asserts", "{asserts}", true, true, 2, map[string]string{"type": "warning"})

	// Counters are cumulative from the server's start, so the batch carries that start time.
	for _, rm := range b.ResourceMetrics(nil, nil) {
		for _, m := range rm.ScopeMetrics[0].Metrics {
			if m.Name == "mongodb.operation.count" && m.GetSum().DataPoints[0].StartTimeUnixNano == 0 {
				t.Error("cumulative sums must carry a start time")
			}
		}
	}
}

// A mongos router and an old server report fewer fields; the ones that are missing must simply not be
// emitted rather than emitted as zero.
func TestRecordServerStatusWithMissingFields(t *testing.T) {
	b := integrations.NewBatch(time.Now(), 0)
	RecordServerStatus(b, bson.M{"process": "mongos", "connections": bson.M{"current": int32(2)}})
	ps := testutil.Points(b)
	testutil.Expect(t, ps, "mongodb.connection.count", "{connections}", true, false, 2, map[string]string{"type": "current"})
	for _, name := range []string{"mongodb.memory.usage", "mongodb.cache.operations", "mongodb.uptime"} {
		if got := testutil.Find(ps, name, nil); len(got) != 0 {
			t.Errorf("%s must not be emitted when the server does not report it", name)
		}
	}
}

func TestRecordDBStats(t *testing.T) {
	b := integrations.NewBatch(time.Now(), 0)
	RecordDBStats(b, "shop", bson.M{
		"collections": int32(12), "objects": int64(1_500_000), "indexes": int32(31),
		"dataSize": float64(53_687_091_200), "storageSize": float64(21_474_836_480), "indexSize": float64(1_073_741_824),
		"views": int32(2),
	})
	ps := testutil.Points(b)
	db := map[string]string{"db.namespace": "shop"}
	testutil.Expect(t, ps, "mongodb.collection.count", "{collections}", true, false, 12, db)
	testutil.Expect(t, ps, "mongodb.object.count", "{objects}", true, false, 1_500_000, db)
	testutil.Expect(t, ps, "mongodb.data.size", "By", true, false, 53_687_091_200, db)
	testutil.Expect(t, ps, "mongodb.index.size", "By", true, false, 1_073_741_824, db)
	testutil.Expect(t, ps, "mongodb.view.count", "{views}", true, false, 2, db)
}

func TestRecordReplicaSet(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	b := integrations.NewBatch(now, 0)
	RecordReplicaSet(b, bson.M{
		"set": "rs0",
		"members": bson.A{
			bson.M{"name": "mongo-1:27017", "state": int32(1), "stateStr": "PRIMARY", "optimeDate": now},
			bson.M{"name": "mongo-2:27017", "state": int32(2), "stateStr": "SECONDARY", "self": true,
				"optimeDate": now.Add(-12 * time.Second)},
		},
	})
	res := map[string]string{}
	for _, kv := range b.ResourceAttrs() {
		res[kv.Key] = kv.Value.GetStringValue()
	}
	if res["mongodb.replica_set.name"] != "rs0" {
		t.Errorf("resource attributes = %v", res)
	}
	ps := testutil.Points(b)
	if p := testutil.One(t, ps, "mongodb.replica_set.member", nil); p.Attrs["state"] != "secondary" {
		t.Errorf("member state = %v", p.Attrs)
	}
	testutil.Expect(t, ps, "mongodb.replica_set.members", "{members}", true, false, 2, nil)
	// This member is twelve seconds behind the primary's last applied operation.
	testutil.Expect(t, ps, "mongodb.replica_set.lag", "s", false, false, 12, nil)
}

// Without a primary in the answer there is nothing to be behind; reporting a lag of 0 would say the
// opposite of what is happening.
func TestRecordReplicaSetWithoutAPrimary(t *testing.T) {
	now := time.Now()
	b := integrations.NewBatch(now, 0)
	RecordReplicaSet(b, bson.M{
		"set": "rs0",
		"members": bson.A{
			bson.M{"state": int32(2), "stateStr": "SECONDARY", "self": true, "optimeDate": now},
			bson.M{"state": int32(2), "stateStr": "SECONDARY", "optimeDate": now},
		},
	})
	if got := testutil.Find(testutil.Points(b), "mongodb.replica_set.lag", nil); len(got) != 0 {
		t.Fatalf("lag must not be reported without a primary: %+v", got)
	}
}

func TestNumberReadsEveryBSONNumericType(t *testing.T) {
	doc := bson.M{"a": bson.M{"i32": int32(3), "i64": int64(4), "f": 5.5, "s": "six"}}
	for path, want := range map[string]float64{"a.i32": 3, "a.i64": 4, "a.f": 5.5} {
		got, ok := number(doc, path)
		if !ok || got != want {
			t.Errorf("number(%q) = %v, %v; want %v", path, got, ok, want)
		}
	}
	for _, path := range []string{"a.s", "a.missing", "missing.x", "a"} {
		if _, ok := number(doc, path); ok {
			t.Errorf("number(%q) must report that there is no number there", path)
		}
	}
}

func TestWantedDatabases(t *testing.T) {
	dbs := []database{{Name: "admin"}, {Name: "config"}, {Name: "local"}, {Name: "shop"}}
	inst := testutil.Instance()

	c := &collector{inst: inst}
	if got := c.wanted(dbs); len(got) != 4 {
		t.Errorf("without settings every database is read: %v", got)
	}

	inst.Settings.ExcludeDatabases = []string{"local", "config"}
	if got := c.wanted(dbs); len(got) != 2 || got[0] != "admin" || got[1] != "shop" {
		t.Errorf("excluded databases must be left out: %v", got)
	}

	inst.Settings.Databases = []string{"shop"}
	if got := c.wanted(dbs); len(got) != 1 || got[0] != "shop" {
		t.Errorf("an allow list wins: %v", got)
	}

	// The cap is what keeps an installation with a database per tenant from becoming the metric store's
	// cardinality problem.
	many := make([]database, MaxDatabases+10)
	for i := range many {
		many[i] = database{Name: string(rune('a'+i%26)) + string(rune('0'+i/26))}
	}
	empty := &collector{inst: testutil.Instance()}
	if got := empty.wanted(many); len(got) != MaxDatabases {
		t.Errorf("wanted %d databases, want the cap of %d", len(got), MaxDatabases)
	}
}

func TestClassify(t *testing.T) {
	var se *integrations.StatusError
	err := classify(errString("connection() error occurred during connection handshake: auth error: Authentication failed."))
	if !asStatus(err, &se) || se.Status != "needs_configuration" {
		t.Fatalf("an authentication failure must ask for configuration, got %v", err)
	}
	if err := classify(errString("server selection error: context deadline exceeded")); !integrations.IsUnreachable(err) {
		t.Fatalf("a server selection failure is unreachable, got %v", err)
	}
	if err := classify(errString("some other failure")); integrations.IsUnreachable(err) {
		t.Fatalf("an unrelated failure must not be reported as unreachable: %v", err)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func asStatus(err error, target **integrations.StatusError) bool {
	se, ok := err.(*integrations.StatusError)
	if ok {
		*target = se
	}
	return ok
}
