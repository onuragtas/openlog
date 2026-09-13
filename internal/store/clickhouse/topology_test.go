package clickhouse

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	ch "github.com/ClickHouse/clickhouse-go/v2"
)

func rep(host string) []Replica { return []Replica{{Num: 1, Host: host, Port: 9000}} }

func TestTopologySlotsFollowWeightsInShardOrder(t *testing.T) {
	// Given out of order on purpose; slots must follow shard_num order.
	topo, err := NewTopology("c", []Shard{
		{Num: 2, Weight: 1, Replicas: rep("b")},
		{Num: 1, Weight: 2, Replicas: rep("a")},
		{Num: 3, Weight: 0, Replicas: rep("z")}, // weight 0 receives nothing
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []uint32{1, 1, 2}
	for key := uint64(0); key < 9; key++ {
		if got := topo.Shards[topo.ShardIndex(key)].Num; got != want[key%3] {
			t.Errorf("key %d -> shard %d, want %d", key, got, want[key%3])
		}
	}
	same, _ := NewTopology("c", []Shard{{Num: 1, Weight: 2, Replicas: rep("x")}, {Num: 2, Weight: 1, Replicas: rep("y")}, {Num: 3, Replicas: rep("q")}})
	if same.Layout != topo.Layout {
		t.Errorf("layout depends on replicas: %s vs %s", same.Layout, topo.Layout)
	}
	other, _ := NewTopology("c", []Shard{{Num: 1, Weight: 1, Replicas: rep("a")}, {Num: 2, Weight: 1, Replicas: rep("b")}})
	if other.Layout == topo.Layout {
		t.Error("layout must change with weights")
	}
	if _, err := NewTopology("c", []Shard{{Num: 1, Weight: 0, Replicas: rep("a")}}); err == nil {
		t.Error("expected error without weight")
	}
}

func TestCheckShardingKeys(t *testing.T) {
	engines := map[string]string{
		"logs":  "Distributed('{cluster}', 'openlog', 'logs_local', cityHash64(tenant_id, host_id))",
		"spans": "Distributed('openlog', 'openlog', 'spans_local', cityHash64(tenant_id, host_id), 'policy')",
	}
	err := checkShardingKeys(engines, map[string]string{
		"logs":  "cityHash64(tenant_id, host_id)",
		"spans": "cityHash64(tenant_id, trace_id)",
		"hosts": "cityHash64(tenant_id, host_id)",
	})
	if err == nil || !strings.Contains(err.Error(), "table spans") || !errors.Is(err, ErrSchemaNotReady) || strings.Contains(err.Error(), "table logs") {
		t.Errorf("err = %v", err)
	}
	if err := checkShardingKeys(engines, map[string]string{"logs": "cityHash64(tenant_id,host_id)"}); err != nil {
		t.Errorf("whitespace must not matter: %v", err)
	}
}

type recordedInsert struct {
	conn  string
	table string
	token string
	rows  int
}

type fakeConn struct {
	Conn
	name string
}

func testWriter(t *testing.T, topo *Topology, fail map[string]int) (*ShardedWriter, *[]recordedInsert) {
	t.Helper()
	w := newShardedWriter(nil, ShardedOptions{Database: "openlog", Cluster: "c", ResolveAddr: func(r Replica) string { return r.Addr() }},
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	w.open = func(addr string) (Conn, error) { return fakeConn{name: addr}, nil }
	var mu sync.Mutex
	var got []recordedInsert
	w.insert = func(_ context.Context, conn Conn, table string, _ []string, s ch.Settings, rows [][]any) error {
		mu.Lock()
		defer mu.Unlock()
		name := conn.(fakeConn).name
		if fail[name] > 0 {
			fail[name]--
			return errors.New("connection refused")
		}
		got = append(got, recordedInsert{conn: name, table: table, token: s["insert_deduplication_token"].(string), rows: len(rows)})
		return nil
	}
	if err := w.setTopology(topo); err != nil {
		t.Fatal(err)
	}
	return w, &got
}

func TestShardedWriterSplitsByKeyAndDedupsRetries(t *testing.T) {
	topo, _ := NewTopology("c", []Shard{
		{Num: 1, Weight: 1, InternalReplication: true, Replicas: []Replica{{Num: 1, Host: "s1r1", Port: 9000}, {Num: 2, Host: "s1r2", Port: 9000}}},
		{Num: 2, Weight: 1, InternalReplication: true, Replicas: rep("s2r1")},
	})
	// s2r1 fails once: the whole Insert fails, the retry must only resend shard 2.
	w, got := testWriter(t, topo, map[string]int{"s2r1:9000": 1})
	cols := []string{"tenant_id", "host_id", "v"}
	var rows [][]any
	want := map[uint32]int{}
	for i := range 50 {
		h := "host-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		rows = append(rows, []any{"t1", h, i})
		want[topo.Shards[topo.ShardIndex(CityHash64("t1", h))].Num]++
	}
	if want[1] == 0 || want[2] == 0 {
		t.Fatalf("test keys do not cover both shards: %v", want)
	}
	ctx := context.Background()
	if err := w.Insert(ctx, "logs", []string{"tenant_id", "host_id"}, cols, "tok", rows); err == nil {
		t.Fatal("expected shard 2 failure")
	}
	if err := w.Insert(ctx, "logs", []string{"tenant_id", "host_id"}, cols, "tok", rows); err != nil {
		t.Fatal(err)
	}
	perShard := map[string]int{}
	for _, g := range *got {
		if g.table != "`openlog`.`logs_local`" {
			t.Errorf("table %s", g.table)
		}
		perShard[g.token] += g.rows
	}
	t1, t2 := ShardToken("tok", 1, topo.Layout), ShardToken("tok", 2, topo.Layout)
	if len(*got) != 2 || perShard[t1] != want[1] || perShard[t2] != want[2] {
		t.Errorf("inserts %+v, want shard rows %v with tokens %s / %s (shard 1 sent once)", *got, want, t1, t2)
	}
}

func TestShardedWriterFailsOverToNextReplica(t *testing.T) {
	topo, _ := NewTopology("c", []Shard{
		{Num: 1, Weight: 1, Replicas: []Replica{{Num: 1, Host: "r1", Port: 9000}, {Num: 2, Host: "r2", Port: 9000}}},
	})
	w, got := testWriter(t, topo, map[string]int{"r1:9000": 100, "r2:9000": 100})
	rows := [][]any{{"t", "h"}}
	if err := w.Insert(context.Background(), "hosts", []string{"tenant_id", "host_id"}, []string{"tenant_id", "host_id"}, "a", rows); err == nil {
		t.Fatal("expected error when all replicas fail")
	}
	w2, got2 := testWriter(t, topo, map[string]int{"r1:9000": 100})
	for i := range 4 {
		tok := string(rune('a' + i))
		if err := w2.Insert(context.Background(), "hosts", []string{"tenant_id", "host_id"}, []string{"tenant_id", "host_id"}, tok, rows); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	for _, g := range *got2 {
		if g.conn != "r2:9000" {
			t.Errorf("insert went to failing replica: %+v", g)
		}
	}
	if len(*got) != 0 || len(*got2) != 4 {
		t.Errorf("got %v / %v", *got, *got2)
	}
}
