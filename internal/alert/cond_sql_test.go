package alert

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/onuragtas/openlog/internal/api/query"
)

// recordingConn captures every statement and returns empty results.
type recordingConn struct {
	driver.Conn
	mu  sync.Mutex
	sql []string
}

func (c *recordingConn) Query(_ context.Context, sql string, _ ...any) (driver.Rows, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sql = append(c.sql, sql)
	return emptyRows{}, nil
}

type emptyRows struct{ driver.Rows }

func (emptyRows) Next() bool   { return false }
func (emptyRows) Close() error { return nil }
func (emptyRows) Err() error   { return nil }

var tableRef = regexp.MustCompile("`openlog`\\.(\\w+)( FINAL)?")

func assertScoped(t *testing.T, stmts []string) {
	t.Helper()
	if len(stmts) == 0 {
		t.Fatal("no statement executed")
	}
	for _, sql := range stmts {
		refs := tableRef.FindAllStringIndex(sql, -1)
		if len(refs) == 0 {
			t.Errorf("statement without table: %s", sql)
		}
		for _, r := range refs {
			if !strings.HasPrefix(sql[r[1]:], " WHERE (tenant_id = {tenant_id:String})") {
				t.Errorf("table reference not tenant-scoped: %s", sql)
			}
		}
	}
}

func mustParse(t *testing.T, typ, cond string) Condition {
	t.Helper()
	c, err := ruleTypes[typ].Parse(json.RawMessage(cond))
	if err != nil {
		t.Fatalf("parse %s: %v", cond, err)
	}
	return c
}

// hostile values that must never reach SQL text: they are bound parameters.
const evil = `x' OR 1=1) UNION ALL SELECT * FROM openlog.logs_local WHERE tenant_id != '`

func TestConditionsAreTenantScoped(t *testing.T) {
	evilJSON, _ := json.Marshal(evil)
	conds := map[string][]string{
		TypeMetricThreshold: {
			`{"metric":"system.cpu.utilization","operator":"gt","threshold":0.9,"group_by":["host","attr.cpu.mode","resource.env","service"],
			  "filters":[{"field":"attr.cpu.mode","op":"not_in","values":["idle"]},{"field":"host.name","op":"eq","values":[` + string(evilJSON) + `]},
			             {"field":"resource.env","op":"in","values":["prod","stage"]},{"field":"service.name","op":"contains","values":["api"]}]}`,
			`{"metric":` + string(evilJSON) + `,"aggregation":"p95","operator":"gt","threshold":1,"group_by":["host"]}`,
			`{"metric":"system.network.io","aggregation":"rate","operator":"gt","threshold":1}`,
		},
		TypeLogMatch: {
			`{"query":` + string(evilJSON) + `,"severity_min":"ERROR","operator":"gte","threshold":1,"group_by":["host"],
			  "filters":[{"field":"attr.log.file.path","op":"eq","values":["/var/log/x"]}]}`,
		},
		TypeNoData: {
			`{"signal":"host","group_by":["host"],"filters":[{"field":"resource.env","op":"eq","values":["prod"]}]}`,
			`{"signal":"metric","metric":"system.cpu.utilization","group_by":["host"]}`,
			`{"signal":"log","group_by":["service"]}`,
		},
		TypeAPM: {
			`{"service_name":` + string(evilJSON) + `,"environment":"prod","metric":"p95_ms","operator":"gt","threshold":500,"group_by":["transaction"]}`,
			`{"service_name":"checkout","metric":"apdex","operator":"lt","threshold":0.8,"recovery_threshold":0.9,"min_requests":10}`,
		},
		TypeDiscovery: {
			`{"event":"service_disappeared","match":"redis","filters":[{"field":"host.name","op":"eq","values":[` + string(evilJSON) + `]}]}`,
			`{"event":"port_opened"}`,
		},
	}
	end := t0
	for typ, list := range conds {
		for _, raw := range list {
			c := mustParse(t, typ, raw)
			conn := &recordingConn{}
			sc, err := query.New(conn, "openlog", time.Second).Scope("tenant-a")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Evaluate(context.Background(), sc, end, Limits{}); err != nil {
				t.Fatalf("%s evaluate: %v", typ, err)
			}
			if _, err := c.Range(context.Background(), sc, end.Add(-time.Hour), end, time.Minute, Limits{}); err != nil {
				t.Fatalf("%s range: %v", typ, err)
			}
			assertScoped(t, conn.sql)
			for _, sql := range conn.sql {
				if strings.Contains(sql, "UNION") || strings.Contains(sql, "1=1") || strings.Contains(sql, "logs_local") {
					t.Errorf("%s: user value reached SQL text: %s", typ, sql)
				}
			}
		}
	}
}

// Building a condition query with fragments the query layer forbids must fail instead of running.
func TestAttributeKeysAreValidated(t *testing.T) {
	bad := []string{
		`{"metric":"m","operator":"gt","threshold":1,"filters":[{"field":"attr.x]{tenant_id}","op":"eq","values":["a"]}]}`,
		`{"metric":"m","operator":"gt","threshold":1,"group_by":["attr.a b"]}`,
		`{"metric":"m","operator":"gt","threshold":1,"filters":[{"field":"tenant_id","op":"eq","values":["a"]}]}`,
		`{"metric":"m","operator":"gt","threshold":1,"filters":[{"field":"host.id","op":"regex","values":["a"]}]}`,
		`{"metric":"m","operator":"gt","threshold":1,"unknown":true}`,
	}
	for _, raw := range bad {
		if _, err := ruleTypes[TypeMetricThreshold].Parse(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	// Hosts have no service or data point attributes.
	if _, err := ruleTypes[TypeNoData].Parse(json.RawMessage(`{"signal":"host","group_by":["service"]}`)); err == nil {
		t.Error("no_data host grouped by service accepted")
	}
	if _, err := ruleTypes[TypeDiscovery].Parse(json.RawMessage(`{"event":"port_opened","filters":[{"field":"attr.x","op":"eq","values":["a"]}]}`)); err == nil {
		t.Error("discovery attribute filter accepted")
	}
}
