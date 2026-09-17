package cloudconnect

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The Azure Monitor client against an httptest fake: the client-credentials token (asked once, then cached),
// the resource listing with its nextLink pages, the per-resource metrics call, and partial failures.

type azureFake struct {
	mu    sync.Mutex
	calls map[string]int

	// resourcePages are the resource listings, returned in order; a page with a nextLink is followed.
	resourcePages []azureResourceList
	// metricValue is the average every metric answer carries.
	metricValue float64
	// failMetricsFor makes the metrics call of these resource names fail with 500.
	failMetricsFor map[string]bool
	// tokenStatus, when non-zero, is returned by the token endpoint.
	tokenStatus int
	// throttleMetricsOnce returns 429 on the first metrics call.
	throttleMetricsOnce bool
	throttled           bool
}

func newAzureFake() *azureFake {
	return &azureFake{calls: map[string]int{}, metricValue: 7.5, failMetricsFor: map[string]bool{}}
}

func (f *azureFake) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[name]++
	return f.calls[name]
}

func (f *azureFake) get(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

func (f *azureFake) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/oauth2/v2.0/token"):
			f.count("token")
			if f.tokenStatus != 0 {
				w.WriteHeader(f.tokenStatus)
				_, _ = w.Write([]byte(`{"error":{"code":"invalid_client","message":"the client secret is wrong"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"token-1","expires_in":3600}`))

		case strings.HasSuffix(r.URL.Path, "/providers/Microsoft.Insights/metrics"):
			n := f.count("metrics")
			if f.throttleMetricsOnce && n == 1 {
				f.mu.Lock()
				f.throttled = true
				f.mu.Unlock()
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			if r.Header.Get("Authorization") != "Bearer token-1" {
				t.Errorf("metrics call without the bearer token: %q", r.Header.Get("Authorization"))
			}
			name := resourceNameFromPath(r.URL.Path)
			if f.failMetricsFor[name] {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"code":"InternalError","message":"metrics unavailable"}}`))
				return
			}
			_ = json.NewEncoder(w).Encode(azureMetricsAnswer("cpu_percent", f.metricValue))

		case strings.HasSuffix(r.URL.Path, "/resources"):
			idx := f.count("resources") - 1
			page := azureResourceList{}
			f.mu.Lock()
			if idx < len(f.resourcePages) {
				page = f.resourcePages[idx]
			}
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(page)

		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// resourceNameFromPath takes the resource name out of ".../databases/<name>/providers/Microsoft.Insights/...".
// A resource id contains "providers" itself, so it is the last one that precedes the metrics segment.
func resourceNameFromPath(path string) string {
	parts := strings.Split(path, "/")
	name := ""
	for i, p := range parts {
		if p == "providers" && i > 0 {
			name = parts[i-1]
		}
	}
	return name
}

// azureMetricsAnswer builds a metrics answer with one timeseries and one point.
func azureMetricsAnswer(metric string, value float64) map[string]any {
	return map[string]any{"value": []any{map[string]any{
		"name": map[string]any{"value": metric},
		"unit": "Percent",
		"timeseries": []any{map[string]any{
			"metadatavalues": []any{},
			"data": []any{map[string]any{
				"timeStamp": "2026-09-17T10:00:00Z",
				"average":   value,
			}},
		}},
	}}}
}

func azureResources(names ...string) azureResourceList {
	out := azureResourceList{}
	for _, n := range names {
		out.Value = append(out.Value, azureResource{
			ID:   "/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.Sql/servers/srv/databases/" + n,
			Name: n, Type: "Microsoft.Sql/servers/databases", Location: "westeurope",
		})
	}
	return out
}

func collectAzure(t *testing.T, f *azureFake, budget *Budget, services ...string) ([]Point, CollectResult, error) {
	t.Helper()
	srv := f.server(t)
	p := newAzure(ProviderOptions{BaseURL: srv.URL, HTTP: srv.Client()})
	svcs := make([]Service, 0, len(services))
	for _, id := range services {
		s, ok := ServiceByID(ProviderAzure, id)
		if !ok {
			t.Fatalf("unknown service %q", id)
		}
		svcs = append(svcs, s)
	}
	end := time.Unix(1_700_000_400, 0).UTC()
	req := CollectRequest{Scope: "sub-1", Services: svcs, Budget: budget, Start: end.Add(-5 * time.Minute), End: end,
		Credentials: Credentials{TenantID: "tenant-1", ClientID: "client-1", ClientSecret: "secret"}}
	var points []Point
	res, err := p.Collect(context.Background(), req, func(pt Point) bool {
		if !budget.AddMetric() {
			return false
		}
		points = append(points, pt)
		return true
	})
	return points, res, err
}

func TestAzureCollectsAndCachesToken(t *testing.T) {
	f := newAzureFake()
	f.resourcePages = []azureResourceList{azureResources("orders", "catalog")}

	points, res, err := collectAzure(t, f, NewBudget(100, 100), "azure_sql")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("collected %d points, want one per database", len(points))
	}
	if res.Services[0].Err != nil {
		t.Fatalf("service failed: %v", res.Services[0].Err)
	}
	// One token for the whole poll, not one per resource: that is the difference between 2 and N+1 requests
	// against the identity endpoint.
	if n := f.get("token"); n != 1 {
		t.Errorf("token requested %d times, want 1", n)
	}
	if n := f.get("metrics"); n != 2 {
		t.Errorf("metrics requested %d times, want one per resource", n)
	}
	p := points[0]
	if got, want := p.Name(), "cloud.azure.azure_sql.cpu_percent"; got != want {
		t.Errorf("metric name = %q, want %q", got, want)
	}
	if p.Value != 7.5 || p.Region != "westeurope" || p.AccountID != "sub-1" {
		t.Errorf("point = %+v", p)
	}
	attrs := p.ResourceAttributes("conn-1", "Prod Azure")
	if attrs["cloud.platform"] != "azure_sql" || attrs["cloud.resource.name"] == "" ||
		!strings.HasPrefix(attrs["cloud.resource.id"], "/subscriptions/sub-1/") {
		t.Errorf("resource attributes = %v", attrs)
	}
}

// The resource listing is paged through nextLink.
func TestAzureFollowsResourcePages(t *testing.T) {
	f := newAzureFake()
	first := azureResources("orders")
	srvURL := ""
	// The nextLink is absolute; the fake serves it from the same handler.
	f.resourcePages = []azureResourceList{first, azureResources("catalog", "billing")}
	f.resourcePages[0].NextLink = "PLACEHOLDER"

	srv := f.server(t)
	srvURL = srv.URL + "/management/subscriptions/sub-1/resources?api-version=" + azureResourcesAPIVersion
	f.resourcePages[0].NextLink = srvURL

	p := newAzure(ProviderOptions{BaseURL: srv.URL, HTTP: srv.Client()})
	svc, _ := ServiceByID(ProviderAzure, "azure_sql")
	budget := NewBudget(100, 100)
	end := time.Unix(1_700_000_400, 0).UTC()
	var points []Point
	_, err := p.Collect(context.Background(), CollectRequest{Scope: "sub-1", Services: []Service{svc},
		Budget: budget, Start: end.Add(-5 * time.Minute), End: end,
		Credentials: Credentials{TenantID: "t", ClientID: "c", ClientSecret: "s"}},
		func(pt Point) bool { points = append(points, pt); return budget.AddMetric() })
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(points) != 3 {
		t.Fatalf("collected %d points, want 3 across both pages", len(points))
	}
}

// One unreadable resource is reported, not silently skipped: the run must not look complete.
func TestAzureReportsSkippedResources(t *testing.T) {
	f := newAzureFake()
	f.resourcePages = []azureResourceList{azureResources("orders", "catalog", "billing")}
	f.failMetricsFor["catalog"] = true

	points, res, err := collectAzure(t, f, NewBudget(100, 100), "azure_sql")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("collected %d points, want the two readable resources", len(points))
	}
	if res.Services[0].Err == nil {
		t.Fatal("the skipped resource is not reported")
	}
	if msg := res.Services[0].Err.Error(); !strings.Contains(msg, "1 of 3 resources") {
		t.Errorf("error %q does not say how many resources were skipped", msg)
	}
}

// A 429 is retried after the provider's Retry-After.
func TestAzureRetriesThrottling(t *testing.T) {
	f := newAzureFake()
	f.resourcePages = []azureResourceList{azureResources("orders")}
	f.throttleMetricsOnce = true

	budget := NewBudget(100, 100)
	points, _, err := collectAzure(t, f, budget, "azure_sql")
	if err != nil {
		t.Fatalf("Collect after a throttle: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("collected %d points after the retry", len(points))
	}
	if n := f.get("metrics"); n != 2 {
		t.Errorf("metrics called %d times, want 2 (one throttled, one retried)", n)
	}
	if _, _, throttled, _ := budget.Stats(); throttled != 1 {
		t.Errorf("throttles recorded = %d, want 1", throttled)
	}
}

// A rejected service principal fails the whole scope: nothing in the subscription can be read.
func TestAzureTokenFailureFailsScope(t *testing.T) {
	f := newAzureFake()
	f.tokenStatus = http.StatusUnauthorized
	f.resourcePages = []azureResourceList{azureResources("orders")}

	_, _, err := collectAzure(t, f, NewBudget(100, 100), "azure_sql")
	var ae *AuthError
	if err == nil || !asError(err, &ae) {
		t.Fatalf("Collect = %v, want an AuthError for the scope", err)
	}
	if !strings.Contains(err.Error(), "client secret") {
		t.Errorf("error %q does not carry the provider's message", err)
	}
}
