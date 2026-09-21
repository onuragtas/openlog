package rum

import (
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// Sanitize keeps a closed list of attributes and silently drops everything else, which is the right default
// and also the reason a new field is worth a test: an attribute the SDK sends and the server does not know
// does not fail anywhere — it simply never appears, and the screen that needed it is empty for a reason
// nobody can see from either side.

func resourceSpan(attrs ...*commonpb.KeyValue) *tracepb.Span {
	base := []*commonpb.KeyValue{attr(AttrEvent, EventResource), attr(AttrSessionID, testSession)}
	return &tracepb.Span{Name: "GET /assets/app.js", Attributes: append(base, attrs...)}
}

func resAttr(req interface {
	GetResourceSpans() []*tracepb.ResourceSpans
}, key string) string {
	for _, rs := range req.GetResourceSpans() {
		for _, kv := range rs.GetResource().GetAttributes() {
			if kv.GetKey() == key {
				return kv.GetValue().GetStringValue()
			}
		}
	}
	return ""
}

func TestSanitizeKeepsStaticAssetTiming(t *testing.T) {
	req := request(nil, resourceSpan(
		attr(AttrResourceInitiator, "script"),
		attr(AttrResourceTransferSize, "18422"),
		attr(AttrResourceEncodedSize, "18422"),
		attr(AttrResourceCached, "false"),
	))
	if res := Sanitize(req, testKey(), Scope{}); res.Kept != 1 {
		t.Fatalf("kept %d spans", res.Kept)
	}
	sp := firstSpan(t, req)
	for key, want := range map[string]string{
		AttrResourceInitiator:    "script",
		AttrResourceTransferSize: "18422",
		AttrResourceEncodedSize:  "18422",
		AttrResourceCached:       "false",
	} {
		if got := spanAttr(sp, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// The namespace is not a wildcard: a field the server has not been taught is dropped like any other.
func TestSanitizeDropsUnknownResourceAttributes(t *testing.T) {
	req := request(nil, resourceSpan(attr("openlog.rum.resource.protocol", "h2")))
	Sanitize(req, testKey(), Scope{})
	if got := spanAttr(firstSpan(t, req), "openlog.rum.resource.protocol"); got != "" {
		t.Errorf("an attribute nothing knows about was stored: %q", got)
	}
}

// A mobile application describes itself with none of the browser fields, so these had to be added before a
// mobile client could say anything true about the build it is running (mobile-agent.md).
func TestSanitizeKeepsMobileResourceAttributes(t *testing.T) {
	req := request([]*commonpb.KeyValue{
		attr("service.version", "4.2.1"),
		attr("device.model.identifier", "iPhone15,2"),
		attr("device.manufacturer", "Apple"),
		attr("service.name", "i-am-not-shop-web"),
		attr("device.id", "a-persistent-device-identifier"),
	}, resourceSpan())
	Sanitize(req, testKey(), Scope{})

	for key, want := range map[string]string{
		"service.version":         "4.2.1",
		"device.model.identifier": "iPhone15,2",
		"device.manufacturer":     "Apple",
	} {
		if got := resAttr(req, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	// The name still comes from the key, whatever the payload claims: that is what stops a copied key
	// writing under another application's identity.
	if got := resAttr(req, AttrServiceName); got != testKey().ServiceName {
		t.Errorf("service.name = %q, want the key's %q", got, testKey().ServiceName)
	}
	// device.id is deliberately absent from the allowlist: a persistent device identifier is a privacy
	// decision, not an oversight, and it must not arrive by being sent.
	if got := resAttr(req, "device.id"); got != "" {
		t.Errorf("device.id was stored: %q", got)
	}
}
