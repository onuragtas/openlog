package migrate

import (
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/onuragtas/openlog/internal/migrate/phase"
	"github.com/onuragtas/openlog/schema"
)

func TestSplitStatements(t *testing.T) {
	sql := `-- header; with semicolon
CREATE TABLE a (x String DEFAULT 'a;b') ENGINE = Memory;
/* block; comment */ CREATE TABLE b (y String DEFAULT 'it''s; \'ok\'') ENGINE = Memory ;
SELECT "weird;ident", ` + "`back;tick`" + ` -- trailing; comment
;
;;
`
	got := SplitStatements(sql)
	want := []string{
		"CREATE TABLE a (x String DEFAULT 'a;b') ENGINE = Memory",
		"CREATE TABLE b (y String DEFAULT 'it''s; \\'ok\\'') ENGINE = Memory",
		"SELECT \"weird;ident\", `back;tick`",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d statements: %q", len(got), got)
	}
	for i := range want {
		if strings.Join(strings.Fields(got[i]), " ") != want[i] {
			t.Errorf("stmt %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLoadOrderAndCluster(t *testing.T) {
	fsys := fstest.MapFS{
		"sql/0002_b.sql": {Data: []byte("-- openlog:phase expand\nCREATE TABLE x ON CLUSTER '{cluster}' ENGINE = Distributed('{cluster}', openlog, y);")},
		"sql/0001_a.sql": {Data: []byte("-- openlog:phase expand\nSELECT 1; SELECT 2;")},
		"sql/README.txt": {Data: []byte("ignored")},
	}
	ms, err := Load(fsys, "sql", "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].Version != 1 || ms[0].Name != "a" || ms[1].Version != 2 {
		t.Fatalf("migrations %+v", ms)
	}
	if !reflect.DeepEqual(ms[0].Statements, []string{"SELECT 1", "SELECT 2"}) {
		t.Errorf("stmts %q", ms[0].Statements)
	}
	if want := "CREATE TABLE x ON CLUSTER 'prod' ENGINE = Distributed('prod', openlog, y)"; ms[1].Statements[0] != want {
		t.Errorf("got %q", ms[1].Statements[0])
	}
	if _, err := Load(fsys, "sql", "bad'name"); err == nil {
		t.Error("expected invalid cluster error")
	}
	if _, err := Load(fstest.MapFS{"sql/x.sql": {Data: []byte("")}}, "sql", "openlog"); err == nil {
		t.Error("expected bad filename error")
	}
	if _, err := Load(fstest.MapFS{"sql/0001_a.sql": {Data: []byte("SELECT 1;")}}, "sql", "openlog"); err == nil || !strings.Contains(err.Error(), "missing header") {
		t.Errorf("expected missing header error, got %v", err)
	}
}

func TestEmbeddedSchema(t *testing.T) {
	ms, err := Load(schema.ClickHouse, "clickhouse", "openlog")
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) < 5 {
		t.Fatalf("only %d migrations embedded", len(ms))
	}
	for _, m := range ms {
		if m.Header.Phase != phase.Expand && m.Header.Phase != phase.Contract {
			t.Errorf("migration %d has no phase", m.Version)
		}
	}
	for _, m := range ms {
		for _, st := range m.Statements {
			if strings.Contains(st, "{cluster}") {
				t.Errorf("unreplaced macro in %d: %s", m.Version, st)
			}
			// Expand migrations: CREATE ... IF NOT EXISTS, or idempotent column/index additions (ALTER TABLE ...
			// ADD COLUMN IF NOT EXISTS, e.g. 0006_apm; ADD INDEX IF NOT EXISTS, 0009_containers); anything else
			// (DROP, MODIFY, RENAME) is not allowed here.
			idempotentAdds := func(clause string) bool {
				return strings.Count(st, clause) > 0 && strings.Count(st, clause) == strings.Count(st, clause+" IF NOT EXISTS")
			}
			isAdd := strings.HasPrefix(st, "ALTER TABLE") && (idempotentAdds("ADD COLUMN") || idempotentAdds("ADD INDEX")) &&
				!strings.Contains(st, "DROP") && !strings.Contains(st, "MODIFY") && !strings.Contains(st, "RENAME")
			if !strings.HasPrefix(st, "CREATE") && !isAdd {
				t.Errorf("unexpected statement start in %d: %.40q", m.Version, st)
			}
		}
	}
}

func TestPlanSteps(t *testing.T) {
	ms := []Migration{
		{Version: 1, Name: "a", Header: phase.Header{Phase: phase.Expand}},
		{Version: 2, Name: "drop", Header: phase.Header{Phase: phase.Contract, RequiresAllAtLeast: "0.9.1"}},
		{Version: 3, Name: "b", Header: phase.Header{Phase: phase.Expand}},
	}
	old := &phase.Gate{Self: "0.9.1", Known: true, Instances: []phase.Instance{{Component: "openlog-processor", InstanceID: "p1", Version: "0.9.0"}}}
	steps := PlanSteps(ms, map[uint32]bool{1: true}, old)
	got := []string{steps[0].Action, steps[1].Action, steps[2].Action}
	if want := []string{phase.ActionApplied, phase.ActionSkip, phase.ActionApply}; !reflect.DeepEqual(got, want) {
		t.Fatalf("actions %v, want %v (%+v)", got, want, steps)
	}
	if !strings.Contains(steps[1].Reason, "openlog-processor p1 runs 0.9.0") {
		t.Errorf("reason %q", steps[1].Reason)
	}
	steps = PlanSteps(ms, map[uint32]bool{1: true}, &phase.Gate{Self: "0.9.1", Known: true})
	if steps[1].Action != phase.ActionApply {
		t.Errorf("contract not applied without old instances: %+v", steps[1])
	}
}
