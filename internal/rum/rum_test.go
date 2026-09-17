package rum

import (
	"math"
	"strings"
	"testing"

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/onuragtas/openlog/internal/otlputil"
)

// ---- origins ----

func TestParseOrigins(t *testing.T) {
	got, err := ParseOrigins([]string{" HTTPS://App.Example.com/ ", "https://*.example.com", "https://app.example.com"})
	if err != nil {
		t.Fatalf("ParseOrigins: %v", err)
	}
	// Lower-cased, trailing slash removed, duplicates collapsed, order kept.
	want := []string{"https://app.example.com", "https://*.example.com"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseOriginsRejects(t *testing.T) {
	cases := map[string][]string{
		"empty list":        {},
		"wildcard all":      {"*"},
		"no scheme":         {"app.example.com"},
		"ftp scheme":        {"ftp://app.example.com"},
		"with a path":       {"https://app.example.com/ingest"},
		"bare wildcard tld": {"https://*.com"},
		"credentials":       {"https://user:pw@app.example.com"},
	}
	for name, in := range cases {
		if _, err := ParseOrigins(in); err == nil {
			t.Errorf("%s: expected an error for %v", name, in)
		}
	}
}

func TestOriginAllowed(t *testing.T) {
	allow := []string{"https://app.example.com", "https://*.wild.example", "http://localhost:3000"}
	cases := []struct {
		origin string
		want   bool
	}{
		{"https://app.example.com", true},
		{"https://APP.example.com", true},   // the header's case is not significant
		{"https://app.example.com/", true},  // a trailing slash is tolerated
		{"http://app.example.com", false},   // scheme must match
		{"https://evil.example.com", false}, // a different host
		{"https://a.wild.example", true},
		{"https://a.b.wild.example", true},     // a wildcard covers any subdomain depth
		{"https://wild.example", false},        // the bare domain is not a subdomain
		{"https://a.wild.example:8443", false}, // a port must be listed exactly
		{"https://nope.wild.example.evil", false},
		{"http://localhost:3000", true},
		{"", false},
		{"null", false}, // a sandboxed iframe: never matched
	}
	for _, c := range cases {
		if got := OriginAllowed(allow, c.origin); got != c.want {
			t.Errorf("OriginAllowed(%q) = %v, want %v", c.origin, got, c.want)
		}
	}
}

// ---- routes ----

func TestRouteFromURL(t *testing.T) {
	cases := []struct{ hint, raw, want string }{
		// A router's own route always wins: no amount of inspection recovers "/orders/:id" from a path.
		{"/orders/:id", "https://shop.example.com/orders/8f3a", "/orders/:id"},
		// Without a hint the path is templated with the APM segment rules.
		{"", "https://shop.example.com/orders/42", "/orders/{id}"},
		{"", "https://shop.example.com/u/2b1e6f9a-1c4d-4f2a-9a3b-7c8d9e0f1a2b", "/u/{uuid}"},
		// The query and the fragment never reach a route: that is where tokens and personal data live.
		{"", "https://shop.example.com/search?q=alice@example.com&token=abc#r", "/search"},
		{"/p?x=1", "", "/p"},
		// A bare path, a full URL and a site root all work.
		{"", "/checkout", "/checkout"},
		{"", "https://shop.example.com", "/"},
		{"", "", "/"},
	}
	for _, c := range cases {
		if got := RouteFromURL(c.hint, c.raw); got != c.want {
			t.Errorf("RouteFromURL(%q, %q) = %q, want %q", c.hint, c.raw, got, c.want)
		}
	}
}

func TestRouteIsBounded(t *testing.T) {
	// The rollup key is this function's output, so a hostile page must not be able to mint a huge one.
	long := "/" + strings.Repeat("segment/", 40)
	got := RouteFromURL("", long)
	if len(got) > MaxRouteBytes {
		t.Errorf("route of %d bytes exceeds the %d byte bound: %q", len(got), MaxRouteBytes, got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("a path past the segment cap should collapse, got %q", got)
	}
	if r := RouteFromURL(strings.Repeat("x", 5000), ""); len(r) > MaxRouteBytes {
		t.Errorf("an oversized hint was not truncated: %d bytes", len(r))
	}
}

// ---- payload sanitizing ----

func attr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

const testSession = "0123456789abcdef0123456789abcdef"

func testKey() Key {
	return Key{KeyID: "k1", TenantID: "t1", ServiceName: "shop-web", Environment: "production",
		Origins: []string{"https://shop.example.com"}, RateLimitPerMinute: 6000, SampleRate: 1}
}

// request builds a one-span export request with the given resource and span attributes.
func request(resource []*commonpb.KeyValue, spans ...*tracepb.Span) *coltrace.ExportTraceServiceRequest {
	return &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource:   &resourcepb.Resource{Attributes: resource},
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}},
	}}}
}

func rumSpan(attrs ...*commonpb.KeyValue) *tracepb.Span {
	base := []*commonpb.KeyValue{attr(AttrEvent, EventPageView), attr(AttrSessionID, testSession)}
	return &tracepb.Span{Name: "pageview /x", Attributes: append(base, attrs...)}
}

// firstSpan returns the only surviving span, failing when there is not exactly one.
func firstSpan(t *testing.T, req *coltrace.ExportTraceServiceRequest) *tracepb.Span {
	t.Helper()
	var spans []*tracepb.Span
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			spans = append(spans, ss.GetSpans()...)
		}
	}
	if len(spans) != 1 {
		t.Fatalf("expected exactly 1 span, got %d", len(spans))
	}
	return spans[0]
}

func TestSanitizeForcesServiceIdentity(t *testing.T) {
	// A copied key must not be able to write under another service's name, or to claim to be a host: the
	// resource is rebuilt from the key, never merged with what the page sent.
	req := request([]*commonpb.KeyValue{
		attr("service.name", "checkout-api"),
		attr("host.id", "prod-db-1"),
		attr("openlog.agent.name", "openlog-infra-agent"),
		attr("browser.platform", "macOS"),
	}, rumSpan())
	Sanitize(req, testKey())

	res := otlputil.AttrsToMap(req.GetResourceSpans()[0].GetResource().GetAttributes())
	if res["service.name"] != "shop-web" {
		t.Errorf("service.name = %q, want the key's shop-web", res["service.name"])
	}
	if res["deployment.environment.name"] != "production" {
		t.Errorf("environment = %q, want production", res["deployment.environment.name"])
	}
	// host.id drives host<->service linkage and the SaaS plan host limit; it must never come from a page.
	if _, ok := res["host.id"]; ok {
		t.Error("host.id survived sanitizing: a browser key could consume host quota and corrupt inventory")
	}
	if _, ok := res["openlog.agent.name"]; ok {
		t.Error("an arbitrary openlog.* resource attribute survived sanitizing")
	}
	if res["browser.platform"] != "macOS" {
		t.Error("an allowlisted browser attribute was dropped")
	}
	if res[AttrEntityType] != EntityTypeBrowserApp {
		t.Errorf("entity type = %q, want %q", res[AttrEntityType], EntityTypeBrowserApp)
	}
}

func TestSanitizeDropsNonRUMSpans(t *testing.T) {
	// The endpoint must not be usable as a general-purpose trace writer.
	plain := &tracepb.Span{Name: "SELECT users", Attributes: []*commonpb.KeyValue{attr("db.system", "postgresql")}}
	req := request(nil, plain, rumSpan())
	res := Sanitize(req, testKey())
	if res.Kept != 1 || res.Dropped[ReasonNotRUM] != 1 {
		t.Fatalf("kept %d, dropped %v; want 1 kept and 1 not_rum_event", res.Kept, res.Dropped)
	}
	if got := firstSpan(t, req).GetName(); got != "pageview /x" {
		t.Errorf("the wrong span survived: %q", got)
	}
}

func TestSanitizeRequiresSessionID(t *testing.T) {
	bad := &tracepb.Span{Name: "pageview", Attributes: []*commonpb.KeyValue{attr(AttrEvent, EventPageView), attr(AttrSessionID, "nope")}}
	res := Sanitize(request(nil, bad), testKey())
	if res.Kept != 0 || res.Dropped[ReasonNoSession] != 1 {
		t.Fatalf("kept %d, dropped %v; want the span dropped for a malformed session id", res.Kept, res.Dropped)
	}
}

func TestSanitizeRecomputesVitalRating(t *testing.T) {
	// The rating decides the good/poor counters in the rollup, so it is never taken from the payload.
	span := &tracepb.Span{Name: "vital lcp", Attributes: []*commonpb.KeyValue{
		attr(AttrEvent, EventVital), attr(AttrSessionID, testSession),
		attr(AttrVitalName, "lcp"), attr(AttrVitalValue, "9000"),
		attr(AttrVitalRating, RatingGood), // a lie: 9 s is poor
	}}
	req := request(nil, span)
	Sanitize(req, testKey())
	got := otlputil.AttrsToMap(firstSpan(t, req).GetAttributes())
	if got[AttrVitalRating] != RatingPoor {
		t.Errorf("rating = %q, want %q (recomputed from the published thresholds)", got[AttrVitalRating], RatingPoor)
	}
}

func TestSanitizeDropsUnknownVitals(t *testing.T) {
	for _, bad := range []*tracepb.Span{
		{Name: "vital x", Attributes: []*commonpb.KeyValue{attr(AttrEvent, EventVital), attr(AttrSessionID, testSession), attr(AttrVitalName, "made_up"), attr(AttrVitalValue, "1")}},
		{Name: "vital lcp", Attributes: []*commonpb.KeyValue{attr(AttrEvent, EventVital), attr(AttrSessionID, testSession), attr(AttrVitalName, "lcp"), attr(AttrVitalValue, "not-a-number")}},
	} {
		if res := Sanitize(request(nil, bad), testKey()); res.Kept != 0 {
			t.Errorf("an invalid vital was stored: %+v", res)
		}
	}
}

func TestSanitizeSetsSamplingWeightFromTheKey(t *testing.T) {
	// A page claiming a tiny sampling ratio would multiply every weighted count in the UI.
	span := rumSpan(attr("sampling.ratio", "0.000001"))
	span.TraceState = "ot=th:0"
	key := testKey()
	key.SampleRate = 0.25
	req := request(nil, span)
	Sanitize(req, key)

	got := firstSpan(t, req)
	if got.GetTraceState() != "" {
		t.Errorf("tracestate = %q, want it cleared (apm.SampleWeight trusts ot=th ahead of sampling.ratio)", got.GetTraceState())
	}
	if ratio := otlputil.AttrsToMap(got.GetAttributes())["sampling.ratio"]; ratio != "0.25" {
		t.Errorf("sampling.ratio = %q, want the key's 0.25", ratio)
	}
}

func TestSanitizeAssignsSpanKind(t *testing.T) {
	// A page view must never be an APM entry span, or a browser app would manufacture transactions.
	span := rumSpan()
	span.Kind = tracepb.Span_SPAN_KIND_SERVER
	req := request(nil, span)
	Sanitize(req, testKey())
	if k := firstSpan(t, req).GetKind(); k != tracepb.Span_SPAN_KIND_INTERNAL {
		t.Errorf("kind = %v, want INTERNAL so it cannot become a transaction", k)
	}
}

func TestSanitizeErrorBecomesGroupableError(t *testing.T) {
	span := &tracepb.Span{Name: "error TypeError", Attributes: []*commonpb.KeyValue{
		attr(AttrEvent, EventError), attr(AttrSessionID, testSession),
	}, Events: []*tracepb.Span_Event{
		{Name: "exception", Attributes: []*commonpb.KeyValue{
			attr("exception.type", "TypeError"),
			attr("exception.message", "x is not a function"),
			attr("exception.stacktrace", strings.Repeat("a", MaxStackBytes+500)),
		}},
		{Name: "custom", Attributes: []*commonpb.KeyValue{attr("k", "v")}},
	}}
	req := request(nil, span)
	Sanitize(req, testKey())
	got := firstSpan(t, req)
	if got.GetStatus().GetCode() != tracepb.Status_STATUS_CODE_ERROR {
		t.Error("an error event must get status ERROR, or apm.Derive computes no error group for it")
	}
	if len(got.GetEvents()) != 1 || got.GetEvents()[0].GetName() != "exception" {
		t.Fatalf("expected only the exception event, got %d events", len(got.GetEvents()))
	}
	stack := otlputil.AttrsToMap(got.GetEvents()[0].GetAttributes())["exception.stacktrace"]
	if len(stack) > MaxStackBytes {
		t.Errorf("stack of %d bytes exceeds the %d byte bound", len(stack), MaxStackBytes)
	}
}

func TestSanitizeBoundsSpanCount(t *testing.T) {
	spans := make([]*tracepb.Span, MaxSpansPerRequest+25)
	for i := range spans {
		spans[i] = rumSpan()
	}
	res := Sanitize(request(nil, spans...), testKey())
	if res.Kept != MaxSpansPerRequest || res.Dropped[ReasonTooManySpans] != 25 {
		t.Fatalf("kept %d, dropped %v; want %d kept and 25 rejected", res.Kept, res.Dropped, MaxSpansPerRequest)
	}
	if msg := res.Message(); !strings.Contains(msg, "too_many_spans") {
		t.Errorf("partial success message %q does not name the reason", msg)
	}
}

func TestSanitizeNormalizesRouteAndDropsUnknownAttributes(t *testing.T) {
	req := request(nil, rumSpan(
		attr(AttrURLPath, "/orders/42"),
		attr(AttrURLFull, "https://shop.example.com/orders/42?token=secret"),
		attr("evil.attribute", "x"),
	))
	Sanitize(req, testKey())
	got := otlputil.AttrsToMap(firstSpan(t, req).GetAttributes())
	if got[AttrRoute] != "/orders/{id}" {
		t.Errorf("route = %q, want the normalized /orders/{id}", got[AttrRoute])
	}
	if _, ok := got["evil.attribute"]; ok {
		t.Error("an attribute outside the allowlist survived")
	}
	if got[AttrDeviceType] != DeviceUnknown {
		t.Errorf("device type = %q, want it normalized to %q", got[AttrDeviceType], DeviceUnknown)
	}
}

// ---- vital thresholds and the histogram ----

func TestRate(t *testing.T) {
	cases := []struct {
		vital string
		value float64
		want  string
	}{
		{VitalLCP, 2500, RatingGood}, // the boundary is inclusive
		{VitalLCP, 2501, RatingNeedsImprovement},
		{VitalLCP, 4001, RatingPoor},
		{VitalCLS, 0.1, RatingGood},
		{VitalCLS, 0.3, RatingPoor},
		{VitalINP, 200, RatingGood},
		{"made_up", 1, ""},
	}
	for _, c := range cases {
		if got := Rate(c.vital, c.value); got != c.want {
			t.Errorf("Rate(%s, %v) = %q, want %q", c.vital, c.value, got, c.want)
		}
	}
}

func TestBucketMatchesClickHouseExpression(t *testing.T) {
	// The Go bucket and the SQL in 0094_rum.sql must agree, or the stored histogram and any Go-side
	// computation would disagree about which bucket a value is in.
	for _, v := range []float64{0.001, 0.05, 0.1, 1, 250, 2500, 9000} {
		want := int16(math.Max(MinBucket, math.Min(MaxBucket, math.Ceil(8*math.Log2(v)))))
		if got := Bucket(v); got != want {
			t.Errorf("Bucket(%v) = %d, want %d", v, got, want)
		}
	}
	if Bucket(0) != MinBucket || Bucket(-1) != MinBucket {
		t.Error("a non-positive value must land in the lowest bucket")
	}
}

func TestHistQuantile(t *testing.T) {
	// A histogram built from known values: the quantile must land in the same bucket as the exact answer,
	// i.e. within one bucket width (< 9.1%).
	values := []float64{100, 200, 400, 800, 1600, 3200}
	counts := map[int16]float64{}
	for _, v := range values {
		counts[Bucket(v)]++
	}
	keys := make([]int16, 0, len(counts))
	vals := make([]float64, 0, len(counts))
	for k, v := range counts {
		keys = append(keys, k)
		vals = append(vals, v)
	}
	h := NewHist(keys, vals)
	if total := h.Total(); total != float64(len(values)) {
		t.Fatalf("total = %v, want %d", total, len(values))
	}
	p50 := h.Quantile(0.5)
	if p50 < 400*0.91 || p50 > 800*1.1 {
		t.Errorf("p50 = %v, expected it near the 400–800 range", p50)
	}
	if p100 := h.Quantile(1); p100 < 3200*0.91 {
		t.Errorf("p100 = %v, expected it near 3200", p100)
	}
	if got := (Hist{}).Quantile(0.75); !math.IsNaN(got) {
		t.Errorf("an empty histogram must give NaN (rendered as null), got %v", got)
	}
}

func TestHistQuantileHandlesSmallValues(t *testing.T) {
	// CLS lives well below 1; the APM floor of bucket -80 would put every good score in one bucket, which
	// is exactly why this scheme clamps at -160.
	h := NewHist([]int16{Bucket(0.02), Bucket(0.05), Bucket(0.3)}, []float64{1, 1, 1})
	p75 := h.Quantile(0.75)
	if p75 < 0.05 || p75 > 0.35 {
		t.Errorf("p75 = %v, expected it between the 0.05 and 0.3 samples", p75)
	}
	if Bucket(0.02) <= MinBucket {
		t.Error("a typical good CLS must not be clamped into the lowest bucket")
	}
}
