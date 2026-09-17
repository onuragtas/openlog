package rum

import (
	"strconv"
	"strings"

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/onuragtas/openlog/internal/otlputil"
)

// Payload validation (rum.md §3.3). This is the part that makes a public key safe to hand out.
//
// The rule is **rewrite, do not inspect**. A payload arriving with a browser key is not checked for bad
// values and then stored as sent; it is taken apart and rebuilt from the key's own configuration plus an
// allowlist of fields. Anything the SDK sends that is not on the allowlist is dropped, so a field openlog
// learns to interpret later cannot already be under an attacker's control.
//
// Concretely, what a holder of a copied browser key still cannot do:
//
//   - write under another service's name — `service.name` comes from the key (§Resource below), so RUM data
//     can never appear on the APM page of a backend service;
//   - become a host — every resource attribute is discarded, so `host.id` cannot be injected. That matters
//     beyond tidiness: `host.id` drives host↔service linkage and, in SaaS mode, the plan's host limit
//     (gate.go), so an injected one would both corrupt inventory and consume quota;
//   - inflate counts — the sampling weight is computed from the key's `sample_rate`, never from the
//     payload's `tracestate` or `sampling.ratio`;
//   - invent a rollup key — the route is re-normalized here and the vital rating is recomputed from the
//     published thresholds;
//   - write anything that is not RUM — a span without a known `openlog.rum.event` is dropped, so the
//     endpoint cannot be used as a general-purpose trace writer;
//   - spend unbounded storage — every count and length below is capped.

// Bounds of one RUM export request. They are generous for a real page and small enough that a single
// request can never be expensive.
const (
	// MaxSpansPerRequest is the number of RUM events one request may carry.
	MaxSpansPerRequest = 1000
	// MaxAttributesPerSpan bounds the attributes kept on one span.
	MaxAttributesPerSpan = 64
	// MaxAttrValueBytes bounds an ordinary attribute value.
	MaxAttrValueBytes = 2048
	// MaxNameBytes bounds a span name.
	MaxNameBytes = 256
	// MaxMessageBytes bounds an exception message.
	MaxMessageBytes = 1024
	// MaxStackBytes bounds an exception stack trace. Minified bundles produce long frames and the stack is
	// what makes a JS error actionable, so this is the one generous limit.
	MaxStackBytes = 16384
	// MaxEventsPerSpan bounds span events; only `exception` events are kept at all.
	MaxEventsPerSpan = 4
	// MaxURLBytes bounds url.full.
	MaxURLBytes = 1024
)

// Drop reasons reported through Keys.Rejected and the OTLP partial success message.
const (
	ReasonNotRUM       = "not_rum_event"
	ReasonTooManySpans = "too_many_spans"
	ReasonBadVital     = "invalid_vital"
	ReasonNoSession    = "missing_session_id"
)

// Result reports what Sanitize did, so the caller can answer with an OTLP partial success and count metrics.
type Result struct {
	// Kept is the number of spans that survived.
	Kept int
	// Dropped counts removed spans by reason.
	Dropped map[string]int
}

// DroppedTotal is the number of removed spans.
func (r Result) DroppedTotal() int {
	var n int
	for _, v := range r.Dropped {
		n += v
	}
	return n
}

// Message summarizes the drops for the OTLP partial success body, or "" when nothing was dropped.
func (r Result) Message() string {
	if len(r.Dropped) == 0 {
		return ""
	}
	parts := make([]string, 0, len(r.Dropped))
	for _, reason := range []string{ReasonTooManySpans, ReasonNotRUM, ReasonBadVital, ReasonNoSession} {
		if n := r.Dropped[reason]; n > 0 {
			parts = append(parts, strconv.Itoa(n)+" "+reason)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "rejected: " + strings.Join(parts, ", ")
}

func (r *Result) drop(reason string, n int) {
	if n <= 0 {
		return
	}
	if r.Dropped == nil {
		r.Dropped = map[string]int{}
	}
	r.Dropped[reason] += n
}

// resourceAllowed are the browser-reported resource attributes kept, in addition to the ones the server sets
// itself. Everything else is discarded. The list is short on purpose: each entry is a fact about the visitor's
// browser that the UI shows, and nothing here identifies a person or links to infrastructure.
var resourceAllowed = map[string]bool{
	"browser.brands":      true,
	"browser.platform":    true,
	"browser.mobile":      true,
	"browser.language":    true,
	"user_agent.original": true,
	AttrOSName:            true,
	"os.version":          true,
}

// spanAllowed are the span attributes kept on a RUM span. Values are bounded; the ones that decide a rollup
// key (route, vital name, rating, device type) are overwritten by Sanitize rather than taken from here.
var spanAllowed = map[string]bool{
	AttrEvent: true, AttrSessionID: true, AttrPageViewID: true,
	AttrRoute: true, AttrPageViewKind: true,
	AttrVitalName: true, AttrVitalValue: true, AttrVitalRating: true,
	AttrErrorSource: true,
	AttrDeviceType:  true, AttrBrowserName: true, AttrBrowserVersion: true, AttrOSName: true,
	AttrURLPath: true, AttrURLFull: true, AttrURLDomain: true,
	AttrTimingTTFB: true, AttrTimingDNS: true, AttrTimingConnect: true, AttrTimingTLS: true,
	AttrTimingResponse: true, AttrTimingDOMInteractive: true, AttrTimingDOMContentLoaded: true,
	AttrTimingLoadEvent: true,
	// Standard HTTP client attributes of a fetch/XHR span, so a browser request looks like any other
	// client span in the trace view and on the service map.
	"http.request.method":       true,
	"http.response.status_code": true,
	"server.address":            true,
	"error.type":                true,
}

// Sanitize rewrites req in place to exactly what key is allowed to write, dropping everything else. It
// returns what happened; req may end up with no spans at all.
func Sanitize(req *coltrace.ExportTraceServiceRequest, key Key) Result {
	var res Result
	budget := MaxSpansPerRequest
	kept := req.ResourceSpans[:0]
	for _, rs := range req.GetResourceSpans() {
		// The resource is rebuilt, never merged: see the package comment.
		rs.Resource = buildResource(rs.GetResource(), key)
		rs.SchemaUrl = ""
		scopes := rs.ScopeSpans[:0]
		for _, ss := range rs.GetScopeSpans() {
			ss.SchemaUrl = ""
			spans := ss.Spans[:0]
			for _, sp := range ss.GetSpans() {
				if budget <= 0 {
					res.drop(ReasonTooManySpans, 1)
					continue
				}
				reason := sanitizeSpan(sp, key)
				if reason != "" {
					res.drop(reason, 1)
					continue
				}
				budget--
				spans = append(spans, sp)
			}
			ss.Spans = spans
			if len(spans) > 0 {
				scopes = append(scopes, ss)
			}
		}
		rs.ScopeSpans = scopes
		if len(scopes) > 0 {
			kept = append(kept, rs)
		}
	}
	req.ResourceSpans = kept
	res.Kept = MaxSpansPerRequest - budget
	return res
}

// buildResource returns the resource of a RUM payload: the identity openlog assigns, plus the handful of
// browser facts of resourceAllowed.
func buildResource(in *resourcepb.Resource, key Key) *resourcepb.Resource {
	attrs := []*commonpb.KeyValue{
		str(AttrServiceName, key.ServiceName),
		str(AttrEntityType, EntityTypeBrowserApp),
		str(AttrSDKName, SDKName),
		str(AttrSDKLanguage, SDKLanguage),
	}
	if key.Environment != "" {
		attrs = append(attrs, str(AttrEnvironment, key.Environment))
	}
	for _, kv := range in.GetAttributes() {
		k := kv.GetKey()
		if !resourceAllowed[k] {
			continue
		}
		if v := truncate(otlputil.AnyValueString(kv.GetValue()), MaxAttrValueBytes); v != "" {
			attrs = append(attrs, str(k, v))
		}
	}
	// The SDK version is informational; keep the reported one but bound it.
	if v := truncate(otlputil.AttrString(in.GetAttributes(), AttrSDKVersion), 64); v != "" {
		attrs = append(attrs, str(AttrSDKVersion, v))
	}
	return &resourcepb.Resource{Attributes: attrs}
}

// sanitizeSpan rewrites one span and returns a drop reason, or "" to keep it.
func sanitizeSpan(sp *tracepb.Span, key Key) string {
	in := otlputil.AttrsToMap(sp.GetAttributes())
	event := in[AttrEvent]
	if !Events(event) {
		return ReasonNotRUM
	}
	// A session id is what ties a page view to the rest of the visit and what rum_sessions is keyed on.
	// Without one the row would be unattributable, so the span is not worth storing.
	session := hexOnly(in[AttrSessionID], 32)
	if session == "" {
		return ReasonNoSession
	}

	out := make([]*commonpb.KeyValue, 0, MaxAttributesPerSpan)
	add := func(k, v string) {
		if v != "" && len(out) < MaxAttributesPerSpan {
			out = append(out, str(k, v))
		}
	}

	// Server-decided attributes first, so a payload that also sent them cannot win a duplicate.
	add(AttrEvent, event)
	add(AttrSessionID, session)
	add(AttrPageViewID, hexOnly(in[AttrPageViewID], 16))
	route := RouteFromURL(in[AttrRoute], firstNonEmpty(in[AttrURLPath], in[AttrURLFull]))
	add(AttrRoute, route)
	add(AttrDeviceType, Device(in[AttrDeviceType]))

	switch event {
	case EventPageView:
		kind := in[AttrPageViewKind]
		if kind != PageViewRouteChange {
			kind = PageViewLoad
		}
		add(AttrPageViewKind, kind)
	case EventVital:
		name := strings.ToLower(strings.TrimSpace(in[AttrVitalName]))
		if _, ok := Thresholds[name]; !ok {
			return ReasonBadVital
		}
		value, err := strconv.ParseFloat(in[AttrVitalValue], 64)
		if err != nil || value < 0 || value > 1e9 {
			return ReasonBadVital
		}
		add(AttrVitalName, name)
		add(AttrVitalValue, strconv.FormatFloat(value, 'g', -1, 64))
		add(AttrVitalRating, Rate(name, value))
	case EventError:
		if src := in[AttrErrorSource]; src == "unhandledrejection" || src == "console" {
			add(AttrErrorSource, src)
		} else {
			add(AttrErrorSource, "error")
		}
	}

	// The remaining allowlisted attributes, bounded.
	for k, v := range in {
		switch k {
		case AttrEvent, AttrSessionID, AttrPageViewID, AttrRoute, AttrDeviceType,
			AttrPageViewKind, AttrVitalName, AttrVitalValue, AttrVitalRating, AttrErrorSource:
			continue // already decided above
		}
		if !spanAllowed[k] {
			continue
		}
		limit := MaxAttrValueBytes
		if k == AttrURLFull {
			limit = MaxURLBytes
		}
		add(k, truncate(v, limit))
	}
	// The sampling weight is the key's, not the payload's (apm.md §4 rule 3).
	if key.SampleRate > 0 && key.SampleRate < 1 && len(out) < MaxAttributesPerSpan {
		out = append(out, str("sampling.ratio", strconv.FormatFloat(key.SampleRate, 'g', -1, 64)))
	}
	sp.Attributes = out
	// tracestate may carry ot=th/ot=p, which apm.SampleWeight would trust ahead of sampling.ratio.
	sp.TraceState = ""
	sp.Links = nil
	sp.DroppedAttributesCount, sp.DroppedEventsCount, sp.DroppedLinksCount = 0, 0, 0
	sp.Name = truncate(sp.GetName(), MaxNameBytes)

	// Kind is assigned, not accepted: page views, vitals and errors must never become APM entry spans
	// (which need kind server/consumer), so a browser app can never manufacture transactions or Apdex.
	if event == EventResource {
		sp.Kind = tracepb.Span_SPAN_KIND_CLIENT
	} else {
		sp.Kind = tracepb.Span_SPAN_KIND_INTERNAL
	}

	sanitizeEvents(sp, event)
	return ""
}

// sanitizeEvents keeps only `exception` events, bounded, and forces the span status of an error so it reaches
// the error fingerprinting and the error inbox (apm.md §3).
func sanitizeEvents(sp *tracepb.Span, event string) {
	events := sp.Events[:0]
	for _, ev := range sp.GetEvents() {
		if ev.GetName() != "exception" || len(events) >= MaxEventsPerSpan {
			continue
		}
		a := otlputil.AttrsToMap(ev.GetAttributes())
		ev.Attributes = []*commonpb.KeyValue{
			str("exception.type", truncate(firstNonEmpty(a["exception.type"], "Error"), MaxNameBytes)),
			str("exception.message", truncate(a["exception.message"], MaxMessageBytes)),
			str("exception.stacktrace", truncate(a["exception.stacktrace"], MaxStackBytes)),
		}
		ev.DroppedAttributesCount = 0
		events = append(events, ev)
	}
	sp.Events = events
	if event != EventError {
		return
	}
	sp.Status = &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR}
	if len(events) == 0 {
		// An error without an exception event would group as the generic "error" type; give it the span
		// name so the inbox at least separates distinct errors.
		sp.Status.Message = truncate(sp.GetName(), MaxMessageBytes)
	}
}

func str(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

// hexOnly returns v when it is exactly n lower-case hex characters, else "". Ids are used as map keys and
// shown in the UI, so anything else is not worth normalizing.
func hexOnly(v string, n int) string {
	if len(v) != n {
		return ""
	}
	for i := 0; i < n; i++ {
		c := v[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return ""
		}
	}
	return v
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// truncate cuts v to at most n bytes without splitting a UTF-8 rune.
func truncate(v string, n int) string {
	if len(v) <= n {
		return v
	}
	for n > 0 && v[n]&0xc0 == 0x80 {
		n--
	}
	return v[:n]
}
