package diskspace

import (
	"strings"
	"testing"
	"time"
)

// A 1000-byte disk keeps the arithmetic of these tests readable: free 90 means 91 % used.
func diskFree(free uint64) Disk { return Disk{Host: "ch-1", Name: "default", Total: 1000, Free: free} }

func shedOn(mut ...func(*Settings)) Effective {
	on := true
	s := Settings{ShedEnabled: &on}
	for _, m := range mut {
		m(&s)
	}
	return s.Resolve()
}

func minPartitions(n int) func(*Settings) { return func(s *Settings) { s.ShedMinPartitions = &n } }
func maxDrops(n int) func(*Settings)      { return func(s *Settings) { s.ShedMaxDropsPerRun = &n } }

// days builds n consecutive partitions of one table, each of the given size, oldest first. Real partition ids of
// a toDate() partition are fixed-width YYYYMMDD, which is what makes sorting the strings sort the days; a
// shorter-by-a-digit id would sort wrongly and the planner's "oldest first" would silently mean something else.
func days(table string, n int, each uint64) []Partition {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var out []Partition
	for i := range n {
		out = append(out, Partition{Table: table, ID: base.AddDate(0, 0, i).Format("20060102"), Bytes: each})
	}
	return out
}

// daysFrom is days() starting at an offset, for a table whose range does not line up with another's.
func daysFrom(table string, from, n int, each uint64) []Partition {
	all := days(table, from+n, each)
	return all[from:]
}

func names(plan []Drop) string {
	var out []string
	for _, d := range plan {
		out = append(out, d.Unit+"/"+d.Partition)
	}
	return strings.Join(out, " ")
}

func TestNothingIsDroppedWhileSheddingIsOff(t *testing.T) {
	// The disk is nearly full and there is plenty to drop: being off still has to mean nothing happens.
	if plan := Plan(diskFree(5), days("logs_local", 10, 100), Defaults()); plan != nil {
		t.Fatalf("dropped %d partitions with shedding off: %s", len(plan), names(plan))
	}
}

func TestNothingIsDroppedBelowTheStartLevel(t *testing.T) {
	// 89 % against a start of 90.
	if plan := Plan(diskFree(110), days("logs_local", 10, 100), shedOn()); plan != nil {
		t.Fatalf("dropped at 89%%: %s", names(plan))
	}
}

func TestOnlyEnoughIsDroppedToReachTheStopLevel(t *testing.T) {
	// used 910, stop 85 % of 1000 = 850, so 60 bytes are needed and one 100-byte day covers it.
	plan := Plan(diskFree(90), days("logs_local", 10, 100), shedOn(minPartitions(1)))
	if len(plan) != 1 {
		t.Fatalf("dropped %d partitions, want 1: %s", len(plan), names(plan))
	}
	if plan[0].Partition != "20260101" {
		t.Fatalf("dropped %s, want the oldest day", plan[0].Partition)
	}
	if got := PlanBytes(plan); got != 100 {
		t.Fatalf("freed estimate = %d, want 100", got)
	}
}

func TestTheOldestDaysGoFirst(t *testing.T) {
	// 300 bytes needed out of 10-byte days: the first thirty-odd days, in order.
	plan := Plan(diskFree(0), days("logs_local", 60, 10), shedOn(minPartitions(1), maxDrops(1000)))
	if len(plan) < 2 {
		t.Fatalf("dropped %d partitions: %s", len(plan), names(plan))
	}
	for i := 1; i < len(plan); i++ {
		if plan[i-1].Partition >= plan[i].Partition {
			t.Fatalf("not oldest-first: %s", names(plan))
		}
	}
}

func TestTheCheapestDataIsGivenUpFirst(t *testing.T) {
	var parts []Partition
	parts = append(parts, days("profiles_local", 10, 20)...)
	parts = append(parts, days("logs_local", 10, 100)...)
	parts = append(parts, days("metrics_local", 10, 100)...)
	plan := Plan(diskFree(90), parts, shedOn(minPartitions(1)))
	if len(plan) == 0 {
		t.Fatal("dropped nothing")
	}
	if plan[0].Unit != "profiles" {
		t.Fatalf("first drop was %q, want profiles: %s", plan[0].Unit, names(plan))
	}
	// Raw metrics are the last thing given up, so they must not appear while logs are still available.
	for _, d := range plan {
		if d.Unit == "raw metrics" {
			t.Fatalf("gave up raw metrics too early: %s", names(plan))
		}
	}
}

func TestATableKeepsItsFloorOfPartitions(t *testing.T) {
	// Four days and a floor of three: exactly one may go, however much is still needed.
	plan := Plan(diskFree(0), days("logs_local", 4, 10), shedOn(minPartitions(3), maxDrops(1000)))
	if len(plan) != 1 {
		t.Fatalf("dropped %d partitions, want 1: %s", len(plan), names(plan))
	}
}

func TestATableAtItsFloorIsLeftAlone(t *testing.T) {
	if plan := Plan(diskFree(0), days("logs_local", 3, 10), shedOn(minPartitions(3))); plan != nil {
		t.Fatalf("dropped a table that was already at its floor: %s", names(plan))
	}
}

func TestOneRunDropsNoMoreThanItsLimit(t *testing.T) {
	plan := Plan(diskFree(0), days("logs_local", 100, 1), shedOn(minPartitions(1), maxDrops(4)))
	if len(plan) != 4 {
		t.Fatalf("dropped %d partitions, want 4: %s", len(plan), names(plan))
	}
}

func TestCoupledTablesGiveUpTheSameDay(t *testing.T) {
	// trace_index points into spans: an index into rows that are gone is worse than no index.
	var parts []Partition
	parts = append(parts, days("spans_local", 10, 50)...)
	parts = append(parts, days("trace_index_local", 10, 5)...)
	plan := Plan(diskFree(90), parts, shedOn(minPartitions(1)))
	if len(plan) == 0 {
		t.Fatal("dropped nothing")
	}
	for _, d := range plan {
		if len(d.Tables) != 2 {
			t.Fatalf("%s dropped %v, want both spans and trace_index", d.Partition, d.Tables)
		}
	}
}

func TestASharedDayIsSkippedWhenOneHalfIsAtItsFloor(t *testing.T) {
	// spans has ten days and room to give one; trace_index has only the last three, so it is at its floor.
	// The three days they share must survive: dropping spans there would leave the index pointing at nothing.
	// The seven days only spans has are fair game, because there is no index of those days to dangle.
	shared := map[string]bool{}
	var parts []Partition
	parts = append(parts, days("spans_local", 10, 50)...)
	for _, p := range daysFrom("trace_index_local", 7, 3, 5) {
		parts = append(parts, p)
		shared[p.ID] = true
	}
	plan := Plan(diskFree(0), parts, shedOn(minPartitions(3), maxDrops(1000)))
	dropped := 0
	for _, d := range plan {
		if d.Unit != "traces" {
			continue
		}
		dropped++
		if shared[d.Partition] {
			t.Fatalf("dropped %s, a day trace_index still has while at its floor: %v", d.Partition, d.Tables)
		}
	}
	if dropped == 0 {
		t.Fatal("dropped none of the spans-only days")
	}
}

func TestTablesOutsideTheOrderAreNeverDropped(t *testing.T) {
	// The long-term rollup, billing and the APM rollups are not sheddable at any pressure. metrics_1m is also
	// partitioned by month, so a single drop would take a month of it.
	parts := append(days("metrics_1m_local", 10, 100), days("usage_signals_1h_local", 10, 100)...)
	parts = append(parts, days("apm_transactions_1m_local", 10, 100)...)
	if plan := Plan(diskFree(0), parts, shedOn(minPartitions(1), maxDrops(1000))); plan != nil {
		t.Fatalf("dropped from a protected table: %s", names(plan))
	}
}

func TestADiskOfUnknownSizeIsLeftAlone(t *testing.T) {
	// total_space 0 means ClickHouse could not size the disk. Treating that as full would delete everything.
	if plan := Plan(Disk{Host: "ch-1", Name: "default"}, days("logs_local", 10, 100), shedOn(minPartitions(1))); plan != nil {
		t.Fatalf("dropped against a disk of unknown size: %s", names(plan))
	}
}

func TestShedLevelsCannotDeleteBeforeTheDiskIsReportedCritical(t *testing.T) {
	// The mistake this rule exists for: setting a low level expecting a warning and getting a deletion.
	low := 30
	s := Settings{ShedStartPercent: &low}
	if err := s.Validate(); err == nil {
		t.Fatal("accepted a shed level below the critical reporting level")
	}
}
