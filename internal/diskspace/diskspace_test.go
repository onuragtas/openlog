package diskspace

import (
	"context"
	"log/slog"
	"testing"
)

// capture collects the records a Checker writes, so a test can assert how often a disk was reported.
type capture struct{ records []slog.Record }

func (h *capture) Enabled(context.Context, slog.Level) bool { return true }
func (h *capture) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *capture) WithGroup(string) slog.Handler            { return h }
func (h *capture) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r)
	return nil
}

func (h *capture) atLeast(l slog.Level) int {
	n := 0
	for _, r := range h.records {
		if r.Level >= l {
			n++
		}
	}
	return n
}

// disk of the given used percentage, on a 100-byte disk.
func diskAt(usedPercent uint64) Disk {
	return Disk{Host: "ch-1", Name: "default", Total: 100, Free: 100 - usedPercent}
}

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

func TestLevelIsTheHighestThresholdReached(t *testing.T) {
	th := []int{80, 90}
	for _, tc := range []struct {
		pct  float64
		want int
	}{{0, 0}, {79.9, 0}, {80, 80}, {89.9, 80}, {90, 90}, {99.9, 90}} {
		if got := Level(tc.pct, th); got != tc.want {
			t.Errorf("Level(%v) = %d, want %d", tc.pct, got, tc.want)
		}
	}
}

func TestADiskIsReportedOnceNotOnEveryCheck(t *testing.T) {
	h := &capture{}
	c := &Checker{Log: slog.New(h)}
	for range 5 {
		c.last = Snapshot{Reported: c.levels([]Disk{diskAt(88)}, Defaults())}
	}
	if got := c.last.Reported["ch-1/default"]; got != 80 {
		t.Fatalf("level = %d, want 80", got)
	}
	if n := h.atLeast(slog.LevelWarn); n != 1 {
		t.Fatalf("reported %d times over 5 checks, want 1", n)
	}
}

func TestDriftingJustBelowAThresholdDoesNotReArmIt(t *testing.T) {
	// 78 % is under 80 but inside the hysteresis band, so the disk has not recovered: re-arming here would
	// report again on the next point of drift upwards.
	h := &capture{}
	c := &Checker{Log: slog.New(h)}
	c.last = Snapshot{Reported: c.levels([]Disk{diskAt(88)}, Defaults())}
	c.last = Snapshot{Reported: c.levels([]Disk{diskAt(78)}, Defaults())}
	if got := c.last.Reported["ch-1/default"]; got != 80 {
		t.Fatalf("level = %d, want it still latched at 80", got)
	}
	c.last = Snapshot{Reported: c.levels([]Disk{diskAt(88)}, Defaults())}
	if n := h.atLeast(slog.LevelWarn); n != 1 {
		t.Fatalf("reported %d times, want 1: the drift re-armed the threshold", n)
	}
}

func TestFallingClearOfTheBandArmsTheThresholdAgain(t *testing.T) {
	h := &capture{}
	c := &Checker{Log: slog.New(h)}
	c.last = Snapshot{Reported: c.levels([]Disk{diskAt(88)}, Defaults())}
	c.last = Snapshot{Reported: c.levels([]Disk{diskAt(70)}, Defaults())}
	if got := c.last.Reported["ch-1/default"]; got != 0 {
		t.Fatalf("level = %d, want 0 after falling clear", got)
	}
	c.last = Snapshot{Reported: c.levels([]Disk{diskAt(88)}, Defaults())}
	if n := h.atLeast(slog.LevelWarn); n != 2 {
		t.Fatalf("reported %d times, want 2: rise, recovery, rise again", n)
	}
}

func TestTheTopLevelIsReportedAsAnError(t *testing.T) {
	h := &capture{}
	c := &Checker{Log: slog.New(h)}
	c.last = Snapshot{Reported: c.levels([]Disk{diskAt(95)}, Defaults())}
	if n := h.atLeast(slog.LevelError); n != 1 {
		t.Fatalf("errors = %d, want 1", n)
	}
}

func TestARestartDoesNotReportALevelAlreadyReported(t *testing.T) {
	stored := Snapshot{Reported: map[string]int{"ch-1/default": 90}}
	h := &capture{}
	c := &Checker{Log: slog.New(h), Load: func(context.Context) (Snapshot, bool, error) { return stored, true, nil }}
	c.restore(context.Background())
	c.last.Reported = c.levels([]Disk{diskAt(91)}, Defaults())
	if got := c.last.Reported["ch-1/default"]; got != 90 {
		t.Fatalf("level = %d, want 90 carried over", got)
	}
	if n := h.atLeast(slog.LevelWarn); n != 0 {
		t.Fatalf("reported %d times after a restart, want 0", n)
	}
}
