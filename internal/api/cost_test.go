package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/onuragtas/openlog/internal/cost"
)

// costPaths are every cost endpoint, with a range so the handlers do real work.
var costPaths = []string{
	"/api/v1/costs/summary?from=1757757600000&to=1757761200000",
	"/api/v1/costs/hosts?from=1757757600000&to=1757761200000",
	"/api/v1/costs/services?from=1757757600000&to=1757761200000",
	"/api/v1/costs/containers?from=1757757600000&to=1757761200000",
	"/api/v1/costs/hosts/h1?from=1757757600000&to=1757761200000",
	"/api/v1/costs/trend?from=1757757600000&to=1757761200000",
	"/api/v1/costs/prices",
}

func costServer(t *testing.T) (*Server, *recordingConn) {
	t.Helper()
	s, conn := newTestServer(t)
	tbl, err := cost.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	conn.hostKnown = true // one host ("h1") exists in the organization
	s.SetCostPrices(tbl)
	return s, conn
}

func costGet(t *testing.T, s *Server, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("openlog-license-key", "key-a")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// Without a price table the feature is off: the routes do not exist at all, rather than
// answering with prices of zero.
func TestCostEndpointsNeedAPriceTable(t *testing.T) {
	s, _ := newTestServer(t)
	for _, p := range costPaths {
		code, body := costGet(t, s, p)
		if code != http.StatusNotFound || !strings.Contains(body, "no such endpoint") {
			t.Errorf("%s: status %d body %s", p, code, body)
		}
	}
}

func TestCostEndpointsAnswer(t *testing.T) {
	s, _ := costServer(t)
	for _, p := range costPaths {
		code, body := costGet(t, s, p)
		if code != http.StatusOK {
			t.Errorf("%s: status %d body %s", p, code, body)
		}
	}
}

// Every response must carry the caveat: the version and date of the table, and that the number
// is an estimate. A cost shown without them would read as a bill.
func TestCostResponsesCarryPricingProvenance(t *testing.T) {
	s, _ := costServer(t)
	for _, p := range costPaths[:len(costPaths)-1] { // /prices returns the table itself
		_, body := costGet(t, s, p)
		var resp struct {
			Pricing struct {
				Version   int    `json:"version"`
				Updated   string `json:"updated"`
				Currency  string `json:"currency"`
				Note      string `json:"note"`
				Estimated bool   `json:"estimated"`
			} `json:"pricing"`
		}
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		pr := resp.Pricing
		if pr.Version == 0 || pr.Updated == "" || pr.Currency != "USD" || pr.Note == "" || !pr.Estimated {
			t.Errorf("%s: pricing = %+v", p, pr)
		}
	}
}

// The summary always reports the idle share as its own number, even with no data.
func TestCostSummaryShape(t *testing.T) {
	s, _ := costServer(t)
	_, body := costGet(t, s, costPaths[0])
	var resp struct {
		Summary struct {
			Currency      string   `json:"currency"`
			Total         *float64 `json:"total"`
			Services      *float64 `json:"services"`
			Unallocated   *float64 `json:"unallocated"`
			Unattributed  *float64 `json:"unattributed"`
			Idle          *float64 `json:"idle"`
			IdleShare     *float64 `json:"idle_share"`
			PerHour       *float64 `json:"per_hour"`
			Hosts         *int     `json:"hosts"`
			PricedHosts   *int     `json:"priced_hosts"`
			UnpricedHosts *int     `json:"unpriced_hosts"`
		} `json:"summary"`
		From int64 `json:"from"`
		To   int64 `json:"to"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	sm := resp.Summary
	if sm.Total == nil || sm.Services == nil || sm.Unallocated == nil || sm.Unattributed == nil ||
		sm.Idle == nil || sm.IdleShare == nil || sm.PerHour == nil {
		t.Errorf("summary is missing a bucket: %s", body)
	}
	if sm.Hosts == nil || sm.PricedHosts == nil || sm.UnpricedHosts == nil {
		t.Errorf("summary is missing host counts: %s", body)
	}
	// The host exists but sends no metrics, so it cannot be priced — and must be counted as such.
	if *sm.Hosts != 1 || *sm.UnpricedHosts != 1 || *sm.PricedHosts != 0 {
		t.Errorf("host counts = %d/%d/%d", *sm.Hosts, *sm.PricedHosts, *sm.UnpricedHosts)
	}
	if resp.From != 1757757600000 || resp.To != 1757761200000 {
		t.Errorf("range = %d..%d", resp.From, resp.To)
	}
}

// A host of another organization must be indistinguishable from an unknown one.
func TestCostHostNotFound(t *testing.T) {
	s, conn := costServer(t)
	conn.hostKnown = false
	code, body := costGet(t, s, "/api/v1/costs/hosts/nosuchhost")
	if code != http.StatusNotFound || !strings.Contains(body, "host not found") {
		t.Errorf("status %d body %s", code, body)
	}
}

func TestCostBadRequests(t *testing.T) {
	s, _ := costServer(t)
	cases := map[string]int{
		"/api/v1/costs/summary?from=notatime":                       http.StatusBadRequest,
		"/api/v1/costs/summary?from=1757761200000&to=1757757600000": http.StatusBadRequest,
		"/api/v1/costs/hosts?limit=0":                               http.StatusBadRequest,
		"/api/v1/costs/trend?step=1ms":                              http.StatusBadRequest,
		"/api/v1/costs/trend?step=nonsense":                         http.StatusBadRequest,
		"/api/v1/costs/hosts/h1?from=notatime":                      http.StatusBadRequest,
		"/api/v1/costs/summary?from=1757757600000&to=1757761200000": http.StatusOK,
	}
	for path, want := range cases {
		if code, body := costGet(t, s, path); code != want {
			t.Errorf("%s: status %d, want %d (%s)", path, code, want, body)
		}
	}
}

// Every statement the cost endpoints run must be bound to the caller's tenant: the price of a
// machine is as confidential as the telemetry it came from.
func TestCostQueriesAreTenantScoped(t *testing.T) {
	s, conn := costServer(t)
	for _, p := range costPaths {
		costGet(t, s, p)
	}
	if len(conn.sql) == 0 {
		t.Fatal("no statements executed")
	}
	for _, sql := range conn.sql {
		refs := tableRef.FindAllStringSubmatchIndex(sql, -1)
		if len(refs) == 0 {
			t.Errorf("statement without table: %s", sql)
			continue
		}
		for _, r := range refs {
			if !strings.HasPrefix(sql[r[1]:], " WHERE (tenant_id = {tenant_id:String})") {
				t.Errorf("unscoped table reference: %s", sql)
			}
		}
	}
}

// The price table endpoint returns the effective table, so an operator can confirm an override
// took effect without reading the container's filesystem.
func TestCostPricesEndpoint(t *testing.T) {
	s, _ := costServer(t)
	code, body := costGet(t, s, "/api/v1/costs/prices")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	var tbl cost.Table
	if err := json.Unmarshal([]byte(body), &tbl); err != nil {
		t.Fatal(err)
	}
	if tbl.Version == 0 || tbl.Updated == "" || tbl.Currency != "USD" || tbl.Note == "" {
		t.Errorf("table = %+v", tbl)
	}
	if _, ok := tbl.Instances["aws"]["m5.large"]; !ok {
		t.Error("table has no aws/m5.large entry")
	}
}

func TestCostHours(t *testing.T) {
	metrics := map[string]map[string]costAgg{
		costCapacityMetric: {"": {minutes: 30}},
		costCPUCountMetric: {"": {minutes: 12}},
	}
	if got := costHours(metrics, 1); got != 0.5 {
		t.Errorf("hours = %v, want 0.5", got)
	}
	// A host cannot have reported for longer than the range asked about.
	if got := costHours(map[string]map[string]costAgg{costCapacityMetric: {"": {minutes: 600}}}, 1); got != 1 {
		t.Errorf("clamped hours = %v, want 1", got)
	}
	if got := costHours(map[string]map[string]costAgg{}, 1); got != 0 {
		t.Errorf("hours without metrics = %v", got)
	}
}

func TestCostHostUsage(t *testing.T) {
	// "1 − idle" is exact even when a mode openlog does not know is reported.
	metrics := map[string]map[string]costAgg{
		costCPUUtilMetric:  {"idle": {sum: 0.25, count: 1}, "user": {sum: 0.5, count: 1}, "steal": {sum: 0.25, count: 1}},
		costMemUsageMetric: {"used": {sum: 800, count: 1}, "free": {sum: 200, count: 1}},
	}
	cores, bytes, known := costHostUsage(metrics, 4)
	if !known || cores != 3 || bytes != 800 {
		t.Errorf("cores=%v bytes=%v known=%v, want 3 800 true", cores, bytes, known)
	}

	// Without an idle series the non-idle modes are summed.
	cores, _, _ = costHostUsage(map[string]map[string]costAgg{
		costCPUUtilMetric: {"user": {sum: 0.25, count: 1}, "system": {sum: 0.25, count: 1}},
	}, 8)
	if cores != 4 {
		t.Errorf("cores without idle = %v, want 4", cores)
	}

	// A host that sends no system metrics is not "0% busy", it is unknown.
	if _, _, known := costHostUsage(map[string]map[string]costAgg{}, 4); known {
		t.Error("usage must be unknown without metrics")
	}
}

func TestSafeRatio(t *testing.T) {
	cases := []struct{ a, b, want float64 }{
		{1, 4, 0.25}, {1, 0, 0}, {-1, 4, 0}, {0, 0, 0},
	}
	for _, c := range cases {
		if got := safeRatio(c.a, c.b); got != c.want {
			t.Errorf("safeRatio(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
