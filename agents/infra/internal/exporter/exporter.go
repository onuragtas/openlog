// Package exporter sends OTLP/HTTP protobuf payloads to the openlog ingest
// endpoint with gzip compression, retries and request splitting.
package exporter

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// Signal identifies an OTLP signal endpoint.
type Signal string

// Supported signals.
const (
	SignalMetrics Signal = "metrics"
	SignalLogs    Signal = "logs"
	// SignalTraces carries spans received by the PHP forwarder (docs/contracts/php-agent.md §6).
	SignalTraces Signal = "traces"
)

// LicenseHeader carries the ingest license key.
const LicenseHeader = "openlog-license-key"

// Error describes a failed export.
type Error struct {
	StatusCode int // 0 for transport errors
	Retryable  bool
	RetryAfter time.Duration
	Err        error
}

func (e *Error) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("export failed: HTTP %d: %v", e.StatusCode, e.Err)
	}
	return fmt.Sprintf("export failed: %v", e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// IsRetryable reports whether err is worth buffering and retrying later.
func IsRetryable(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Retryable
	}
	return err != nil && !errors.Is(err, context.Canceled)
}

// Exporter posts payloads to <Endpoint>/v1/<signal>.
type Exporter struct {
	Endpoint    string
	LicenseKey  string
	UserAgent   string
	Client      *http.Client
	MaxAttempts int           // total attempts per Send (>= 1)
	BaseBackoff time.Duration // first retry delay
	MaxBackoff  time.Duration // cap for a single delay, including Retry-After

	// Sleep waits between attempts; replaceable in tests.
	Sleep func(ctx context.Context, d time.Duration) error
}

// New returns an exporter with production defaults.
func New(endpoint, licenseKey, version string, timeout time.Duration) *Exporter {
	return &Exporter{
		Endpoint:    strings.TrimRight(endpoint, "/"),
		LicenseKey:  licenseKey,
		UserAgent:   "openlog-infra-agent/" + version,
		Client:      &http.Client{Timeout: timeout},
		MaxAttempts: 3,
		BaseBackoff: time.Second,
		MaxBackoff:  30 * time.Second,
	}
}

// Send compresses and posts a serialized protobuf payload, retrying transient
// failures. The returned error is an *Error for HTTP/transport failures.
func (e *Exporter) Send(ctx context.Context, signal Signal, payload []byte) error {
	body, err := gzipBytes(payload)
	if err != nil {
		return &Error{Err: err}
	}
	attempts := max(e.MaxAttempts, 1)
	var last *Error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			d := e.backoff(attempt)
			if last.RetryAfter > 0 {
				d = last.RetryAfter
			}
			if d > e.MaxBackoff {
				return last // server asks for a longer pause: buffer instead of blocking
			}
			if err := e.sleep(ctx, d); err != nil {
				return &Error{Err: err}
			}
		}
		last = e.post(ctx, signal, body)
		if last == nil {
			return nil
		}
		if !last.Retryable {
			return last
		}
	}
	return last
}

func (e *Exporter) post(ctx context.Context, signal Signal, body []byte) *Error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.Endpoint+"/v1/"+string(signal), bytes.NewReader(body))
	if err != nil {
		return &Error{Err: err}
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set(LicenseHeader, e.LicenseKey)
	if e.UserAgent != "" {
		req.Header.Set("User-Agent", e.UserAgent)
	}
	client := e.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return &Error{Err: err, Retryable: ctx.Err() == nil}
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	out := &Error{StatusCode: resp.StatusCode, Err: errors.New(strings.TrimSpace(string(msg)))}
	switch resp.StatusCode {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		out.Retryable = true
		out.RetryAfter = ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	case http.StatusBadGateway, http.StatusGatewayTimeout, http.StatusRequestTimeout:
		out.Retryable = true
	}
	return out
}

// backoff returns an exponential delay with jitter in [d/2, d).
func (e *Exporter) backoff(attempt int) time.Duration {
	d := e.BaseBackoff << (attempt - 1)
	if d <= 0 || d > e.MaxBackoff {
		d = e.MaxBackoff
	}
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

func (e *Exporter) sleep(ctx context.Context, d time.Duration) error {
	if e.Sleep != nil {
		return e.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ParseRetryAfter parses a Retry-After value (delta seconds or HTTP date).
func ParseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

func gzipBytes(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// SplitLogs splits a LogsData into requests whose serialized size stays at or
// below maxBytes (uncompressed). Record order is preserved, so a trailing
// snapshot-complete record always ends up last in the last request. A single
// record larger than the limit is sent on its own.
func SplitLogs(data *logspb.LogsData, maxBytes int) []*logspb.LogsData {
	if proto.Size(data) <= maxBytes {
		return []*logspb.LogsData{data}
	}
	var out []*logspb.LogsData
	for _, rl := range data.ResourceLogs {
		for _, sl := range rl.ScopeLogs {
			shell := func() (*logspb.LogsData, *logspb.ScopeLogs) {
				s := &logspb.ScopeLogs{Scope: sl.Scope, SchemaUrl: sl.SchemaUrl}
				return &logspb.LogsData{ResourceLogs: []*logspb.ResourceLogs{{
					Resource: rl.Resource, SchemaUrl: rl.SchemaUrl, ScopeLogs: []*logspb.ScopeLogs{s},
				}}}, s
			}
			cur, curScope := shell()
			base := proto.Size(cur)
			size := base
			for _, rec := range sl.LogRecords {
				// Each repeated field entry adds a tag and a length prefix.
				n := proto.Size(rec)
				n += 1 + varintLen(uint64(n))
				if len(curScope.LogRecords) > 0 && size+n > maxBytes {
					out = append(out, cur)
					cur, curScope = shell()
					size = base
				}
				curScope.LogRecords = append(curScope.LogRecords, rec)
				size += n
			}
			if len(curScope.LogRecords) > 0 {
				out = append(out, cur)
			}
		}
	}
	return out
}

func varintLen(v uint64) int {
	n := 1
	for v >= 0x80 {
		v >>= 7
		n++
	}
	return n
}
