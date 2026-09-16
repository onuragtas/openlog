package synthetics

import (
	"strings"
	"testing"
	"time"
)

func sampleResult() Result {
	return Result{CheckID: "c1", TenantID: "tenant-a", Location: LocationLocal, Name: "Checkout",
		URL: "https://shop.example.com/health", Method: "GET", At: time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC),
		Success: true, StatusCode: 200, DurationMs: 123.5, DNSMs: 2, ConnectMs: 10, TLSMs: 30, FirstByteMs: 100,
		ResponseBytes: 4}
}

// colIndex keeps the assertions readable and independent of the column order.
func colIndex(t *testing.T, columns []string, name string) int {
	t.Helper()
	for i, c := range columns {
		if c == name {
			return i
		}
	}
	t.Fatalf("no column %q", name)
	return -1
}

// Every row must have exactly one value per column: a mismatch would be a ClickHouse insert error only a
// live cluster would show.
func TestRunRowsMatchColumns(t *testing.T) {
	rows := runRows([]Result{sampleResult(), sampleResult().fail(ErrorStatus, "HTTP 500, expected 200")})
	if len(rows) != 2 {
		t.Fatalf("%d rows for 2 results", len(rows))
	}
	for i, row := range rows {
		if len(row) != len(runColumns) {
			t.Fatalf("row %d has %d values for %d columns", i, len(row), len(runColumns))
		}
	}
	ok, bad := rows[0], rows[1]
	if ok[colIndex(t, runColumns, "tenant_id")] != "tenant-a" || ok[colIndex(t, runColumns, "check_id")] != "c1" {
		t.Errorf("identity: %v", ok)
	}
	if ok[colIndex(t, runColumns, "success")] != true || bad[colIndex(t, runColumns, "success")] != false {
		t.Errorf("success flags: %v %v", ok[colIndex(t, runColumns, "success")], bad[colIndex(t, runColumns, "success")])
	}
	if bad[colIndex(t, runColumns, "error_kind")] != ErrorStatus {
		t.Errorf("error kind %v", bad[colIndex(t, runColumns, "error_kind")])
	}
	// The status column is UInt16 and the timings Float32: the driver rejects a plain int or float64.
	if _, isU16 := ok[colIndex(t, runColumns, "status_code")].(uint16); !isU16 {
		t.Errorf("status_code is %T, want uint16", ok[colIndex(t, runColumns, "status_code")])
	}
	if _, isF32 := ok[colIndex(t, runColumns, "duration_ms")].(float32); !isF32 {
		t.Errorf("duration_ms is %T, want float32", ok[colIndex(t, runColumns, "duration_ms")])
	}
	if _, isU32 := ok[colIndex(t, runColumns, "response_bytes")].(uint32); !isU32 {
		t.Errorf("response_bytes is %T, want uint32", ok[colIndex(t, runColumns, "response_bytes")])
	}
}

// Each result becomes two gauge data points, so metric alert rules and dashboards see a check as a metric.
func TestMetricRowsMirrorEachResult(t *testing.T) {
	rows := metricRows([]Result{sampleResult()})
	if len(rows) != 2 {
		t.Fatalf("%d metric rows for one result, want 2", len(rows))
	}
	for i, row := range rows {
		if len(row) != len(metricColumns) {
			t.Fatalf("metric row %d has %d values for %d columns", i, len(row), len(metricColumns))
		}
	}
	nameAt, valueAt := colIndex(t, metricColumns, "metric_name"), colIndex(t, metricColumns, "value")
	if rows[0][nameAt] != MetricSuccess || rows[1][nameAt] != MetricDuration {
		t.Fatalf("metric names: %v %v", rows[0][nameAt], rows[1][nameAt])
	}
	if rows[0][valueAt] != 1.0 {
		t.Errorf("success value %v, want 1", rows[0][valueAt])
	}
	if rows[1][valueAt] != 123.5 {
		t.Errorf("duration value %v, want the run duration", rows[1][valueAt])
	}
	if rows[0][colIndex(t, metricColumns, "metric_type")] != "gauge" {
		t.Errorf("metric type %v, want gauge (so metrics_1m rolls it up)", rows[0][colIndex(t, metricColumns, "metric_type")])
	}
	if rows[1][colIndex(t, metricColumns, "unit")] != "ms" {
		t.Errorf("duration unit %v", rows[1][colIndex(t, metricColumns, "unit")])
	}
	attrs, ok := rows[0][colIndex(t, metricColumns, "attributes")].(map[string]string)
	if !ok {
		t.Fatalf("attributes are %T", rows[0][colIndex(t, metricColumns, "attributes")])
	}
	for k, want := range map[string]string{"check.id": "c1", "check.name": "Checkout", "location": LocationLocal,
		"http.request.method": "GET", "url.full": "https://shop.example.com/health", "http.response.status_code": "200"} {
		if attrs[k] != want {
			t.Errorf("attribute %s = %q, want %q", k, attrs[k], want)
		}
	}
	if _, has := attrs["error.kind"]; has {
		t.Errorf("a successful run carries an error.kind: %v", attrs)
	}
}

func TestMetricRowsOfFailedRun(t *testing.T) {
	rows := metricRows([]Result{sampleResult().fail(ErrorTimeout, "the request timed out")})
	valueAt := colIndex(t, metricColumns, "value")
	if rows[0][valueAt] != 0.0 {
		t.Errorf("success value %v for a failed run, want 0", rows[0][valueAt])
	}
	attrs := rows[0][colIndex(t, metricColumns, "attributes")].(map[string]string)
	if attrs["error.kind"] != ErrorTimeout {
		t.Errorf("error.kind = %q", attrs["error.kind"])
	}
}

// A run that never reached the server has no status code, so the attribute is left out rather than sent as 0.
func TestMetricRowsWithoutStatusCode(t *testing.T) {
	r := sampleResult()
	r.StatusCode = 0
	rows := metricRows([]Result{r.fail(ErrorDNS, "the host could not be resolved")})
	attrs := rows[0][colIndex(t, metricColumns, "attributes")].(map[string]string)
	if _, has := attrs["http.response.status_code"]; has {
		t.Errorf("status code attribute present without a response: %v", attrs)
	}
}

// The series id identifies the time series: stable across runs, different when a label changes.
func TestSeriesIDIsStable(t *testing.T) {
	a := seriesID("tenant-a", MetricSuccess, map[string]string{"check.id": "c1", "location": "local"})
	// The same attributes in a different insertion order are the same series.
	b := seriesID("tenant-a", MetricSuccess, map[string]string{"location": "local", "check.id": "c1"})
	if a != b {
		t.Fatalf("series id depends on map order: %d != %d", a, b)
	}
	for name, id := range map[string]uint64{
		"other tenant":    seriesID("tenant-b", MetricSuccess, map[string]string{"check.id": "c1", "location": "local"}),
		"other metric":    seriesID("tenant-a", MetricDuration, map[string]string{"check.id": "c1", "location": "local"}),
		"other check":     seriesID("tenant-a", MetricSuccess, map[string]string{"check.id": "c2", "location": "local"}),
		"extra attribute": seriesID("tenant-a", MetricSuccess, map[string]string{"check.id": "c1", "location": "local", "error.kind": "dns"}),
	} {
		if id == a {
			t.Errorf("%s shares the series id", name)
		}
	}
}

func TestClampStatus(t *testing.T) {
	for in, want := range map[int]int{200: 200, 599: 599, 0: 0, -1: 0, 600: 0} {
		if got := clampStatus(in); got != want {
			t.Errorf("clampStatus(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestFailTruncatesTheMessage(t *testing.T) {
	r := sampleResult().fail(ErrorRequest, strings.Repeat("x", maxErrorBytes+100))
	if len(r.Error) != maxErrorBytes {
		t.Errorf("message length %d, want the %d byte cap", len(r.Error), maxErrorBytes)
	}
	if r.Success {
		t.Error("fail left the result successful")
	}
}
