package diskspace

import "testing"

func TestUsedRatioCountsWhatClickHouseCannotHave(t *testing.T) {
	// free_space excludes the blocks ext4 reserves for root: they are not available to ClickHouse, so they
	// count as used. Reporting them as free would put the number a few percent under the real pressure.
	d := Disk{Total: 100, Free: 10}
	if got := d.UsedRatio(); got != 0.9 {
		t.Fatalf("used ratio = %v, want 0.9", got)
	}
}

func TestUsedRatioOfADiskWithoutASizeIsZero(t *testing.T) {
	// total_space is 0 for disks ClickHouse cannot size; dividing by it would be +Inf or NaN and would then
	// cross every threshold at once.
	if got := (Disk{}).UsedRatio(); got != 0 {
		t.Fatalf("used ratio = %v, want 0", got)
	}
}

func TestUsedRatioSurvivesMoreFreeThanTotal(t *testing.T) {
	// Seen on remote and cache disks; unsigned arithmetic would wrap to an enormous ratio.
	if got := (Disk{Total: 100, Free: 400}).UsedRatio(); got != 0 {
		t.Fatalf("used ratio = %v, want 0", got)
	}
}

func TestTheFullestReplicaDecidesForTheCluster(t *testing.T) {
	// An average would hide the one node that is about to stop accepting parts.
	s := Snapshot{Disks: []Disk{
		{Host: "ch-1", Name: "default", Total: 100, Free: 90},
		{Host: "ch-2", Name: "default", Total: 100, Free: 5},
		{Host: "ch-3", Name: "default", Total: 100, Free: 50},
	}}
	worst, ok := s.Fullest()
	if !ok {
		t.Fatal("no disk reported")
	}
	if worst.Host != "ch-2" {
		t.Fatalf("fullest = %s, want ch-2", worst.Host)
	}
	if worst.UsedRatio() != 0.95 {
		t.Fatalf("used ratio = %v, want 0.95", worst.UsedRatio())
	}
}

func TestFullestReportsNothingWhenNoDiskAnswered(t *testing.T) {
	// Every replica unreachable must not read as an empty, and therefore healthy, disk.
	if _, ok := (Snapshot{}).Fullest(); ok {
		t.Fatal("reported a disk from an empty snapshot")
	}
}
