package processor

import (
	"testing"
	"time"
)

func TestIngestUsageRows(t *testing.T) {
	r := NewRows()
	at := time.Date(2026, 9, 14, 10, 59, 59, 0, time.UTC)
	r.AddIngestUsage("t2", "logs", at, 100)
	r.AddIngestUsage("t1", "traces", at, 50)
	r.AddIngestUsage("t1", "traces", at.Add(-30*time.Minute), 25)
	r.AddIngestUsage("t1", "traces", at.Add(time.Second), 10) // next hour
	if r.Len(TableUsageIngest) != 3 {
		t.Fatalf("len %d", r.Len(TableUsageIngest))
	}
	vals := r.Values(TableUsageIngest)
	if len(vals) != 3 || len(vals[0]) != len(Columns[TableUsageIngest]) {
		t.Fatalf("values %v", vals)
	}
	first := r.UsageIngest()[0]
	if first.TenantID != "t1" || !first.Hour.Equal(time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)) || first.Requests != 2 || first.Bytes != 75 {
		t.Errorf("first row %+v", first)
	}
	if _, ok := ShardingKeys[TableUsageIngest]; !ok {
		t.Error("no sharding key")
	}
	found := false
	for _, tb := range Tables {
		found = found || tb == TableUsageIngest
	}
	if !found {
		t.Error("usage table is not inserted")
	}
}
