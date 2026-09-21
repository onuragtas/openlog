package rum

import (
	"math"
	"sort"
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
	ReasonBadCustom    = "invalid_custom_event"
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
	// A mobile application has no user agent and no browser, so nothing above describes it. These are the
	// OTel names for what it can say about itself (mobile-agent.md).
	//
	// `service.version` is accepted from the payload while `service.name` is forced from the key, and the
	// asymmetry is deliberate: the name decides which application's data this is and must not be claimable,
	// while the version only labels the build within that application — a key that lies about it pollutes
	// its own release health and nothing else. It is also what release health will group by.
	"service.version":         true,
	"device.model.identifier": true,
	"device.manufacturer":     true,
}

// spanAllowed are the span attributes kept on a RUM span. Values are bounded; the ones that decide a rollup
// key (route, vital name, rating, device type) are overwritten by Sanitize rather than taken from here.
var spanAllowed = map[string]bool{
	AttrEvent: true, AttrSessionID: true, AttrPageViewID: true,
	AttrRoute: true, AttrPageViewKind: true,
	AttrVitalName: true, AttrVitalValue: true, AttrVitalRating: true,
	AttrErrorSource: true,
	// The application's own identifier for the person using it (identify()). Accepted from the payload —
	// unlike the country, which is forced — because nothing on the server can know it. Bounded, never
	// interpreted: openlog cannot tell an opaque account key from an e-mail address, which is why rum.md
	// §3.7 asks the operator for the former.
	AttrUserID:     true,
	AttrCustomName: true, AttrCustomValue: true, AttrCustomUnit: true,
	AttrDeviceType: true, AttrBrowserName: true, AttrBrowserVersion: true, AttrOSName: true,
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
	// Static asset timing. Accepted rather than derived: only the browser knows what it fetched and
	// whether the cache answered.
	AttrResourceInitiator: true, AttrResourceTransferSize: true,
	AttrResourceEncodedSize: true, AttrResourceCached: true,
}

// Sanitize rewrites req in place to exactly what key is allowed to write, dropping everything else. It
// returns what happened; req may end up with no spans at all.
//
// sc is what the server established about the request rather than what the payload claims — currently the
// visitor's country, resolved by a trusted proxy. It is written onto every span here, where a value the
// payload also sent cannot win.
func Sanitize(req *coltrace.ExportTraceServiceRequest, key Key, sc Scope) Result {
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
				reason := sanitizeSpan(sp, key, sc)
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
func sanitizeSpan(sp *tracepb.Span, key Key, sc Scope) string {
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
	// Forced like service.name: a country a page could send is a country a page could invent. Empty when no
	// trusted proxy resolved one, which is an honest blank rather than a guess.
	add(AttrGeoCountry, sc.Country)

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
	case EventCustom:
		// The name is what every query written against a custom event groups by, so an unnamed one is not a
		// smaller event — it is an unqueryable row. The value is optional: counting is as valid as timing.
		name := truncate(strings.TrimSpace(in[AttrCustomName]), MaxCustomNameBytes)
		if name == "" {
			return ReasonBadCustom
		}
		add(AttrCustomName, name)
		if raw, ok := in[AttrCustomValue]; ok && strings.TrimSpace(raw) != "" {
			value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < -MaxCustomValue || value > MaxCustomValue {
				return ReasonBadCustom
			}
			add(AttrCustomValue, strconv.FormatFloat(value, 'g', -1, 64))
			if unit := truncate(strings.TrimSpace(in[AttrCustomUnit]), MaxCustomUnitBytes); unit != "" {
				add(AttrCustomUnit, unit)
			}
		}
		// Parameters are sorted before the cap is applied, not taken as they come: Go map order is random,
		// so "the first 16" would otherwise mean a different 16 on every request, and which parameter
		// survived would depend on the run rather than on the payload.
		keys := make([]string, 0, MaxCustomParams)
		for k := range in {
			if strings.HasPrefix(k, AttrCustomParamPrefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for i, k := range keys {
			if i >= MaxCustomParams {
				break
			}
			name := strings.TrimPrefix(k, AttrCustomParamPrefix)
			if name == "" || len(name) > MaxCustomParamKeyBytes || !validParamKey(name) {
				continue
			}
			add(k, truncate(in[k], MaxCustomParamValueBytes))
		}
	}

	// The remaining allowlisted attributes, bounded.
	for k, v := range in {
		switch k {
		case AttrEvent, AttrSessionID, AttrPageViewID, AttrRoute, AttrDeviceType,
			AttrPageViewKind, AttrVitalName, AttrVitalValue, AttrVitalRating, AttrErrorSource,
			AttrCustomName, AttrCustomValue, AttrCustomUnit:
			continue // already decided above
		}
		// Custom event parameters were decided above, in a namespace no allowlist entry could cover.
		if strings.HasPrefix(k, AttrCustomParamPrefix) || !spanAllowed[k] {
			continue
		}
		limit := MaxAttrValueBytes
		switch k {
		case AttrURLFull:
			limit = MaxURLBytes
		case AttrUserID:
			limit = MaxUserIDBytes
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

// validParamKey reports whether a custom event parameter name is one a query can address without quoting:
// lower-case letters, digits, dot, underscore and hyphen. A name outside that is dropped rather than escaped
// — the application chose it, and a name that cannot be written in a FACET is not a usable dimension.
func validParamKey(k string) bool {
	for _, r := range k {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
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
