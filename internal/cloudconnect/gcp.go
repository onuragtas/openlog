package cloudconnect

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GCP Cloud Monitoring client.
//
// One timeSeries.list call per metric type returns every resource of that metric in the project, so GCP needs
// no separate inventory step: the monitored resource and its labels travel with each series. Pages are
// followed with pageToken, bounded per metric by gcpPageLimit and by the poll's budget.
//
// Authentication is a service account key: a signed JWT assertion is exchanged for an access token, which is
// cached until shortly before it expires. There is no dependency on a Google SDK — the assertion is a signed
// JSON document, which is the whole of the protocol.

const (
	gcpTokenURI    = "https://oauth2.googleapis.com/token"
	gcpMonitoring  = "https://monitoring.googleapis.com"
	gcpScope       = "https://www.googleapis.com/auth/monitoring.read"
	gcpAssertion   = "urn:ietf:params:oauth:grant-type:jwt-bearer"
	gcpTokenTTL    = time.Hour
	gcpPageLimit   = 20
	gcpPageSize    = 500
	gcpMaxJWTSkew  = 60 * time.Second
	gcpMaxPageSize = 100000
)

type gcpProvider struct {
	o      ProviderOptions
	tokens *tokenCache
}

func newGCP(o ProviderOptions) *gcpProvider { return &gcpProvider{o: o, tokens: newTokenCache()} }

// ID implements Provider.
func (g *gcpProvider) ID() string { return ProviderGCP }

func (g *gcpProvider) monitoringURL() string {
	if g.o.BaseURL != "" {
		return strings.TrimSuffix(g.o.BaseURL, "/") + "/monitoring"
	}
	return gcpMonitoring
}

func (g *gcpProvider) tokenURL(creds Credentials) string {
	if g.o.BaseURL != "" {
		return strings.TrimSuffix(g.o.BaseURL, "/") + "/token"
	}
	if creds.TokenURI != "" {
		return creds.TokenURI
	}
	return gcpTokenURI
}

// gcpTimeSeries is one series of a timeSeries.list answer.
type gcpTimeSeries struct {
	Metric struct {
		Type   string            `json:"type"`
		Labels map[string]string `json:"labels"`
	} `json:"metric"`
	Resource struct {
		Type   string            `json:"type"`
		Labels map[string]string `json:"labels"`
	} `json:"resource"`
	Points []struct {
		Interval struct {
			EndTime string `json:"endTime"`
		} `json:"interval"`
		Value struct {
			DoubleValue *float64 `json:"doubleValue"`
			Int64Value  *string  `json:"int64Value"`
			BoolValue   *bool    `json:"boolValue"`
		} `json:"value"`
	} `json:"points"`
}

type gcpTimeSeriesResponse struct {
	TimeSeries    []gcpTimeSeries `json:"timeSeries"`
	NextPageToken string          `json:"nextPageToken"`
}

// Collect implements Provider: every requested service of one project.
func (g *gcpProvider) Collect(ctx context.Context, req CollectRequest, emit Emit) (CollectResult, error) {
	out := CollectResult{Services: make([]ServiceResult, 0, len(req.Services))}
	token, err := g.token(ctx, req.Credentials, req.Budget)
	if err != nil {
		return out, err
	}
	for _, svc := range req.Services {
		if ctx.Err() != nil {
			out.Truncated = true
			break
		}
		n, err := g.collectService(ctx, req, svc, token, emit)
		out.Services = append(out.Services, ServiceResult{Service: svc.ID, Metrics: n, Err: err})
		if isBudget(err) {
			out.Truncated = true
			break
		}
	}
	return out, nil
}

func (g *gcpProvider) collectService(ctx context.Context, req CollectRequest, svc Service, token string, emit Emit) (int, error) {
	written := 0
	var firstErr error
	for _, m := range svc.Metrics {
		n, err := g.metricSeries(ctx, req, svc, m, token, emit)
		written += n
		if err != nil {
			if isBudget(err) {
				return written, err
			}
			// One metric type that is not enabled in the project must not lose the others.
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return written, firstErr
}

// metricSeries pages over one metric type of the project.
func (g *gcpProvider) metricSeries(ctx context.Context, req CollectRequest, svc Service, m Metric,
	token string, emit Emit) (int, error) {
	start, end := windowFor(svc, req.Start, req.End)
	metricType := svc.Namespace + "/" + m.Name
	written := 0
	pageToken := ""
	for page := 0; page < gcpPageLimit; page++ {
		q := url.Values{
			"filter":                       {`metric.type="` + metricType + `"`},
			"interval.startTime":           {start.UTC().Format(time.RFC3339)},
			"interval.endTime":             {end.UTC().Format(time.RFC3339)},
			"aggregation.alignmentPeriod":  {strconv.Itoa(gcpAlignment(svc, start, end)) + "s"},
			"aggregation.perSeriesAligner": {gcpAligner(m.Stat)},
			"pageSize":                     {strconv.Itoa(gcpPageSize)},
			"view":                         {"FULL"},
		}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		endpoint := g.monitoringURL() + "/v3/projects/" + url.PathEscape(req.Scope) + "/timeSeries?" + q.Encode()
		var res gcpTimeSeriesResponse
		if err := g.get(ctx, req.Budget, endpoint, token, &res); err != nil {
			return written, err
		}
		for _, ts := range res.TimeSeries {
			name, dims := gcpResource(svc, ts)
			for _, p := range ts.Points {
				value, ok := gcpValue(p.Value.DoubleValue, p.Value.Int64Value, p.Value.BoolValue)
				if !ok {
					continue
				}
				at, err := time.Parse(time.RFC3339, p.Interval.EndTime)
				if err != nil {
					continue
				}
				point := Point{
					Provider: ProviderGCP, Service: svc.ID, Platform: svc.Platform,
					MetricName: m.Name, Unit: m.Unit, Stat: m.Stat,
					Value: value, Timestamp: at.UTC(),
					Region: ts.Resource.Labels["location"], AccountID: req.Scope,
					ResourceName: name, Dimensions: dims,
				}
				if point.Region == "" {
					point.Region = ts.Resource.Labels["zone"]
				}
				if !emit(point) {
					return written, fmt.Errorf("%w: the metric cap of this poll is reached", ErrBudget)
				}
				written++
			}
		}
		if pageToken = res.NextPageToken; pageToken == "" {
			break
		}
	}
	return written, nil
}

func (g *gcpProvider) get(ctx context.Context, b *Budget, endpoint, token string, out any) error {
	return retryRequest(ctx, b, func() error {
		req, err := http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		return doJSON(ctx, g.o.client(), b, ProviderGCP, req, out)
	})
}

// token exchanges a signed service-account assertion for an access token, cached until shortly before expiry.
func (g *gcpProvider) token(ctx context.Context, creds Credentials, b *Budget) (string, error) {
	key := creds.ClientEmail
	now := g.o.now()
	if t, ok := g.tokens.get(key, now); ok {
		return t, nil
	}
	assertion, err := g.assertion(creds, now)
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {gcpAssertion}, "assertion": {assertion}}.Encode()
	var res struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	err = retryRequest(ctx, b, func() error {
		req, err := http.NewRequest(http.MethodPost, g.tokenURL(creds), strings.NewReader(form))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return doJSON(ctx, g.o.client(), b, ProviderGCP, req, &res)
	})
	if err != nil {
		return "", err
	}
	if res.AccessToken == "" {
		return "", &AuthError{Provider: ProviderGCP, Msg: "the token endpoint returned no access token"}
	}
	ttl := time.Duration(res.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = gcpTokenTTL
	}
	g.tokens.put(key, res.AccessToken, now, ttl)
	return res.AccessToken, nil
}

// assertion builds the RS256-signed JWT that identifies the service account.
func (g *gcpProvider) assertion(creds Credentials, now time.Time) (string, error) {
	key, err := parseRSAKey(creds.PrivateKey)
	if err != nil {
		return "", &AuthError{Provider: ProviderGCP, Msg: "the service account private key could not be parsed: " + err.Error()}
	}
	aud := g.tokenURL(creds)
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iss":   creds.ClientEmail,
		"scope": gcpScope,
		"aud":   aud,
		"iat":   now.Add(-gcpMaxJWTSkew).Unix(),
		"exp":   now.Add(gcpTokenTTL).Unix(),
	}
	hb, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signing := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// parseRSAKey reads a PEM private key in PKCS#8 (what a Google key file carries) or PKCS#1.
func parseRSAKey(pemKey string) (*rsa.PrivateKey, error) {
	// A key pasted from a JSON key file often keeps its escaped newlines.
	pemKey = strings.ReplaceAll(strings.TrimSpace(pemKey), `\n`, "\n")
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		return nil, errors.New("not a PEM block")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("the key is not an RSA key")
		}
		return rk, nil
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

// Test implements Provider: a token plus one small timeSeries read proves the account can read the project.
func (g *gcpProvider) Test(ctx context.Context, creds Credentials, scope string) error {
	b := NewBudget(1, 4)
	token, err := g.token(ctx, creds, b)
	if err != nil {
		return err
	}
	now := g.o.now().UTC()
	q := url.Values{
		"filter":             {`metric.type="compute.googleapis.com/instance/cpu/utilization"`},
		"interval.startTime": {now.Add(-10 * time.Minute).Format(time.RFC3339)},
		"interval.endTime":   {now.Format(time.RFC3339)},
		"pageSize":           {"1"},
	}
	var res gcpTimeSeriesResponse
	return g.get(ctx, b, g.monitoringURL()+"/v3/projects/"+url.PathEscape(scope)+"/timeSeries?"+q.Encode(), token, &res)
}

// ---- helpers ----

// gcpResourceLabels names the label that identifies the resource, per monitored resource type, with a
// fallback list for types the catalog gains later.
var gcpResourceLabels = map[string]string{
	"cloudsql_database":   "database_id",
	"gcs_bucket":          "bucket_name",
	"cloud_function":      "function_name",
	"pubsub_subscription": "subscription_id",
	"gce_instance":        "instance_id",
}

var gcpResourceFallback = []string{"instance_id", "database_id", "bucket_name", "function_name",
	"subscription_id", "topic_id", "cluster_name", "service_name", "revision_name", "job_id"}

// gcpResource returns the resource name of a series and its remaining labels as attributes.
func gcpResource(svc Service, ts gcpTimeSeries) (string, map[string]string) {
	labels := ts.Resource.Labels
	nameKey := gcpResourceLabels[svc.ResourceType]
	if nameKey == "" || labels[nameKey] == "" {
		for _, k := range gcpResourceFallback {
			if labels[k] != "" {
				nameKey = k
				break
			}
		}
	}
	name := labels[nameKey]
	// A Cloud SQL database_id is "project:instance"; the instance alone is the useful name.
	if svc.ResourceType == "cloudsql_database" {
		if _, inst, ok := strings.Cut(name, ":"); ok {
			name = inst
		}
	}
	dims := map[string]string{}
	for k, v := range labels {
		// project_id is already cloud.account.id and location/zone is already cloud.region.
		if k == nameKey || k == "project_id" || k == "location" || k == "zone" {
			continue
		}
		put(dims, "cloud.gcp.resource."+snakeName(k), v)
	}
	for k, v := range ts.Metric.Labels {
		put(dims, "cloud.gcp.metric."+snakeName(k), v)
	}
	return name, dims
}

// gcpValue reads the typed value of a point.
func gcpValue(d *float64, i *string, b *bool) (float64, bool) {
	switch {
	case d != nil:
		return *d, true
	case i != nil:
		n, err := strconv.ParseFloat(*i, 64)
		return n, err == nil
	case b != nil:
		if *b {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// gcpAligner maps the catalog statistic to a Cloud Monitoring aligner.
func gcpAligner(stat string) string {
	switch stat {
	case "Sum", "Total":
		return "ALIGN_SUM"
	case "Maximum":
		return "ALIGN_MAX"
	case "Minimum":
		return "ALIGN_MIN"
	}
	return "ALIGN_MEAN"
}

// gcpAlignment is the alignment period in seconds, derived like the AWS period.
func gcpAlignment(svc Service, start, end time.Time) int {
	if svc.PeriodSeconds > 0 {
		return svc.PeriodSeconds
	}
	secs := int(end.Sub(start) / time.Second)
	return min(max(secs/60*60, 60), 3600)
}
