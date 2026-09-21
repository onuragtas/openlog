package ingest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/onuragtas/openlog/internal/rum"
)

// The visitor's country arrives in a header, which makes two things worth holding in place: that openlog
// reads it only from the header an operator named, and that it reads nothing when they named none.
//
// The second is the one that matters. OPENLOG_RUM_GEO_HEADER is not a formatting preference — it is the
// statement "something I trust writes this". An installation that has not said so is behind no proxy, so
// the header is whatever the caller decided to send, and honouring it would turn a self-declared string
// into a stored fact about a person's location.

// rumPostWithHeader posts a RUM batch carrying one extra request header. rumPost cannot: it sets only the
// key and the origin, which is all the other tests need.
func rumPostWithHeader(h http.Handler, key, origin, header, value string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/rum", strings.NewReader(rumBody))
	req.Header.Set("Content-Type", ctJSON)
	req.Header.Set("openlog-browser-key", key)
	req.Header.Set("Origin", origin)
	if header != "" {
		req.Header.Set(header, value)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// producedCountry returns geo.country.iso_code of the single produced span.
func producedCountry(t *testing.T, p *fakeProducer) string {
	t.Helper()
	if len(p.msgs) != 1 {
		t.Fatalf("produced %d records, want 1", len(p.msgs))
	}
	var req coltrace.ExportTraceServiceRequest
	if err := proto.Unmarshal(p.msgs[0].Value, &req); err != nil {
		t.Fatal(err)
	}
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				for _, kv := range sp.GetAttributes() {
					if kv.GetKey() == rum.AttrGeoCountry {
						return kv.GetValue().GetStringValue()
					}
				}
			}
		}
	}
	return ""
}

func TestRUMRecordsTheCountryFromTheConfiguredHeader(t *testing.T) {
	s, p, _ := rumService(t)
	s.SetRUMGeoHeader("CF-IPCountry")
	w := rumPostWithHeader(s.HTTPHandler(), "olb_good", "https://shop.example.com", "CF-IPCountry", "tr")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if got := producedCountry(t, p); got != "TR" {
		t.Errorf("country = %q, want TR", got)
	}
}

// The gate, not the parser: a header openlog was never told to trust is a header from the open internet.
func TestRUMIgnoresTheCountryHeaderWhenNoneIsConfigured(t *testing.T) {
	s, p, _ := rumService(t)
	w := rumPostWithHeader(s.HTTPHandler(), "olb_good", "https://shop.example.com", "CF-IPCountry", "TR")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if got := producedCountry(t, p); got != "" {
		t.Errorf("country = %q: an unconfigured header was trusted", got)
	}
}

// Only the named header counts. A proxy that writes CF-IPCountry does not make X-Country trustworthy.
func TestRUMReadsOnlyTheNamedHeader(t *testing.T) {
	s, p, _ := rumService(t)
	s.SetRUMGeoHeader("CF-IPCountry")
	w := rumPostWithHeader(s.HTTPHandler(), "olb_good", "https://shop.example.com", "X-Country", "TR")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if got := producedCountry(t, p); got != "" {
		t.Errorf("country = %q: a header other than the configured one was read", got)
	}
}

func TestRUMDiscardsAnUnusableCountry(t *testing.T) {
	for _, value := range []string{"XX", "T1", "ZZZ", "1", "", "Türkiye"} {
		s, p, _ := rumService(t)
		s.SetRUMGeoHeader("CF-IPCountry")
		w := rumPostWithHeader(s.HTTPHandler(), "olb_good", "https://shop.example.com", "CF-IPCountry", value)
		if w.Code != http.StatusOK {
			t.Fatalf("%q: status = %d", value, w.Code)
		}
		if got := producedCountry(t, p); got != "" {
			t.Errorf("header %q stored country %q, want empty", value, got)
		}
	}
}
