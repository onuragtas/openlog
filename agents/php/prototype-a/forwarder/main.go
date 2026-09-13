// Command forwarder is the option-A spike's local collector: it receives one JSON datagram per PHP request
// from the openlog PHP extension on a unix datagram socket and exports batched OTLP/HTTP protobuf to openlog.
//
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

type wireException struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Where   string `json:"where"`
}

type wireSpan struct {
	SpanID    string         `json:"span_id"`
	Parent    string         `json:"parent_span_id"`
	Name      string         `json:"name"`
	Kind      int32          `json:"kind"`
	Start     uint64         `json:"start"`
	Dur       uint64         `json:"dur"`
	Status    int32          `json:"status"`
	Attrs     map[string]any `json:"attrs"`
	Exception *wireException `json:"exception"`
}

type wireMsg struct {
	V       int        `json:"v"`
	Service string     `json:"service"`
	TraceID string     `json:"trace_id"`
	Spans   []wireSpan `json:"spans"`
	Dropped int        `json:"dropped"`
}

type item struct {
	service string
	span    *tracepb.Span
}

var (
	statReceived   atomic.Int64
	statBadMsg     atomic.Int64
	statQueueDrops atomic.Int64
	statExported   atomic.Int64
	statExportErrs atomic.Int64
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	sockPath := env("OPENLOG_PHP_SOCKET", "/run/openlog/php.sock")
	endpoint := env("OPENLOG_ENDPOINT", "http://localhost:4318") + "/v1/traces"
	key := os.Getenv("OPENLOG_LICENSE_KEY")
	maxBatch, _ := strconv.Atoi(env("OPENLOG_BATCH_SPANS", "2000"))
	flushEvery, _ := time.ParseDuration(env("OPENLOG_FLUSH_INTERVAL", "1s"))
	hostName, _ := os.Hostname()

	_ = os.Remove(sockPath) // stale socket from a previous (killed) forwarder
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sockPath, Net: "unixgram"})
	if err != nil {
		slog.Error("listen", "err", err)
		os.Exit(1)
	}
	_ = os.Chmod(sockPath, 0o777) // PHP-FPM workers run as another user
	_ = conn.SetReadBuffer(8 << 20)

	queue := make(chan item, 50000)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	go receive(conn, queue)
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	go func() {
		t := time.NewTicker(10 * time.Second)
		for range t.C {
			slog.Info("stats", "datagrams", statReceived.Load(), "bad", statBadMsg.Load(), "queue_drops", statQueueDrops.Load(),
				"spans_exported", statExported.Load(), "export_errors", statExportErrs.Load())
		}
	}()

	slog.Info("forwarder listening", "socket", sockPath, "endpoint", endpoint)
	client := &http.Client{Timeout: 5 * time.Second}
	batch := map[string][]*tracepb.Span{}
	n := 0
	flush := func() {
		if n == 0 {
			return
		}
		export(client, endpoint, key, hostName, batch)
		batch = map[string][]*tracepb.Span{}
		n = 0
	}
	ticker := time.NewTicker(flushEvery)
	for {
		select {
		case it := <-queue:
			batch[it.service] = append(batch[it.service], it.span)
			n++
			if n >= maxBatch {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			for len(queue) > 0 {
				it := <-queue
				batch[it.service] = append(batch[it.service], it.span)
				n++
			}
			flush()
			_ = os.Remove(sockPath)
			return
		}
	}
}

func receive(conn *net.UnixConn, queue chan<- item) {
	buf := make([]byte, 128<<10)
	for {
		n, _, err := conn.ReadFromUnix(buf)
		if err != nil {
			return
		}
		statReceived.Add(1)
		var m wireMsg
		if err := json.Unmarshal(buf[:n], &m); err != nil || m.V != 1 {
			statBadMsg.Add(1)
			continue
		}
		traceID, err := hex.DecodeString(m.TraceID)
		if err != nil || len(traceID) != 16 {
			statBadMsg.Add(1)
			continue
		}
		for i := range m.Spans {
			sp := convert(traceID, &m.Spans[i])
			if sp == nil {
				continue
			}
			select {
			case queue <- item{service: m.Service, span: sp}:
			default:
				statQueueDrops.Add(1) // never block the socket reader
			}
		}
	}
}

func convert(traceID []byte, w *wireSpan) *tracepb.Span {
	spanID, err := hex.DecodeString(w.SpanID)
	if err != nil || len(spanID) != 8 {
		return nil
	}
	var parent []byte
	if w.Parent != "" {
		parent, _ = hex.DecodeString(w.Parent)
	}
	sp := &tracepb.Span{
		TraceId:           traceID,
		SpanId:            spanID,
		ParentSpanId:      parent,
		Name:              w.Name,
		Kind:              tracepb.Span_SpanKind(w.Kind),
		StartTimeUnixNano: w.Start,
		EndTimeUnixNano:   w.Start + w.Dur,
		Status:            &tracepb.Status{Code: tracepb.Status_StatusCode(w.Status)},
	}
	for k, v := range w.Attrs {
		sp.Attributes = append(sp.Attributes, kv(k, v))
	}
	if w.Exception != nil {
		sp.Status.Message = w.Exception.Message
		sp.Events = append(sp.Events, &tracepb.Span_Event{
			TimeUnixNano: w.Start + w.Dur,
			Name:         "exception",
			Attributes: []*commonpb.KeyValue{
				kv("exception.type", w.Exception.Type),
				kv("exception.message", w.Exception.Message),
				kv("exception.stacktrace", w.Exception.Where),
			},
		})
	}
	return sp
}

func kv(k string, v any) *commonpb.KeyValue {
	a := &commonpb.AnyValue{}
	switch x := v.(type) {
	case string:
		a.Value = &commonpb.AnyValue_StringValue{StringValue: x}
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			a.Value = &commonpb.AnyValue_IntValue{IntValue: int64(x)}
		} else {
			a.Value = &commonpb.AnyValue_DoubleValue{DoubleValue: x}
		}
	case bool:
		a.Value = &commonpb.AnyValue_BoolValue{BoolValue: x}
	default:
		a.Value = &commonpb.AnyValue_StringValue{StringValue: fmt.Sprint(x)}
	}
	return &commonpb.KeyValue{Key: k, Value: a}
}

func export(client *http.Client, endpoint, key, hostName string, batch map[string][]*tracepb.Span) {
	req := &collectortrace.ExportTraceServiceRequest{}
	total := 0
	for svc, spans := range batch {
		total += len(spans)
		req.ResourceSpans = append(req.ResourceSpans, &tracepb.ResourceSpans{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				kv("service.name", svc),
				kv("host.name", hostName),
				kv("telemetry.sdk.language", "php"),
				kv("telemetry.sdk.name", "openlog-php-agent-spike"),
				kv("openlog.agent.name", "openlog-php-agent"),
				kv("openlog.agent.version", "0.0.1-spike"),
			}},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Scope: &commonpb.InstrumentationScope{Name: "openlog-php-ext", Version: "0.0.1-spike"},
				Spans: spans,
			}},
		})
	}
	body, err := proto.Marshal(req)
	if err != nil {
		statExportErrs.Add(1)
		return
	}
	for attempt := 0; attempt < 3; attempt++ {
		r, _ := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/x-protobuf")
		r.Header.Set("openlog-license-key", key)
		resp, err := client.Do(r)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode/100 == 2 {
				statExported.Add(int64(total))
				return
			}
			if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusServiceUnavailable {
				slog.Warn("export rejected", "status", resp.StatusCode)
				break
			}
		}
		time.Sleep(time.Duration(250*(attempt+1)) * time.Millisecond)
	}
	statExportErrs.Add(1)
}
