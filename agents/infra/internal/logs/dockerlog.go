package logs

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/containers"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// Container log record attributes (semantic-conventions §4).
const (
	SourceContainer = "container"
	AttrIOStream    = "log.iostream"
)

var (
	attrsContainer       = []*commonpb.KeyValue{otlputil.Str(AttrSource, SourceContainer)}
	attrsContainerStdout = []*commonpb.KeyValue{otlputil.Str(AttrSource, SourceContainer), otlputil.Str(AttrIOStream, "stdout")}
	attrsContainerStderr = []*commonpb.KeyValue{otlputil.Str(AttrSource, SourceContainer), otlputil.Str(AttrIOStream, "stderr")}
)

// dockerJSONLine is one entry of Docker's json-file log driver:
// {"log":"message\n","stream":"stdout","time":"2026-09-14T10:00:00.123456789Z"}.
type dockerJSONLine struct {
	Log    string    `json:"log"`
	Stream string    `json:"stream"`
	Time   time.Time `json:"time"`
}

// parseDockerJSON decodes one json-file line; ok is false for anything else.
func parseDockerJSON(line []byte) (dockerJSONLine, bool) {
	var e dockerJSONLine
	if len(line) == 0 || line[0] != '{' || json.Unmarshal(line, &e) != nil {
		return dockerJSONLine{}, false
	}
	return e, true
}

func streamIndex(name string) int {
	switch name {
	case "stdout":
		return containers.StreamStdout
	case "stderr":
		return containers.StreamStderr
	}
	return 0
}

func streamName(i int) string {
	switch i {
	case containers.StreamStdout:
		return "stdout"
	case containers.StreamStderr:
		return "stderr"
	}
	return ""
}

// pendingLine collects the parts of one log message. Docker splits messages longer than
// 16 KiB into several entries (json-file) or frames (API); only the last ends with "\n".
type pendingLine struct {
	buf       []byte
	ts        time.Time // time of the first part
	start     int64     // file offset of the first part (json-file)
	started   bool
	truncated bool
}

// add appends b, keeping at most maxBytes (the rest of the message is dropped and the line marked truncated).
func (p *pendingLine) add(b []byte, maxBytes int) {
	p.started = true
	if maxBytes > 0 && len(p.buf)+len(b) > maxBytes {
		b = b[:max(0, maxBytes-len(p.buf))]
		p.truncated = true
	}
	p.buf = append(p.buf, b...)
}

func (p *pendingLine) reset() {
	p.buf, p.ts, p.start, p.started, p.truncated = p.buf[:0], time.Time{}, 0, false, false
}

// emitLineFunc receives a complete line (valid only during the call); false stops the stream.
type emitLineFunc func(stream int, line []byte, ts time.Time, truncated bool) bool

// lineSplitter turns a Docker log stream (timestamps=1) into lines. Multiplexed frames carry one
// message each with its own "<RFC3339Nano> " prefix, also on every part of a split message;
// raw (TTY) streams are arbitrary chunks of "<timestamp> text\n".
type lineSplitter struct {
	raw      bool
	maxBytes int
	parts    [3]pendingLine
}

func (s *lineSplitter) push(stream int, payload []byte, emit emitLineFunc) bool {
	if stream < 0 || stream >= len(s.parts) {
		stream = containers.StreamStdout
	}
	p := &s.parts[stream]
	limit := s.maxBytes
	if s.raw && limit > 0 {
		limit += 64 // raw lines still carry their timestamp prefix
	}
	var ts time.Time
	if !s.raw {
		if t, rest, ok := containers.SplitTimestamp(payload); ok {
			ts, payload = t, rest
		}
		if !p.started {
			p.ts = ts
		}
	}
	for {
		i := bytes.IndexByte(payload, '\n')
		if i < 0 {
			if len(payload) > 0 {
				p.add(payload, limit)
			}
			return true
		}
		p.add(payload[:i], limit)
		if !s.complete(stream, p, emit) {
			return false
		}
		payload = payload[i+1:]
		if len(payload) == 0 {
			return true
		}
		p.ts = ts
	}
}

func (s *lineSplitter) complete(stream int, p *pendingLine, emit emitLineFunc) bool {
	line, ts := p.buf, p.ts
	if s.raw {
		if t, rest, ok := containers.SplitTimestamp(line); ok {
			ts, line = t, rest
		}
	}
	ok := emit(stream, bytes.TrimSuffix(line, []byte{'\r'}), ts, p.truncated)
	p.reset()
	return ok
}

// flush emits unterminated messages (end of the stream).
func (s *lineSplitter) flush(emit emitLineFunc) {
	for i := range s.parts {
		if s.parts[i].started {
			if !s.complete(i, &s.parts[i], emit) {
				return
			}
		}
	}
}

// Trace context in JSON log bodies (structured loggers of OTel-instrumented applications).
const maxTraceScanBytes = 16 << 10

var (
	traceIDKeys = []string{"trace_id", "traceId", "traceid", "trace.id", "otelTraceID", "TraceId", "traceID"}
	spanIDKeys  = []string{"span_id", "spanId", "spanid", "span.id", "otelSpanID", "SpanId", "spanID"}
)

// traceContext returns the trace and span id of a JSON object body with a 32-hex trace id
// field (trace_id, traceId, trace.id, otelTraceID, …) and an optional 16-hex span id.
func traceContext(body string) (traceID, spanID []byte) {
	s := strings.TrimSpace(body)
	if len(s) < 2 || s[0] != '{' || len(s) > maxTraceScanBytes || !strings.Contains(s, "race") {
		return nil, nil
	}
	var m map[string]any
	if json.Unmarshal([]byte(s), &m) != nil {
		return nil, nil
	}
	if traceID = hexField(m, traceIDKeys, 16); traceID == nil {
		return nil, nil
	}
	return traceID, hexField(m, spanIDKeys, 8)
}

func hexField(m map[string]any, keys []string, n int) []byte {
	for _, k := range keys {
		v, ok := m[k].(string)
		if !ok || len(v) != 2*n {
			continue
		}
		b, err := hex.DecodeString(v)
		if err != nil {
			continue
		}
		for _, c := range b {
			if c != 0 {
				return b
			}
		}
	}
	return nil
}

// containerRecord builds a container log record: body limits and masking as for files,
// log.iostream, the Docker timestamp, severity heuristic and trace context of JSON bodies.
func (m *Manager) containerRecord(body []byte, stream string, ts time.Time, truncated bool, observed time.Time) (*logspb.LogRecord, int) {
	b, trunc := sanitize(string(body), m.cfg.MaxLineBytes, m.cfg.MaskSecrets)
	attrs := attrsContainer
	switch stream {
	case "stdout":
		attrs = attrsContainerStdout
	case "stderr":
		attrs = attrsContainerStderr
	}
	if truncated || trunc {
		attrs = append(append(make([]*commonpb.KeyValue, 0, len(attrs)+1), attrs...), otlputil.Bool(AttrTruncated, true))
	}
	rec := &logspb.LogRecord{
		ObservedTimeUnixNano: uint64(observed.UnixNano()),
		Body:                 &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: b}},
		Attributes:           attrs,
	}
	if !ts.IsZero() {
		rec.TimeUnixNano = uint64(ts.UnixNano())
	}
	if m.cfg.ParseSeverity {
		rec.SeverityNumber, rec.SeverityText = ParseSeverity(b)
	}
	rec.TraceId, rec.SpanId = traceContext(b)
	return rec, len(b)
}
