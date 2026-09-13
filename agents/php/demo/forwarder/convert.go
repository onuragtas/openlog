// Copied from agents/infra/internal/phpforwarder/convert.go (keep in sync); standalone demo forwarder.
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"

	"google.golang.org/protobuf/proto"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// ScopeName is the instrumentation scope of converted PHP spans.
const ScopeName = "openlog-php"

// Attributes set by the forwarder (php-agent.md §2.1, §2.3).
const (
	AttrSamplingRatio = "sampling.ratio"
	AttrDroppedSpans  = "openlog.php.dropped_spans"
	AttrIncomplete    = "openlog.php.incomplete"
)

const (
	flagSampled     = 0x01
	flagHasIsRemote = uint32(tracepb.SpanFlags_SPAN_FLAGS_CONTEXT_HAS_IS_REMOTE_MASK)
	flagIsRemote    = uint32(tracepb.SpanFlags_SPAN_FLAGS_CONTEXT_IS_REMOTE_MASK)
)

// hostAttributes selects the infra agent resource attributes added to PHP resources: host.*, os.* and
// openlog.agent.* (php-agent.md §2.1).
func hostAttributes(res *resourcepb.Resource) []*commonpb.KeyValue {
	var out []*commonpb.KeyValue
	for _, kv := range res.GetAttributes() {
		if strings.HasPrefix(kv.Key, "host.") || strings.HasPrefix(kv.Key, "os.") || strings.HasPrefix(kv.Key, "openlog.agent.") {
			out = append(out, kv)
		}
	}
	return out
}

// converted is one trace ready for batching.
type converted struct {
	resourceKey string
	resource    []*commonpb.KeyValue // extension attributes only; host attributes are added per batch
	scopeVer    string
	spans       []*tracepb.Span
}

// convertTrace merges the parts of a trace into spans with the forwarder attributes: sampling.ratio and
// openlog.php.dropped_spans on the root span, openlog.php.incomplete on the root (or on every span without a root),
// and span flags that tell the backend whether a span's parent is local (same trace message) or remote.
func convertTrace(t *trace) converted {
	first := t.parts[0]
	c := converted{resource: first.resource}
	var sb strings.Builder
	for _, kv := range first.resource {
		sb.WriteString(kv.Key)
		sb.WriteByte(0)
		sb.WriteString(kv.Value.GetStringValue())
		sb.WriteByte(0)
		if kv.Key == "telemetry.distro.version" {
			c.scopeVer = kv.Value.GetStringValue()
		}
	}
	c.resourceKey = sb.String()

	var dropped int64
	n := 0
	for _, m := range t.parts {
		dropped = max(dropped, m.droppedSpans)
		n += len(m.spans)
	}
	c.spans = make([]*tracepb.Span, 0, n)
	ids := make(map[[8]byte]bool, n)
	for _, m := range t.parts {
		for _, sp := range m.spans {
			if !ids[[8]byte(sp.SpanId)] {
				ids[[8]byte(sp.SpanId)] = true
				c.spans = append(c.spans, sp)
			}
		}
	}

	var root *tracepb.Span
	var orphans []*tracepb.Span
	for _, sp := range c.spans {
		flags := uint32(flagSampled)
		switch {
		case len(sp.ParentSpanId) == 0:
			orphans = append(orphans, sp)
			if root == nil {
				root = sp
			}
		case ids[[8]byte(sp.ParentSpanId)]:
			flags |= flagHasIsRemote // parent is local
		default:
			orphans = append(orphans, sp)
			if t.complete {
				flags |= flagHasIsRemote | flagIsRemote // continued from an incoming traceparent
			}
		}
		sp.Flags = flags
	}
	if root == nil && len(orphans) > 0 && (t.complete || len(orphans) == 1) {
		root = orphans[0]
		for _, sp := range orphans[1:] {
			if sp.StartTimeUnixNano < root.StartTimeUnixNano {
				root = sp
			}
		}
	}

	ratio := first.samplingRatio
	if root != nil {
		setAttr(root, AttrSamplingRatio, &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: ratio}})
		if dropped > 0 {
			setAttr(root, AttrDroppedSpans, &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: dropped}})
		}
		if !t.complete {
			setAttr(root, AttrIncomplete, &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: true}})
		}
		return c
	}
	for _, sp := range c.spans {
		setAttr(sp, AttrSamplingRatio, &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: ratio}})
		setAttr(sp, AttrIncomplete, &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: true}})
	}
	return c
}

// setAttr sets or replaces an attribute; the forwarder's value wins over the extension's.
func setAttr(sp *tracepb.Span, key string, v *commonpb.AnyValue) {
	for _, kv := range sp.Attributes {
		if kv.Key == key {
			kv.Value = v
			return
		}
	}
	sp.Attributes = append(sp.Attributes, &commonpb.KeyValue{Key: key, Value: v})
}

// batcher groups converted traces into TracesData payloads with one ResourceSpans per distinct resource.
type batcher struct {
	host     []*commonpb.KeyValue
	maxBytes int

	groups map[string]*tracepb.ResourceSpans
	order  []string
	spans  int
	size   int
}

func newBatcher(host []*commonpb.KeyValue, maxBytes int) *batcher {
	return &batcher{host: host, maxBytes: maxBytes, groups: map[string]*tracepb.ResourceSpans{}}
}

// add appends the spans of c and returns full payloads (the batch is flushed whenever it reaches maxBytes, even in
// the middle of a trace: span flags keep entry detection independent of the request a span travels in).
func (b *batcher) add(c converted) []*tracepb.TracesData {
	var out []*tracepb.TracesData
	for _, sp := range c.spans {
		rs := b.groups[c.resourceKey]
		if rs == nil {
			attrs := make([]*commonpb.KeyValue, 0, len(c.resource)+len(b.host)+1)
			attrs = append(attrs, c.resource...)
			attrs = append(attrs, strKV("telemetry.sdk.language", "php"))
			attrs = append(attrs, b.host...)
			rs = &tracepb.ResourceSpans{
				Resource:   &resourcepb.Resource{Attributes: attrs},
				ScopeSpans: []*tracepb.ScopeSpans{{Scope: &commonpb.InstrumentationScope{Name: ScopeName, Version: c.scopeVer}}},
			}
			b.groups[c.resourceKey] = rs
			b.order = append(b.order, c.resourceKey)
			b.size += proto.Size(rs)
		}
		rs.ScopeSpans[0].Spans = append(rs.ScopeSpans[0].Spans, sp)
		b.spans++
		b.size += proto.Size(sp) + 4
		if b.size >= b.maxBytes {
			out = append(out, b.take())
		}
	}
	return out
}

// take returns the current batch (nil when empty) and starts a new one.
func (b *batcher) take() *tracepb.TracesData {
	if b.spans == 0 {
		return nil
	}
	td := &tracepb.TracesData{ResourceSpans: make([]*tracepb.ResourceSpans, 0, len(b.order))}
	for _, k := range b.order {
		td.ResourceSpans = append(td.ResourceSpans, b.groups[k])
	}
	b.groups, b.order, b.spans, b.size = map[string]*tracepb.ResourceSpans{}, nil, 0, 0
	return td
}

// SpanCount counts the spans of a payload.
func SpanCount(td *tracepb.TracesData) int {
	n := 0
	for _, rs := range td.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			n += len(ss.Spans)
		}
	}
	return n
}
