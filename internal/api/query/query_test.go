package query

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

func scope(t *testing.T, tenant string) *Scope {
	t.Helper()
	s, err := New(nil, "openlog", time.Second).Scope(tenant)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var tableRef = regexp.MustCompile("`openlog`\\.(\\w+)")

// assertTenantPerTable checks that every table reference is immediately
// followed by the mandatory tenant predicate.
func assertTenantPerTable(t *testing.T, sql string) {
	t.Helper()
	refs := tableRef.FindAllStringIndex(sql, -1)
	if len(refs) == 0 {
		t.Fatalf("no table reference in %s", sql)
	}
	for _, r := range refs {
		rest := sql[r[1]:]
		rest = strings.TrimPrefix(rest, " FINAL")
		if !strings.HasPrefix(rest, " WHERE (tenant_id = {tenant_id:String})") {
			t.Errorf("table reference at %d not tenant-filtered: %s", r[0], sql)
		}
	}
}

func TestTenantAlwaysInjected(t *testing.T) {
	s := scope(t, "tenant-a")
	q := s.From(Hosts).Columns("host_id").Final()
	sql, params, err := q.Build()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "SELECT host_id FROM `openlog`.hosts FINAL WHERE (tenant_id = {tenant_id:String})" {
		t.Errorf("sql = %s", sql)
	}
	if params["tenant_id"] != "tenant-a" || len(params) != 1 {
		t.Errorf("params = %v", params)
	}
	assertTenantPerTable(t, sql)
}

func TestSubqueriesAreTenantFiltered(t *testing.T) {
	s := scope(t, "t1")
	latest := s.From(InventorySnapshots).Columns("host_id", "argMax(snapshot_id, snapshot_time)").GroupBy("host_id")
	items := s.From(InventoryItems).Columns("host_id", "item_key").
		Where("category = {category:String}").Param("category", "package").
		WhereIn("(host_id, snapshot_id)", latest).
		OrderBy("item_key").Limit(10)
	outer := s.FromSub(items).Columns("count()")
	sql, params, err := outer.Build()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(sql, "tenant_id = {tenant_id:String}") != 2 {
		t.Errorf("expected 2 tenant predicates: %s", sql)
	}
	assertTenantPerTable(t, sql)
	if !strings.Contains(sql, "GLOBAL IN (SELECT host_id, argMax(snapshot_id, snapshot_time) FROM `openlog`.inventory_snapshots") {
		t.Errorf("sub-select not rendered: %s", sql)
	}
	if params["category"] != "package" || params["tenant_id"] != "t1" {
		t.Errorf("params %v", params)
	}
}

func TestForbiddenFragments(t *testing.T) {
	s := scope(t, "t1")
	bad := []string{
		"tenant_id = 'other'",
		"TENANT_ID != ''",
		"1=1 OR 1=1; DROP TABLE x",
		"host_id IN (SELECT host_id FROM openlog.hosts_local)",
		"x -- comment",
		"x /* c */",
		"host_id GLOBAL IN logs",
		"host_id IN openlog.hosts",
		"count() FROM spans_local",
		"dictGet('d', 'x', 1)",
		"getSetting('readonly')",
		"remote('127.0.0.1', openlog.hosts)",
		"clusterAllReplicas('openlog', x)",
		"x UNION ALL y",
		"name = system.one",
		"1 SETTINGS readonly=0",
	}
	for _, frag := range bad {
		for name, q := range map[string]*Select{
			"where":   s.From(Logs).Columns("body").Where(frag),
			"columns": s.From(Logs).Columns(frag),
			"order":   s.From(Logs).Columns("body").OrderBy(frag),
			"group":   s.From(Logs).Columns("body").GroupBy(frag),
		} {
			if _, _, err := q.Build(); !errors.Is(err, ErrInvalid) {
				t.Errorf("%s fragment %q accepted", name, frag)
			}
		}
	}
	// Legitimate fragments pass.
	ok := s.From(Metrics).Columns("series_id", "toStartOfInterval(timestamp, toIntervalSecond({step:UInt32})) AS t",
		"argMax(value, timestamp) AS vlast", "mapFilter((k, v) -> has({group_by:Array(String)}, k), attributes) AS g").
		Where("metric_name = {name:String}").Param("name", "system.cpu.utilization").Param("step", uint32(60)).
		Param("group_by", []string{"cpu.mode", "it's"}).GroupBy("series_id", "t")
	sql, params, err := ok.Build()
	if err != nil {
		t.Fatalf("legit query rejected: %v", err)
	}
	if params["group_by"] != `['cpu.mode','it\'s']` {
		t.Errorf("array param %q", params["group_by"])
	}
	assertTenantPerTable(t, sql)
}

func TestReservedAndInvalidParams(t *testing.T) {
	s := scope(t, "t1")
	for _, name := range []string{"tenant_id", "Tenant", "1x", "a-b", ""} {
		if _, _, err := s.From(Logs).Columns("body").Param(name, "x").Build(); !errors.Is(err, ErrInvalid) {
			t.Errorf("param %q accepted", name)
		}
	}
	if _, _, err := s.From(Logs).Columns("body").Param("a", struct{}{}).Build(); err == nil {
		t.Error("unsupported type accepted")
	}
	if _, _, err := s.From(Logs).Columns("body").Param("a", "x").Param("a", "y").Build(); err == nil {
		t.Error("conflicting param accepted")
	}
}

func TestScopeIsolation(t *testing.T) {
	db := New(nil, "openlog", time.Second)
	if _, err := db.Scope(""); !errors.Is(err, ErrInvalid) {
		t.Error("empty tenant accepted")
	}
	a, _ := db.Scope("a")
	b, _ := db.Scope("b")
	sub := b.From(Hosts).Columns("host_id")
	if _, _, err := a.From(Logs).Columns("body").WhereIn("host_id", sub).Build(); !errors.Is(err, ErrInvalid) {
		t.Error("cross-tenant sub-select accepted in WhereIn")
	}
	if _, _, err := a.FromSub(sub).Columns("host_id").Build(); !errors.Is(err, ErrInvalid) {
		t.Error("cross-tenant sub-select accepted in FromSub")
	}
	if _, err := a.Query(context.Background(), sub); !errors.Is(err, ErrInvalid) {
		t.Error("query of another scope executed")
	}
	// A tenant value is only ever a bound parameter, never SQL text.
	evil, _ := db.Scope("x' OR '1'='1")
	sql, params, err := evil.From(Hosts).Columns("host_id").Build()
	if err != nil || strings.Contains(sql, "OR '1'") || params["tenant_id"] != "x' OR '1'='1" {
		t.Errorf("tenant leaked into SQL: %s %v", sql, err)
	}
}
