package usage

import (
	"bytes"
	"math"
	"strings"
	"testing"
	"time"
)

func TestPeriods(t *testing.T) {
	now := time.Date(2026, 2, 14, 12, 0, 0, 0, time.FixedZone("X", 3*3600))
	p := PeriodOf(now)
	if p.ID() != "2026-02" || !p.Start.Equal(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)) || !p.End.Equal(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("period %+v", p)
	}
	for v, want := range map[string]string{"": "2026-02", "current": "2026-02", "previous": "2026-01", "2025-12": "2025-12"} {
		got, err := ParsePeriod(v, now)
		if err != nil || got.ID() != want {
			t.Errorf("ParsePeriod(%q) = %s, %v", v, got.ID(), err)
		}
	}
	if _, err := ParsePeriod("2026-13", now); err == nil {
		t.Error("invalid month accepted")
	}
	if !p.Elapsed(now).Equal(now) || !PeriodOf(now.AddDate(0, -2, 0)).Elapsed(now).Equal(time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)) {
		t.Error("Elapsed")
	}
}

func TestProject(t *testing.T) {
	p := PeriodOf(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)) // 30 days
	now := p.Start.Add(10 * 24 * time.Hour)                    // 10 days elapsed
	if got := Project(100, 0, p, now); math.Abs(got-300) > 1e-9 {
		t.Errorf("linear projection %v, want 300", got)
	}
	if got := Project(100, 5, p, now); math.Abs(got-200) > 1e-9 {
		t.Errorf("rate projection %v, want 100 + 5*20", got)
	}
	if got := Project(100, 0, p, p.Start.Add(30*time.Minute)); got != 100 {
		t.Errorf("too early %v", got)
	}
	if got := Project(100, 7, p, p.End.Add(time.Hour)); got != 100 {
		t.Errorf("past period %v", got)
	}
	if got := RecentDailyAverage([]float64{1, 2, 3, 4, 100}, 3); got != 3 {
		t.Errorf("average of the last 3 complete days = %v", got)
	}
	if got := RecentDailyAverage([]float64{100}, 7); got != 0 {
		t.Errorf("only today: %v", got)
	}
}

func TestExportCSV(t *testing.T) {
	days := []Day{{Day: "2026-09-01", Signals: []SignalUsage{{Signal: "logs", Items: 10, Bytes: 2000, IngestBytes: 1500, IngestRequests: 2}},
		IngestBytes: 1500, Hosts: 3, Query: QueryUsage{Queries: 4, CPUSeconds: 0.25}}}
	var buf bytes.Buffer
	if err := WriteCSV(&buf, "acme", "2026-09", days); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, line := range []string{"tenant_id,period,day,metric,value", "acme,2026-09,2026-09-01,logs.items,10", "acme,2026-09,2026-09-01,logs.ingest_bytes,1500",
		"acme,2026-09,2026-09-01,hosts,3", "acme,2026-09,2026-09-01,query_cpu_seconds,0.25"} {
		if !strings.Contains(out, line+"\n") {
			t.Errorf("missing %q in\n%s", line, out)
		}
	}
}
