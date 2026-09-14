//go:build apmga

// ClickHouse integration tests of the APM GA API: logs cursor pagination (ties in the same nanosecond and fully
// identical rows), deployments, the error inbox with affected dimensions and the map path. See
// internal/apm/apmga_integration_test.go for the environment.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/migrate"
	"github.com/onuragtas/openlog/internal/store/clickhouse"
	"github.com/onuragtas/openlog/internal/tenant"
	"github.com/onuragtas/openlog/schema"
)

func gaServer(t *testing.T) (*Server, clickhouse.Conn, string) {
	t.Helper()
	addr := os.Getenv("OPENLOG_APMGA_CLICKHOUSE_ADDR")
	if addr == "" {
		addr = "127.0.0.1:19001"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	conn, err := clickhouse.OpenRetry(ctx, clickhouse.Options{Addr: []string{addr}, Database: "default", User: "openlog", Password: "openlog"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	ms, err := migrate.Load(schema.ClickHouse, "clickhouse", "openlog")
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Run(ctx, conn, ms, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	tenantID := fmt.Sprintf("t-apmga-api-%d", time.Now().UnixNano())
	res, err := tenant.ParseStatic("key-ga=" + tenantID)
	if err != nil {
		t.Fatal(err)
	}
	s := New(config.API{QueryTimeout: 30 * time.Second, MaxRows: 5000}, query.New(conn, "openlog", 30*time.Second), auth.StaticAuthenticator{Resolver: res},
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	return s, conn, tenantID
}

func gaGet(t *testing.T, s *Server, path string, out any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("openlog-license-key", "key-ga")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatal(err)
	}
}

func gaExec(t *testing.T, conn clickhouse.Conn, sql string, args ...any) {
	t.Helper()
	if err := conn.Exec(context.Background(), fmt.Sprintf(sql, args...)); err != nil {
		t.Fatalf("%v\n%s", err, fmt.Sprintf(sql, args...))
	}
}

func TestAPMGALogsCursor(t *testing.T) {
	s, conn, tenantID := gaServer(t)
	base := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	type row struct {
		ns   int64
		body string
	}
	var rows []row
	// 10 rows in one nanosecond (4 of them fully identical), 6 in another, and singles.
	for i := 0; i < 10; i++ {
		body := fmt.Sprintf("tie-%d", i)
		if i < 4 {
			body = "dup"
		}
		rows = append(rows, row{base.UnixNano() + 7, body})
	}
	for i := 0; i < 6; i++ {
		rows = append(rows, row{base.UnixNano() + 3, fmt.Sprintf("tie2-%d", i)})
	}
	for i := 0; i < 9; i++ {
		rows = append(rows, row{base.UnixNano() - int64(i+1)*1000, fmt.Sprintf("single-%d", i)})
	}
	var values []string
	for _, r := range rows {
		values = append(values, fmt.Sprintf("('%s', fromUnixTimestamp64Nano(%d), fromUnixTimestamp64Nano(%d), 'api', 'h1', 'INFO', 9, '', '', '%s', map(), map())",
			tenantID, r.ns, base.UnixNano(), r.body))
	}
	gaExec(t, conn, `INSERT INTO openlog.logs_local (tenant_id, timestamp, observed_timestamp, service_name, host_id, severity_text, severity_number,
		trace_id, span_id, body, attributes, resource_attributes) VALUES %s`, strings.Join(values, ","))

	from, to := base.Add(-time.Minute).UnixMilli(), base.Add(time.Minute).UnixMilli()
	for _, limit := range []int{1, 3, 4, 7, 25, 100} {
		var got []string
		cursor := ""
		for page := 0; ; page++ {
			if page > 40 {
				t.Fatalf("limit %d: no end", limit)
			}
			path := fmt.Sprintf("/api/v1/logs?from=%d&to=%d&limit=%d", from, to, limit)
			if cursor != "" {
				path += "&cursor=" + url.QueryEscape(cursor)
			}
			var resp struct {
				Logs []struct {
					Timestamp string `json:"timestamp"`
					Body      string `json:"body"`
				} `json:"logs"`
				NextCursor *string `json:"next_cursor"`
			}
			gaGet(t, s, path, &resp)
			if len(resp.Logs) > limit {
				t.Fatalf("limit %d: page of %d", limit, len(resp.Logs))
			}
			for _, l := range resp.Logs {
				got = append(got, l.Body)
			}
			if resp.NextCursor == nil {
				break
			}
			cursor = *resp.NextCursor
		}
		want := make([]string, len(rows))
		for i, r := range rows {
			want[i] = r.body
		}
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("limit %d: rows %v, want %v", limit, got, want)
		}
	}
}

func TestAPMGADeploymentsInboxAndPath(t *testing.T) {
	s, conn, tenantID := gaServer(t)
	now := time.Now().UTC().Truncate(time.Minute)
	span := func(ts time.Time, trace, spanID, parent, svc, kind, version, peerType, peerName, txn string, entry bool, group uint64) string {
		errType, isErr := "", "false"
		if group != 0 {
			errType, isErr = "Timeout", "true"
		}
		return fmt.Sprintf("('%s', fromUnixTimestamp64Nano(%d), 2000000, '%s', '%s', '%s', '%s', '%s', 'unset', '%s', 'shop', 'prod', 'h-%s', map('service.version', '%s'), %v, 'web', '%s', %s, 1, '%s', '%s', %d, '%s', 'db <n>')",
			tenantID, ts.UnixNano(), trace, spanID, parent, txn, kind, svc, svc, version, entry, txn, isErr, peerType, peerName, group, errType)
	}
	var values []string
	for m := 120; m >= 1; m-- {
		version := "1.0"
		if m <= 60 {
			version = "1.1"
		}
		ts := now.Add(-time.Duration(m) * time.Minute)
		trace := fmt.Sprintf("%032x", m)
		values = append(values,
			span(ts, trace, "000000000000000a", "", "frontend", "server", "7", "", "", "GET /checkout", true, 0),
			span(ts.Add(time.Millisecond), trace, "000000000000000b", "000000000000000a", "frontend", "client", "7", "external", "orders:8080", "GET /checkout", false, 0),
			span(ts.Add(2*time.Millisecond), trace, "000000000000000c", "000000000000000b", "orders", "server", version, "", "", "GET /orders/{id}", true, 0),
			span(ts.Add(3*time.Millisecond), trace, "000000000000000d", "000000000000000c", "orders", "client", version, "db", "postgresql/orders", "GET /orders/{id}", false, 0))
		if m <= 30 {
			values = append(values, span(ts.Add(4*time.Millisecond), trace, "000000000000000e", "000000000000000c", "orders", "internal", version, "", "", "GET /orders/{id}", false, 0x5a1f))
		}
	}
	gaExec(t, conn, `INSERT INTO openlog.spans_local (tenant_id, timestamp, duration_ns, trace_id, span_id, parent_span_id, name, kind, status_code,
		service_name, service_namespace, deployment_environment, host_id, resource_attributes, is_entry, transaction_type, transaction_name,
		is_error, sample_weight, peer_type, peer_name, error_group_id, error_type, error_message) VALUES %s`, strings.Join(values, ","))
	gaExec(t, conn, `INSERT INTO openlog.logs_local (tenant_id, timestamp, observed_timestamp, service_name, host_id, severity_text, severity_number,
		trace_id, span_id, body, attributes, resource_attributes) VALUES ('%s', fromUnixTimestamp64Nano(%d), now64(9), 'orders', 'h-orders', 'ERROR', 17, '%032x', '000000000000000c', 'boom', map(), map())`,
		tenantID, now.Add(-10*time.Minute).UnixNano(), 10)

	rng := fmt.Sprintf("from=%d&to=%d", now.Add(-3*time.Hour).UnixMilli(), now.UnixMilli())

	var deps struct {
		Deployments []deploymentJSON `json:"deployments"`
	}
	gaGet(t, s, "/api/v1/apm/services/orders/deployments?"+fmt.Sprintf("from=%d&to=%d", now.Add(-100*time.Minute).UnixMilli(), now.UnixMilli()), &deps)
	if len(deps.Deployments) != 1 || deps.Deployments[0].Version != "1.1" || deps.Deployments[0].PreviousVersion != "1.0" ||
		deps.Deployments[0].T != now.Add(-60*time.Minute).UnixMilli() {
		t.Errorf("deployments %+v", deps.Deployments)
	}
	var cmp struct {
		Before struct{ Requests float64 } `json:"before"`
		After  struct{ Requests float64 } `json:"after"`
	}
	gaGet(t, s, fmt.Sprintf("/api/v1/apm/services/orders/deployments/compare?at=%d&window=30m", now.Add(-60*time.Minute).UnixMilli()), &cmp)
	if cmp.Before.Requests != 30 || cmp.After.Requests != 30 {
		t.Errorf("compare %+v", cmp)
	}

	var inbox struct {
		Groups []struct {
			GroupID string  `json:"group_id"`
			Count   float64 `json:"count"`
			Status  string  `json:"status"`
		} `json:"groups"`
		Counts   map[string]int `json:"counts"`
		Workflow bool           `json:"workflow"`
	}
	gaGet(t, s, "/api/v1/apm/errors?status=unresolved&"+rng, &inbox)
	if len(inbox.Groups) != 1 || inbox.Groups[0].GroupID != "0000000000005a1f" || inbox.Groups[0].Count != 30 || inbox.Groups[0].Status != "unresolved" ||
		inbox.Counts["unresolved"] != 1 || inbox.Workflow {
		t.Errorf("inbox %+v", inbox)
	}
	var detail struct {
		Affected map[string][]affectedJSON `json:"affected"`
		Samples  []struct {
			Version string `json:"version"`
		} `json:"samples"`
	}
	gaGet(t, s, "/api/v1/apm/services/orders/errors/0000000000005a1f?"+rng, &detail)
	if v := detail.Affected["versions"]; len(v) != 1 || v[0].Value != "1.1" || v[0].Count != 30 {
		t.Errorf("affected versions %+v", v)
	}
	if h := detail.Affected["hosts"]; len(h) != 1 || h[0].Value != "h-orders" {
		t.Errorf("affected hosts %+v", h)
	}
	if tx := detail.Affected["transactions"]; len(tx) != 1 || tx[0].Value != "GET /orders/{id}" || len(detail.Samples) == 0 || detail.Samples[0].Version != "1.1" {
		t.Errorf("affected transactions %+v samples %+v", tx, detail.Samples)
	}

	var path struct {
		TraceCount int      `json:"trace_count"`
		Nodes      []string `json:"nodes"`
		Edges      []string `json:"edges"`
	}
	gaGet(t, s, "/api/v1/apm/map/path?service=frontend&transaction="+url.QueryEscape("GET /checkout")+"&"+rng, &path)
	fe, or := "service:frontend|shop|prod", "service:orders|shop|prod"
	wantEdges := []string{fe + "->external:orders:8080", fe + "->" + or, or + "->db:postgresql/orders"}
	sort.Strings(wantEdges)
	if path.TraceCount != 50 || strings.Join(path.Edges, ",") != strings.Join(wantEdges, ",") {
		t.Errorf("path %+v", path)
	}

	var logs struct {
		Logs []struct {
			Body string `json:"body"`
		} `json:"logs"`
	}
	gaGet(t, s, "/api/v1/logs?transaction="+url.QueryEscape("GET /checkout")+"&transaction_service=frontend&span_id=000000000000000C&"+rng, &logs)
	if len(logs.Logs) != 1 || logs.Logs[0].Body != "boom" {
		t.Errorf("transaction logs %+v", logs)
	}

	var m ApmMapResponseForTest
	gaGet(t, s, "/api/v1/apm/map?environment=prod&"+rng, &m)
	for _, n := range m.Nodes {
		if n.ID == or && n.HostCount != 1 {
			t.Errorf("orders node host_count %+v", n)
		}
	}
}

type ApmMapResponseForTest struct {
	Nodes []mapNodeJSON `json:"nodes"`
}
