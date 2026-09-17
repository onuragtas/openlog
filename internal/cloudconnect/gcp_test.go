package cloudconnect

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The Cloud Monitoring client against an httptest fake: the signed JWT assertion exchanged for a token (once
// per poll), timeSeries pagination, the typed value shapes, and a metric type that is not enabled.

// testRSAKey generates a service account key in the PEM form a Google key file carries.
func testRSAKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

type gcpFake struct {
	mu    sync.Mutex
	calls map[string]int
	// assertions records the JWT sent to the token endpoint.
	assertions []string
	// byMetric answers a request whose filter contains the key; a service asks once per metric type, so the
	// answers are keyed by metric rather than by call order.
	byMetric map[string]gcpTimeSeriesResponse
	// nextPage answers any request that carries a pageToken.
	nextPage gcpTimeSeriesResponse
	// failMetric makes the timeSeries call for this metric type fail with 403.
	failMetric string
}

func newGCPFake() *gcpFake { return &gcpFake{calls: map[string]int{}} }

func (f *gcpFake) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[name]++
	return f.calls[name]
}

func (f *gcpFake) get(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

func (f *gcpFake) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/token"):
			f.count("token")
			_ = r.ParseForm()
			f.mu.Lock()
			f.assertions = append(f.assertions, r.Form.Get("assertion"))
			f.mu.Unlock()
			if got := r.Form.Get("grant_type"); got != gcpAssertion {
				t.Errorf("grant_type = %q, want %q", got, gcpAssertion)
			}
			_, _ = w.Write([]byte(`{"access_token":"gcp-token","expires_in":3600}`))

		case strings.HasSuffix(r.URL.Path, "/timeSeries"):
			f.count("timeSeries")
			if r.Header.Get("Authorization") != "Bearer gcp-token" {
				t.Errorf("timeSeries call without the bearer token: %q", r.Header.Get("Authorization"))
			}
			filter := r.URL.Query().Get("filter")
			if f.failMetric != "" && strings.Contains(filter, f.failMetric) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"status":"PERMISSION_DENIED","message":"metric type not enabled"}}`))
				return
			}
			f.mu.Lock()
			page := gcpTimeSeriesResponse{}
			if r.URL.Query().Get("pageToken") != "" {
				page = f.nextPage
			} else {
				for k, v := range f.byMetric {
					if strings.Contains(filter, k) {
						page = v
						break
					}
				}
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

// gcpSeries builds one Cloud SQL series with a double value.
func gcpSeries(metricType, instance string, value float64) gcpTimeSeries {
	var ts gcpTimeSeries
	ts.Metric.Type = metricType
	ts.Metric.Labels = map[string]string{}
	ts.Resource.Type = "cloudsql_database"
	ts.Resource.Labels = map[string]string{"project_id": "proj-1", "database_id": "proj-1:" + instance, "region": "europe-west1"}
	v := value
	ts.Points = append(ts.Points, struct {
		Interval struct {
			EndTime string `json:"endTime"`
		} `json:"interval"`
		Value struct {
			DoubleValue *float64 `json:"doubleValue"`
			Int64Value  *string  `json:"int64Value"`
			BoolValue   *bool    `json:"boolValue"`
		} `json:"value"`
	}{
		Interval: struct {
			EndTime string `json:"endTime"`
		}{EndTime: "2026-09-17T10:00:00Z"},
		Value: struct {
			DoubleValue *float64 `json:"doubleValue"`
			Int64Value  *string  `json:"int64Value"`
			BoolValue   *bool    `json:"boolValue"`
		}{DoubleValue: &v},
	})
	return ts
}

func collectGCP(t *testing.T, f *gcpFake, budget *Budget, service string) ([]Point, CollectResult, error) {
	t.Helper()
	srv := f.server(t)
	p := newGCP(ProviderOptions{BaseURL: srv.URL, HTTP: srv.Client()})
	svc, ok := ServiceByID(ProviderGCP, service)
	if !ok {
		t.Fatalf("unknown service %q", service)
	}
	end := time.Unix(1_700_000_400, 0).UTC()
	req := CollectRequest{Scope: "proj-1", Services: []Service{svc}, Budget: budget,
		Start: end.Add(-5 * time.Minute), End: end,
		Credentials: Credentials{ClientEmail: "sa@proj-1.iam.gserviceaccount.com", PrivateKey: testRSAKey(t)}}
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

func TestGCPCollectsAndSignsAssertion(t *testing.T) {
	f := newGCPFake()
	// cloud_sql has four metric types; only cpu/utilization answers, the rest come back empty.
	f.byMetric = map[string]gcpTimeSeriesResponse{"cpu/utilization": {TimeSeries: []gcpTimeSeries{
		gcpSeries("cloudsql.googleapis.com/database/cpu/utilization", "orders", 0.42),
		gcpSeries("cloudsql.googleapis.com/database/cpu/utilization", "catalog", 0.11),
	}}}

	points, res, err := collectGCP(t, f, NewBudget(100, 100), "cloud_sql")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.Services[0].Err != nil {
		t.Fatalf("service failed: %v", res.Services[0].Err)
	}
	if len(points) != 2 {
		t.Fatalf("collected %d points, want 2", len(points))
	}
	// One token for the poll, though the service has four metric types.
	if n := f.get("token"); n != 1 {
		t.Errorf("token requested %d times, want 1", n)
	}
	p := points[0]
	if got, want := p.Name(), "cloud.gcp.cloud_sql.cpu_utilization"; got != want {
		t.Errorf("metric name = %q, want %q", got, want)
	}
	// database_id is "project:instance"; the instance alone is the useful resource name.
	if p.ResourceName != "orders" || p.AccountID != "proj-1" {
		t.Errorf("point = %+v", p)
	}
	if p.Value != 0.42 {
		t.Errorf("value = %v", p.Value)
	}

	// The assertion is a three-part JWT whose claims name the service account and the scope.
	f.mu.Lock()
	assertion := f.assertions[0]
	f.mu.Unlock()
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		t.Fatalf("assertion is not a JWT: %q", assertion)
	}
	claims, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("claims: %v", err)
	}
	var c map[string]any
	if err := json.Unmarshal(claims, &c); err != nil {
		t.Fatalf("claims: %v", err)
	}
	if c["iss"] != "sa@proj-1.iam.gserviceaccount.com" || c["scope"] != gcpScope {
		t.Errorf("claims = %v", c)
	}
}

// timeSeries answers are paged through nextPageToken.
func TestGCPFollowsPages(t *testing.T) {
	f := newGCPFake()
	f.byMetric = map[string]gcpTimeSeriesResponse{"cpu/utilization": {
		TimeSeries:    []gcpTimeSeries{gcpSeries("cloudsql.googleapis.com/database/cpu/utilization", "orders", 0.5)},
		NextPageToken: "page-2",
	}}
	f.nextPage = gcpTimeSeriesResponse{
		TimeSeries: []gcpTimeSeries{gcpSeries("cloudsql.googleapis.com/database/cpu/utilization", "catalog", 0.6)},
	}

	points, _, err := collectGCP(t, f, NewBudget(100, 100), "cloud_sql")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("collected %d points, want 2 across both pages", len(points))
	}
}

// A metric type the project has not enabled does not lose the service's other metrics.
func TestGCPMetricFailureIsReported(t *testing.T) {
	f := newGCPFake()
	f.failMetric = "cpu/utilization"
	f.byMetric = map[string]gcpTimeSeriesResponse{"memory/utilization": {TimeSeries: []gcpTimeSeries{
		gcpSeries("cloudsql.googleapis.com/database/memory/utilization", "orders", 0.3)}}}

	points, res, err := collectGCP(t, f, NewBudget(100, 100), "cloud_sql")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.Services[0].Err == nil {
		t.Fatal("the failing metric type is not reported")
	}
	if len(points) == 0 {
		t.Fatal("the remaining metric types were not collected")
	}
	if !strings.Contains(res.Services[0].Err.Error(), "not enabled") {
		t.Errorf("error %q does not carry the provider's message", res.Services[0].Err)
	}
}

// An int64 value arrives as a string in JSON and must still become a number.
func TestGCPParsesInt64Values(t *testing.T) {
	f := newGCPFake()
	ts := gcpSeries("pubsub.googleapis.com/subscription/num_undelivered_messages", "orders", 0)
	ts.Resource.Type = "pubsub_subscription"
	ts.Resource.Labels = map[string]string{"project_id": "proj-1", "subscription_id": "orders-sub"}
	ts.Points[0].Value.DoubleValue = nil
	n := "1234"
	ts.Points[0].Value.Int64Value = &n
	f.byMetric = map[string]gcpTimeSeriesResponse{"num_undelivered_messages": {TimeSeries: []gcpTimeSeries{ts}}}

	points, _, err := collectGCP(t, f, NewBudget(100, 100), "pubsub")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(points) == 0 {
		t.Fatal("no points collected")
	}
	if points[0].Value != 1234 {
		t.Errorf("value = %v, want 1234", points[0].Value)
	}
	if points[0].ResourceName != "orders-sub" {
		t.Errorf("resource name = %q", points[0].ResourceName)
	}
}

// A malformed private key is rejected before any request is made.
func TestGCPRejectsBadPrivateKey(t *testing.T) {
	f := newGCPFake()
	srv := f.server(t)
	p := newGCP(ProviderOptions{BaseURL: srv.URL, HTTP: srv.Client()})
	err := p.Test(context.Background(), Credentials{ClientEmail: "sa@x", PrivateKey: "not a key"}, "proj-1")
	var ae *AuthError
	if err == nil || !asError(err, &ae) {
		t.Fatalf("Test = %v, want an AuthError", err)
	}
	if f.get("token") != 0 {
		t.Error("a request was made with an unusable key")
	}
}
