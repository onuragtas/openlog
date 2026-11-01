package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/tenant"
)

// tableRows answers a statement with rows of column values, set into whatever the reader scans them into.
type tableRows struct {
	driver.Rows
	rows [][]any
	i    int
}

func (r *tableRows) Next() bool {
	if r.i >= len(r.rows) {
		return false
	}
	r.i++
	return true
}

func (r *tableRows) Scan(dest ...any) error {
	row := r.rows[r.i-1]
	for i, d := range dest {
		if i >= len(row) || row[i] == nil {
			continue
		}
		reflect.ValueOf(d).Elem().Set(reflect.ValueOf(row[i]))
	}
	return nil
}

func (*tableRows) Close() error { return nil }
func (*tableRows) Err() error   { return nil }

// scriptedConn answers a statement by the first substring of it that it knows.
type scriptedConn struct {
	recordingConn
	answers map[string][][]any
}

func (c *scriptedConn) Query(ctx context.Context, sql string, args ...any) (driver.Rows, error) {
	if _, err := c.recordingConn.Query(ctx, sql, args...); err != nil {
		return nil, err
	}
	for part, rows := range c.answers {
		if strings.Contains(sql, part) {
			return &tableRows{rows: rows}, nil
		}
	}
	return emptyRows{}, nil
}

func scriptedServer(t *testing.T, answers map[string][][]any) (*Server, *scriptedConn) {
	t.Helper()
	conn := &scriptedConn{answers: answers}
	res, _ := tenant.ParseStatic("key-a=tenant-a")
	s := New(config.API{QueryTimeout: time.Second, MaxRows: 1000}, query.New(conn, "openlog", time.Second), auth.StaticAuthenticator{Resolver: res},
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	return s, conn
}

func podRow(uid, name string, restarts int64, created string) []any {
	now := time.Now()
	return []any{"c1", uid, "prod", "shop", name, "n1", "Deployment", "orders", "Running", "true", "", restarts, "10.0.0.1", "", created, "", "", now, now}
}

// The rows of a later page keep their usage, since it is read for the page and not for the start of the list.
func TestPodsReadUsageForThePageShown(t *testing.T) {
	now := time.Now()
	rows := [][]any{}
	metrics := [][]any{}
	for i := range 5 {
		uid := fmt.Sprintf("pu%d", i)
		rows = append(rows, podRow(uid, fmt.Sprintf("pod-%d", i), int64(i), ""))
		metrics = append(metrics, []any{"k8s.pod.cpu.usage", uid, float64(i) + 0.5, now})
	}
	s, conn := scriptedServer(t, map[string][][]any{"k8s_pods": rows, "`openlog`.metrics": metrics})
	code, body := costGet(t, s, "/api/v1/kubernetes/pods?sort=restarts&dir=asc&limit=2&offset=2")
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var got struct {
		Pods []struct {
			PodName  string   `json:"pod_name"`
			CPUUsage *float64 `json:"cpu_usage"`
		} `json:"pods"`
		Total   int  `json:"total"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 5 || !got.HasMore || len(got.Pods) != 2 || got.Pods[0].PodName != "pod-2" || got.Pods[1].PodName != "pod-3" {
		t.Fatalf("%s", body)
	}
	if got.Pods[0].CPUUsage == nil || *got.Pods[0].CPUUsage != 2.5 || got.Pods[1].CPUUsage == nil {
		t.Errorf("usage lost on a later page: %s", body)
	}
	metricReads := 0
	for _, q := range conn.sql {
		if strings.Contains(q, "`openlog`.metrics") {
			metricReads++
		}
	}
	if metricReads != 1 {
		t.Errorf("%d metric statements", metricReads)
	}
}

// A client that sends neither sort nor dir gets the list it always got, in its order and with its default limit.
func TestPodsWithoutASortKeepTheirOrder(t *testing.T) {
	rows := [][]any{podRow("pu1", "zeta", 0, ""), podRow("pu2", "Alpha", 0, ""), podRow("pu3", "beta", 0, "")}
	s, conn := scriptedServer(t, map[string][][]any{"k8s_pods": rows})
	code, body := costGet(t, s, "/api/v1/kubernetes/pods")
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	if a, b, c := strings.Index(body, "zeta"), strings.Index(body, "Alpha"), strings.Index(body, "beta"); a > b || b > c {
		t.Errorf("reordered: %s", body)
	}
	if !strings.Contains(conn.sql[0], "ORDER BY p_cluster, p_ns, p_name, pod_uid") {
		t.Errorf("%s", conn.sql[0])
	}
	if !strings.Contains(body, `"total":3`) || !strings.Contains(body, `"has_more":false`) {
		t.Errorf("%s", body)
	}
}

func TestEntityListsRefuseAnUnknownSortOrWhatIsReadAfterTheCut(t *testing.T) {
	for _, path := range []string{
		"/api/v1/kubernetes/pods?sort=cpu", "/api/v1/kubernetes/pods?sort=memory", "/api/v1/kubernetes/workloads?sort=restarts", "/api/v1/kubernetes/workloads?sort=cpu",
		"/api/v1/kubernetes/nodes?sort=pods", "/api/v1/containers?sort=cpu", "/api/v1/containers?sort=memory", "/api/v1/containers?dir=up",
		"/api/v1/kubernetes/pods?limit=0", "/api/v1/kubernetes/pods?offset=-1", "/api/v1/vulnerabilities/CVE-1?sort=nope", "/api/v1/hosts/h1/vulnerabilities?sort=nope",
	} {
		s, conn := newTestServer(t)
		conn.hostKnown = true
		if code, _ := costGet(t, s, path); code != http.StatusBadRequest {
			t.Errorf("%s: %d", path, code)
		}
	}
}

func TestEntityKeysSortAsTheyRead(t *testing.T) {
	ids := func(rows []*k8sPodJSON, l listRequest) string {
		page, _ := sortPage(rows, l, podKeys, podTie)
		out := []string{}
		for _, p := range page {
			out = append(out, p.PodUID)
		}
		return strings.Join(out, " ")
	}
	older, newer := "2026-01-01T00:00:00Z", "2026-01-01T00:00:00.5Z"
	ages := func() []*k8sPodJSON {
		return []*k8sPodJSON{{PodUID: "unknown"}, {PodUID: "new", CreatedAt: &newer}, {PodUID: "old", CreatedAt: &older}}
	}
	if got := ids(ages(), listRequest{sort: "age", limit: 10}); got != "old new unknown" {
		t.Errorf("age, largest first: %s", got)
	}
	if got := ids(ages(), listRequest{sort: "age", asc: true, limit: 10}); got != "new old unknown" {
		t.Errorf("age, smallest first: %s", got)
	}
	addrs := []*k8sPodJSON{{PodUID: "ten", PodIP: "10.0.0.10"}, {PodUID: "nine", PodIP: "10.0.0.9"}, {PodUID: "none"}}
	if got := ids(addrs, listRequest{sort: "ip", asc: true, limit: 10}); got != "nine ten none" {
		t.Errorf("ip: %s", got)
	}
	silent := []*k8sPodJSON{{PodUID: "up", Phase: "Running", Status: "Running", Reporting: true}, {PodUID: "gone", Phase: "Running", Status: "Running"}, {PodUID: "done", Phase: "Succeeded", Status: "Succeeded"}}
	if got := ids(silent, listRequest{sort: "status", asc: true, limit: 10}); got != "gone up done" {
		t.Errorf("status: %s", got)
	}
	cs := []*containerJSON{{ContainerID: "a", State: "running", Reporting: true}, {ContainerID: "b", State: "running"}, {ContainerID: "c", State: "running", Health: "unhealthy", Reporting: true}}
	page, _ := sortPage(cs, listRequest{sort: "state", asc: true, limit: 10}, containerKeys, containerTie)
	if page[0].ContainerID != "b" || page[1].ContainerID != "a" || page[2].ContainerID != "c" {
		t.Errorf("state: %+v %+v %+v", page[0], page[1], page[2])
	}
	wls := []*k8sWorkloadJSON{{Name: "fine", Health: "healthy"}, {Name: "worst", Health: "unavailable"}, {Name: "bad", Health: "degraded"}}
	wpage, _ := sortPage(wls, listRequest{sort: "health", limit: 10}, workloadKeys, workloadTie)
	if wpage[0].Name != "worst" || wpage[1].Name != "bad" || wpage[2].Name != "fine" {
		t.Errorf("health: %s %s %s", wpage[0].Name, wpage[1].Name, wpage[2].Name)
	}
}

func TestAdvisoryPastTheLastPageIsNotNotFound(t *testing.T) {
	s, conn := scriptedServer(t, map[string][][]any{"countIf": {{"CVE-1", "critical", 9.8, "the summary", uint64(3), uint64(1)}}})
	code, body := costGet(t, s, "/api/v1/vulnerabilities/CVE-1?sort=package&limit=25&offset=1000")
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	for _, want := range []string{`"findings":[]`, `"has_more":false`, `"severity":"critical"`, `"without_fix":1`} {
		if !strings.Contains(body, want) {
			t.Errorf("no %s in %s", want, body)
		}
	}
	found := false
	for _, q := range conn.sql {
		if strings.Contains(q, "ORDER BY pkg ASC, pkg, vid, hid") && strings.Contains(q, "LIMIT 26") && strings.Contains(q, "OFFSET 1000") {
			found = true
		}
	}
	if !found {
		t.Errorf("%q", conn.sql)
	}
}

func TestAdvisoryWithNoFindingsIsNotFound(t *testing.T) {
	// What an aggregate over nothing answers: one row of zeroes.
	s, _ := scriptedServer(t, map[string][][]any{"countIf": {{"", "", 0.0, "", uint64(0), uint64(0)}}})
	if code, body := costGet(t, s, "/api/v1/vulnerabilities/CVE-1"); code != http.StatusNotFound {
		t.Errorf("%d %s", code, body)
	}
	s, _ = newTestServer(t)
	if code, body := costGet(t, s, "/api/v1/vulnerabilities/CVE-1"); code != http.StatusNotFound {
		t.Errorf("%d %s", code, body)
	}
}

func TestFindingsKeepTheirOrderWithoutASort(t *testing.T) {
	s, conn := newTestServer(t)
	conn.hostKnown = true
	if code, body := costGet(t, s, "/api/v1/hosts/h1/vulnerabilities"); code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	found := false
	for _, q := range conn.sql {
		if strings.Contains(q, "ORDER BY multiIf(sev = 'critical', 0, sev = 'high', 1, sev = 'medium', 2, sev = 'low', 3, 4) ASC, pkg, vid, hid") && strings.Contains(q, "LIMIT 1001") {
			found = true
		}
	}
	if !found {
		t.Errorf("%q", conn.sql)
	}
}

func TestInventorySearchSaysWhenItWasCut(t *testing.T) {
	rows := [][]any{{"h1", "discovered_service", "a", "{}"}, {"h2", "discovered_service", "b", "{}"}, {"h3", "discovered_service", "c", "{}"}}
	for path, want := range map[string]string{
		"/api/v1/inventory/search?category=discovered_service&limit=2": `"has_more":true`,
		"/api/v1/inventory/search?category=discovered_service&limit=3": `"has_more":false`,
	} {
		s, _ := scriptedServer(t, map[string][][]any{"inventory_items": rows})
		code, body := costGet(t, s, path)
		if code != http.StatusOK || !strings.Contains(body, want) {
			t.Errorf("%s: %d %s", path, code, body)
		}
		if strings.HasSuffix(path, "limit=2") && strings.Contains(body, `"h3"`) {
			t.Errorf("the row past the limit was answered: %s", body)
		}
	}
}
