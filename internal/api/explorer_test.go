package api

import (
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Explorer endpoints (fields.go, logsquery.go, metricsexplorer.go): every executed statement is tenant-scoped, and
// invalid requests are rejected before reading ClickHouse.

func explorerGet(h http.Handler, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("openlog-license-key", "key-a")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestExplorerEndpointsAreTenantScoped(t *testing.T) {
	s, conn := newTestServer(t)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	h := s.Handler()
	long := "&from=" + strconv.FormatInt(now.Add(-48*time.Hour).UnixMilli(), 10)
	filters := url.QueryEscape(`[{"key":"severity_text","op":"in","values":["ERROR","WARN"]},{"key":"body.user.id","op":"exists"},{"key":"resource.k8s.pod.name","op":"=","value":"web-1"}]`)
	for _, p := range []string{
		"/api/v1/fields/keys?signal=logs&q=http",
		"/api/v1/fields/keys?signal=metrics&metric=system.cpu.utilization",
		"/api/v1/fields/keys?signal=traces" + long,
		"/api/v1/fields/values?signal=logs&key=resource.k8s.pod.name&q=web&filters=" + filters,
		"/api/v1/fields/values?signal=logs&key=severity_number",
		"/api/v1/fields/values?signal=metrics&key=state&metric=system.cpu.utilization",
		"/api/v1/metrics?q=cpu",
		"/api/v1/metrics?q=cpu" + long,
		"/api/v1/metrics/http.server/duration",
	} {
		if rec := explorerGet(h, p); rec.Code >= 500 || rec.Code == 400 {
			t.Errorf("%s: status %d %s", p, rec.Code, rec.Body)
		}
	}
	for path, body := range map[string]string{
		"/api/v1/logs/query": `{"filters":[{"key":"service.name","op":"=","value":"api"}],` +
			`"groups":[[{"key":"http.status_code","op":">=","value":500}],[{"key":"body","op":"contains","value":"panic"}]],` +
			`"q":"timeout","columns":["attributes.http.route","resource.k8s.pod.name","body.user.id","timestamp"],"include_record":true,` +
			`"order":"asc","cursor":"` + encodeLogCursorOrder(logPos{ts: 1, key: 2}, 1, true) + `"}`,
		"/api/v1/logs/aggregate": `{"group_by":"resource.k8s.namespace.name","limit":5,"filters":[{"key":"host.id","op":"!=","value":"h1"}]}`,
		"/api/v1/metrics/query":  `{"metric":"system.cpu.utilization","filters":[{"key":"host.name","op":"=","value":"h1"}],"group_by":["state"],"from":"2026-09-13T12:00:00Z"}`,
	} {
		if rec := postJSON(h, path, body, "key-a"); rec.Code != 200 {
			t.Errorf("%s: status %d %s", path, rec.Code, rec.Body)
		}
	}
	if rec := postJSON(h, "/api/v1/logs/aggregate", `{}`, "key-a"); rec.Code != 200 {
		t.Errorf("aggregate without group_by: %d %s", rec.Code, rec.Body)
	}
	if len(conn.sql) < 15 {
		t.Fatalf("only %d statements executed", len(conn.sql))
	}
	for _, sql := range conn.sql {
		refs := tableRef.FindAllStringIndex(sql, -1)
		if len(refs) == 0 {
			t.Errorf("statement without table: %s", sql)
		}
		for _, r := range refs {
			if !strings.HasPrefix(sql[r[1]:], " WHERE (tenant_id = {tenant_id:String})") {
				t.Errorf("unscoped table reference: %s", sql)
			}
		}
	}
	// No data for the metric: 404 on metadata, empty series on queries.
	if rec := explorerGet(h, "/api/v1/metrics/system.cpu.utilization"); rec.Code != 404 {
		t.Errorf("unknown metric: %d", rec.Code)
	}
}

func TestExplorerValidation(t *testing.T) {
	s, conn := newTestServer(t)
	h := s.Handler()
	for _, p := range []string{
		"/api/v1/fields/keys",
		"/api/v1/fields/keys?signal=events",
		"/api/v1/fields/keys?signal=logs&metric=x",
		"/api/v1/fields/keys?signal=logs&limit=0",
		"/api/v1/fields/values?signal=logs",
		"/api/v1/fields/values?signal=logs&key=tenant_id",
		"/api/v1/fields/values?signal=logs&key=a&filters=" + url.QueryEscape(`{"key":"a"}`),
		"/api/v1/fields/values?signal=logs&key=a&filters=" + url.QueryEscape(`[{"key":"b","op":"~","value":"x"}]`),
		"/api/v1/fields/values?signal=traces&key=body.user",
		"/api/v1/metrics?limit=-1",
	} {
		if rec := explorerGet(h, p); rec.Code != 400 {
			t.Errorf("%s: status %d, want 400", p, rec.Code)
		}
	}
	many := make([]string, maxLogColumns+1)
	for i := range many {
		many[i] = `"c` + strconv.Itoa(i) + `"`
	}
	desc := encodeLogCursor(logPos{ts: 1, key: 2}, 1)
	for _, tc := range []struct{ path, body string }{
		{"/api/v1/logs/query", `{"filters":[{"key":"service.name","op":"~","value":"x"}]}`},
		{"/api/v1/logs/query", `{"filters":[{"key":"timestamp","op":"=","value":"x"}]}`},
		{"/api/v1/logs/query", `{"unknown":1}`},
		{"/api/v1/logs/query", `{"order":"sideways"}`},
		{"/api/v1/logs/query", `{"order":"asc","cursor":"` + desc + `"}`},
		{"/api/v1/logs/query", `{"columns":[` + strings.Join(many, ",") + `]}`},
		{"/api/v1/logs/query", `{"from":"2026-09-15T12:00:00Z","to":"2026-09-15T11:00:00Z"}`},
		{"/api/v1/logs/query", `{"from":true}`},
		{"/api/v1/logs/query", `{"limit":-5}`},
		{"/api/v1/logs/query", `{"q":"` + strings.Repeat("x", maxLogQueryBytes+1) + `"}`},
		{"/api/v1/logs/aggregate", `{"step":"1ms"}`},
		{"/api/v1/logs/aggregate", `{"limit":51}`},
		{"/api/v1/logs/aggregate", `{"group_by":"timestamp"}`},
		{"/api/v1/metrics/query", `{}`},
		{"/api/v1/metrics/query", `{"metric":"m","group_by":["value"]}`},
		{"/api/v1/metrics/query", `{"metric":"m","group_by":["a","b","c","d","e","f"]}`},
		{"/api/v1/metrics/query", `{"metric":"m","limit":500}`},
		{"/api/v1/metrics/query", `{"metric":"m","filters":[{"key":"a","op":"in"}]}`},
	} {
		if rec := postJSON(h, tc.path, tc.body, "key-a"); rec.Code != 400 {
			t.Errorf("%s %s: status %d, want 400 (%s)", tc.path, truncate(tc.body, 80), rec.Code, rec.Body)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/logs/query", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Authorization", "Bearer key-a")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Errorf("text/plain body: %d", rec.Code)
	}
	for _, sql := range conn.sql {
		if strings.Contains(sql, "logs") && !strings.Contains(sql, "metrics") {
			// Invalid log requests must fail before any statement; metric validation reads metadata only after
			// the request itself is valid.
			t.Errorf("statement executed for an invalid request: %s", sql)
		}
	}
}

func TestAggregateStep(t *testing.T) {
	base := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		span time.Duration
		want time.Duration
	}{
		{15 * time.Minute, 10 * time.Second},
		{time.Hour, 30 * time.Second},
		{24 * time.Hour, 15 * time.Minute},
		{7 * 24 * time.Hour, 2 * time.Hour},
		{3 * 365 * 24 * time.Hour, 7 * 24 * time.Hour},
	} {
		got, err := aggregateStep("", base, base.Add(tc.span))
		if err != nil || got != tc.want {
			t.Errorf("%v: %v %v, want %v", tc.span, got, err, tc.want)
		}
	}
	if got, err := aggregateStep("90s", base, base.Add(time.Hour)); err != nil || got != 90*time.Second {
		t.Errorf("explicit: %v %v", got, err)
	}
	if _, err := aggregateStep("1s", base, base.Add(24*time.Hour)); err == nil {
		t.Error("86400 buckets accepted")
	}
}

func TestLogCursorOrder(t *testing.T) {
	asc := encodeLogCursorOrder(logPos{ts: 5, key: 7}, 2, true)
	if _, _, err := decodeLogCursor(asc); err == nil {
		t.Error("GET /logs accepted an ascending cursor")
	}
	pos, n, err := decodeLogCursorOrder(asc, true)
	if err != nil || pos != (logPos{ts: 5, key: 7}) || n != 2 {
		t.Errorf("%+v %d %v", pos, n, err)
	}
	positions := []logPos{{1, 1}, {2, 1}, {3, 1}}
	_, end, next := logPageOrder(positions, nil, 0, 2, true)
	if end != 2 || next == "" {
		t.Fatalf("end %d next %q", end, next)
	}
	if p, _, err := decodeLogCursorOrder(next, true); err != nil || p != (logPos{2, 1}) {
		t.Errorf("next cursor %+v %v", p, err)
	}
}

func TestHistogramQuantile(t *testing.T) {
	for _, tc := range []struct {
		q      float64
		bounds []float64
		counts []uint64
		want   float64
	}{
		{0.5, []float64{1, 2, 4}, []uint64{0, 10, 10, 0}, 2},
		{0.25, []float64{1, 2, 4}, []uint64{0, 10, 10, 0}, 1.5},
		{0.75, []float64{1, 2, 4}, []uint64{0, 10, 10, 0}, 3},
		{0.5, []float64{1, 2, 4}, []uint64{0, 0, 0, 5}, 4},
		{0.5, []float64{10}, []uint64{4, 0}, 5},
		{0.5, []float64{-1, 1}, []uint64{2, 0, 0}, -1},
		{0.99, []float64{0.005, 0.01, 0.025}, []uint64{50, 30, 20, 0}, 0.01 + 0.015*19/20},
	} {
		if got := histogramQuantile(tc.q, tc.bounds, tc.counts); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("q=%v %v %v: %v, want %v", tc.q, tc.bounds, tc.counts, got, tc.want)
		}
	}
	for _, tc := range []struct {
		bounds []float64
		counts []uint64
	}{{nil, nil}, {[]float64{1}, []uint64{0, 0}}, {[]float64{1, 2}, []uint64{1, 2}}} {
		if got := histogramQuantile(0.5, tc.bounds, tc.counts); !math.IsNaN(got) {
			t.Errorf("%v %v: %v, want NaN", tc.bounds, tc.counts, got)
		}
	}
}

func TestDistAccumulator(t *testing.T) {
	bounds := []float64{1, 2}
	g := []string{"api"}
	// Cumulative: series 1 increases, then resets; series 2 starts one bucket later.
	acc := newDistAccumulator(false)
	for _, row := range []distRow{
		{series: 1, g: g, t: 0, blast: []uint64{1, 1, 0}, bounds: bounds, clast: 2, slast: 3},
		{series: 1, g: g, t: 60_000, blast: []uint64{3, 2, 0}, bounds: bounds, clast: 5, slast: 9},
		{series: 1, g: g, t: 120_000, blast: []uint64{1, 0, 0}, bounds: bounds, clast: 1, slast: 0.5},
		{series: 2, g: g, t: 60_000, blast: []uint64{7, 7, 7}, bounds: bounds, clast: 21, slast: 30},
		{series: 2, g: g, t: 120_000, blast: []uint64{7, 9, 7}, bounds: bounds, clast: 23, slast: 34},
	} {
		acc.add(&row)
	}
	byT := map[int64]*distBucket{}
	for _, b := range acc.buckets() {
		byT[b.t] = b
	}
	if b := byT[0]; b != nil && b.hasCount {
		t.Errorf("first cumulative bucket has an increase: %+v", b)
	}
	b := byT[60_000]
	if b == nil || b.count != 3 || b.sum != 6 || len(b.counts) != 3 || b.counts[0] != 2 || b.counts[1] != 1 {
		t.Fatalf("bucket 60s: %+v", b)
	}
	if v, ok := b.value("avg", time.Minute); !ok || v != 2 {
		t.Errorf("avg %v %v", v, ok)
	}
	if v, ok := b.value("rate", time.Minute); !ok || v != 0.05 {
		t.Errorf("rate %v %v", v, ok)
	}
	b = byT[120_000]
	// series 1 reset: its current values; series 2: [0,2,0], count 2, sum 4.
	if b.count != 3 || b.sum != 4.5 || b.counts[0] != 1 || b.counts[1] != 2 {
		t.Errorf("bucket 120s: %+v", b)
	}
	if v, ok := b.value("p50", time.Minute); !ok || math.Abs(v-1.25) > 1e-9 {
		t.Errorf("p50 %v %v", v, ok)
	}
	// Delta temporality sums the bucket's points; summaries average stored quantiles.
	acc = newDistAccumulator(true)
	acc.add(&distRow{series: 1, g: g, t: 0, bsum: []uint64{4, 0, 0}, bounds: bounds, csum: 4, ssum: 2})
	acc.add(&distRow{series: 2, g: g, t: 0, bsum: []uint64{0, 4, 0}, bounds: []float64{5, 6}, csum: 4, ssum: 20})
	b = acc.buckets()[0]
	if b.count != 8 || b.sum != 22 || b.counts[0] != 4 || b.counts[1] != 0 {
		t.Errorf("delta bucket (different layouts are not merged): %+v", b)
	}
	acc = newDistAccumulator(false)
	acc.add(&distRow{series: 1, g: g, t: 0, quantiles: []float64{0.5, 0.99}, qvalues: []float64{10, 90}})
	acc.add(&distRow{series: 2, g: g, t: 0, quantiles: []float64{0.5, 0.99}, qvalues: []float64{20, math.NaN()}})
	b = acc.buckets()[0]
	if v, ok := b.value("p50", time.Minute); !ok || v != 15 {
		t.Errorf("summary p50 %v %v", v, ok)
	}
	if v, ok := b.value("p99", time.Minute); !ok || v != 90 {
		t.Errorf("summary p99 %v %v", v, ok)
	}
	if _, ok := b.value("p90", time.Minute); ok {
		t.Error("summary without the p90 level returned a value")
	}
}

func TestSeriesSetLimit(t *testing.T) {
	ss := newSeriesSet([]string{"host.name"}, 2)
	ss.add([]string{"b"}, 2, 1)
	ss.add([]string{"b"}, 1, 2)
	ss.add([]string{"a"}, 1, math.NaN())
	ss.add([]string{"a"}, 1, 3)
	ss.add([]string{"c"}, 1, 4)
	out := ss.result()
	if !ss.truncated || len(out) != 2 || out[0].Attributes["host.name"] != "a" || out[1].Points[0][0].(int64) != 1 {
		t.Errorf("truncated %v series %+v", ss.truncated, out)
	}
}

func TestMetricAggregations(t *testing.T) {
	for _, tc := range []struct {
		m   metricInfoJSON
		def string
	}{
		{metricInfoJSON{Type: "gauge"}, "avg"},
		{metricInfoJSON{Type: "sum", Monotonic: true}, "rate"},
		{metricInfoJSON{Type: "sum"}, "last"},
		{metricInfoJSON{Type: "histogram"}, "p95"},
		{metricInfoJSON{Type: "exponential_histogram"}, "p95"},
		{metricInfoJSON{Type: "summary"}, "avg"},
	} {
		aggs, def := metricAggregations(&tc.m)
		found := false
		for _, a := range aggs {
			found = found || a == def
		}
		if def != tc.def || !found {
			t.Errorf("%+v: %v %s", tc.m, aggs, def)
		}
	}
}
