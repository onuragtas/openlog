package cloudconnect

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/processor"
)

// The metric rows are the contract with everything downstream: the column list of `metrics`, the sharding key
// and the series id the processor computes for OTLP points. A drift in any of them splits a series or puts a
// row where reads do not expect it.

type fakeInserter struct {
	mu sync.Mutex
	// failTimes makes the first n inserts fail.
	failTimes int
	calls     int
	table     string
	keyCols   []string
	cols      []string
	tokens    []string
	rows      [][]any
}

func (f *fakeInserter) Insert(_ context.Context, table string, keyColumns, columns []string, token string, rows [][]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= f.failTimes {
		return errors.New("clickhouse unavailable")
	}
	f.table, f.keyCols, f.cols = table, keyColumns, columns
	f.tokens = append(f.tokens, token)
	f.rows = append(f.rows, rows...)
	return nil
}

func (f *fakeInserter) snapshot() (int, [][]any, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, append([][]any(nil), f.rows...), append([]string(nil), f.tokens...)
}

func testPoint(resource string) CollectedPoint {
	return CollectedPoint{TenantID: "tenant-a", ConnectionID: "conn-1", ConnectionName: "Prod AWS",
		Point: Point{Provider: ProviderAWS, Service: "rds", Platform: "aws_rds", MetricName: "CPUUtilization",
			Unit: "1", Stat: "Average", Value: 12.5, Timestamp: time.Unix(1_700_000_000, 0).UTC(),
			Region: "eu-central-1", AccountID: "123456789012", ResourceName: resource}}
}

func testWriter(ins BlockInserter, o WriterOptions) *Writer {
	o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewWriter(ins, o)
}

func TestMetricRowsMatchTheMetricsTable(t *testing.T) {
	rows := metricRows([]CollectedPoint{testPoint("orders-db")})
	if len(rows) != 1 {
		t.Fatalf("built %d rows", len(rows))
	}
	row := rows[0]
	if len(row) != len(metricColumns) {
		t.Fatalf("row has %d values, the column list has %d", len(row), len(metricColumns))
	}
	// Positions that downstream code depends on.
	idx := map[string]int{}
	for i, c := range metricColumns {
		idx[c] = i
	}
	if row[idx["tenant_id"]] != "tenant-a" {
		t.Errorf("tenant_id = %v", row[idx["tenant_id"]])
	}
	if row[idx["metric_name"]] != "cloud.aws.rds.cpu_utilization" {
		t.Errorf("metric_name = %v", row[idx["metric_name"]])
	}
	// A provider already aggregated the window, so the point is a gauge: metrics_1m rolls gauges up.
	if row[idx["metric_type"]] != "gauge" || row[idx["is_monotonic"]] != false {
		t.Errorf("metric_type/is_monotonic = %v/%v", row[idx["metric_type"]], row[idx["is_monotonic"]])
	}
	if row[idx["unit"]] != "1" || row[idx["value"]] != 12.5 {
		t.Errorf("unit/value = %v/%v", row[idx["unit"]], row[idx["value"]])
	}
	if row[idx["scope_name"]] != scopeName {
		t.Errorf("scope_name = %v", row[idx["scope_name"]])
	}
	resAttrs, ok := row[idx["resource_attributes"]].(map[string]string)
	if !ok || resAttrs["cloud.provider"] != "aws" || resAttrs["cloud.resource.name"] != "orders-db" {
		t.Errorf("resource_attributes = %v", row[idx["resource_attributes"]])
	}
	attrs, ok := row[idx["attributes"]].(map[string]string)
	if !ok || attrs["cloud.metric.name"] != "CPUUtilization" {
		t.Errorf("attributes = %v", row[idx["attributes"]])
	}
	// The series id must be the one the processor computes for the same point, so a cloud series and an OTLP
	// series of the same shape are one series, not two.
	want := processor.SeriesID("tenant-a", "cloud.aws.rds.cpu_utilization", resAttrs, attrs)
	if row[idx["series_id"]] != want {
		t.Errorf("series_id = %v, want processor.SeriesID = %v", row[idx["series_id"]], want)
	}
}

// Two resources of the same metric are two series: the resource name is a resource attribute, and the
// processor's hash covers resource attributes, so they must not collide.
func TestSeriesIDDistinguishesResources(t *testing.T) {
	rows := metricRows([]CollectedPoint{testPoint("orders-db"), testPoint("catalog-db"), testPoint("orders-db")})
	idx := 0
	for i, c := range metricColumns {
		if c == "series_id" {
			idx = i
		}
	}
	a, b, again := rows[0][idx], rows[1][idx], rows[2][idx]
	if a == b {
		t.Error("two resources share one series id")
	}
	if a != again {
		t.Error("the same resource produced two series ids")
	}
}

func TestWriterWritesToTheMetricsTable(t *testing.T) {
	ins := &fakeInserter{}
	w := testWriter(ins, WriterOptions{})
	w.Add([]CollectedPoint{testPoint("orders-db"), testPoint("catalog-db")})

	if n := w.Flush(context.Background()); n != 2 {
		t.Fatalf("Flush = %d, want 2", n)
	}
	if ins.table != "metrics" {
		t.Errorf("table = %q", ins.table)
	}
	// Direct shard inserts must use the metrics table's own sharding key.
	if len(ins.keyCols) != 2 || ins.keyCols[0] != "tenant_id" || ins.keyCols[1] != "host_id" {
		t.Errorf("key columns = %v", ins.keyCols)
	}
	if len(ins.cols) != len(metricColumns) {
		t.Errorf("columns = %d, want %d", len(ins.cols), len(metricColumns))
	}
}

// A failed insert is retried with the same deduplication token, so ReplicatedMergeTree drops the block it
// already has instead of storing the points twice.
func TestWriterRetriesWithTheSameToken(t *testing.T) {
	ins := &fakeInserter{failTimes: 1}
	w := testWriter(ins, WriterOptions{})
	w.Add([]CollectedPoint{testPoint("orders-db")})

	if n := w.Flush(context.Background()); n != 0 {
		t.Fatalf("a failed flush reported %d rows written", n)
	}
	if n := w.Flush(context.Background()); n != 1 {
		t.Fatalf("the retry wrote %d rows, want 1", n)
	}
	calls, rows, tokens := ins.snapshot()
	if calls != 2 {
		t.Errorf("insert called %d times", calls)
	}
	if len(rows) != 1 {
		t.Errorf("stored %d rows, want the batch once", len(rows))
	}
	if len(tokens) != 1 {
		t.Fatalf("recorded %d tokens", len(tokens))
	}
}

// When ClickHouse is unavailable for long enough the buffer drops the oldest points rather than growing
// without bound; the drop is counted, not silent.
func TestWriterDropsOldestWhenFull(t *testing.T) {
	ins := &fakeInserter{}
	w := testWriter(ins, WriterOptions{MaxBatch: 2, MaxBuffered: 2})
	w.Add([]CollectedPoint{testPoint("a"), testPoint("b")})
	w.Add([]CollectedPoint{testPoint("c")})

	if n := w.Flush(context.Background()); n != 2 {
		t.Fatalf("Flush = %d, want the buffer cap of 2", n)
	}
	_, rows, _ := ins.snapshot()
	if len(rows) != 2 {
		t.Fatalf("wrote %d rows", len(rows))
	}
	// The newest point survived, the oldest was dropped.
	nameIdx := 0
	for i, c := range metricColumns {
		if c == "resource_attributes" {
			nameIdx = i
		}
	}
	last := rows[1][nameIdx].(map[string]string)["cloud.resource.name"]
	if last != "c" {
		t.Errorf("the newest point was dropped: last resource = %q", last)
	}
}

func TestWriterFlushWithNothingBuffered(t *testing.T) {
	w := testWriter(&fakeInserter{}, WriterOptions{})
	if n := w.Flush(context.Background()); n != 0 {
		t.Errorf("Flush with an empty buffer = %d", n)
	}
}
