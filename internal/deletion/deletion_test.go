package deletion

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestPlanMutations(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	parts := map[string]map[string]uint64{
		"spans_local":           {"20260913": 5, "20260912": 0},
		"logs_local":            {"20260913": 1, "20260912": 2, "bad'id": 9},
		"usage_ingest_1h_local": {"202609": 3},
	}
	submitted := map[string]map[string]time.Time{"logs_local": {"20260912": now.Add(-5 * time.Minute), "20260913": now.Add(-time.Hour)}}
	got := PlanMutations(parts, submitted, now, 30*time.Minute, 10)
	var keys []string
	for _, m := range got {
		keys = append(keys, m.Table+"/"+m.PartitionID)
	}
	want := "logs_local/20260913,spans_local/20260913,usage_ingest_1h_local/202609"
	if strings.Join(keys, ",") != want {
		t.Fatalf("plan %v, want %s", keys, want)
	}
	if got := PlanMutations(parts, nil, now, time.Minute, 2); len(got) != 2 {
		t.Fatalf("max not applied: %v", got)
	}
	sql := Mutation{Table: "logs_local", PartitionID: "20260913"}.SQL("openlog", "openlog", "t_abc")
	if sql != "ALTER TABLE `openlog`.`logs_local` ON CLUSTER 'openlog' DELETE IN PARTITION ID '20260913' WHERE tenant_id = 't_abc'" {
		t.Fatalf("sql %s", sql)
	}
}

func TestPseudonymAndHash(t *testing.T) {
	a, b := NewPseudonym(), NewPseudonym()
	if a == b || !regexp.MustCompile(`^deleted-user-[0-9a-f]{10}$`).MatchString(a) {
		t.Fatalf("pseudonyms %q %q", a, b)
	}
	if h := SubjectHash("acme"); len(h) != 64 || h != SubjectHash("acme") {
		t.Fatalf("hash %q", h)
	}
	for id, ok := range map[string]bool{"acme": true, "a_b-c9": true, "Acme": false, "x'y": false, "": false} {
		if ValidTenant(id) != ok {
			t.Errorf("ValidTenant(%q)", id)
		}
	}
	err := &SoleOwnerError{Orgs: []OrgRef{{ID: "1", Name: "Acme"}, {ID: "2", Name: "Beta"}}}
	if !strings.Contains(err.Error(), "Acme, Beta") {
		t.Fatal(err)
	}
}
