package cloudconnect

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/alert/secrets"
)

// One poll of one scope: decrypt the connection's credentials, ask the provider for the window since the last
// poll, hand every data point to the sink and report honestly what happened.
//
// The guardrails all live here rather than in the provider clients, so the three clients stay small and the
// policy is stated once:
//
//   - Budget caps the data points and the provider requests of one poll (provider.go).
//   - A service that fails does not lose the services after it; the run is partial and names the failure.
//   - A scope-wide failure (rejected credentials) is an error for that scope only — the other regions of the
//     same connection are separate schedule rows and keep running.
//   - Reaching a cap is partial, never ok: a number that stopped early must not look complete.

// CollectedPoint is one data point with the tenant and connection it belongs to.
type CollectedPoint struct {
	TenantID       string
	ConnectionID   string
	ConnectionName string
	Point
}

// PointSink receives collected data points. Add must not block (metrics.go buffers and writes them).
type PointSink interface {
	Add(rows []CollectedPoint)
}

// window overlap: the provider aligns and publishes a point some time after the fact, so each poll re-reads a
// little of the previous window. ReplacingMergeTree is not involved here — a re-read point is simply the same
// series and timestamp again, which the rollup treats as one value.
const windowOverlap = 2 * time.Minute

// minWindow keeps a very short interval from asking for a window no provider will answer.
const minWindow = 5 * time.Minute

// CollectorOptions configure a Collector.
type CollectorOptions struct {
	// Keys decrypts the stored credentials; without a configured keyring nothing can be polled.
	Keys *secrets.Keyring
	// Provider builds a provider client (tests replace it with a fake or an httptest-backed one).
	Provider func(id string, o ProviderOptions) (Provider, error)
	// ProviderOptions are passed to every provider client (HTTP client, test endpoints, clock).
	ProviderOptions ProviderOptions
	Log             *slog.Logger
	Now             func() time.Time
}

// Collector performs one poll. It is safe for concurrent use.
type Collector struct {
	sink PointSink
	o    CollectorOptions
}

// NewCollector creates a collector.
func NewCollector(sink PointSink, o CollectorOptions) *Collector {
	if o.Provider == nil {
		o.Provider = providerFor
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Collector{sink: sink, o: o}
}

func (c *Collector) now() time.Time { return c.o.Now().UTC() }

// Collect runs one poll of one claimed scope and never returns an error: a failure is a Run with a status and
// a message, which is what the connection list and the history show.
func (c *Collector) Collect(ctx context.Context, due Due) Run {
	start := c.now()
	run := Run{Scope: due.Scope, StartedAt: start, Status: StatusOK, Services: []ServiceRun{}}
	finish := func(r Run) Run {
		r.DurationMs = float64(c.now().Sub(start)) / float64(time.Millisecond)
		return r
	}

	creds, err := c.credentials(due)
	if err != nil {
		return finish(failedRun(run, err.Error()))
	}
	provider, err := c.o.Provider(due.Connection.Provider, c.o.ProviderOptions)
	if err != nil {
		return finish(failedRun(run, err.Error()))
	}
	services := make([]Service, 0, len(due.Connection.Services))
	for _, id := range due.Connection.Services {
		if svc, ok := ServiceByID(due.Connection.Provider, id); ok {
			services = append(services, svc)
		}
	}
	if len(services) == 0 {
		return finish(failedRun(run, "the connection has no known services"))
	}

	budget := NewBudget(due.Connection.MaxMetricsPerPoll, due.Connection.MaxAPICallsPerPoll)
	end := c.now()
	req := CollectRequest{
		Scope: due.Scope, Services: services, Credentials: creds,
		Start: windowStart(end, due.Connection.Interval()), End: end, Budget: budget,
	}

	// Points are buffered and handed to the sink in one call, so a poll either contributes its batch or
	// nothing; the sink itself is what talks to ClickHouse.
	buf := make([]CollectedPoint, 0, 256)
	emit := func(p Point) bool {
		if !budget.AddMetric() {
			return false
		}
		buf = append(buf, CollectedPoint{TenantID: due.TenantID, ConnectionID: due.Connection.ID,
			ConnectionName: due.Connection.Name, Point: p})
		return true
	}

	res, scopeErr := provider.Collect(ctx, req, emit)
	if len(buf) > 0 && c.sink != nil {
		c.sink.Add(buf)
	}
	metrics, calls, throttled, cappedAt := budget.Stats()
	run.Metrics, run.APICalls, run.Throttled = metrics, calls, throttled

	for _, s := range res.Services {
		sr := ServiceRun{Service: s.Service, Metrics: s.Metrics}
		if s.Err != nil {
			sr.Error = truncate(s.Err.Error(), MaxErrorBytes)
		}
		run.Services = append(run.Services, sr)
	}
	return finish(c.classify(run, res, scopeErr, cappedAt))
}

// classify decides the run's status and message. The rule is deliberately strict: anything that did not
// finish is partial, and only a run where nothing could be collected is an error.
func (c *Collector) classify(run Run, res CollectResult, scopeErr error, cappedAt string) Run {
	if scopeErr != nil {
		return failedRun(run, scopeErr.Error())
	}
	var failed []string
	for _, s := range res.Services {
		if s.Err != nil && !isBudget(s.Err) {
			failed = append(failed, s.Service+": "+s.Err.Error())
		}
	}
	switch {
	case len(failed) == len(res.Services) && len(failed) > 0:
		// Every service failed: nothing about this scope was collected.
		return failedRun(run, strings.Join(failed, "; "))
	case len(failed) > 0:
		run.Status = StatusPartial
		run.Error = truncate(strings.Join(failed, "; "), MaxErrorBytes)
	case cappedAt != "" || res.Truncated:
		run.Status = StatusPartial
		run.Error = capMessage(cappedAt)
	default:
		run.Status = StatusOK
	}
	return run
}

func capMessage(cappedAt string) string {
	switch cappedAt {
	case "metrics":
		return "the metric cap of this poll was reached; raise max_metrics_per_poll or collect fewer services"
	case "api_calls":
		return "the API call cap of this poll was reached; raise max_api_calls_per_poll or collect fewer services"
	}
	return "the poll stopped before every service was read"
}

func failedRun(run Run, msg string) Run {
	run.Status = StatusError
	run.Error = truncate(msg, MaxErrorBytes)
	return run
}

// credentials decrypts the connection's stored credentials. The plaintext never leaves this call: it is
// handed to the provider client and dropped with the request.
func (c *Collector) credentials(due Due) (Credentials, error) {
	if due.CredentialsEnc == "" {
		return Credentials{}, ErrNoCredentials
	}
	if !c.o.Keys.Configured() {
		return Credentials{}, secrets.ErrNoKey
	}
	raw, err := c.o.Keys.Decrypt(due.CredentialsEnc, CredentialsAAD(due.Connection.OrgID, due.Connection.ID))
	if err != nil {
		// The message names the failure, never the ciphertext or any field of it.
		return Credentials{}, fmt.Errorf("the stored credentials could not be decrypted: %w", err)
	}
	creds, err := DecodeCredentials(raw)
	if err != nil {
		return Credentials{}, errors.New("the stored credentials could not be decoded")
	}
	return creds, nil
}

// windowStart is the beginning of the polled window: one interval back plus an overlap, so a point the
// provider published late is still picked up by the next poll.
func windowStart(end time.Time, interval time.Duration) time.Time {
	w := interval + windowOverlap
	if w < minWindow {
		w = minWindow
	}
	return end.Add(-w)
}

// Test runs a provider's cheap credential check for one scope. It is the "test connection" action of the API
// and stores nothing.
func (c *Collector) Test(ctx context.Context, provider string, creds Credentials, scope string) error {
	p, err := c.o.Provider(provider, c.o.ProviderOptions)
	if err != nil {
		return err
	}
	return p.Test(ctx, creds, scope)
}
