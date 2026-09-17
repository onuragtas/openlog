package cloudconnect

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// AWS CloudWatch client.
//
// Collection is two steps per service, which is what keeps it both correct and cheap:
//
//  1. ListMetrics for the service's namespace returns every metric that exists in the region together with
//     its dimensions, so openlog learns which resources are there without a per-service inventory API.
//  2. GetMetricData reads them in batches of at most awsMaxQueries queries per request (the API's own limit),
//     and the results map back to their dimensions by the query id.
//
// The alternative — a SEARCH() expression — needs one call instead of two, but it identifies a series only by
// a formatted label string, which would make the resource name a parsing problem. The two-step form is what
// makes cloud.resource.name exact.
//
// Requests use the CloudWatch JSON protocol (X-Amz-Target) signed with SigV4 for service "monitoring".

const (
	// awsMaxQueries is the MetricDataQueries limit of one GetMetricData request.
	awsMaxQueries = 500
	// awsListPageLimit bounds how many ListMetrics pages one service reads, so a region with a very large
	// number of metrics cannot make a single poll run forever; the budget caps it too.
	awsListPageLimit = 20
	awsTargetPrefix  = "GraniteServiceVersion20100801."
	awsJSONContent   = "application/x-amz-json-1.0"
)

type awsProvider struct {
	o ProviderOptions
}

func newAWS(o ProviderOptions) *awsProvider { return &awsProvider{o: o} }

// ID implements Provider.
func (a *awsProvider) ID() string { return ProviderAWS }

// endpoint is the regional CloudWatch endpoint, or the test server when BaseURL is set.
func (a *awsProvider) endpoint(service, region string) string {
	if a.o.BaseURL != "" {
		return strings.TrimSuffix(a.o.BaseURL, "/") + "/" + service
	}
	return "https://" + service + "." + region + ".amazonaws.com/"
}

// awsDimension is one CloudWatch dimension.
type awsDimension struct {
	Name  string `json:"Name"`
	Value string `json:"Value"`
}

// awsMetric is one entry of a ListMetrics answer.
type awsMetric struct {
	Namespace  string         `json:"Namespace"`
	MetricName string         `json:"MetricName"`
	Dimensions []awsDimension `json:"Dimensions"`
}

type awsListMetricsResponse struct {
	Metrics   []awsMetric `json:"Metrics"`
	NextToken string      `json:"NextToken"`
}

type awsMetricStat struct {
	Metric awsMetric `json:"Metric"`
	Period int       `json:"Period"`
	Stat   string    `json:"Stat"`
}

type awsMetricDataQuery struct {
	ID         string        `json:"Id"`
	MetricStat awsMetricStat `json:"MetricStat"`
	ReturnData bool          `json:"ReturnData"`
}

type awsGetMetricDataRequest struct {
	MetricDataQueries []awsMetricDataQuery `json:"MetricDataQueries"`
	// The JSON protocol takes epoch seconds.
	StartTime int64  `json:"StartTime"`
	EndTime   int64  `json:"EndTime"`
	NextToken string `json:"NextToken,omitempty"`
	// Newest first, so a truncated read keeps the most recent points.
	ScanBy string `json:"ScanBy"`
}

type awsMetricDataResult struct {
	ID         string    `json:"Id"`
	Label      string    `json:"Label"`
	Timestamps []float64 `json:"Timestamps"`
	Values     []float64 `json:"Values"`
	StatusCode string    `json:"StatusCode"`
}

type awsGetMetricDataResponse struct {
	MetricDataResults []awsMetricDataResult `json:"MetricDataResults"`
	NextToken         string                `json:"NextToken"`
	Messages          []struct {
		Code  string `json:"Code"`
		Value string `json:"Value"`
	} `json:"Messages"`
}

// Collect implements Provider: every requested service of one region.
func (a *awsProvider) Collect(ctx context.Context, req CollectRequest, emit Emit) (CollectResult, error) {
	out := CollectResult{Services: make([]ServiceResult, 0, len(req.Services))}
	// The account id is not part of a CloudWatch answer; one STS call per poll gives an exact
	// cloud.account.id. A failure is not fatal: the metrics are still worth collecting without it.
	account := a.accountID(ctx, req)
	for _, svc := range req.Services {
		if ctx.Err() != nil {
			out.Truncated = true
			break
		}
		n, err := a.collectService(ctx, req, svc, account, emit)
		out.Services = append(out.Services, ServiceResult{Service: svc.ID, Metrics: n, Err: err})
		if isBudget(err) {
			out.Truncated = true
			break
		}
	}
	return out, nil
}

// collectService lists the service's metrics and reads the ones the catalog knows.
func (a *awsProvider) collectService(ctx context.Context, req CollectRequest, svc Service, account string, emit Emit) (int, error) {
	wanted := map[string]Metric{}
	for _, m := range svc.Metrics {
		wanted[m.Name] = m
	}
	metrics, err := a.listMetrics(ctx, req, svc, wanted)
	if err != nil {
		return 0, err
	}
	if len(metrics) == 0 {
		return 0, nil
	}
	start, end := windowFor(svc, req.Start, req.End)
	period := awsPeriod(svc, start, end)

	written := 0
	for batch := range chunks(metrics, awsMaxQueries) {
		queries := make([]awsMetricDataQuery, 0, len(batch))
		for i, m := range batch {
			queries = append(queries, awsMetricDataQuery{
				ID:         "m" + strconv.Itoa(i),
				ReturnData: true,
				MetricStat: awsMetricStat{Metric: m, Period: period, Stat: wanted[m.MetricName].Stat},
			})
		}
		n, err := a.getMetricData(ctx, req, svc, account, batch, queries, wanted, emit)
		written += n
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

// listMetrics pages over the namespace and keeps the metrics the catalog knows.
func (a *awsProvider) listMetrics(ctx context.Context, req CollectRequest, svc Service, wanted map[string]Metric) ([]awsMetric, error) {
	var (
		out   []awsMetric
		token string
	)
	for page := 0; page < awsListPageLimit; page++ {
		body := map[string]any{"Namespace": svc.Namespace}
		if token != "" {
			body["NextToken"] = token
		}
		var res awsListMetricsResponse
		if err := a.call(ctx, req, "ListMetrics", body, &res); err != nil {
			return out, err
		}
		for _, m := range res.Metrics {
			// Only the catalog's metrics, and only series that name a resource: a namespace also carries
			// account-wide aggregates with no dimensions, which would collide into one nameless series.
			if _, ok := wanted[m.MetricName]; !ok {
				continue
			}
			if svc.ResourceDimension != "" && dimensionValue(m.Dimensions, svc.ResourceDimension) == "" {
				continue
			}
			out = append(out, m)
		}
		token = res.NextToken
		if token == "" {
			break
		}
	}
	return out, nil
}

// getMetricData reads one batch and emits its points. batch and queries are parallel: query "m<i>" belongs to
// batch[i], which is how a value gets its dimensions back.
func (a *awsProvider) getMetricData(ctx context.Context, req CollectRequest, svc Service, account string,
	batch []awsMetric, queries []awsMetricDataQuery, wanted map[string]Metric, emit Emit) (int, error) {
	start, end := windowFor(svc, req.Start, req.End)
	written := 0
	token := ""
	for {
		body := awsGetMetricDataRequest{MetricDataQueries: queries, StartTime: start.Unix(), EndTime: end.Unix(),
			NextToken: token, ScanBy: "TimestampDescending"}
		var res awsGetMetricDataResponse
		if err := a.call(ctx, req, "GetMetricData", body, &res); err != nil {
			return written, err
		}
		for _, r := range res.MetricDataResults {
			idx, ok := queryIndex(r.ID, len(batch))
			if !ok {
				continue
			}
			m := batch[idx]
			spec := wanted[m.MetricName]
			for i := range r.Values {
				if i >= len(r.Timestamps) {
					break
				}
				p := Point{
					Provider: ProviderAWS, Service: svc.ID, Platform: svc.Platform,
					MetricName: m.MetricName, Unit: spec.Unit, Stat: spec.Stat,
					Value:     r.Values[i],
					Timestamp: time.Unix(int64(r.Timestamps[i]), 0).UTC(),
					Region:    req.Scope, AccountID: account,
					ResourceName: dimensionValue(m.Dimensions, svc.ResourceDimension),
					Dimensions:   awsDimensions(m.Dimensions, svc.ResourceDimension),
				}
				if !emit(p) {
					return written, fmt.Errorf("%w: the metric cap of this poll is reached", ErrBudget)
				}
				written++
			}
		}
		if token = res.NextToken; token == "" {
			return written, nil
		}
	}
}

// call sends one signed CloudWatch JSON request, with the shared retry and backoff.
func (a *awsProvider) call(ctx context.Context, req CollectRequest, target string, body, out any) error {
	return retryRequest(ctx, req.Budget, func() error {
		httpReq, raw, err := jsonRequest(http.MethodPost, a.endpoint("monitoring", req.Scope), body)
		if err != nil {
			return err
		}
		httpReq.Header.Set("Content-Type", awsJSONContent)
		httpReq.Header.Set("X-Amz-Target", awsTargetPrefix+target)
		signV4(httpReq, sha256Hex(raw), req.Credentials, "monitoring", req.Scope, a.o.now())
		return doJSON(ctx, a.o.client(), req.Budget, ProviderAWS, httpReq, out)
	})
}

// accountID asks STS who we are, so every point carries cloud.account.id. It is best effort: without it the
// attribute is simply absent.
func (a *awsProvider) accountID(ctx context.Context, req CollectRequest) string {
	form := url.Values{"Action": {"GetCallerIdentity"}, "Version": {"2011-06-15"}}.Encode()
	httpReq, err := http.NewRequest(http.MethodPost, a.endpoint("sts", req.Scope), strings.NewReader(form))
	if err != nil {
		return ""
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	httpReq.Header.Set("Accept", "application/json")
	signV4(httpReq, sha256Hex([]byte(form)), req.Credentials, "sts", req.Scope, a.o.now())
	var res struct {
		GetCallerIdentityResponse struct {
			GetCallerIdentityResult struct {
				Account string `json:"Account"`
			} `json:"GetCallerIdentityResult"`
		} `json:"GetCallerIdentityResponse"`
	}
	if err := doJSON(ctx, a.o.client(), req.Budget, ProviderAWS, httpReq, &res); err != nil {
		return ""
	}
	return res.GetCallerIdentityResponse.GetCallerIdentityResult.Account
}

// Test implements Provider: one ListMetrics call proves the key can read CloudWatch in the region.
func (a *awsProvider) Test(ctx context.Context, creds Credentials, scope string) error {
	req := CollectRequest{Scope: scope, Credentials: creds, Budget: NewBudget(1, 2)}
	var res awsListMetricsResponse
	return a.call(ctx, req, "ListMetrics", map[string]any{"Namespace": "AWS/RDS"}, &res)
}

// ---- helpers ----

// queryIndex maps a query id ("m12") back to its position in the batch.
func queryIndex(id string, n int) (int, bool) {
	if !strings.HasPrefix(id, "m") {
		return 0, false
	}
	i, err := strconv.Atoi(id[1:])
	if err != nil || i < 0 || i >= n {
		return 0, false
	}
	return i, true
}

func dimensionValue(dims []awsDimension, name string) string {
	if name == "" {
		return ""
	}
	for _, d := range dims {
		if d.Name == name {
			return d.Value
		}
	}
	return ""
}

// awsDimensions maps the remaining dimensions to attributes, leaving out the one that already became
// cloud.resource.name. CloudWatch dimension names are PascalCase, so they are converted like metric names.
func awsDimensions(dims []awsDimension, resource string) map[string]string {
	out := map[string]string{}
	for _, d := range dims {
		if d.Name == resource || d.Value == "" {
			continue
		}
		out["cloud.aws.dimension."+snakeName(d.Name)] = d.Value
	}
	return out
}

// awsPeriod is the aggregation period: the service's own when it publishes rarely, else the poll window
// rounded down to whole minutes and clamped to what CloudWatch accepts.
func awsPeriod(svc Service, start, end time.Time) int {
	if svc.PeriodSeconds > 0 {
		return svc.PeriodSeconds
	}
	secs := int(end.Sub(start) / time.Second)
	return min(max(secs/60*60, 60), 3600)
}

// windowFor widens the poll window for services whose metrics are published rarely.
func windowFor(svc Service, start, end time.Time) (time.Time, time.Time) {
	if svc.LookbackSeconds > 0 {
		return end.Add(-time.Duration(svc.LookbackSeconds) * time.Second), end
	}
	return start, end
}

// chunks yields slices of at most n elements.
func chunks[T any](s []T, n int) func(func([]T) bool) {
	return func(yield func([]T) bool) {
		for i := 0; i < len(s); i += n {
			if !yield(s[i:min(i+n, len(s))]) {
				return
			}
		}
	}
}
