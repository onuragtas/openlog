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

// Azure Monitor client.
//
// Collection is two steps per service: the Resource Graph of the subscription lists the resources of the
// service's resource type, and the metrics API is then asked per resource. Azure has no "all resources of a
// type" metrics call, so the number of requests grows with the number of resources — which is exactly why the
// per-poll API-call cap matters here and why a subscription with many resources should be split over
// several connections or polled less often.
//
// Authentication is a service principal (client credentials); the token is cached until shortly before it
// expires, so one poll asks the identity endpoint once, not once per resource.

const (
	azureResourcesAPIVersion = "2021-04-01"
	azureMetricsAPIVersion   = "2018-01-01"
	// azureResourcePageLimit bounds how many pages of resources one service reads per poll.
	azureResourcePageLimit = 20
	azureScope             = "https://management.azure.com/.default"
)

type azureProvider struct {
	o      ProviderOptions
	tokens *tokenCache
}

func newAzure(o ProviderOptions) *azureProvider {
	return &azureProvider{o: o, tokens: newTokenCache()}
}

// ID implements Provider.
func (z *azureProvider) ID() string { return ProviderAzure }

func (z *azureProvider) managementURL() string {
	if z.o.BaseURL != "" {
		return strings.TrimSuffix(z.o.BaseURL, "/") + "/management"
	}
	return "https://management.azure.com"
}

func (z *azureProvider) loginURL() string {
	if z.o.BaseURL != "" {
		return strings.TrimSuffix(z.o.BaseURL, "/") + "/login"
	}
	return "https://login.microsoftonline.com"
}

// azureResource is one row of the resource listing.
type azureResource struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Location string `json:"location"`
}

type azureResourceList struct {
	Value    []azureResource `json:"value"`
	NextLink string          `json:"nextLink"`
}

// azureMetricsResponse is the answer of the per-resource metrics call.
type azureMetricsResponse struct {
	Value []struct {
		Name struct {
			Value string `json:"value"`
		} `json:"name"`
		Unit       string `json:"unit"`
		Timeseries []struct {
			Metadatavalues []struct {
				Name struct {
					Value string `json:"value"`
				} `json:"name"`
				Value string `json:"value"`
			} `json:"metadatavalues"`
			Data []map[string]any `json:"data"`
		} `json:"timeseries"`
	} `json:"value"`
}

// Collect implements Provider: every requested service of one subscription.
func (z *azureProvider) Collect(ctx context.Context, req CollectRequest, emit Emit) (CollectResult, error) {
	out := CollectResult{Services: make([]ServiceResult, 0, len(req.Services))}
	token, err := z.token(ctx, req.Credentials, req.Budget)
	if err != nil {
		// Without a token nothing in this subscription can be read: that is a scope-wide failure.
		return out, err
	}
	for _, svc := range req.Services {
		if ctx.Err() != nil {
			out.Truncated = true
			break
		}
		n, err := z.collectService(ctx, req, svc, token, emit)
		out.Services = append(out.Services, ServiceResult{Service: svc.ID, Metrics: n, Err: err})
		if isBudget(err) {
			out.Truncated = true
			break
		}
	}
	return out, nil
}

func (z *azureProvider) collectService(ctx context.Context, req CollectRequest, svc Service, token string, emit Emit) (int, error) {
	resources, err := z.listResources(ctx, req, svc, token)
	if err != nil {
		return 0, err
	}
	written, failed := 0, 0
	var firstErr error
	for _, r := range resources {
		n, err := z.resourceMetrics(ctx, req, svc, r, token, emit)
		written += n
		if err != nil {
			// One unreadable resource must not lose the rest of the service; a budget refusal does stop it.
			if isBudget(err) {
				return written, err
			}
			failed++
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	// Skipped resources are reported, never swallowed: a service that silently returned two thirds of its
	// resources would look healthy while a third of the estate was missing from the charts.
	if failed > 0 {
		return written, fmt.Errorf("%d of %d resources could not be read: %w", failed, len(resources), firstErr)
	}
	return written, nil
}

// listResources pages over the subscription's resources of the service's type.
func (z *azureProvider) listResources(ctx context.Context, req CollectRequest, svc Service, token string) ([]azureResource, error) {
	q := url.Values{
		"api-version": {azureResourcesAPIVersion},
		"$filter":     {"resourceType eq '" + svc.Namespace + "'"},
	}
	next := z.managementURL() + "/subscriptions/" + url.PathEscape(req.Scope) + "/resources?" + q.Encode()
	var out []azureResource
	for page := 0; page < azureResourcePageLimit && next != ""; page++ {
		var res azureResourceList
		if err := z.get(ctx, req.Budget, next, token, &res); err != nil {
			return out, err
		}
		out = append(out, res.Value...)
		next = res.NextLink
	}
	return out, nil
}

// resourceMetrics reads the service's metrics of one resource.
func (z *azureProvider) resourceMetrics(ctx context.Context, req CollectRequest, svc Service, r azureResource,
	token string, emit Emit) (int, error) {
	names := make([]string, 0, len(svc.Metrics))
	stats := map[string]Metric{}
	aggs := map[string]bool{}
	for _, m := range svc.Metrics {
		names = append(names, m.Name)
		stats[strings.ToLower(m.Name)] = m
		aggs[m.Stat] = true
	}
	start, end := windowFor(svc, req.Start, req.End)
	q := url.Values{
		"api-version": {azureMetricsAPIVersion},
		"metricnames": {strings.Join(names, ",")},
		"aggregation": {strings.Join(sortedKeys(aggs), ",")},
		"timespan":    {start.UTC().Format(time.RFC3339) + "/" + end.UTC().Format(time.RFC3339)},
		"interval":    {azureInterval(svc, start, end)},
	}
	// The resource id is already an absolute path ("/subscriptions/.../providers/...").
	endpoint := z.managementURL() + r.ID + "/providers/Microsoft.Insights/metrics?" + q.Encode()
	var res azureMetricsResponse
	if err := z.get(ctx, req.Budget, endpoint, token, &res); err != nil {
		return 0, err
	}
	written := 0
	for _, m := range res.Value {
		spec, ok := stats[strings.ToLower(m.Name.Value)]
		if !ok {
			continue
		}
		for _, ts := range m.Timeseries {
			dims := map[string]string{}
			for _, md := range ts.Metadatavalues {
				put(dims, "cloud.azure.dimension."+snakeName(md.Name.Value), md.Value)
			}
			for _, d := range ts.Data {
				value, ok := azureValue(d, spec.Stat)
				if !ok {
					continue
				}
				at, ok := azureTime(d)
				if !ok {
					continue
				}
				p := Point{
					Provider: ProviderAzure, Service: svc.ID, Platform: svc.Platform,
					MetricName: m.Name.Value, Unit: spec.Unit, Stat: spec.Stat,
					Value: value, Timestamp: at,
					Region: r.Location, AccountID: req.Scope,
					ResourceID: r.ID, ResourceName: r.Name,
					Dimensions: dims,
				}
				if !emit(p) {
					return written, fmt.Errorf("%w: the metric cap of this poll is reached", ErrBudget)
				}
				written++
			}
		}
	}
	return written, nil
}

// get sends one authenticated GET with the shared retry and backoff.
func (z *azureProvider) get(ctx context.Context, b *Budget, endpoint, token string, out any) error {
	return retryRequest(ctx, b, func() error {
		req, err := http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		return doJSON(ctx, z.o.client(), b, ProviderAzure, req, out)
	})
}

// token returns a client-credentials access token for the service principal, cached until shortly before it
// expires.
func (z *azureProvider) token(ctx context.Context, creds Credentials, b *Budget) (string, error) {
	key := creds.TenantID + "/" + creds.ClientID
	now := z.o.now()
	if t, ok := z.tokens.get(key, now); ok {
		return t, nil
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {creds.ClientID},
		"client_secret": {creds.ClientSecret},
		"scope":         {azureScope},
	}.Encode()
	endpoint := z.loginURL() + "/" + url.PathEscape(creds.TenantID) + "/oauth2/v2.0/token"
	var res struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	err := retryRequest(ctx, b, func() error {
		req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return doJSON(ctx, z.o.client(), b, ProviderAzure, req, &res)
	})
	if err != nil {
		return "", err
	}
	if res.AccessToken == "" {
		return "", &AuthError{Provider: ProviderAzure, Msg: "the identity endpoint returned no access token"}
	}
	z.tokens.put(key, res.AccessToken, now, time.Duration(res.ExpiresIn)*time.Second)
	return res.AccessToken, nil
}

// Test implements Provider: a token plus one resource listing proves the principal can read the subscription.
func (z *azureProvider) Test(ctx context.Context, creds Credentials, scope string) error {
	b := NewBudget(1, 4)
	token, err := z.token(ctx, creds, b)
	if err != nil {
		return err
	}
	endpoint := z.managementURL() + "/subscriptions/" + url.PathEscape(scope) + "/resources?" +
		url.Values{"api-version": {azureResourcesAPIVersion}, "$top": {"1"}}.Encode()
	var res azureResourceList
	return z.get(ctx, b, endpoint, token, &res)
}

// ---- helpers ----

// azureValue reads the field of a data point that matches the requested aggregation.
func azureValue(d map[string]any, stat string) (float64, bool) {
	v, ok := d[strings.ToLower(stat)]
	if !ok {
		return 0, false
	}
	f, ok := v.(float64)
	return f, ok
}

// azureTime reads the point's timestamp (timeStamp, RFC3339).
func azureTime(d map[string]any) (time.Time, bool) {
	s, ok := d["timeStamp"].(string)
	if !ok {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// azureInterval is the ISO-8601 duration of the aggregation, derived from the poll window like the AWS period.
func azureInterval(svc Service, start, end time.Time) string {
	secs := svc.PeriodSeconds
	if secs == 0 {
		secs = int(end.Sub(start) / time.Second)
	}
	mins := min(max(secs/60, 1), 60)
	if mins >= 60 {
		return "PT1H"
	}
	// Azure accepts a fixed set of intervals; the next one at or below the window keeps the answer small.
	for _, allowed := range []int{30, 15, 5, 1} {
		if mins >= allowed {
			return "PT" + strconv.Itoa(allowed) + "M"
		}
	}
	return "PT1M"
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Few entries and a stable order matters only for the request being deterministic in tests.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
