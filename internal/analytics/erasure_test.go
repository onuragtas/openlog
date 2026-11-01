package analytics

import (
	"strings"
	"testing"
)

// The statement text outlives the request in system.mutations and the query log, so what is in it is the point.
func TestStatementsCoverEveryTableAndCarryOnlyTheHash(t *testing.T) {
	sts := Statements("openlog", "openlog", "tenant-a", "shop-web", 42)
	if len(sts) != len(Tables) {
		t.Fatalf("%d statements for %d tables", len(sts), len(Tables))
	}
	for i, st := range sts {
		if !strings.Contains(st, "`openlog`."+Tables[i]+" ON CLUSTER 'openlog' DELETE") {
			t.Errorf("wrong target: %s", st)
		}
		if !strings.Contains(st, "tenant_id = 'tenant-a' AND app = 'shop-web' AND actor_hash = 42") {
			t.Errorf("wrong scope: %s", st)
		}
	}
}

func TestStatementsEscapeWhatTheyQuote(t *testing.T) {
	st := Statements("openlog", "c", "t", `a' OR 1=1 --\`, 1)[0]
	if !strings.Contains(st, `app = 'a\' OR 1=1 --\\' AND`) {
		t.Errorf("application not escaped: %s", st)
	}
}
