package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/onuragtas/openlog/terraform/internal/client"
)

// Acceptance tests run the real plugin protocol — plan, apply, refresh, import — against a fake openlog API
// instead of a live one, so they exercise the resources without a stack and without touching anyone's data.
// resource.Test skips unless TF_ACC is set and a terraform binary is on PATH, which keeps `go test ./...`
// dependency-free; the fake below is what makes running them cheap when it is set.

var testProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"openlog": providerserver.NewProtocol6WithError(New("test")()),
}

// fakeAPI is an in-memory openlog API: the endpoints the provider uses, with the behaviour the provider
// depends on — assigned ids, server-side defaulting of conditions, a version that rises on every write and is
// checked on update.
type fakeAPI struct {
	mu    sync.Mutex
	seq   int
	rules map[string]*client.AlertRule
	slos  map[string]*client.SLO
}

func newFakeAPI(t *testing.T) *httptest.Server {
	t.Helper()
	f := &fakeAPI{rules: map[string]*client.AlertRule{}, slos: map[string]*client.SLO{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/alerts/rules", f.createRule)
	mux.HandleFunc("GET /api/v1/alerts/rules/{id}", f.getRule)
	mux.HandleFunc("PUT /api/v1/alerts/rules/{id}", f.updateRule)
	mux.HandleFunc("DELETE /api/v1/alerts/rules/{id}", f.deleteRule)
	mux.HandleFunc("POST /api/v1/slos", f.createSLO)
	mux.HandleFunc("GET /api/v1/slos/{id}", f.getSLO)
	mux.HandleFunc("PUT /api/v1/slos/{id}", f.updateSLO)
	mux.HandleFunc("DELETE /api/v1/slos/{id}", f.deleteSLO)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Every request must carry the documented credential; a test that loses it should fail loudly.
		if r.Header.Get("Authorization") != "Bearer ola_test" {
			writeAPIError(w, http.StatusUnauthorized, "unauthenticated", "missing or wrong API key")
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeAPIJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeAPI) nextID(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s-%08d-0000-4000-8000-000000000000", prefix, f.seq)
}

func (f *fakeAPI) createRule(w http.ResponseWriter, r *http.Request) {
	var in client.AlertRule
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON body")
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_argument", "name: must be 1-200 characters")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	in.ID = f.nextID("aaaaaaaa")
	in.Version = 1
	f.normalizeRule(&in)
	f.rules[in.ID] = &in
	writeAPIJSON(w, http.StatusCreated, in)
}

// normalizeRule fills in what the real API defaults, so the tests see the same "server adds fields" behaviour
// the condition handling is built for.
func (f *fakeAPI) normalizeRule(x *client.AlertRule) {
	if x.Severity == "" {
		x.Severity = "warning"
	}
	if x.IntervalSeconds == 0 {
		x.IntervalSeconds = 60
	}
	if x.Flapping == nil {
		x.Flapping = &client.Flapping{Enabled: true, Transitions: 4, WindowSeconds: 3600, HoldSeconds: 600}
	}
	if x.ChannelIDs == nil {
		x.ChannelIDs = []string{}
	}
	if x.Labels == nil {
		x.Labels = map[string]string{}
	}
	cond := map[string]any{}
	_ = json.Unmarshal(x.Condition, &cond)
	if x.Type == "metric_threshold" {
		for k, v := range map[string]any{"aggregation": "avg", "window_seconds": float64(300), "missing_data": "keep"} {
			if _, ok := cond[k]; !ok {
				cond[k] = v
			}
		}
	}
	x.Condition, _ = json.Marshal(cond)
	x.CreatedByEmail = "terraform@example.com"
	x.CreatedAt = "2026-09-17T00:00:00.000000000Z"
	x.UpdatedAt = "2026-09-17T00:00:00.000000000Z"
}

func (f *fakeAPI) getRule(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	x, ok := f.rules[r.PathValue("id")]
	if !ok {
		writeAPIError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	writeAPIJSON(w, http.StatusOK, x)
}

func (f *fakeAPI) updateRule(w http.ResponseWriter, r *http.Request) {
	var in client.AlertRule
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON body")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	id := r.PathValue("id")
	old, ok := f.rules[id]
	if !ok {
		writeAPIError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	// Optimistic concurrency, as the real API does it.
	if in.Version != 0 && in.Version != old.Version {
		writeAPIError(w, http.StatusConflict, "failed_precondition", "conflicting change; reload and try again")
		return
	}
	in.ID, in.Version = id, old.Version+1
	f.normalizeRule(&in)
	f.rules[id] = &in
	writeAPIJSON(w, http.StatusOK, in)
}

func (f *fakeAPI) deleteRule(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rules, r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeAPI) createSLO(w http.ResponseWriter, r *http.Request) {
	var in client.SLO
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON body")
		return
	}
	if in.SLIType == "availability" && in.LatencyThresholdMs != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_argument", "latency_threshold_ms: only applies to a latency SLI")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	in.ID = f.nextID("bbbbbbbb")
	f.stampSLO(&in)
	f.slos[in.ID] = &in
	writeAPIJSON(w, http.StatusCreated, in)
}

func (f *fakeAPI) stampSLO(x *client.SLO) {
	x.CreatedByEmail = "terraform@example.com"
	x.UpdatedByEmail = "terraform@example.com"
	x.CreatedAt = "2026-09-17T00:00:00.000000000Z"
	x.UpdatedAt = "2026-09-17T00:00:00.000000000Z"
}

func (f *fakeAPI) getSLO(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	x, ok := f.slos[r.PathValue("id")]
	if !ok {
		writeAPIError(w, http.StatusNotFound, "not_found", "slo not found")
		return
	}
	writeAPIJSON(w, http.StatusOK, x)
}

func (f *fakeAPI) updateSLO(w http.ResponseWriter, r *http.Request) {
	var in client.SLO
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON body")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	id := r.PathValue("id")
	if _, ok := f.slos[id]; !ok {
		writeAPIError(w, http.StatusNotFound, "not_found", "slo not found")
		return
	}
	in.ID = id
	f.stampSLO(&in)
	f.slos[id] = &in
	writeAPIJSON(w, http.StatusOK, in)
}

func (f *fakeAPI) deleteSLO(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.slos, r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

// setupAcc points the provider at the fake API through the environment, as an operator would.
func setupAcc(t *testing.T) {
	t.Helper()
	srv := newFakeAPI(t)
	t.Setenv("OPENLOG_ENDPOINT", srv.URL)
	t.Setenv("OPENLOG_API_KEY", "ola_test")
	t.Setenv("OPENLOG_ORG_ID", "")
}

const accProviderConfig = `
provider "openlog" {}
`

func TestAccAlertRule(t *testing.T) {
	setupAcc(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				// Create: a short condition, which the API answers with a defaulted one.
				Config: accProviderConfig + `
resource "openlog_alert_rule" "cpu" {
  name     = "Host CPU high"
  type     = "metric_threshold"
  severity = "critical"

  condition = jsonencode({
    metric    = "system.cpu.utilization"
    operator  = "gt"
    threshold = 0.9
  })
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("openlog_alert_rule.cpu", "id"),
					resource.TestCheckResourceAttr("openlog_alert_rule.cpu", "version", "1"),
					resource.TestCheckResourceAttr("openlog_alert_rule.cpu", "severity", "critical"),
					// openlog picked the interval of the rule type.
					resource.TestCheckResourceAttr("openlog_alert_rule.cpu", "interval_seconds", "60"),
					// The defaults it filled in are visible, without changing the configured condition.
					resource.TestMatchResourceAttr("openlog_alert_rule.cpu", "condition_effective",
						regexp.MustCompile(`"window_seconds":\s*300`)),
					resource.TestCheckResourceAttr("openlog_alert_rule.cpu", "flapping.enabled", "true"),
					resource.TestCheckResourceAttr("openlog_alert_rule.cpu", "flapping.transitions", "4"),
				),
			},
			{
				// The plan right after apply must be empty: this is what the condition handling is for.
				Config: accProviderConfig + `
resource "openlog_alert_rule" "cpu" {
  name     = "Host CPU high"
  type     = "metric_threshold"
  severity = "critical"

  condition = jsonencode({
    metric    = "system.cpu.utilization"
    operator  = "gt"
    threshold = 0.9
  })
}
`,
				PlanOnly: true,
			},
			{
				// Update: the version rises and the change is applied.
				Config: accProviderConfig + `
resource "openlog_alert_rule" "cpu" {
  name        = "Host CPU high"
  description = "now with a runbook"
  type        = "metric_threshold"
  severity    = "warning"
  runbook_url = "https://runbooks.example.com/cpu"

  condition = jsonencode({
    metric    = "system.cpu.utilization"
    operator  = "gt"
    threshold = 0.95
  })
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("openlog_alert_rule.cpu", "version", "2"),
					resource.TestCheckResourceAttr("openlog_alert_rule.cpu", "severity", "warning"),
					resource.TestCheckResourceAttr("openlog_alert_rule.cpu", "runbook_url", "https://runbooks.example.com/cpu"),
				),
			},
			{
				ResourceName:      "openlog_alert_rule.cpu",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func TestAccSLO(t *testing.T) {
	setupAcc(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: accProviderConfig + `
resource "openlog_slo" "checkout" {
  name         = "Checkout availability"
  service_name = "checkout"
  environment  = "prod"
  sli_type     = "availability"
  objective    = 99.9
  window_days  = 28
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("openlog_slo.checkout", "id"),
					resource.TestCheckResourceAttr("openlog_slo.checkout", "objective", "99.9"),
					resource.TestCheckResourceAttr("openlog_slo.checkout", "environment", "prod"),
					// Not configured, so it must stay absent rather than become "".
					resource.TestCheckNoResourceAttr("openlog_slo.checkout", "service_namespace"),
					resource.TestCheckNoResourceAttr("openlog_slo.checkout", "latency_threshold_ms"),
				),
			},
			{
				ResourceName:      "openlog_slo.checkout",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// An objective of 100 leaves no error budget; the schema rejects it before anything is sent.
func TestAccSLORejectsFullObjective(t *testing.T) {
	setupAcc(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: accProviderConfig + `
resource "openlog_slo" "impossible" {
  name         = "No failures at all"
  service_name = "checkout"
  sli_type     = "availability"
  objective    = 100
  window_days  = 7
}
`,
				ExpectError: regexp.MustCompile(`Invalid objective`),
			},
		},
	})
}

// A latency threshold on an availability SLO is rejected by the API, and the field path must reach the user.
func TestAccSLOSurfacesFieldErrors(t *testing.T) {
	setupAcc(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: accProviderConfig + `
resource "openlog_slo" "wrong" {
  name                 = "Checkout latency"
  service_name         = "checkout"
  sli_type             = "availability"
  latency_threshold_ms = 300
  objective            = 99.9
  window_days          = 7
}
`,
				ExpectError: regexp.MustCompile(`latency_threshold_ms: only applies to a latency SLI`),
			},
		},
	})
}
