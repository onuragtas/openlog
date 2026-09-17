package cloudconnect

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/alert/secrets"
)

// The collector is where the guardrails and the honesty live: what counts as ok, partial and error, and that
// a credential never survives a failure as a readable string.

func testKeyring(t *testing.T) *secrets.Keyring {
	t.Helper()
	kr, err := secrets.NewKeyring(base64.StdEncoding.EncodeToString(make([]byte, 32)), "")
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

// fakeCloudProvider emits a fixed number of points per service and can fail in the ways a real one does.
type fakeCloudProvider struct {
	perService int
	// serviceErr fails the first service.
	serviceErr error
	// allServicesErr fails every service.
	allServicesErr error
	// scopeErr fails the whole scope (rejected credentials).
	scopeErr error
	testErr  error

	mu       sync.Mutex
	gotCreds Credentials
}

func (f *fakeCloudProvider) ID() string { return ProviderAWS }

func (f *fakeCloudProvider) Collect(_ context.Context, req CollectRequest, emit Emit) (CollectResult, error) {
	f.mu.Lock()
	f.gotCreds = req.Credentials
	f.mu.Unlock()
	if f.scopeErr != nil {
		return CollectResult{}, f.scopeErr
	}
	res := CollectResult{}
	for i, svc := range req.Services {
		n := 0
		for range f.perService {
			if !emit(Point{Provider: ProviderAWS, Service: svc.ID, Platform: svc.Platform,
				MetricName: "CPUUtilization", Unit: "1", Stat: "Average", Value: 1,
				Timestamp: time.Unix(1_700_000_000, 0).UTC(), Region: req.Scope, ResourceName: "db-0"}) {
				res.Truncated = true
				break
			}
			n++
		}
		err := f.allServicesErr
		if err == nil && i == 0 {
			err = f.serviceErr
		}
		res.Services = append(res.Services, ServiceResult{Service: svc.ID, Metrics: n, Err: err})
	}
	return res, nil
}

func (f *fakeCloudProvider) Test(context.Context, Credentials, string) error { return f.testErr }

type fakeSink struct {
	mu   sync.Mutex
	rows []CollectedPoint
}

func (s *fakeSink) Add(rows []CollectedPoint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, rows...)
}

func (s *fakeSink) added() []CollectedPoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]CollectedPoint(nil), s.rows...)
}

// collectorFor wires a collector around a fake provider.
func collectorFor(t *testing.T, p Provider, sink PointSink) *Collector {
	t.Helper()
	return NewCollector(sink, CollectorOptions{
		Keys:     testKeyring(t),
		Provider: func(string, ProviderOptions) (Provider, error) { return p, nil },
	})
}

// dueFor builds a claimed scope whose credentials are encrypted for it.
func dueFor(t *testing.T, kr *secrets.Keyring, services []string, maxMetrics int) Due {
	t.Helper()
	conn := Connection{ID: "53000000-0000-4000-8000-000000000001", OrgID: "org-1", Input: Input{
		Name: "Prod AWS", Provider: ProviderAWS, IngestMode: IngestPoll, Enabled: true,
		Scopes: []string{"eu-central-1"}, Services: services, PollIntervalSeconds: 300,
		MaxMetricsPerPoll: maxMetrics, MaxAPICallsPerPoll: 100,
	}}
	raw, err := Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "top-secret-key"}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	enc, _, err := kr.Encrypt(raw, CredentialsAAD(conn.OrgID, conn.ID))
	if err != nil {
		t.Fatal(err)
	}
	return Due{Connection: conn, TenantID: "tenant-a", Scope: "eu-central-1", CredentialsEnc: enc}
}

func TestCollectSuccess(t *testing.T) {
	kr := testKeyring(t)
	p := &fakeCloudProvider{perService: 2}
	sink := &fakeSink{}
	c := NewCollector(sink, CollectorOptions{Keys: kr, Provider: func(string, ProviderOptions) (Provider, error) { return p, nil }})

	run := c.Collect(context.Background(), dueFor(t, kr, []string{"rds"}, 100))
	if run.Status != StatusOK || run.Error != "" {
		t.Fatalf("run = %+v", run)
	}
	if run.Metrics != 2 {
		t.Errorf("metrics = %d, want 2", run.Metrics)
	}
	if run.DurationMs < 0 {
		t.Errorf("duration = %v", run.DurationMs)
	}
	rows := sink.added()
	if len(rows) != 2 {
		t.Fatalf("sink received %d points", len(rows))
	}
	if rows[0].TenantID != "tenant-a" || rows[0].ConnectionName != "Prod AWS" ||
		rows[0].ConnectionID != "53000000-0000-4000-8000-000000000001" {
		t.Errorf("point lost its tenant or connection: %+v", rows[0])
	}
	// The provider was handed the decrypted credentials.
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gotCreds.AccessKeyID != "AKIDEXAMPLE" || p.gotCreds.SecretAccessKey != "top-secret-key" {
		t.Errorf("credentials were not decrypted: %+v", p.gotCreds)
	}
}

// One failing service of several is partial: what was collected is real, but the run is not complete.
func TestCollectPartialWhenOneServiceFails(t *testing.T) {
	kr := testKeyring(t)
	p := &fakeCloudProvider{perService: 1, serviceErr: errors.New("metrics unavailable")}
	c := collectorFor(t, p, &fakeSink{})

	run := c.Collect(context.Background(), dueFor(t, kr, []string{"rds", "lambda"}, 100))
	if run.Status != StatusPartial {
		t.Fatalf("status = %q, want %q (%s)", run.Status, StatusPartial, run.Error)
	}
	if !strings.Contains(run.Error, "rds") || !strings.Contains(run.Error, "metrics unavailable") {
		t.Errorf("error %q does not name the failing service", run.Error)
	}
	if len(run.Services) != 2 || run.Services[0].Error == "" || run.Services[1].Error != "" {
		t.Errorf("per-service outcome = %+v", run.Services)
	}
}

// Every service failing means nothing about this scope was collected.
func TestCollectErrorWhenEveryServiceFails(t *testing.T) {
	kr := testKeyring(t)
	p := &fakeCloudProvider{allServicesErr: errors.New("access denied")}
	c := collectorFor(t, p, &fakeSink{})

	run := c.Collect(context.Background(), dueFor(t, kr, []string{"rds", "lambda"}, 100))
	if run.Status != StatusError {
		t.Fatalf("status = %q, want %q", run.Status, StatusError)
	}
	if !strings.Contains(run.Error, "access denied") {
		t.Errorf("error = %q", run.Error)
	}
}

// A scope-wide failure (rejected credentials) is an error for this scope only.
func TestCollectErrorOnScopeFailure(t *testing.T) {
	kr := testKeyring(t)
	p := &fakeCloudProvider{scopeErr: &AuthError{Provider: ProviderAWS, Msg: "the credentials were rejected"}}
	c := collectorFor(t, p, &fakeSink{})

	run := c.Collect(context.Background(), dueFor(t, kr, []string{"rds"}, 100))
	if run.Status != StatusError || !strings.Contains(run.Error, "rejected") {
		t.Fatalf("run = %+v", run)
	}
}

// Reaching the metric cap is partial and says so: a number that stopped early must not look complete.
func TestCollectPartialAtMetricCap(t *testing.T) {
	kr := testKeyring(t)
	p := &fakeCloudProvider{perService: 10}
	sink := &fakeSink{}
	c := collectorFor(t, p, sink)

	run := c.Collect(context.Background(), dueFor(t, kr, []string{"rds"}, 3))
	if run.Status != StatusPartial {
		t.Fatalf("status = %q, want %q", run.Status, StatusPartial)
	}
	if run.Metrics != 3 {
		t.Errorf("metrics = %d, want the cap of 3", run.Metrics)
	}
	if !strings.Contains(run.Error, "max_metrics_per_poll") {
		t.Errorf("error %q does not say which cap was reached", run.Error)
	}
	if len(sink.added()) != 3 {
		t.Errorf("sink received %d points", len(sink.added()))
	}
}

// Credentials that cannot be decrypted fail the run with a message that carries no ciphertext.
func TestCollectErrorOnUndecryptableCredentials(t *testing.T) {
	kr := testKeyring(t)
	due := dueFor(t, kr, []string{"rds"}, 100)
	// The same ciphertext under another connection's AAD must not open.
	due.Connection.ID = "53000000-0000-4000-8000-000000000002"
	c := collectorFor(t, &fakeCloudProvider{perService: 1}, &fakeSink{})

	run := c.Collect(context.Background(), due)
	if run.Status != StatusError {
		t.Fatalf("status = %q, want %q", run.Status, StatusError)
	}
	if !strings.Contains(run.Error, "could not be decrypted") {
		t.Errorf("error = %q", run.Error)
	}
	if strings.Contains(run.Error, due.CredentialsEnc) || strings.Contains(run.Error, "ol1:") {
		t.Errorf("the error leaks the ciphertext: %q", run.Error)
	}
}

func TestCollectErrorWithoutStoredCredentials(t *testing.T) {
	kr := testKeyring(t)
	due := dueFor(t, kr, []string{"rds"}, 100)
	due.CredentialsEnc = ""
	c := collectorFor(t, &fakeCloudProvider{perService: 1}, &fakeSink{})

	run := c.Collect(context.Background(), due)
	if run.Status != StatusError || !strings.Contains(run.Error, "credentials") {
		t.Fatalf("run = %+v", run)
	}
}

// A connection whose services are all unknown (a catalog entry was removed) fails loudly rather than
// reporting a healthy poll that collected nothing.
func TestCollectErrorOnUnknownServices(t *testing.T) {
	kr := testKeyring(t)
	due := dueFor(t, kr, []string{"no-such-service"}, 100)
	c := collectorFor(t, &fakeCloudProvider{perService: 1}, &fakeSink{})

	run := c.Collect(context.Background(), due)
	if run.Status != StatusError || !strings.Contains(run.Error, "no known services") {
		t.Fatalf("run = %+v", run)
	}
}

func TestCollectorTest(t *testing.T) {
	c := collectorFor(t, &fakeCloudProvider{}, &fakeSink{})
	if err := c.Test(context.Background(), ProviderAWS, Credentials{AccessKeyID: "A", SecretAccessKey: "S"}, "eu-central-1"); err != nil {
		t.Fatalf("Test: %v", err)
	}

	failing := collectorFor(t, &fakeCloudProvider{testErr: errors.New("the credentials were rejected")}, &fakeSink{})
	err := failing.Test(context.Background(), ProviderAWS, Credentials{AccessKeyID: "A", SecretAccessKey: "S"}, "eu-central-1")
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("Test = %v, want the provider's failure", err)
	}
}

// The polled window covers the interval plus an overlap, so a point the provider published late is still
// picked up by the next poll.
func TestWindowStartCoversTheIntervalWithOverlap(t *testing.T) {
	end := time.Unix(1_700_000_000, 0).UTC()
	if got := end.Sub(windowStart(end, 5*time.Minute)); got != 5*time.Minute+windowOverlap {
		t.Errorf("window = %s, want the interval plus the overlap", got)
	}
	// A very short interval still asks for a window the providers will answer.
	if got := end.Sub(windowStart(end, time.Minute)); got != minWindow {
		t.Errorf("window = %s, want at least %s", got, minWindow)
	}
}
