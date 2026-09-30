package diskspace

import (
	"context"
	"strings"
	"testing"
)

func TestOnlyADayCanBeDropped(t *testing.T) {
	for _, id := range []string{"", "2026010", "202601011", "2026-01-01", "20260a01", "202601"} {
		if validPartitionID(id) {
			t.Errorf("%q was accepted as a day", id)
		}
	}
	if !validPartitionID("20260101") {
		t.Error("a real partition id was rejected")
	}
}

// A nil connection is deliberate: every case here has to be refused before anything reaches ClickHouse.
func TestAPartitionThatIsNotADayIsRefused(t *testing.T) {
	s := &Shedder{}
	_, err := s.Apply(context.Background(), []Drop{{Unit: "logs", Partition: "2026", Tables: []string{"logs_local"}}})
	if err == nil {
		t.Fatal("accepted a partition id that is not a day")
	}
	if !strings.Contains(err.Error(), "not a day") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestATableOutsideTheOrderIsRefused(t *testing.T) {
	// The planner never produces this. The guard is here because this is the only code in openlog that deletes
	// telemetry outright, and a future caller building its own Drop must not be able to aim it anywhere.
	s := &Shedder{}
	_, err := s.Apply(context.Background(), []Drop{
		{Unit: "made up", Partition: "20260101", Tables: []string{"metrics_1m_local"}},
	})
	if err == nil {
		t.Fatal("accepted a table outside the shed order")
	}
	if !strings.Contains(err.Error(), "not a sheddable table") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestADryRunDropsNothingAndStillReportsThePlan(t *testing.T) {
	// With DryRun the connection is never touched, so a nil one proves no statement was issued.
	s := &Shedder{DryRun: true}
	plan := []Drop{
		{Unit: "traces", Partition: "20260101", Tables: []string{"spans_local", "trace_index_local"}, Bytes: 10},
		{Unit: "logs", Partition: "20260102", Tables: []string{"logs_local"}, Bytes: 20},
	}
	done, err := s.Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	if len(done) != 2 {
		t.Fatalf("reported %d drops, want 2", len(done))
	}
	if PlanBytes(done) != 30 {
		t.Fatalf("reported %d bytes, want 30", PlanBytes(done))
	}
}

func TestEveryTableInTheOrderIsSheddable(t *testing.T) {
	// ShedTables and the guard are built from the same source; this fails if they ever drift apart.
	for _, table := range ShedTables() {
		if !shedTableSet[table] {
			t.Errorf("%s is in the order but the guard would refuse it", table)
		}
	}
	if len(shedTableSet) != len(ShedTables()) {
		t.Errorf("guard has %d tables, order has %d", len(shedTableSet), len(ShedTables()))
	}
}
