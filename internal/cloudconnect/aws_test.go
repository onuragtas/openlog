package cloudconnect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The CloudWatch client against an httptest fake: the request shape (signed, JSON protocol, X-Amz-Target),
// ListMetrics pagination, GetMetricData batching at the API's 500-query limit, throttling with backoff, and
// what happens when one service fails or a cap is reached.

// awsFake records the calls and answers ListMetrics / GetMetricData / STS.
type awsFake struct {
	mu sync.Mutex
	// calls counts requests per X-Amz-Target (and "sts").
	calls map[string]int
	// queries records the MetricDataQueries length of every GetMetricData call.
	queries []int
	// auth records the Authorization header of every signed call.
	auth []string

	// listPages are the ListMetrics answers, returned in order; a page with a NextToken is followed.
	listPages []awsListMetricsResponse
	// values is the value every GetMetricData result carries.
	values []float64
	// status, when set for a target, is returned instead of a success answer on the nth call.
	failTarget string
	failTimes  int
	failStatus int
	failBody   string
}

func newAWSFake() *awsFake {
	return &awsFake{calls: map[string]int{}, values: []float64{42}}
}

func (f *awsFake) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[name]++
	return f.calls[name]
}

func (f *awsFake) get(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

func (f *awsFake) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.mu.Unlock()

		if strings.HasSuffix(r.URL.Path, "/sts") {
			f.count("sts")
			_, _ = w.Write([]byte(`{"GetCallerIdentityResponse":{"GetCallerIdentityResult":{"Account":"123456789012"}}}`))
			return
		}
		target := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), awsTargetPrefix)
		n := f.count(target)
		if f.failTarget == target && n <= f.failTimes {
			w.WriteHeader(f.failStatus)
			_, _ = w.Write([]byte(f.failBody))
			return
		}
		switch target {
		case "ListMetrics":
			f.mu.Lock()
			page := awsListMetricsResponse{}
			if idx := f.calls["ListMetrics"] - 1; idx < len(f.listPages) {
				page = f.listPages[idx]
			}
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(page)
		case "GetMetricData":
			var req awsGetMetricDataRequest
			_ = json.Unmarshal(body, &req)
			f.mu.Lock()
			f.queries = append(f.queries, len(req.MetricDataQueries))
			values := f.values
			f.mu.Unlock()
			res := awsGetMetricDataResponse{}
			for _, q := range req.MetricDataQueries {
				r := awsMetricDataResult{ID: q.ID, StatusCode: "Complete"}
				for i, v := range values {
					r.Timestamps = append(r.Timestamps, float64(1_700_000_000+i*60))
					r.Values = append(r.Values, v)
				}
				res.MetricDataResults = append(res.MetricDataResults, r)
			}
			_ = json.NewEncoder(w).Encode(res)
		default:
			t.Errorf("unexpected X-Amz-Target %q", target)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// awsMetrics builds n RDS metric entries, each on its own instance.
func awsMetrics(metric string, n int) []awsMetric {
	out := make([]awsMetric, 0, n)
	for i := range n {
		out = append(out, awsMetric{Namespace: "AWS/RDS", MetricName: metric,
			Dimensions: []awsDimension{{Name: "DBInstanceIdentifier", Value: fmt.Sprintf("db-%d", i)}}})
	}
	return out
}

func awsRequest(t *testing.T, scope string, budget *Budget, services ...string) CollectRequest {
	t.Helper()
	svcs := make([]Service, 0, len(services))
	for _, id := range services {
		s, ok := ServiceByID(ProviderAWS, id)
		if !ok {
			t.Fatalf("unknown service %q", id)
		}
		svcs = append(svcs, s)
	}
	end := time.Unix(1_700_000_400, 0).UTC()
	return CollectRequest{Scope: scope, Services: svcs, Budget: budget, Start: end.Add(-5 * time.Minute), End: end,
		Credentials: Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret"}}
}

func collectAWS(t *testing.T, f *awsFake, req CollectRequest) ([]Point, CollectResult, error) {
	t.Helper()
	srv := f.server(t)
	p := newAWS(ProviderOptions{BaseURL: srv.URL, HTTP: srv.Client(), Now: func() time.Time { return time.Unix(1_700_000_400, 0) }})
	var points []Point
	res, err := p.Collect(context.Background(), req, func(pt Point) bool {
		if !req.Budget.AddMetric() {
			return false
		}
		points = append(points, pt)
		return true
	})
	return points, res, err
}

func TestAWSCollectsAndMapsAttributes(t *testing.T) {
	f := newAWSFake()
	f.listPages = []awsListMetricsResponse{{Metrics: awsMetrics("CPUUtilization", 2)}}
	f.values = []float64{12.5}

	points, res, err := collectAWS(t, f, awsRequest(t, "eu-central-1", NewBudget(100, 100), "rds"))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("collected %d points, want 2", len(points))
	}
	if len(res.Services) != 1 || res.Services[0].Err != nil || res.Services[0].Metrics != 2 {
		t.Fatalf("service result = %+v", res.Services)
	}
	p := points[0]
	if got, want := p.Name(), "cloud.aws.rds.cpu_utilization"; got != want {
		t.Errorf("metric name = %q, want %q", got, want)
	}
	if p.Value != 12.5 || p.Region != "eu-central-1" {
		t.Errorf("point = %+v", p)
	}
	// The account id comes from the one STS call, not from a CloudWatch answer.
	if p.AccountID != "123456789012" {
		t.Errorf("account id = %q", p.AccountID)
	}
	res1 := p.ResourceAttributes("conn-1", "Prod AWS")
	if res1["cloud.provider"] != "aws" || res1["cloud.platform"] != "aws_rds" ||
		res1["cloud.resource.name"] != "db-0" || res1["cloud.region"] != "eu-central-1" {
		t.Errorf("resource attributes = %v", res1)
	}
	if attrs := p.Attributes(); attrs["cloud.metric.name"] != "CPUUtilization" || attrs["cloud.metric.stat"] != "Average" {
		t.Errorf("attributes = %v", attrs)
	}
	// Every request is signed with the monitoring credential scope.
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.auth {
		if !strings.Contains(a, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/") {
			t.Fatalf("request was not signed: %q", a)
		}
	}
}

// ListMetrics is paged; every page's metrics are read.
func TestAWSFollowsListMetricsPages(t *testing.T) {
	f := newAWSFake()
	f.listPages = []awsListMetricsResponse{
		{Metrics: awsMetrics("CPUUtilization", 2), NextToken: "page-2"},
		{Metrics: awsMetrics("DatabaseConnections", 3)},
	}
	points, _, err := collectAWS(t, f, awsRequest(t, "eu-west-1", NewBudget(100, 100), "rds"))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(points) != 5 {
		t.Fatalf("collected %d points, want 5 (2 + 3 across both pages)", len(points))
	}
	if n := f.get("ListMetrics"); n != 2 {
		t.Errorf("ListMetrics called %d times, want 2", n)
	}
}

// GetMetricData takes at most awsMaxQueries queries per request.
func TestAWSBatchesGetMetricData(t *testing.T) {
	f := newAWSFake()
	f.listPages = []awsListMetricsResponse{{Metrics: awsMetrics("CPUUtilization", awsMaxQueries+120)}}
	points, _, err := collectAWS(t, f, awsRequest(t, "us-east-1", NewBudget(10000, 100), "rds"))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(points) != awsMaxQueries+120 {
		t.Fatalf("collected %d points", len(points))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queries) != 2 {
		t.Fatalf("GetMetricData called %d times, want 2", len(f.queries))
	}
	if f.queries[0] != awsMaxQueries || f.queries[1] != 120 {
		t.Errorf("batch sizes %v, want [%d 120]", f.queries, awsMaxQueries)
	}
}

// A throttled request is retried after a backoff and then succeeds; the throttle is counted in the budget.
func TestAWSRetriesThrottling(t *testing.T) {
	f := newAWSFake()
	f.listPages = []awsListMetricsResponse{{Metrics: awsMetrics("CPUUtilization", 1)}}
	f.failTarget, f.failTimes, f.failStatus = "GetMetricData", 1, http.StatusBadRequest
	// AWS answers a throttled request with 400 and an error code rather than 429.
	f.failBody = `{"__type":"com.amazon.coral.availability#ThrottlingException","message":"Rate exceeded"}`

	budget := NewBudget(100, 100)
	points, _, err := collectAWS(t, f, awsRequest(t, "eu-central-1", budget, "rds"))
	if err != nil {
		t.Fatalf("Collect after a throttle: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("collected %d points after the retry", len(points))
	}
	if n := f.get("GetMetricData"); n != 2 {
		t.Errorf("GetMetricData called %d times, want 2 (one throttled, one retried)", n)
	}
	if _, _, throttled, _ := budget.Stats(); throttled != 1 {
		t.Errorf("throttles recorded = %d, want 1", throttled)
	}
}

// Rejected credentials are not retried: another attempt only burns quota.
func TestAWSDoesNotRetryAuthFailure(t *testing.T) {
	f := newAWSFake()
	f.failTarget, f.failTimes, f.failStatus = "ListMetrics", 5, http.StatusForbidden
	f.failBody = `{"__type":"AccessDenied","message":"not authorized to perform cloudwatch:ListMetrics"}`

	_, res, err := collectAWS(t, f, awsRequest(t, "eu-central-1", NewBudget(100, 100), "rds"))
	if err != nil {
		t.Fatalf("a service failure must not fail the scope: %v", err)
	}
	if len(res.Services) != 1 || res.Services[0].Err == nil {
		t.Fatalf("service result = %+v, want the failure recorded", res.Services)
	}
	if n := f.get("ListMetrics"); n != 1 {
		t.Errorf("ListMetrics called %d times, want 1 (no retry on 403)", n)
	}
	if msg := res.Services[0].Err.Error(); !strings.Contains(msg, "not authorized") {
		t.Errorf("error %q does not carry the provider's message", msg)
	}
}

// One failing service does not lose the services after it.
func TestAWSPartialFailureKeepsOtherServices(t *testing.T) {
	f := newAWSFake()
	// The first ListMetrics (rds) fails; the second (lambda) answers. The failure is a 403, which is not
	// retried — a 500 would be, and the retry would consume the next service's page.
	f.listPages = []awsListMetricsResponse{{}, {Metrics: []awsMetric{{Namespace: "AWS/Lambda", MetricName: "Invocations",
		Dimensions: []awsDimension{{Name: "FunctionName", Value: "checkout"}}}}}}
	f.failTarget, f.failTimes, f.failStatus = "ListMetrics", 1, http.StatusForbidden
	f.failBody = `{"__type":"AccessDenied","message":"not authorized to perform cloudwatch:ListMetrics"}`

	points, res, err := collectAWS(t, f, awsRequest(t, "eu-central-1", NewBudget(100, 100), "rds", "lambda"))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Services) != 2 {
		t.Fatalf("service results = %+v, want both services reported", res.Services)
	}
	if res.Services[0].Err == nil {
		t.Error("the failing service is not reported as failed")
	}
	if res.Services[1].Err != nil || len(points) == 0 {
		t.Errorf("the second service did not run: %+v, %d points", res.Services[1], len(points))
	}
}

// The metric cap stops collection and is reported, rather than silently returning a partial number.
func TestAWSStopsAtMetricCap(t *testing.T) {
	f := newAWSFake()
	f.listPages = []awsListMetricsResponse{{Metrics: awsMetrics("CPUUtilization", 10)}}
	budget := NewBudget(3, 100)

	points, res, err := collectAWS(t, f, awsRequest(t, "eu-central-1", budget, "rds"))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(points) != 3 {
		t.Fatalf("collected %d points, want the cap of 3", len(points))
	}
	if !res.Truncated {
		t.Error("the result does not report that a cap stopped it")
	}
	if _, _, _, capped := budget.Stats(); capped != "metrics" {
		t.Errorf("budget capped at %q, want \"metrics\"", capped)
	}
}

// The API-call cap stops a poll that would otherwise keep paging.
func TestAWSStopsAtAPICallCap(t *testing.T) {
	f := newAWSFake()
	f.listPages = []awsListMetricsResponse{
		{Metrics: awsMetrics("CPUUtilization", 1), NextToken: "p2"},
		{Metrics: awsMetrics("CPUUtilization", 1), NextToken: "p3"},
		{Metrics: awsMetrics("CPUUtilization", 1)},
	}
	// One call is spent on STS, one on the first ListMetrics; the next page has no slot left.
	budget := NewBudget(100, 2)
	_, res, err := collectAWS(t, f, awsRequest(t, "eu-central-1", budget, "rds"))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if !res.Truncated {
		t.Error("the result does not report that the API call cap stopped it")
	}
	if _, calls, _, capped := budget.Stats(); calls > 2 || capped != "api_calls" {
		t.Errorf("calls = %d, capped at %q", calls, capped)
	}
}

func TestAWSTestConnection(t *testing.T) {
	f := newAWSFake()
	f.listPages = []awsListMetricsResponse{{Metrics: awsMetrics("CPUUtilization", 1)}}
	srv := f.server(t)
	p := newAWS(ProviderOptions{BaseURL: srv.URL, HTTP: srv.Client()})
	if err := p.Test(context.Background(), Credentials{AccessKeyID: "A", SecretAccessKey: "S"}, "eu-central-1"); err != nil {
		t.Fatalf("Test: %v", err)
	}

	bad := newAWSFake()
	bad.failTarget, bad.failTimes, bad.failStatus = "ListMetrics", 5, http.StatusForbidden
	bad.failBody = `{"__type":"AccessDenied","message":"no permission"}`
	badSrv := bad.server(t)
	p2 := newAWS(ProviderOptions{BaseURL: badSrv.URL, HTTP: badSrv.Client()})
	err := p2.Test(context.Background(), Credentials{AccessKeyID: "A", SecretAccessKey: "S"}, "eu-central-1")
	var ae *AuthError
	if err == nil || !asError(err, &ae) {
		t.Fatalf("Test with a rejected key = %v, want an AuthError", err)
	}
}
