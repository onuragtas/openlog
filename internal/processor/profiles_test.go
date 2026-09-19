package processor

import (
	"reflect"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pprofile/pprofileotlp"

	"github.com/onuragtas/openlog/internal/queue"
)

// profilesMsg builds an OTLP profiles payload: one CPU sample of main → handleRequest → db.Query. Everything
// lives in the dictionary and the sample points at it by index, which is the encoding the decode arm has to
// get right; withSampleType false leaves the profile without one, which is not storable.
func profilesMsg(t *testing.T, withSampleType bool) []byte {
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
	kv := dict.AttributeTable().AppendEmpty()
	kv.SetKeyStrindex(str("thread.name"))
	kv.Value().SetStr("worker-3")

	rp := pf.ResourceProfiles().AppendEmpty()
	rp.Resource().Attributes().PutStr("service.name", "checkout")
	rp.Resource().Attributes().PutStr("service.namespace", "shop")
	rp.Resource().Attributes().PutStr("deployment.environment.name", "production")
	rp.Resource().Attributes().PutStr("host.id", "h-1")
	p := rp.ScopeProfiles().AppendEmpty().Profiles().AppendEmpty()
	if withSampleType {
		p.SampleType().SetTypeStrindex(str("cpu"))
		p.SampleType().SetUnitStrindex(str("nanoseconds"))
	}
	p.SetDurationNano(10_000_000_000)
	p.SetTime(pcommon.NewTimestampFromTime(recv))
	s := p.Samples().AppendEmpty()
	s.SetStackIndex(0)
	s.AttributeIndices().Append(0)
	s.Values().Append(1_500_000)

	b, err := req.MarshalProto()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func profileRecord(t *testing.T, value []byte) *kgo.Record {
	t.Helper()
	rec := &kgo.Record{Topic: queue.Topic("openlog", queue.SignalProfiles), Partition: 0, Offset: 1, Value: value}
	for k, v := range goodHeaders() {
		rec.Headers = append(rec.Headers, kgo.RecordHeader{Key: k, Value: []byte(v)})
	}
	return rec
}

// The decode arm is the only place profiles enter the row set, so this covers the whole path from the Kafka
// record to the columns of profiles_local: the dictionary is expanded, the stack is stored root first, and
// the payload is metered. A signal that decodes but is not metered is a silent hole in the bill.
func TestDecodeProfilesRecord(t *testing.T) {
	p := newTestProcessor(&fakeWriter{}, &fakeConsumer{})
	rows := NewRows()
	p.decodeRecord(rows, profileRecord(t, profilesMsg(t, true)))

	if len(rows.Profiles) != 1 {
		t.Fatalf("%d profile rows, want 1 (dropped %v)", len(rows.Profiles), rows.Dropped)
	}
	r := rows.Profiles[0]
	if want := []string{"main", "handleRequest", "db.Query"}; !reflect.DeepEqual(r.Stack, want) {
		t.Errorf("stack %v, want %v", r.Stack, want)
	}
	if r.TenantID != "t1" || r.ServiceName != "checkout" || r.ServiceNamespace != "shop" ||
		r.Environment != "production" || r.HostID != "h-1" {
		t.Errorf("identity: %+v", r)
	}
	if r.ProfileType != "cpu" || r.Unit != "nanoseconds" || r.Leaf != "db.Query" ||
		r.Value != 1_500_000 || r.DurationNs != 10_000_000_000 {
		t.Errorf("measurement: %+v", r)
	}
	// Sample attributes are what a flame graph is filtered by; left as dictionary indices they would be
	// unreadable to every query.
	if r.Attributes["thread.name"] != "worker-3" {
		t.Errorf("sample attributes %v", r.Attributes)
	}
	if r.ResourceAttributes["service.namespace"] != "shop" {
		t.Errorf("resource attributes %v", r.ResourceAttributes)
	}
	if n := rows.Len(TableUsageIngest); n != 1 {
		t.Errorf("usage rows %d, want 1", n)
	}
}

func TestDecodeProfilesRejectsUndecodable(t *testing.T) {
	p := newTestProcessor(&fakeWriter{}, &fakeConsumer{})
	rows := NewRows()
	p.decodeRecord(rows, profileRecord(t, []byte{0xff, 0xff, 0xff}))
	if len(rows.Profiles) != 0 || rows.Len(TableUsageIngest) != 0 {
		t.Fatalf("rows %d usage %d from an undecodable record", len(rows.Profiles), rows.Len(TableUsageIngest))
	}
}

// A profile without a sample type says nothing about what its numbers mean, so it is dropped whole rather
// than stored with an empty type: the percentages a flame graph shows are of the samples it holds, so half a
// profile is not a smaller truth but a wrong one.
func TestDecodeProfilesDropsProfileWithoutSampleType(t *testing.T) {
	p := newTestProcessor(&fakeWriter{}, &fakeConsumer{})
	rows := NewRows()
	p.decodeRecord(rows, profileRecord(t, profilesMsg(t, false)))
	if len(rows.Profiles) != 0 || rows.Dropped["invalid_profile"] != 1 {
		t.Fatalf("rows %d dropped %v", len(rows.Profiles), rows.Dropped)
	}
}
