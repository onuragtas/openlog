package migrate

import (
	"strings"
	"testing"

	"github.com/onuragtas/openlog/schema"
)

func TestAPMRetentionStatements(t *testing.T) {
	stmts, err := APMRetentionStatements("openlog", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 8 {
		t.Fatalf("%d statements", len(stmts))
	}
	for _, st := range stmts {
		if !strings.HasPrefix(st, "ALTER TABLE openlog.apm_") || !strings.Contains(st, "_local ON CLUSTER 'openlog' MODIFY TTL ") ||
			!strings.HasSuffix(st, "+ INTERVAL 7 DAY") {
			t.Errorf("statement %q", st)
		}
	}
	for _, bad := range []int{0, 3651} {
		if _, err := APMRetentionStatements("openlog", bad); err == nil {
			t.Errorf("accepted %d days", bad)
		}
	}
	if _, err := APMRetentionStatements("x'; DROP", 7); err == nil {
		t.Error("accepted a hostile cluster name")
	}

	// Every TTL table of 0006_apm.sql is covered, with the same expression at the default retention.
	ms, err := Load(schema.ClickHouse, "clickhouse", "openlog")
	if err != nil {
		t.Fatal(err)
	}
	var created []string
	for _, m := range ms {
		if m.Name != "apm" {
			continue
		}
		for _, st := range m.Statements {
			st = strings.Join(strings.Fields(st), " ")
			if strings.HasPrefix(st, "CREATE TABLE IF NOT EXISTS openlog.apm_") && strings.Contains(st, " TTL ") {
				created = append(created, st)
			}
		}
	}
	defaults, _ := APMRetentionStatements("openlog", DefaultAPMRetentionDays)
	if len(created) != len(defaults) {
		t.Fatalf("0006_apm has %d TTL tables, retention covers %d", len(created), len(defaults))
	}
	for i, st := range defaults {
		table := strings.Fields(st)[2] // openlog.apm_x_local
		expr := st[strings.Index(st, "MODIFY TTL ")+len("MODIFY TTL "):]
		found := false
		for _, c := range created {
			if strings.Contains(c, "EXISTS "+table+" ") && strings.Contains(c, "TTL "+expr) {
				found = true
			}
		}
		if !found {
			t.Errorf("statement %d: %s not in 0006_apm with TTL %q", i, table, expr)
		}
	}
}
