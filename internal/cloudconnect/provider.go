package cloudconnect

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The provider contract. One Provider reads the metrics of one scope (an AWS region, an Azure subscription, a
// GCP project) and hands every data point to an Emit function; the collector turns those points into rows of
// `metrics` (metrics.go), so nothing downstream knows they came from a cloud API.
//
// A provider never decides policy: the caps, the retries and what counts as a partial failure belong to the
// collector and to Budget below, so the three clients stay small and testable against httptest fakes.

// Emit receives one data point. It returns false when the caller has collected everything it may (the
// per-poll metric cap): a provider must stop requesting more and return what it has.
type Emit func(Point) bool

// Provider reads metrics of one cloud.
type Provider interface {
	// ID is the provider name (aws, azure, gcp).
	ID() string
	// Collect reads the requested services of one scope. It returns a per-service outcome; err is non-nil
	// only when the whole scope failed (bad credentials, an unusable scope), never for one failed service.
	Collect(ctx context.Context, req CollectRequest, emit Emit) (CollectResult, error)
	// Test makes one cheap call that proves the credentials can read metrics of the scope.
	Test(ctx context.Context, creds Credentials, scope string) error
}

// providers is the registry; aws.go, azure.go and gcp.go register themselves through newProvider.
func providerFor(id string, o ProviderOptions) (Provider, error) {
	switch id {
	case ProviderAWS:
		return newAWS(o), nil
	case ProviderAzure:
		return newAzure(o), nil
	case ProviderGCP:
		return newGCP(o), nil
	}
	return nil, fmt.Errorf("unknown cloud provider %q", id)
}

// ProviderOptions configure a provider client (tests replace the endpoints and the clock).
type ProviderOptions struct {
	// HTTP is the client every request uses (default: a client with a 30s timeout).
	HTTP *http.Client
	// BaseURL overrides the provider's endpoint; tests point it at an httptest server. Empty = the real
	// endpoints, derived per scope.
	BaseURL string
	Now     func() time.Time
}

func (o ProviderOptions) client() *http.Client {
	if o.HTTP != nil {
		return o.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (o ProviderOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// CollectRequest is one poll of one scope.
type CollectRequest struct {
	// Scope is the region, subscription or project.
	Scope string
	// Services are the catalog services to read, already validated against the provider.
	Services []Service
	Credentials
	// Start and End bound the window read from the provider (the collector overlaps them slightly, so a
	// late-arriving point is not missed).
	Start, End time.Time
	// Budget caps what this poll may spend; a provider must take a slot before every request.
	Budget *Budget
}

// ServiceResult is the outcome of one service inside one scope.
type ServiceResult struct {
	Service string
	Metrics int
	Err     error
}

// CollectResult is what one scope produced.
type CollectResult struct {
	Services []ServiceResult
	// Truncated reports that a cap stopped the run before every service was read.
	Truncated bool
}

// Point is one data point of a managed cloud resource, before it becomes a row of `metrics`.
type Point struct {
	Provider string
	// Service is the catalog service id (rds, azure_sql, cloud_sql).
	Service string
	// Platform is the cloud.platform attribute (aws_rds, azure_sql, gcp_cloud_sql).
	Platform string
	// MetricName is the provider's own metric name (CPUUtilization, cpu_percent, cpu/utilization).
	MetricName string
	Unit       string
	// Stat is the aggregation the value represents (Average, Sum, Maximum, Total).
	Stat      string
	Value     float64
	Timestamp time.Time
	// Region is cloud.region; for Azure and GCP it is the scope when the provider reports no finer location.
	Region string
	// AccountID is cloud.account.id: the AWS account, the Azure subscription or the GCP project.
	AccountID string
	// ResourceID is the provider's unique id (ARN, Azure resource id, GCP resource name); "" when the
	// provider only names the resource.
	ResourceID string
	// ResourceName is the human-readable resource (a DB instance identifier, a bucket, a function).
	ResourceName string
	// Dimensions are the provider's remaining dimensions, already named as openlog attributes.
	Dimensions map[string]string
}

// Metric name prefix of every collected point.
const metricPrefix = "cloud"

// Name is the openlog metric name of the point: cloud.<provider>.<service>.<metric in snake_case>. The
// provider's own spelling is kept as the cloud.metric.name attribute, so nothing is lost by the conversion
// (docs/contracts/semantic-conventions.md §9).
func (p Point) Name() string {
	return metricPrefix + "." + p.Provider + "." + p.Service + "." + snakeName(p.MetricName)
}

// ResourceAttributes are the attributes describing the resource the point belongs to. They land in the
// resource_attributes column, like an agent's resource attributes, so the Metrics Explorer groups by them.
func (p Point) ResourceAttributes(connectionID, connectionName string) map[string]string {
	attrs := map[string]string{
		"cloud.provider":                p.Provider,
		"cloud.platform":                p.Platform,
		"openlog.entity.type":           "cloud_resource",
		"openlog.cloud.connection.id":   connectionID,
		"openlog.cloud.connection.name": connectionName,
		"openlog.cloud.service":         p.Service,
	}
	put(attrs, "cloud.region", p.Region)
	put(attrs, "cloud.account.id", p.AccountID)
	put(attrs, "cloud.resource.id", p.ResourceID)
	put(attrs, "cloud.resource.name", p.ResourceName)
	return attrs
}

// Attributes are the data point's own attributes: the provider's metric name and statistic, plus every
// dimension that is not already a resource attribute.
func (p Point) Attributes() map[string]string {
	attrs := map[string]string{"cloud.metric.name": p.MetricName}
	put(attrs, "cloud.metric.stat", p.Stat)
	for k, v := range p.Dimensions {
		put(attrs, k, v)
	}
	return attrs
}

func put(m map[string]string, k, v string) {
	if v != "" {
		m[k] = v
	}
}

// snakeName converts a provider metric name to openlog's snake_case: separators become underscores, camel and
// acronym boundaries are split ("CPUUtilization" -> cpu_utilization, "HTTPCode_Target_5XX_Count" ->
// http_code_target_5xx_count, "Percentage CPU" -> percentage_cpu, "cpu/utilization" -> cpu_utilization).
func snakeName(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	runes := []rune(s)
	for i, r := range runes {
		switch {
		case r == '/' || r == ' ' || r == '-' || r == '.' || r == '_':
			b.WriteByte('_')
			continue
		case i > 0 && isUpper(r) && isLower(runes[i-1]):
			// a lowercase before an uppercase starts a new word (Read|IOPS). A digit before an uppercase does
			// not: "5XX" and "E2E" are one word each, not "5_XX" and "E2_E".
			b.WriteByte('_')
		case i > 0 && isUpper(r) && isUpper(runes[i-1]) && i+1 < len(runes) && isLower(runes[i+1]):
			// the last uppercase of a run starts the next word (CPU|Utilization)
			b.WriteByte('_')
		case i > 0 && isDigit(r) && isLower(runes[i-1]):
			// a digit after a letter starts a new word (Http|5xx)
			b.WriteByte('_')
		}
		b.WriteRune(toLower(r))
	}
	return collapse(b.String())
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }
func isLower(r rune) bool { return r >= 'a' && r <= 'z' }
func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func toLower(r rune) rune {
	if isUpper(r) {
		return r + ('a' - 'A')
	}
	return r
}

// collapse removes repeated and leading/trailing underscores left by the separators.
func collapse(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prev := byte('_')
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' && prev == '_' {
			continue
		}
		b.WriteByte(c)
		prev = c
	}
	return strings.Trim(b.String(), "_")
}

// ---- guardrails ----

// Budget is what one poll of one scope may spend. These APIs are billed per request and rate-limit hard, so
// every provider call takes a slot first and stops when there is none left; the collector reports a run that
// hit a cap as partial rather than pretending it saw everything.
type Budget struct {
	mu         sync.Mutex
	maxMetrics int
	maxCalls   int
	metrics    int
	calls      int
	throttled  int
	cappedAt   string
}

// NewBudget creates a budget for one poll.
func NewBudget(maxMetrics, maxCalls int) *Budget {
	return &Budget{maxMetrics: maxMetrics, maxCalls: maxCalls}
}

// TakeCall reserves one provider request. It returns false when the API-call cap is reached.
func (b *Budget) TakeCall() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.calls >= b.maxCalls {
		if b.cappedAt == "" {
			b.cappedAt = "api_calls"
		}
		return false
	}
	b.calls++
	return true
}

// AddMetric counts one collected data point and reports whether more may be collected.
func (b *Budget) AddMetric() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.metrics >= b.maxMetrics {
		if b.cappedAt == "" {
			b.cappedAt = "metrics"
		}
		return false
	}
	b.metrics++
	return true
}

// MetricsLeft is how many more data points this poll may collect.
func (b *Budget) MetricsLeft() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return max(b.maxMetrics-b.metrics, 0)
}

// Throttle records that the provider rate-limited us.
func (b *Budget) Throttle() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.throttled++
}

// Stats returns what the poll spent and which cap stopped it ("" when none did).
func (b *Budget) Stats() (metrics, calls, throttled int, cappedAt string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.metrics, b.calls, b.throttled, b.cappedAt
}

// ---- provider errors ----

// ErrBudget reports that a cap stopped the run before it finished.
var ErrBudget = errors.New("the poll reached its budget")

// ThrottleError is a provider rate-limit answer (HTTP 429, or an AWS Throttling error code). RetryAfter is
// the provider's hint, zero when it gave none.
type ThrottleError struct {
	Provider   string
	RetryAfter time.Duration
	Msg        string
}

func (e *ThrottleError) Error() string {
	if e.Msg == "" {
		return e.Provider + " rate-limited the request"
	}
	return e.Provider + " rate-limited the request: " + e.Msg
}

// AuthError is a rejected credential or a missing permission (401, 403). It is not retried: retrying a wrong
// key only burns quota, and the connection's status should say so.
type AuthError struct {
	Provider string
	Msg      string
}

func (e *AuthError) Error() string { return e.Provider + ": " + e.Msg }

// APIError is any other provider answer that is not success.
type APIError struct {
	Provider string
	Status   int
	Msg      string
}

func (e *APIError) Error() string {
	if e.Msg == "" {
		return fmt.Sprintf("%s returned HTTP %d", e.Provider, e.Status)
	}
	return fmt.Sprintf("%s returned HTTP %d: %s", e.Provider, e.Status, e.Msg)
}

// isBudget reports that a guardrail, not the provider, stopped the work. The collector turns it into a
// partial run instead of an error: what was collected before the cap is still valid.
func isBudget(err error) bool { return err != nil && errors.Is(err, ErrBudget) }

// retryable reports whether another attempt can help: a throttle, or a provider-side 5xx.
func retryable(err error) bool {
	var te *ThrottleError
	if errors.As(err, &te) {
		return true
	}
	var ae *APIError
	return errors.As(err, &ae) && ae.Status >= 500
}
