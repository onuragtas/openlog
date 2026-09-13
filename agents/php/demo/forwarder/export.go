// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// exporter posts payloads as OTLP/HTTP protobuf, one at a time, with retries for transient failures
// (network errors, 429, 502, 503, 504; Retry-After honored up to maxRetryAfter).
type exporter struct {
	endpoint string
	key      string
	client   *http.Client
	stats    *counters

	queue       chan payload
	closeOnce   sync.Once
	maxAttempts int
	backoff     func(attempt int) time.Duration
	sleep       func(time.Duration)
}

type payload struct {
	body  []byte
	spans int
}

const maxRetryAfter = 30 * time.Second

func newExporter(endpoint, key string, st *counters) *exporter {
	return &exporter{
		endpoint: endpoint, key: key, stats: st,
		client:      &http.Client{Timeout: 10 * time.Second},
		queue:       make(chan payload, 512),
		maxAttempts: 5,
		backoff:     func(a int) time.Duration { return time.Duration(250<<a) * time.Millisecond }, // 0.25 0.5 1 2 s
		sleep:       time.Sleep,
	}
}

// enqueue serializes td and queues it; it never blocks the receive path (a full queue drops the payload).
func (e *exporter) enqueue(td *tracepb.TracesData, spans int) {
	body, err := proto.Marshal(&collectortrace.ExportTraceServiceRequest{ResourceSpans: td.ResourceSpans})
	if err != nil {
		slog.Error("marshal payload", "error", err)
		e.stats.exportFailedSpans.Add(int64(spans))
		return
	}
	select {
	case e.queue <- payload{body: body, spans: spans}:
	default:
		e.stats.queueDropSpans.Add(int64(spans))
	}
}

func (e *exporter) close() { e.closeOnce.Do(func() { close(e.queue) }) }

func (e *exporter) run() {
	for p := range e.queue {
		e.stats.payloads.Add(1)
		if e.post(p.body) {
			e.stats.exportedSpans.Add(int64(p.spans))
		} else {
			e.stats.exportFailedSpans.Add(int64(p.spans))
		}
	}
}

func (e *exporter) post(body []byte) bool {
	for attempt := 0; attempt < e.maxAttempts; attempt++ {
		if attempt > 0 {
			e.stats.exportRetries.Add(1)
		}
		wait, retry := e.try(body)
		if !retry {
			return wait == 0
		}
		if attempt == e.maxAttempts-1 {
			break
		}
		if wait == 0 {
			wait = e.backoff(attempt)
		}
		e.sleep(wait)
	}
	return false
}

// try performs one request. It returns (0, false) on success, (1, false) on a permanent failure and
// (retryAfter, true) when the request should be retried (retryAfter 0 = use the backoff).
func (e *exporter) try(body []byte) (time.Duration, bool) {
	req, err := http.NewRequest(http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		slog.Error("export request", "error", err)
		return 1, false
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	if e.key != "" {
		req.Header.Set("openlog-license-key", e.key)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		slog.Warn("export failed", "endpoint", e.endpoint, "error", err)
		return 0, true
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode/100 == 2:
		return 0, false
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusBadGateway ||
		resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout:
		var wait time.Duration
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			wait = min(time.Duration(s)*time.Second, maxRetryAfter)
		}
		slog.Warn("export throttled", "status", resp.StatusCode, "retry_after", wait)
		return wait, true
	default:
		slog.Warn("export rejected", "status", resp.StatusCode, "body", string(msg))
		return 1, false
	}
}
