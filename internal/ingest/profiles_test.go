package ingest

import (
	"bytes"
	"net/http"
	"testing"

	"go.opentelemetry.io/collector/pdata/pprofile/pprofileotlp"

	"github.com/onuragtas/openlog/internal/queue"
)

// profilesPayload builds an OTLP profiles request: one CPU sample of main → handleRequest → db.Query, with
// everything in the dictionary and the sample pointing at it by index, the way an agent sends it.
func profilesPayload(t *testing.T, service string) []byte {
	t.Helper()
	req := pprofileotlp.NewExportRequest()
	pf := req.Profiles()
	dict := pf.Dictionary()
	strs := dict.StringTable()
	seen := map[string]int32{}
	str := func(s string) int32 {
		if i, ok := seen[s]; ok {
			return i
		}
		strs.Append(s)
		i := int32(strs.Len() - 1)
		seen[s] = i
		return i
	}
	str("") // index 0 is the empty string by convention
	st := dict.StackTable().AppendEmpty()
	for _, name := range []string{"db.Query", "handleRequest", "main"} { // OTLP orders a stack leaf first
		f := dict.FunctionTable().AppendEmpty()
		f.SetNameStrindex(str(name))
		loc := dict.LocationTable().AppendEmpty()
		loc.Lines().AppendEmpty().SetFunctionIndex(int32(dict.FunctionTable().Len() - 1))
		st.LocationIndices().Append(int32(dict.LocationTable().Len() - 1))
	}
	rp := pf.ResourceProfiles().AppendEmpty()
	if service != "" {
		rp.Resource().Attributes().PutStr("service.name", service)
	}
	p := rp.ScopeProfiles().AppendEmpty().Profiles().AppendEmpty()
	p.SampleType().SetTypeStrindex(str("cpu"))
	p.SampleType().SetUnitStrindex(str("nanoseconds"))
	p.SetDurationNano(10_000_000_000)
	s := p.Samples().AppendEmpty()
	s.SetStackIndex(0)
	s.Values().Append(1_500_000)
	b, err := req.MarshalProto()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestHTTPProfilesProducesUnchanged(t *testing.T) {
	fp := &fakeProducer{}
	s := newTestService(t, fp, 1<<20)
	body := profilesPayload(t, "checkout")

	rec := post(s.HTTPHandler(), "/v1/profiles", ctProtobuf, "goodkey", body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(fp.msgs) != 1 {
		t.Fatalf("%d messages produced, want 1", len(fp.msgs))
	}
	m := fp.msgs[0]
	if want := queue.Topic("openlog", queue.SignalProfiles); m.Topic != want {
		t.Errorf("topic %q, want %q", m.Topic, want)
	}
	// A flame graph is one service over one window, so the partition key is the service (0095_profiles).
	if m.Key != "tenant-a/checkout" || m.TenantID != "tenant-a" {
		t.Errorf("key %q tenant %q", m.Key, m.TenantID)
	}
	// The payload must reach Kafka byte for byte: OTLP profiles are still v1development upstream, and
	// re-encoding them through openlog would make openlog the one that decides what the wire means.
	if !bytes.Equal(m.Value, body) {
		t.Errorf("payload re-encoded: %d bytes produced, %d accepted", len(m.Value), len(body))
	}
}

func TestHTTPProfilesRejects(t *testing.T) {
	fp := &fakeProducer{}
	s := newTestService(t, fp, 1<<20)
	h := s.HTTPHandler()
	body := profilesPayload(t, "checkout")

	// Profiles go through the same authentication as every other signal; an endpoint that skipped it would
	// be a way around the tenant boundary, not a new feature.
	if rec := post(h, "/v1/profiles", ctProtobuf, "", body, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no key: status %d", rec.Code)
	}
	if rec := post(h, "/v1/profiles", ctProtobuf, "badkey", body, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("bad key: status %d", rec.Code)
	}
	if rec := post(h, "/v1/profiles", "text/plain", "goodkey", body, nil); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("content type: status %d", rec.Code)
	}
	if rec := post(h, "/v1/profiles", ctProtobuf, "goodkey", []byte{0xff, 0xff, 0xff}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("garbage: status %d", rec.Code)
	}
	if len(fp.msgs) != 0 {
		t.Errorf("%d messages produced by refused requests", len(fp.msgs))
	}
}

// A payload carrying no profile at all is accepted and produces nothing: an agent whose process was idle for
// the window has nothing to report, and answering it with an error would make it retry forever.
func TestHTTPProfilesEmptyProducesNothing(t *testing.T) {
	fp := &fakeProducer{}
	s := newTestService(t, fp, 1<<20)
	body, err := pprofileotlp.NewExportRequest().MarshalProto()
	if err != nil {
		t.Fatal(err)
	}
	if rec := post(s.HTTPHandler(), "/v1/profiles", ctProtobuf, "goodkey", body, nil); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if len(fp.msgs) != 0 {
		t.Errorf("%d messages produced for an empty payload", len(fp.msgs))
	}
}
