// Package export posts OTLP profiles to the openlog ingest (docs/contracts/ebpf-profiler.md §5).
//
// The rules are the Go agent's, deliberately: this is the same signal reaching the same endpoint, so it
// carries the same headers, the same compression and the same retry schedule. What differs is only where
// the samples came from.
//
// Retries are **bounded**. A profile measures a window that has already passed, so retrying past the next
// window does not save the last profile — it delays the next one. When the budget runs out the payload is
// dropped and said so.
package export

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Doer is the HTTP client. Injected so tests never open a socket.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Options configure an Exporter. Endpoint is the ingest base URL; "/v1/profiles" is appended.
type Options struct {
	Endpoint string
	Headers  map[string]string
	Gzip     bool
	Timeout  time.Duration
	// Elapsed bounds the whole retry sequence of one payload.
	Elapsed time.Duration
	// Initial and Max bound the backoff between attempts.
	Initial time.Duration
	Max     time.Duration
	Client  Doer
	// Sleep is time.Sleep by default; tests replace it so a backoff costs no wall clock.
	Sleep func(time.Duration)
}

type Exporter struct {
	url     string
	headers map[string]string
	gzip    bool
	timeout time.Duration
	elapsed time.Duration
	initial time.Duration
	max     time.Duration
	client  Doer
	sleep   func(time.Duration)
	now     func() time.Time
}

func New(o Options) (*Exporter, error) {
	u, err := url.Parse(o.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("endpoint: %w", err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/profiles"
	headers := make(map[string]string, len(o.Headers))
	for k, v := range o.Headers {
		headers[k] = v
	}
	e := &Exporter{
		url: u.String(), headers: headers, gzip: o.Gzip,
		timeout: o.Timeout, elapsed: o.Elapsed, initial: o.Initial, max: o.Max,
		client: o.Client, sleep: o.Sleep, now: time.Now,
	}
	if e.client == nil {
		e.client = &http.Client{}
	}
	if e.sleep == nil {
		e.sleep = time.Sleep
	}
	if e.initial <= 0 {
		e.initial = time.Second
	}
	if e.max <= 0 {
		e.max = 30 * time.Second
	}
	if e.timeout <= 0 {
		e.timeout = 10 * time.Second
	}
	return e, nil
}

// Send posts one already-marshalled ExportProfilesServiceRequest.
func (e *Exporter) Send(ctx context.Context, body []byte) error {
	if e.gzip {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write(body); err == nil && zw.Close() == nil {
			body = buf.Bytes()
		}
	}
	deadline := e.now().Add(e.elapsed)
	wait := e.initial
	for {
		status, retryAfter, err := e.attempt(ctx, body)
		if err == nil {
			return nil
		}
		// A status the contract does not mark retryable will not differ next time.
		if status != 0 && !Retryable(status) {
			return err
		}
		if ctx.Err() != nil {
			return err
		}
		if !e.now().Before(deadline) {
			return fmt.Errorf("profiles export gave up after %s: %w", e.elapsed, err)
		}
		d := wait
		if retryAfter > 0 {
			d = retryAfter
		}
		e.sleep(d)
		if wait *= 2; wait > e.max {
			wait = e.max
		}
	}
}

func (e *Exporter) attempt(ctx context.Context, body []byte) (int, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	if e.gzip {
		req.Header.Set("Content-Encoding", "gzip")
	}
	for k, v := range e.headers {
		req.Header.Set(k, v)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 == 2 {
		return resp.StatusCode, 0, nil
	}
	var after time.Duration
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			after = time.Duration(secs) * time.Second
		}
	}
	return resp.StatusCode, after, fmt.Errorf("profiles export: %s", resp.Status)
}

// Retryable reports whether the ingest marks this status worth another attempt (ebpf-profiler.md §8).
func Retryable(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}
