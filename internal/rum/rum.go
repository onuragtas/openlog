// Package rum implements real user monitoring (docs/contracts/rum.md, D-136): the attribute contract of the
// browser SDK, the validation and bounding of what a public browser key may write, and the query helpers the
// API reads the rollups with.
//
// The package deliberately has no storage of its own. The browser SDK sends OTLP spans, so a page view, a web
// vital and a JS error are ordinary rows of `spans` and reuse the trace model, the error fingerprinting and
// the error inbox that already exist (apm.md §3). What lives here is everything that must not be trusted: a
// browser key is public, so every payload arriving with one is rewritten to what the key is allowed to say
// before it reaches Kafka.
package rum

// Attribute names of the openlog browser SDK (semantic-conventions.md §10). Where OpenTelemetry has a name
// for a concept — `session.id`, `browser.*`, `url.*`, `device.type` — openlog uses it; the openlog-specific
// ones carry the `openlog.rum.` prefix.
const (
	// AttrEvent marks a span as RUM and says what it is: the one attribute every materialized view of
	// 0094_rum.sql keys off, and the one the ingest requires before accepting a span from a browser key.
	AttrEvent = "openlog.rum.event"

	// AttrSessionID is the SDK's session identifier (32 hex characters).
	AttrSessionID = "session.id"
	// AttrPageViewID ties vitals, errors and requests to the page view they happened on (16 hex characters).
	AttrPageViewID = "openlog.rum.page_view.id"

	// AttrRoute is the normalized route (§4). The server always recomputes it: a page cannot choose its own
	// rollup key, or one loop could mint a million of them.
	AttrRoute = "openlog.rum.route"
	// AttrPageViewKind is "load" or "route_change".
	AttrPageViewKind = "openlog.rum.page_view.kind"

	// AttrVitalName, AttrVitalValue and AttrVitalRating carry one Core Web Vital measurement.
	AttrVitalName   = "openlog.rum.vital.name"
	AttrVitalValue  = "openlog.rum.vital.value"
	AttrVitalRating = "openlog.rum.vital.rating"

	// AttrErrorSource says how an error reached the SDK: "error" (window.onerror),
	// "unhandledrejection" or "console" (console.error, opt-in).
	AttrErrorSource = "openlog.rum.error.source"

	// Device and browser facets of the rollups.
	AttrDeviceType     = "device.type"
	AttrBrowserName    = "browser.name"
	AttrBrowserVersion = "browser.version"
	AttrOSName         = "os.name"

	// URL attributes (OTel). AttrURLPath is the raw path, kept on the span for the detail view; only
	// AttrRoute reaches a rollup key.
	AttrURLPath   = "url.path"
	AttrURLFull   = "url.full"
	AttrURLDomain = "url.domain"

	// Navigation Timing phases in milliseconds (§2.2).
	AttrTimingTTFB             = "openlog.rum.timing.ttfb_ms"
	AttrTimingDNS              = "openlog.rum.timing.dns_ms"
	AttrTimingConnect          = "openlog.rum.timing.connect_ms"
	AttrTimingTLS              = "openlog.rum.timing.tls_ms"
	AttrTimingResponse         = "openlog.rum.timing.response_ms"
	AttrTimingDOMInteractive   = "openlog.rum.timing.dom_interactive_ms"
	AttrTimingDOMContentLoaded = "openlog.rum.timing.dom_content_loaded_ms"
	AttrTimingLoadEvent        = "openlog.rum.timing.load_event_ms"
)

// Resource attributes the ingest sets on every RUM resource (semantic-conventions.md §10). They are set, not
// accepted: `service.name` comes from the browser key, never from the payload, so a copied key cannot be
// used to write telemetry under the name of a service a backend agent owns.
const (
	AttrServiceName = "service.name"
	AttrEnvironment = "deployment.environment.name"
	AttrEntityType  = "openlog.entity.type"
	// EntityTypeBrowserApp marks the resource as a browser application rather than a host or a service
	// somebody deployed, so the UI can tell a RUM app from an APM service without a second table.
	EntityTypeBrowserApp = "browser_app"

	AttrSDKName     = "telemetry.sdk.name"
	AttrSDKLanguage = "telemetry.sdk.language"
	AttrSDKVersion  = "telemetry.sdk.version"

	// SDKName and SDKLanguage identify the openlog browser SDK (agents/browser).
	SDKName     = "openlog-browser"
	SDKLanguage = "webjs"
)

// Event kinds of AttrEvent.
const (
	EventPageView = "page_view"
	EventVital    = "vital"
	EventError    = "error"
	EventResource = "resource"
)

// Events reports whether v is a known event kind. Anything else is rejected: an unknown kind would be stored
// as a span nothing ever reads, which is exactly the free storage a public key must not buy.
func Events(v string) bool {
	switch v {
	case EventPageView, EventVital, EventError, EventResource:
		return true
	}
	return false
}

// Page view kinds.
const (
	PageViewLoad        = "load"
	PageViewRouteChange = "route_change"
)

// Vital names (§2). Anything else is dropped rather than stored under a name no UI knows.
const (
	VitalLCP  = "lcp"
	VitalINP  = "inp"
	VitalCLS  = "cls"
	VitalFCP  = "fcp"
	VitalTTFB = "ttfb"
)

// Vitals lists the collected vitals in display order: the three Core Web Vitals first, then the two
// diagnostics that explain them.
var Vitals = []string{VitalLCP, VitalINP, VitalCLS, VitalFCP, VitalTTFB}

// Ratings of a vital measurement.
const (
	RatingGood             = "good"
	RatingNeedsImprovement = "needs_improvement"
	RatingPoor             = "poor"
)

// Threshold is the good/poor boundary of one vital. The values are the published Core Web Vitals thresholds:
// at or below Good is "good", above Poor is "poor", in between "needs improvement". They are constants
// rather than settings on purpose — the point of the programme is that everybody measures against the same
// bar, and an installation that could move it would be reporting a number that means nothing to anyone else.
type Threshold struct {
	// Unit is "ms" for the time-based vitals and "" for the unitless CLS.
	Unit string
	Good float64
	Poor float64
}

// Thresholds are the Core Web Vitals boundaries (web.dev/vitals; LCP 2.5s/4s, INP 200ms/500ms,
// CLS 0.1/0.25) plus the two diagnostics (FCP 1.8s/3s, TTFB 800ms/1.8s).
var Thresholds = map[string]Threshold{
	VitalLCP:  {Unit: "ms", Good: 2500, Poor: 4000},
	VitalINP:  {Unit: "ms", Good: 200, Poor: 500},
	VitalCLS:  {Unit: "", Good: 0.1, Poor: 0.25},
	VitalFCP:  {Unit: "ms", Good: 1800, Poor: 3000},
	VitalTTFB: {Unit: "ms", Good: 800, Poor: 1800},
}

// Rate returns the rating of value for vital, or "" for an unknown vital. The rating is always computed
// here, never taken from the payload: it is what the rollup counts good/poor by, and a page must not be
// able to report its own LCP as "good".
func Rate(vital string, value float64) string {
	t, ok := Thresholds[vital]
	if !ok {
		return ""
	}
	switch {
	case value <= t.Good:
		return RatingGood
	case value <= t.Poor:
		return RatingNeedsImprovement
	default:
		return RatingPoor
	}
}

// Device types of AttrDeviceType.
const (
	DeviceDesktop = "desktop"
	DeviceMobile  = "mobile"
	DeviceTablet  = "tablet"
	DeviceUnknown = "unknown"
)

// Device normalizes a device type the SDK reported; anything unrecognised becomes DeviceUnknown rather than
// a new facet value in the rollup key.
func Device(v string) string {
	switch v {
	case DeviceDesktop, DeviceMobile, DeviceTablet:
		return v
	}
	return DeviceUnknown
}
