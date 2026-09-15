//go:build integration

// ClickHouse integration tests of the explorer endpoints (fields.go, logsquery.go, metricsexplorer.go) against a
// single-node ClickHouse cluster "openlog" (deploy/compose/clickhouse/openlog-cluster.xml, user openlog/openlog):
// the schema is migrated (including the attribute key index 0080 and the summary quantile columns 0081), logs and
// metrics of two tenants are written into the local tables — metrics through the processor conversion — and the HTTP
// handlers are checked against the seeded values.
//
//	docker run -d --name openlog-s-explorer-ch --hostname clickhouse -p 127.0.0.1:19040:9000 -e CLICKHOUSE_USER=openlog -e CLICKHOUSE_PASSWORD=openlog \
//	  -e CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1 -v "$PWD/deploy/compose/clickhouse/openlog-cluster.xml:/etc/clickhouse-server/config.d/openlog-cluster.xml:ro" \
//	  clickhouse/clickhouse-server:25.8
//	OPENLOG_TEST_CLICKHOUSE_ADDR=127.0.0.1:19040 go test -tags integration -count=1 -run Explorer ./internal/api
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/migrate"
	"github.com/onuragtas/openlog/internal/processor"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
	"github.com/onuragtas/openlog/internal/tenant"
	"github.com/onuragtas/openlog/schema"
)

type explorerEnv struct {
	h                  http.Handler
	now                time.Time
	tenantA, tenantB   string
	logsA              int
	jsonLogsA, errorsA int
}

var (
	explorerOnce sync.Once
	explorerE    *explorerEnv
	explorerErr  error
)

func explorerSetup(t *testing.T) *explorerEnv {
	t.Helper()
	addr := os.Getenv("OPENLOG_TEST_CLICKHOUSE_ADDR")
	if addr == "" {
		t.Skip("OPENLOG_TEST_CLICKHOUSE_ADDR is not set")
	}
	explorerOnce.Do(func() { explorerE, explorerErr = seedExplorer(addr) })
	if explorerErr != nil {
		t.Fatal(explorerErr)
	}
	return explorerE
}

func kv(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func seedExplorer(addr string) (*explorerEnv, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	conn, err := clickhouse.OpenRetry(ctx, clickhouse.Options{Addr: []string{addr}, Database: "default", User: "openlog", Password: "openlog"}, nil)
	if err != nil {
		return nil, err
	}
	ms, err := migrate.Load(schema.ClickHouse, "clickhouse", "openlog")
	if err != nil {
		return nil, err
	}
	if err := migrate.Run(ctx, conn, ms, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		return nil, err
	}
	sfx := strconv.FormatInt(time.Now().UnixNano(), 36)
	e := &explorerEnv{now: time.Now().UTC().Truncate(time.Minute), tenantA: "exp-a-" + sfx, tenantB: "exp-b-" + sfx}

	// Logs: tenant A has 300 records in the last 50 minutes, every 5th with a JSON body and every 4th an error.
	b, err := conn.PrepareBatch(ctx, "INSERT INTO openlog.logs_local (tenant_id, timestamp, observed_timestamp, service_name, host_id, host_name, severity_text, severity_number, trace_id, body, resource_attributes, attributes)")
	if err != nil {
		return nil, err
	}
	for i := 0; i < 300; i++ {
		ts := e.now.Add(-time.Duration(i*10+5) * time.Second)
		svc := []string{"api", "web"}[i%2]
		sev, num := "INFO", uint8(9)
		if i%4 == 0 {
			sev, num = "ERROR", 17
			e.errorsA++
		}
		body := fmt.Sprintf("request %d served", i)
		if i%5 == 0 {
			body = fmt.Sprintf(`{"user":{"id":%d},"msg":"json line"}`, i%3)
			e.jsonLogsA++
		}
		attrs := map[string]string{"http.route": fmt.Sprintf("/r%d", i%3), "http.status_code": strconv.Itoa(200 + (i%3)*150)}
		res := map[string]string{"k8s.pod.name": fmt.Sprintf("pod-%d", i%2)}
		if err := b.Append(e.tenantA, ts, ts, svc, "h1", "h1.local", sev, num, fmt.Sprintf("%032x", i%7), body, res, attrs); err != nil {
			return nil, err
		}
	}
	for i := 0; i < 20; i++ {
		ts := e.now.Add(-time.Duration(i+1) * time.Minute)
		if err := b.Append(e.tenantB, ts, ts, "secret-svc", "hb", "hb", "INFO", uint8(9), "", "tenant b line",
			map[string]string{}, map[string]string{"tenant.b.only": "x"}); err != nil {
			return nil, err
		}
	}
	if err := b.Send(); err != nil {
		return nil, err
	}
	e.logsA = 300

	// Metrics through the processor conversion, one point per 10s (gauges, sums) or minute (distributions) for 60m.
	rows := processor.NewRows()
	res := &resourcepb.Resource{Attributes: []*commonpb.KeyValue{kv("service.name", "checkout"), kv("host.id", "h1")}}
	for tn, tenantID := range []string{e.tenantA, e.tenantB} {
		var metrics []*metricspb.Metric
		name := func(n string) string {
			if tn == 1 {
				return "tenantb." + n
			}
			return n
		}
		start := uint64(e.now.Add(-time.Hour).UnixNano())
		gauge := &metricspb.Gauge{}
		counter := &metricspb.Sum{IsMonotonic: true, AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE}
		deltaSum := &metricspb.Sum{IsMonotonic: true, AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA}
		for i := 0; i < 360; i++ {
			ts := uint64(e.now.Add(-time.Hour + time.Duration(i*10+5)*time.Second).UnixNano())
			for _, st := range []struct {
				state string
				v     float64
			}{{"user", 0.25}, {"system", 0.75}} {
				gauge.DataPoints = append(gauge.DataPoints, &metricspb.NumberDataPoint{TimeUnixNano: ts, Attributes: []*commonpb.KeyValue{kv("state", st.state)},
					Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: st.v}})
			}
			// 1 per second, reset to 0 at i = 180.
			v := int64(i * 10)
			if i >= 180 {
				v = int64((i - 180) * 10)
			}
			counter.DataPoints = append(counter.DataPoints, &metricspb.NumberDataPoint{StartTimeUnixNano: start, TimeUnixNano: ts, Value: &metricspb.NumberDataPoint_AsInt{AsInt: v}})
			deltaSum.DataPoints = append(deltaSum.DataPoints, &metricspb.NumberDataPoint{StartTimeUnixNano: ts - 10e9, TimeUnixNano: ts, Value: &metricspb.NumberDataPoint_AsInt{AsInt: 6}})
		}
		hist := &metricspb.Histogram{AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE}
		exp := &metricspb.ExponentialHistogram{AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA}
		summary := &metricspb.Summary{}
		for k := 1; k <= 60; k++ {
			ts := uint64(e.now.Add(-time.Hour + time.Duration(k)*time.Minute - 55*time.Second).UnixNano())
			sum := float64(50 * k)
			hist.DataPoints = append(hist.DataPoints, &metricspb.HistogramDataPoint{StartTimeUnixNano: start, TimeUnixNano: ts,
				Count: uint64(20 * k), Sum: &sum, ExplicitBounds: []float64{1, 2, 4}, BucketCounts: []uint64{0, uint64(10 * k), uint64(10 * k), 0}})
			esum := 6.0
			exp.DataPoints = append(exp.DataPoints, &metricspb.ExponentialHistogramDataPoint{TimeUnixNano: ts, Count: 4, Sum: &esum, Scale: 0,
				Positive: &metricspb.ExponentialHistogramDataPoint_Buckets{Offset: 0, BucketCounts: []uint64{2, 2}}})
			summary.DataPoints = append(summary.DataPoints, &metricspb.SummaryDataPoint{StartTimeUnixNano: start, TimeUnixNano: ts, Count: uint64(10 * k), Sum: float64(30 * k),
				QuantileValues: []*metricspb.SummaryDataPoint_ValueAtQuantile{{Quantile: 0.5, Value: 2}, {Quantile: 0.99, Value: 9}}})
		}
		metrics = append(metrics,
			&metricspb.Metric{Name: name("explorer.cpu"), Unit: "1", Description: "CPU share", Data: &metricspb.Metric_Gauge{Gauge: gauge}},
			&metricspb.Metric{Name: name("explorer.requests"), Unit: "{request}", Data: &metricspb.Metric_Sum{Sum: counter}},
			&metricspb.Metric{Name: name("explorer.delta"), Data: &metricspb.Metric_Sum{Sum: deltaSum}},
			&metricspb.Metric{Name: name("explorer.latency"), Unit: "s", Data: &metricspb.Metric_Histogram{Histogram: hist}},
			&metricspb.Metric{Name: name("explorer.exp_latency"), Unit: "s", Data: &metricspb.Metric_ExponentialHistogram{ExponentialHistogram: exp}},
			&metricspb.Metric{Name: name("explorer.rpc"), Unit: "ms", Data: &metricspb.Metric_Summary{Summary: summary}},
		)
		rows.AddMetrics(tenantID, e.now, &colmetrics.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource: res, ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: metrics}}}}})
	}
	cols := processor.Columns[processor.TableMetrics]
	mb, err := conn.PrepareBatch(ctx, "INSERT INTO openlog.metrics_local ("+strings.Join(cols, ", ")+")")
	if err != nil {
		return nil, err
	}
	for i := range rows.Metrics {
		if err := mb.Append(rows.Metrics[i].Values()...); err != nil {
			return nil, err
		}
	}
	if err := mb.Send(); err != nil {
		return nil, err
	}

	res2, _ := tenant.ParseStatic("key-a=" + e.tenantA + ",key-b=" + e.tenantB)
	db := query.New(conn, "openlog", 30*time.Second)
	db.SetLimits("api", config.Query{})
	s := New(config.API{QueryTimeout: 30 * time.Second, MaxRows: 1000}, db, auth.StaticAuthenticator{Resolver: res2},
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	s.now = func() time.Time { return e.now }
	e.h = s.Handler()
	return e, nil
}

func (e *explorerEnv) get(t *testing.T, key, path string, out any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatal(err)
	}
}

func (e *explorerEnv) post(t *testing.T, key, path, body string, out any) {
	t.Helper()
	rec := postJSON(e.h, path, body, key)
	if rec.Code != 200 {
		t.Fatalf("POST %s %s: %d %s", path, body, rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatal(err)
	}
}

type keysResp struct {
	Keys []struct {
		Key, Source, Type string
		Count             *int
		Cardinality       *int
	}
	Sampled bool
}

func (k keysResp) find(key string) (string, int, bool) {
	for _, x := range k.Keys {
		if x.Key == key {
			n := 0
			if x.Cardinality != nil {
				n = *x.Cardinality
			}
			return x.Type, n, true
		}
	}
	return "", 0, false
}

func TestExplorerFieldKeysIntegration(t *testing.T) {
	e := explorerSetup(t)
	var kr keysResp
	e.get(t, "key-a", "/api/v1/fields/keys?signal=logs", &kr)
	if kr.Keys[0].Key != "timestamp" || kr.Keys[0].Source != "field" {
		t.Errorf("top-level fields first: %+v", kr.Keys[0])
	}
	for key, typ := range map[string]string{"attributes.http.route": "string", "attributes.http.status_code": "number",
		"resource.k8s.pod.name": "string", "body.user": "string", "body.msg": "string", "severity_number": "number"} {
		got, _, ok := kr.find(key)
		if !ok || got != typ {
			t.Errorf("key %s: found %v type %q, want %q", key, ok, got, typ)
		}
	}
	if _, card, _ := kr.find("attributes.http.route"); card != 3 {
		t.Errorf("http.route cardinality %d, want 3", card)
	}
	if _, _, ok := kr.find("attributes.tenant.b.only"); ok {
		t.Error("tenant B key visible to tenant A")
	}
	e.get(t, "key-a", "/api/v1/fields/keys?signal=logs&q=ROUTE", &kr)
	if _, _, ok := kr.find("attributes.http.route"); !ok || len(kr.Keys) != 1 {
		t.Errorf("q=ROUTE: %+v", kr.Keys)
	}
	e.get(t, "key-a", "/api/v1/fields/keys?signal=metrics&metric=explorer.cpu", &kr)
	if typ, card, ok := kr.find("attributes.state"); !ok || typ != "string" || card != 2 || kr.Sampled {
		t.Errorf("metric keys: %+v", kr)
	}
	if _, _, ok := kr.find("resource.service.name"); !ok {
		t.Errorf("metric resource keys: %+v", kr.Keys)
	}
}

func TestExplorerFieldValuesIntegration(t *testing.T) {
	e := explorerSetup(t)
	var vr struct {
		Key, Type string
		Values    []struct {
			Value string
			Count int
		}
	}
	filters := url.QueryEscape(`[{"key":"service.name","op":"=","value":"api"},{"key":"http.route","op":"=","value":"/r1"}]`)
	e.get(t, "key-a", "/api/v1/fields/values?signal=logs&key=http.route&filters="+filters, &vr)
	// The filter on http.route itself is ignored; service.name=api keeps even i: routes r0, r1, r2 with 50 each.
	if len(vr.Values) != 3 || vr.Values[0].Count != 50 {
		t.Errorf("values %+v", vr)
	}
	e.get(t, "key-a", "/api/v1/fields/values?signal=logs&key=attributes.http.status_code", &vr)
	if vr.Type != "number" || len(vr.Values) != 3 {
		t.Errorf("status codes %+v", vr)
	}
	e.get(t, "key-a", "/api/v1/fields/values?signal=logs&key=severity_text&q=err", &vr)
	if len(vr.Values) != 1 || vr.Values[0].Value != "ERROR" || vr.Values[0].Count != e.errorsA {
		t.Errorf("severity %+v", vr)
	}
	e.get(t, "key-a", "/api/v1/fields/values?signal=logs&key=body.user.id", &vr)
	if len(vr.Values) != 3 {
		t.Errorf("JSON body values %+v", vr)
	}
	e.get(t, "key-b", "/api/v1/fields/values?signal=logs&key=service.name", &vr)
	if len(vr.Values) != 1 || vr.Values[0].Value != "secret-svc" {
		t.Errorf("tenant B services %+v", vr)
	}
}

type logRows struct {
	Rows []struct {
		RowID      string            `json:"id"`
		TS         string            `json:"timestamp"`
		Body       string            `json:"body"`
		Fields     map[string]string `json:"fields"`
		Attributes map[string]string `json:"attributes"`
	}
	NextCursor *string `json:"next_cursor"`
}

func TestExplorerLogsQueryIntegration(t *testing.T) {
	e := explorerSetup(t)
	count := func(body string) int {
		var lr logRows
		e.post(t, "key-a", "/api/v1/logs/query", body, &lr)
		return len(lr.Rows)
	}
	for body, want := range map[string]int{
		`{"limit":1000}`: e.logsA,
		`{"limit":1000,"filters":[{"key":"severity_text","op":"=","value":"ERROR"}]}`:                                                                                              e.errorsA,
		`{"limit":1000,"filters":[{"key":"severity_number","op":">=","value":13}]}`:                                                                                                e.errorsA,
		`{"limit":1000,"filters":[{"key":"http.status_code","op":">","value":400}]}`:                                                                                               100,
		`{"limit":1000,"filters":[{"key":"resource.k8s.pod.name","op":"in","values":["pod-1"]}]}`:                                                                                  150,
		`{"limit":1000,"filters":[{"key":"body.user.id","op":"=","value":"0"}]}`:                                                                                                   20,
		`{"limit":1000,"filters":[{"key":"body.user","op":"exists"}]}`:                                                                                                             e.jsonLogsA,
		`{"limit":1000,"filters":[{"key":"attributes.http.route","op":"regex","value":"^/r[12]$"}]}`:                                                                               200,
		`{"limit":1000,"filters":[{"key":"trace_id","op":"=","value":"00000000000000000000000000000003"}]}`:                                                                        43,
		`{"limit":1000,"q":"JSON LINE"}`:                                                                                                                                           e.jsonLogsA,
		`{"limit":1000,"groups":[[{"key":"service.name","op":"=","value":"api"},{"key":"severity_text","op":"=","value":"ERROR"}],[{"key":"http.route","op":"=","value":"/r0"}]]}`: 75 + 100 - 25,
		`{"limit":1000,"filters":[{"key":"attributes.tenant.b.only","op":"exists"}]}`:                                                                                              0,
	} {
		if got := count(body); got != want {
			t.Errorf("%s: %d rows, want %d", body, got, want)
		}
	}
	// Paging in both orders returns every row exactly once, in order, with the requested columns.
	for _, order := range []string{"desc", "asc"} {
		seen := map[string]bool{}
		cursor, last := "", ""
		for pages := 0; ; pages++ {
			body := `{"limit":7,"order":"` + order + `","columns":["http.route","resource.k8s.pod.name","body.user.id","timestamp"],"include_record":true`
			if cursor != "" {
				body += `,"cursor":"` + cursor + `"`
			}
			var lr logRows
			e.post(t, "key-a", "/api/v1/logs/query", body+"}", &lr)
			for _, r := range lr.Rows {
				if seen[r.RowID] {
					t.Fatalf("%s: row %s repeated", order, r.RowID)
				}
				seen[r.RowID] = true
				if last != "" && ((order == "desc" && r.TS > last) || (order == "asc" && r.TS < last)) {
					t.Fatalf("%s: %s after %s", order, r.TS, last)
				}
				last = r.TS
				if r.Fields["http.route"] == "" || r.Fields["resource.k8s.pod.name"] == "" || r.Fields["timestamp"] != r.TS || r.Attributes["http.route"] != r.Fields["http.route"] {
					t.Fatalf("columns %+v", r)
				}
				if _, has := r.Fields["body.user.id"]; has != strings.HasPrefix(r.Body, "{") {
					t.Fatalf("body.user.id presence for %q: %+v", r.Body, r.Fields)
				}
			}
			if lr.NextCursor == nil {
				break
			}
			cursor = *lr.NextCursor
			if pages > 100 {
				t.Fatal("paging does not end")
			}
		}
		if len(seen) != e.logsA {
			t.Errorf("%s: %d distinct rows, want %d", order, len(seen), e.logsA)
		}
	}
}

func TestExplorerLogsAggregateIntegration(t *testing.T) {
	e := explorerSetup(t)
	type aggResp struct {
		Step   string
		Total  int
		Series []struct {
			Group  string
			Other  bool
			Total  int
			Points [][2]float64
		}
	}
	var ar aggResp
	e.post(t, "key-a", "/api/v1/logs/aggregate", `{"from":`+strconv.FormatInt(e.now.Add(-time.Hour).UnixMilli(), 10)+`}`, &ar)
	if ar.Step != "30s" || ar.Total != e.logsA || len(ar.Series) != 1 || ar.Series[0].Total != e.logsA {
		t.Errorf("volume: %+v", ar)
	}
	e.post(t, "key-a", "/api/v1/logs/aggregate", `{"group_by":"severity_text","step":"10m"}`, &ar)
	if ar.Step != "600s" || len(ar.Series) != 2 || ar.Series[0].Group != "INFO" || ar.Series[1].Total != e.errorsA {
		t.Errorf("by severity: %+v", ar)
	}
	e.post(t, "key-a", "/api/v1/logs/aggregate", `{"group_by":"http.route","limit":1,"filters":[{"key":"service.name","op":"=","value":"web"}]}`, &ar)
	if len(ar.Series) != 2 || ar.Series[0].Other || !ar.Series[1].Other || ar.Series[0].Total+ar.Series[1].Total != 150 {
		t.Errorf("top 1 + other: %+v", ar)
	}
}

type metricResp struct {
	Metric struct {
		Name, Type, Unit, Temporality string
		Monotonic                     bool
	}
	Aggregation, Step string
	Series            []struct {
		Attributes map[string]string
		Points     [][2]float64
	}
	Truncated bool
}

// steady returns the values of the points strictly inside the range (edge buckets are partial).
func (m metricResp) steady(i int) []float64 {
	var out []float64
	pts := m.Series[i].Points
	for j := 1; j < len(pts)-1; j++ {
		out = append(out, pts[j][1])
	}
	return out
}

func TestExplorerMetricsIntegration(t *testing.T) {
	e := explorerSetup(t)
	var list struct {
		Metrics []struct {
			Name, Type, Unit, Description, Temporality string
			Monotonic                                  bool
			Series                                     int
			Services                                   []string
		}
	}
	e.get(t, "key-a", "/api/v1/metrics?q=explorer.", &list)
	types := map[string]string{}
	for _, m := range list.Metrics {
		types[m.Name] = m.Type
		if m.Name == "explorer.cpu" && (m.Description != "CPU share" || m.Series != 2 || len(m.Services) != 1 || m.Services[0] != "checkout") {
			t.Errorf("cpu %+v", m)
		}
		if strings.HasPrefix(m.Name, "tenantb.") {
			t.Errorf("tenant B metric listed: %s", m.Name)
		}
	}
	for n, typ := range map[string]string{"explorer.cpu": "gauge", "explorer.requests": "sum", "explorer.latency": "histogram",
		"explorer.exp_latency": "exponential_histogram", "explorer.rpc": "summary"} {
		if types[n] != typ {
			t.Errorf("%s: type %q, want %q (%v)", n, types[n], typ, types)
		}
	}
	e.get(t, "key-a", "/api/v1/metrics?q=explorer.&from="+strconv.FormatInt(e.now.Add(-48*time.Hour).UnixMilli(), 10), &list)
	if len(list.Metrics) != 6 {
		t.Errorf("long range list: %+v", list.Metrics)
	}
	var detail struct {
		Type               string
		AttributeKeys      []struct{ Key string } `json:"attribute_keys"`
		ResourceKeys       []struct{ Key string } `json:"resource_keys"`
		DefaultAggregation string                 `json:"default_aggregation"`
	}
	e.get(t, "key-a", "/api/v1/metrics/explorer.latency", &detail)
	if detail.Type != "histogram" || detail.DefaultAggregation != "p95" || len(detail.ResourceKeys) == 0 {
		t.Errorf("detail %+v", detail)
	}
	e.get(t, "key-a", "/api/v1/metrics/explorer.cpu", &detail)
	if len(detail.AttributeKeys) != 1 || detail.AttributeKeys[0].Key != "attributes.state" {
		t.Errorf("cpu keys %+v", detail)
	}

	approx := func(name string, got []float64, want, tol float64) {
		t.Helper()
		if len(got) == 0 {
			t.Errorf("%s: no points", name)
		}
		for _, v := range got {
			if math.Abs(v-want) > tol {
				t.Errorf("%s: %v, want %v ± %v", name, got, want, tol)
				return
			}
		}
	}
	var mr metricResp
	e.post(t, "key-a", "/api/v1/metrics/query", `{"metric":"explorer.cpu","group_by":["state"],"step":"60s"}`, &mr)
	if len(mr.Series) != 2 || mr.Series[0].Attributes["state"] != "system" || mr.Aggregation != "avg" {
		t.Fatalf("cpu by state %+v", mr)
	}
	approx("cpu system", mr.steady(0), 0.75, 1e-9)
	approx("cpu user", mr.steady(1), 0.25, 1e-9)
	e.post(t, "key-a", "/api/v1/metrics/query", `{"metric":"explorer.cpu","aggregation":"max","filters":[{"key":"attributes.state","op":"=","value":"user"}]}`, &mr)
	approx("cpu user max", mr.steady(0), 0.25, 1e-9)
	// Rollup (range > 6h) gives the same average.
	e.post(t, "key-a", "/api/v1/metrics/query", `{"metric":"explorer.cpu","step":"10m","from":`+strconv.FormatInt(e.now.Add(-7*time.Hour).UnixMilli(), 10)+`}`, &mr)
	if len(mr.Series) != 1 || mr.Step != "600s" {
		t.Fatalf("rollup %+v", mr)
	}
	approx("cpu rollup", mr.steady(0), 0.5, 1e-9)

	e.post(t, "key-a", "/api/v1/metrics/query", `{"metric":"explorer.requests","step":"60s"}`, &mr)
	if mr.Aggregation != "rate" || mr.Metric.Temporality != "cumulative" || !mr.Metric.Monotonic {
		t.Fatalf("counter %+v", mr)
	}
	rates := mr.steady(0)
	resets := 0
	for _, v := range rates {
		if v < 0 {
			t.Errorf("negative rate %v", rates)
		}
		if math.Abs(v-1) > 1e-9 {
			resets++
		}
	}
	if resets > 1 {
		t.Errorf("rates %v: want 1/s except at the reset", rates)
	}
	e.post(t, "key-a", "/api/v1/metrics/query", `{"metric":"explorer.delta","aggregation":"rate","step":"60s"}`, &mr)
	approx("delta rate", mr.steady(0), 0.6, 1e-9)

	e.post(t, "key-a", "/api/v1/metrics/query", `{"metric":"explorer.latency","aggregation":"p50","step":"60s"}`, &mr)
	approx("histogram p50", mr.steady(0), 2, 1e-9)
	e.post(t, "key-a", "/api/v1/metrics/query", `{"metric":"explorer.latency","aggregation":"p75","step":"60s"}`, &mr)
	approx("histogram p75", mr.steady(0), 3, 1e-9)
	e.post(t, "key-a", "/api/v1/metrics/query", `{"metric":"explorer.latency","aggregation":"avg","step":"60s"}`, &mr)
	approx("histogram avg", mr.steady(0), 2.5, 1e-9)
	if rec := postJSON(e.h, "/api/v1/metrics/query", `{"metric":"explorer.exp_latency","aggregation":"rate_per_minute"}`, "key-a"); rec.Code != 400 {
		t.Errorf("unknown aggregation: %d", rec.Code)
	}
	e.post(t, "key-a", "/api/v1/metrics/query", `{"metric":"explorer.exp_latency","aggregation":"p50","step":"60s"}`, &mr)
	approx("exponential histogram p50", mr.steady(0), 2, 1e-9)
	e.post(t, "key-a", "/api/v1/metrics/query", `{"metric":"explorer.exp_latency","aggregation":"count","step":"60s"}`, &mr)
	approx("exponential histogram count", mr.steady(0), 4, 1e-9)
	e.post(t, "key-a", "/api/v1/metrics/query", `{"metric":"explorer.rpc","aggregation":"p99","step":"60s"}`, &mr)
	approx("summary p99", mr.steady(0), 9, 1e-9)
	e.post(t, "key-a", "/api/v1/metrics/query", `{"metric":"explorer.rpc","aggregation":"avg","step":"60s"}`, &mr)
	approx("summary avg", mr.steady(0), 3, 1e-9)

	// Tenant B cannot read tenant A's metric.
	e.post(t, "key-b", "/api/v1/metrics/query", `{"metric":"explorer.cpu"}`, &mr)
	if len(mr.Series) != 0 || mr.Metric.Type != "" {
		t.Errorf("tenant B read tenant A: %+v", mr)
	}
}
