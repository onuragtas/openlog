//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// APM phase (docs/contracts/apm.md): a tiny OTLP trace generator sends 30 traces
// frontend -> orders -> postgresql to ingest; the phase checks the APM API against the
// known input and against raw spans in ClickHouse.
//
// Input: trace i (0..29) has weight 2 when i%3 == 0 (tracestate ot=th:8, 10 traces), so every
// service has 40 weighted requests; orders fails when i%5 == 0 (6 traces, weighted 8 errors).
//
// Standalone against another stack (e.g. the APM demo; see test/apmdemo/run.sh):
//
//	E2E_APM_ONLY=1 E2E_SKIP_UP=1 E2E_KEEP=1 E2E_ENV_FILE=<env> go test -tags e2e -v -count=1 -run TestAPMStandalone ./test/e2e

const apmTraceCount = 30

var apmRun = strconv.FormatInt(time.Now().UnixNano(), 36)

func apmSvc(role string) string { return "e2e-apm-" + role + "-" + apmRun }

func TestAPMStandalone(t *testing.T) {
	if os.Getenv("E2E_APM_ONLY") != "1" {
		t.Skip("E2E_APM_ONLY!=1")
	}
	testAPM(t)
}

func kvs(kv ...string) []*commonpb.KeyValue {
	out := make([]*commonpb.KeyValue, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, &commonpb.KeyValue{Key: kv[i], Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: kv[i+1]}}})
	}
	return out
}

// apmRequests builds one export request per service per trace (like separate processes).
func apmRequests(base time.Time) []*coltrace.ExportTraceServiceRequest {
	res := func(role string) *resourcepb.Resource {
		return &resourcepb.Resource{Attributes: kvs("service.name", apmSvc(role), "service.namespace", "e2e", "deployment.environment", "e2e",
			"host.id", targetID(), "telemetry.sdk.language", "go", "service.version", "1.0.0-e2e")}
	}
	var reqs []*coltrace.ExportTraceServiceRequest
	for i := range apmTraceCount {
		traceID := []byte(fmt.Sprintf("%s%016x", "apm-e2e-", uint64(i)|uint64(time.Now().UnixNano())<<12))[:16]
		sid := func(k byte) []byte { return []byte{k, 0xe2, 0xe2, byte(i), byte(time.Now().UnixNano()), 1, 2, 3} }
		ts := ""
		if i%3 == 0 {
			ts = "ot=th:8"
		}
		start := base.Add(time.Duration(i) * 4 * time.Second)
		dur := time.Duration(20+(i%10)*5) * time.Millisecond
		span := func(id, parent []byte, name string, kind tracepb.Span_SpanKind, s time.Time, d time.Duration, attrs ...string) *tracepb.Span {
			return &tracepb.Span{TraceId: traceID, SpanId: id, ParentSpanId: parent, Name: name, Kind: kind, TraceState: ts,
				StartTimeUnixNano: uint64(s.UnixNano()), EndTimeUnixNano: uint64(s.Add(d).UnixNano()), Attributes: kvs(attrs...)}
		}
		root, client, server, db := sid(1), sid(2), sid(3), sid(4)
		fe := []*tracepb.Span{
			span(root, nil, "GET /api/orders/:id", tracepb.Span_SPAN_KIND_SERVER, start, dur, "http.request.method", "GET", "http.route", "/api/orders/:id", "http.response.status_code", "200"),
			span(client, root, "GET", tracepb.Span_SPAN_KIND_CLIENT, start.Add(time.Millisecond), dur-2*time.Millisecond, "http.request.method", "GET", "server.address", "orders", "server.port", "8080"),
		}
		status := "200"
		if i%5 == 0 {
			status = "500"
		}
		srvSpan := span(server, client, "GET /orders/{id}", tracepb.Span_SPAN_KIND_SERVER, start.Add(2*time.Millisecond), dur-4*time.Millisecond,
			"http.request.method", "GET", "http.route", "/orders/{id}", "http.response.status_code", status)
		if i%5 == 0 {
			srvSpan.Status = &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR, Message: "inventory unavailable"}
			srvSpan.Events = []*tracepb.Span_Event{{Name: "exception", TimeUnixNano: uint64(start.Add(3 * time.Millisecond).UnixNano()), Attributes: kvs(
				"exception.type", "*errors.errorString", "exception.message", fmt.Sprintf("order %d: inventory shard %d unavailable", 1000+i, i%4),
				"exception.stacktrace", "main.loadInventory(0x1)\n\t/src/orders/main.go:88 +0x1f\nnet/http.HandlerFunc.ServeHTTP()\n\t/usr/local/go/src/net/http/server.go:2322 +0x29\n")}}
		}
		ord := []*tracepb.Span{srvSpan, span(db, server, "SELECT orders", tracepb.Span_SPAN_KIND_CLIENT, start.Add(3*time.Millisecond), dur/3,
			"db.system", "postgresql", "db.name", "orders", "db.statement", fmt.Sprintf("SELECT * FROM orders WHERE id = %d AND status = 'new'", 1000+i))}
		for role, spans := range map[string][]*tracepb.Span{"frontend": fe, "orders": ord} {
			reqs = append(reqs, &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{
				{Resource: res(role), ScopeSpans: []*tracepb.ScopeSpans{{Scope: &commonpb.InstrumentationScope{Name: "e2e-apm"}, Spans: spans}}},
			}})
		}
	}
	return reqs
}

func postTraces(req *coltrace.ExportTraceServiceRequest) error {
	body, err := proto.Marshal(req)
	if err != nil {
		return err
	}
	r, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+env["OPENLOG_OTLP_HTTP_PORT"]+"/v1/traces", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/x-protobuf")
	r.Header.Set("openlog-license-key", ingestKey())
	resp, err := httpClient.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST /v1/traces: HTTP %d: %s", resp.StatusCode, b)
	}
	return nil
}

type apmRedJSON struct {
	Requests   float64  `json:"requests"`
	Throughput float64  `json:"throughput"`
	Errors     float64  `json:"errors"`
	ErrorRate  float64  `json:"error_rate"`
	P95Ms      *float64 `json:"p95_ms"`
	Apdex      *float64 `json:"apdex"`
}

func testAPM(t *testing.T) {
	var hostBefore hostJSON
	_, hostErr := apiGet(key(), "/api/v1/hosts/"+targetID(), nil, &hostBefore)

	base := time.Now().Add(-150 * time.Second).Truncate(time.Second)
	for _, req := range apmRequests(base) {
		if err := postTraces(req); err != nil {
			t.Fatal(err)
		}
	}
	from, to := base.Add(-time.Minute).Truncate(time.Minute), time.Now().Add(time.Minute).Truncate(time.Minute)
	rng := url.Values{"from": {strconv.FormatInt(from.UnixMilli(), 10)}, "to": {strconv.FormatInt(to.UnixMilli(), 10)}}
	orders, frontend := apmSvc("orders"), apmSvc("frontend")
	scoped := func(extra url.Values) url.Values {
		q := url.Values{"namespace": {"e2e"}, "environment": {"e2e"}}
		for k, v := range rng {
			q[k] = v
		}
		for k, v := range extra {
			q[k] = v
		}
		return q
	}

	t.Run("services_and_transactions", func(t *testing.T) {
		eventually(t, 3*time.Minute, 3*time.Second, "APM services list both services with 40 weighted requests", func() error {
			var resp struct {
				Services []struct {
					Name string `json:"service_name"`
					apmRedJSON
					Language string `json:"language"`
				} `json:"services"`
			}
			if _, err := apiGet(key(), "/api/v1/apm/services", rng, &resp); err != nil {
				return err
			}
			got := map[string]float64{}
			for _, s := range resp.Services {
				got[s.Name] = s.Requests
				if s.Name == orders && (s.Errors != 8 || s.Language != "go" || s.Apdex == nil) {
					return fmt.Errorf("orders: %+v", s)
				}
			}
			if got[orders] != 40 || got[frontend] != 40 {
				return fmt.Errorf("requests %v", got)
			}
			return nil
		})
		var txs struct {
			Transactions []struct {
				Name string `json:"transaction_name"`
				apmRedJSON
			} `json:"transactions"`
		}
		if _, err := apiGet(key(), "/api/v1/apm/services/"+url.PathEscape(orders)+"/transactions", scoped(nil), &txs); err != nil {
			t.Fatal(err)
		}
		if len(txs.Transactions) != 1 || txs.Transactions[0].Name != "GET /orders/{id}" || txs.Transactions[0].Requests != 40 || txs.Transactions[0].Errors != 8 {
			t.Errorf("transactions %+v", txs.Transactions)
		}
		// API vs raw spans: counts exact, p95 within the latency bucket of the exact value (apm.md §4.1).
		rows, err := chQuery(`SELECT sum(sample_weight), sumIf(sample_weight, status_code = 'error' OR attributes['http.response.status_code'] = '500'),
			quantileExactWeighted(0.95)(duration_ns / 1e6, toUInt64(sample_weight))
			FROM openlog.spans WHERE service_name = {svc:String} AND kind = 'server' AND attributes['http.route'] = '/orders/{id}'`, map[string]string{"svc": orders})
		if err != nil || len(rows) != 1 {
			t.Fatalf("raw spans: %v %v", rows, err)
		}
		rawReq, _ := strconv.ParseFloat(rows[0][0], 64)
		rawErr, _ := strconv.ParseFloat(rows[0][1], 64)
		rawP95, _ := strconv.ParseFloat(rows[0][2], 64)
		tx := txs.Transactions[0]
		if tx.Requests != rawReq || tx.Errors != rawErr || tx.P95Ms == nil || math.Abs(*tx.P95Ms-rawP95)/rawP95 > 0.092 {
			t.Errorf("API %+v (p95 %v) vs raw requests %v errors %v p95 %v", tx.apmRedJSON, tx.P95Ms, rawReq, rawErr, rawP95)
		}
		t.Logf("GET /orders/{id}: API requests %.0f errors %.0f p95 %.2f ms; raw %.0f / %.0f / %.2f ms", tx.Requests, tx.Errors, *tx.P95Ms, rawReq, rawErr, rawP95)
	})

	t.Run("errors_databases_traces", func(t *testing.T) {
		var groups struct {
			Groups []struct {
				ID      string  `json:"group_id"`
				Type    string  `json:"error_type"`
				Message string  `json:"message"`
				Count   float64 `json:"count"`
			} `json:"groups"`
		}
		if _, err := apiGet(key(), "/api/v1/apm/services/"+url.PathEscape(orders)+"/errors", scoped(nil), &groups); err != nil {
			t.Fatal(err)
		}
		if len(groups.Groups) != 1 || groups.Groups[0].Message != "order <n>: inventory shard <n> unavailable" || groups.Groups[0].Count != 8 {
			t.Fatalf("error groups %+v", groups.Groups)
		}
		var detail struct {
			Stack   string            `json:"stacktrace"`
			Samples []json.RawMessage `json:"samples"`
		}
		if _, err := apiGet(key(), "/api/v1/apm/services/"+url.PathEscape(orders)+"/errors/"+groups.Groups[0].ID, scoped(nil), &detail); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(detail.Stack, "main.loadInventory") || len(detail.Samples) != 6 {
			t.Errorf("group detail: stack %q, %d samples", detail.Stack, len(detail.Samples))
		}
		var dbs struct {
			Queries []struct {
				Statement string  `json:"statement"`
				System    string  `json:"db_system"`
				Calls     float64 `json:"calls"`
			} `json:"queries"`
		}
		if _, err := apiGet(key(), "/api/v1/apm/services/"+url.PathEscape(orders)+"/databases", scoped(nil), &dbs); err != nil {
			t.Fatal(err)
		}
		if len(dbs.Queries) != 1 || dbs.Queries[0].Statement != "SELECT * FROM orders WHERE id = ? AND status = ?" || dbs.Queries[0].Calls != 40 {
			t.Errorf("db queries %+v", dbs.Queries)
		}
		var traces struct {
			Traces []struct {
				TraceID string `json:"trace_id"`
				IsError bool   `json:"is_error"`
			} `json:"traces"`
		}
		q := url.Values{"service": {orders}, "error": {"true"}, "min_duration_ms": {"10"}}
		for k, v := range rng {
			q[k] = v
		}
		if _, err := apiGet(key(), "/api/v1/apm/traces", q, &traces); err != nil {
			t.Fatal(err)
		}
		if len(traces.Traces) != 6 {
			t.Errorf("error trace search: %d, want 6", len(traces.Traces))
		} else if code, err := apiGet(key(), "/api/v1/traces/"+traces.Traces[0].TraceID, nil, nil); code != http.StatusOK {
			t.Errorf("trace from search: %v", err)
		}
	})

	t.Run("service_map", func(t *testing.T) {
		eventually(t, 4*time.Minute, 5*time.Second, "trace-linked edge frontend -> orders replaces the external address", func() error {
			var m struct {
				Nodes []struct{ ID, Type, Name string } `json:"nodes"`
				Edges []struct {
					Source, Target string
					Calls          float64 `json:"calls"`
				} `json:"edges"`
			}
			q := url.Values{"service": {orders}}
			for k, v := range rng {
				q[k] = v
			}
			if _, err := apiGet(key(), "/api/v1/apm/map", q, &m); err != nil {
				return err
			}
			fe := "service:" + frontend + "|e2e|e2e"
			or := "service:" + orders + "|e2e|e2e"
			edges := map[string]float64{}
			for _, e := range m.Edges {
				edges[e.Source+"->"+e.Target] = e.Calls
			}
			if edges[fe+"->"+or] != 40 || edges[or+"->db:postgresql/orders"] != 40 {
				return fmt.Errorf("edges %v", edges)
			}
			for _, n := range m.Nodes {
				if n.ID == "external:orders:8080" {
					return fmt.Errorf("external node not merged into the linked edge: %v", m.Nodes)
				}
			}
			return nil
		})
	})

	t.Run("host_link", func(t *testing.T) {
		var hs struct {
			Services []struct {
				Name string `json:"service_name"`
			} `json:"services"`
		}
		if _, err := apiGet(key(), "/api/v1/apm/hosts/"+targetID()+"/services", nil, &hs); err != nil {
			t.Fatal(err)
		}
		found := 0
		for _, s := range hs.Services {
			if s.Name == orders || s.Name == frontend {
				found++
			}
		}
		if found != 2 {
			t.Errorf("host services %+v", hs.Services)
		}
		var svc struct {
			Hosts []struct {
				HostID string `json:"host_id"`
				Known  bool   `json:"known"`
			} `json:"hosts"`
		}
		if _, err := apiGet(key(), "/api/v1/apm/services/"+url.PathEscape(orders), nil, &svc); err != nil {
			t.Fatal(err)
		}
		if len(svc.Hosts) != 1 || svc.Hosts[0].HostID != targetID() || svc.Hosts[0].Known != (hostErr == nil) {
			t.Errorf("service hosts %+v (host record exists: %v)", svc.Hosts, hostErr == nil)
		}
		// Application spans carry host.id but must not overwrite the infra agent's host record.
		if hostErr == nil {
			var after hostJSON
			if _, err := apiGet(key(), "/api/v1/hosts/"+targetID(), nil, &after); err != nil || after.AgentVersion != hostBefore.AgentVersion || after.OSDescription != hostBefore.OSDescription {
				t.Errorf("host record changed by app spans: before %+v after %+v (%v)", hostBefore, after, err)
			}
		}
	})

	t.Run("apdex_settings", func(t *testing.T) {
		path := "/api/v1/apm/services/" + url.PathEscape(orders) + "/settings?namespace=e2e&environment=e2e"
		// API keys are read-only.
		req, _ := http.NewRequest(http.MethodPut, apiURL(path, nil), strings.NewReader(`{"apdex_t_ms": 1}`))
		req.Header.Set("Authorization", "Bearer "+key())
		req.Header.Set("Content-Type", "application/json")
		if resp, err := httpClient.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
			t.Errorf("PUT with API key: %v %v", resp, err)
		} else {
			resp.Body.Close()
		}
		owner, password := env["OPENLOG_BOOTSTRAP_OWNER_EMAIL"], env["OPENLOG_BOOTSTRAP_OWNER_PASSWORD"]
		if owner == "" {
			t.Skip("no owner credentials in the env file")
		}
		jar, _ := cookiejar.New(nil)
		c := &http.Client{Timeout: 30 * time.Second, Jar: jar}
		lb, _ := json.Marshal(map[string]string{"email": owner, "password": password})
		resp, err := c.Post(apiURL("/api/v1/auth/login", nil), "application/json", bytes.NewReader(lb))
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("login: %v %v", resp, err)
		}
		var me struct {
			CSRF string `json:"csrf_token"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&me)
		resp.Body.Close()
		apdexFor := func(tMs int) float64 {
			r, _ := http.NewRequest(http.MethodPut, apiURL(path, nil), strings.NewReader(fmt.Sprintf(`{"apdex_t_ms": %d}`, tMs)))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-CSRF-Token", me.CSRF)
			resp, err := c.Do(r)
			if err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("PUT settings %d: %v %v", tMs, resp, err)
			}
			resp.Body.Close()
			var txs struct {
				ApdexT       float64 `json:"apdex_t_ms"`
				Transactions []apmRedJSON
			}
			if _, err := apiGet(key(), "/api/v1/apm/services/"+url.PathEscape(orders)+"/transactions", scoped(nil), &txs); err != nil || len(txs.Transactions) != 1 || txs.Transactions[0].Apdex == nil || txs.ApdexT != float64(tMs) {
				t.Fatalf("transactions after PUT %d: %+v %v", tMs, txs, err)
			}
			return *txs.Transactions[0].Apdex
		}
		// T is applied at query time: every request (16-61 ms) is frustrated at 1 ms; at 10 min all
		// non-error requests are satisfied: 32/40.
		if a := apdexFor(1); a != 0 {
			t.Errorf("apdex at T=1ms: %v, want 0", a)
		}
		if a := apdexFor(600000); math.Abs(a-0.8) > 1e-9 {
			t.Errorf("apdex at T=600000ms: %v, want 0.8", a)
		}
	})

	t.Run("tenant_isolation", func(t *testing.T) {
		if otherKey() == "" {
			t.Skip("no second organization in the env file")
		}
		var resp struct {
			Services []struct {
				Name string `json:"service_name"`
			} `json:"services"`
		}
		if _, err := apiGet(otherKey(), "/api/v1/apm/services", rng, &resp); err != nil {
			t.Fatal(err)
		}
		for _, s := range resp.Services {
			if strings.HasPrefix(s.Name, "e2e-apm-") {
				t.Errorf("other tenant sees %s", s.Name)
			}
		}
		if code, _ := apiGet(otherKey(), "/api/v1/apm/services/"+url.PathEscape(orders), nil, nil); code != http.StatusNotFound {
			t.Errorf("other tenant GET service: HTTP %d, want 404", code)
		}
	})
}
