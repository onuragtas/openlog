package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// POST /api/v1/metrics/exemplars (metricsexemplars.go, D-130): tenant-scoped, reads only the exemplar table, spreads
// the result over the range and rejects invalid requests before touching ClickHouse.

func TestMetricExemplarsQueryShape(t *testing.T) {
	s, conn := newTestServer(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	h := s.Handler()

	body := `{"metric":"http.server.duration","from":"2026-09-17T11:00:00Z","to":"2026-09-17T12:00:00Z",` +
		`"filters":[{"key":"service.name","op":"=","value":"checkout"},{"key":"attributes.http.route","op":"=","value":"/pay"}],` +
		`"groups":[[{"key":"host.id","op":"exists"}]],"limit":20}`
	rec := postJSON(h, "/api/v1/metrics/exemplars", body, "key-a")
	if rec.Code != 200 {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	if len(conn.sql) != 1 {
		t.Fatalf("statements = %d, want 1", len(conn.sql))
	}
	sql := conn.sql[0]

	// Only the exemplar table is read, and the tenant predicate is the first condition on it.
	if !strings.Contains(sql, "`openlog`.metric_exemplars WHERE (tenant_id = {tenant_id:String})") {
		t.Errorf("not tenant-scoped:\n%s", sql)
	}
	for _, other := range []string{"metrics_1m", "`openlog`.metrics ", "spans", "logs"} {
		if strings.Contains(sql, other) {
			t.Errorf("reads %s:\n%s", other, sql)
		}
	}
	// The metric name, the range and both filter conditions are bound parameters, never spliced text.
	for _, want := range []string{
		"metric_name = {name:String}",
		"timestamp >= fromUnixTimestamp64Nano({t_from:Int64})",
		"toStartOfInterval(timestamp, toIntervalSecond({bucket:UInt32}))",
		"LIMIT 1 BY b",
		"LIMIT 20",
		"count() OVER () AS total",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q:\n%s", want, sql)
		}
	}
	for _, literal := range []string{"checkout", "/pay", "http.server.duration"} {
		if strings.Contains(sql, literal) {
			t.Errorf("literal %q spliced into SQL:\n%s", literal, sql)
		}
	}
	// One exemplar per bucket, largest value first inside the bucket, buckets in time order.
	if !strings.Contains(sql, "ORDER BY b, value DESC") {
		t.Errorf("wrong ordering:\n%s", sql)
	}
}

func TestMetricExemplarsRejectsInvalidRequests(t *testing.T) {
	s, conn := newTestServer(t)
	h := s.Handler()
	for name, body := range map[string]string{
		"no metric":       `{}`,
		"empty metric":    `{"metric":""}`,
		"limit too large": `{"metric":"m","limit":501}`,
		"negative limit":  `{"metric":"m","limit":-1}`,
		"unknown filter":  `{"metric":"m","filters":[{"key":"tenant_id","op":"=","value":"other"}]}`,
		"bad operator":    `{"metric":"m","filters":[{"key":"service.name","op":"nope","value":"a"}]}`,
		"from after to":   `{"metric":"m","from":"2026-09-17T12:00:00Z","to":"2026-09-17T11:00:00Z"}`,
	} {
		if rec := postJSON(h, "/api/v1/metrics/exemplars", body, "key-a"); rec.Code != 400 {
			t.Errorf("%s: status %d %s", name, rec.Code, rec.Body)
		}
	}
	if len(conn.sql) != 0 {
		t.Errorf("rejected requests must not query ClickHouse, got %d statements", len(conn.sql))
	}
}

func TestMetricExemplarsEmptyResult(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	rec := postJSON(h, "/api/v1/metrics/exemplars", `{"metric":"m"}`, "key-a")
	if rec.Code != 200 {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	var got struct {
		Exemplars []map[string]any `json:"exemplars"`
		Total     uint64           `json:"total"`
		Truncated bool             `json:"truncated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// An empty list, never null: the UI renders it directly.
	if got.Exemplars == nil || len(got.Exemplars) != 0 || got.Total != 0 || got.Truncated {
		t.Errorf("body %s", rec.Body)
	}
}

func TestExemplarBucketSpreadsOverTheRange(t *testing.T) {
	from := time.Date(2026, 9, 17, 11, 0, 0, 0, time.UTC)
	// A one-hour range and 50 exemplars: buckets of 72s, so the dots cover the whole hour.
	if got := exemplarBucket(from, from.Add(time.Hour), 50); got != 72*time.Second {
		t.Errorf("bucket = %s, want 72s", got)
	}
	// Short ranges never go below the explorer's minimum step.
	if got := exemplarBucket(from, from.Add(time.Minute), 50); got != minExemplarBucket {
		t.Errorf("bucket = %s, want %s", got, minExemplarBucket)
	}
}
