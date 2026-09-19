package promscrape

import (
	"encoding/hex"
	"slices"
	"strings"
	"testing"
	"time"

	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

var testNow = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

func metricsByName(ms []*metricspb.Metric) map[string]*metricspb.Metric {
	out := map[string]*metricspb.Metric{}
	for _, m := range ms {
		out[m.Name] = m
	}
	return out
}

func TestConvertReferenceExample(t *testing.T) {
	ms := metricsByName(NewConverter().Convert(parseText(t, textExposition), testNow))

	c := ms["http_requests_total"].GetSum()
	if c == nil || !c.IsMonotonic || c.AggregationTemporality != metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE || len(c.DataPoints) != 2 {
		t.Fatalf("counter = %v", ms["http_requests_total"])
	}
	if dp := c.DataPoints[0]; dp.GetAsDouble() != 1027 || dp.TimeUnixNano != 1395066363000*uint64(time.Millisecond) {
		t.Fatalf("counter point = %v", dp)
	}

	h := ms["http_request_duration_seconds"].GetHistogram()
	if h == nil || len(h.DataPoints) != 1 {
		t.Fatalf("histogram = %v", ms["http_request_duration_seconds"])
	}
	dp := h.DataPoints[0]
	if !slices.Equal(dp.ExplicitBounds, []float64{0.05, 0.1, 0.2, 0.5, 1}) {
		t.Fatalf("bounds = %v", dp.ExplicitBounds)
	}
	// Cumulative le buckets become per-bucket counts; the last one is (1, +Inf].
	if want := []uint64{24054, 9390, 66948, 28997, 4599, 10332}; !slices.Equal(dp.BucketCounts, want) {
		t.Fatalf("bucket counts = %v, want %v", dp.BucketCounts, want)
	}
	if dp.Count != 144320 || dp.GetSum() != 53423 {
		t.Fatalf("count/sum = %d/%v", dp.Count, dp.GetSum())
	}
	var total uint64
	for _, n := range dp.BucketCounts {
		total += n
	}
	if total != dp.Count {
		t.Fatalf("bucket counts sum to %d, count %d", total, dp.Count)
	}

	s := ms["rpc_duration_seconds"].GetSummary()
	if s == nil || len(s.DataPoints) != 1 {
		t.Fatalf("summary = %v", ms["rpc_duration_seconds"])
	}
	sp := s.DataPoints[0]
	if sp.Count != 2693 || sp.Sum != 1.7560473e+07 || len(sp.QuantileValues) != 5 || sp.QuantileValues[4].Quantile != 0.99 || sp.QuantileValues[4].Value != 76656 {
		t.Fatalf("summary point = %v", sp)
	}

	if g := ms["metric_without_timestamp_and_labels"].GetGauge(); g == nil || g.DataPoints[0].GetAsDouble() != 12.47 {
		t.Fatalf("untyped = %v", ms["metric_without_timestamp_and_labels"])
	}
}

func TestConvertCounterStartTimeAndReset(t *testing.T) {
	c := NewConverter()
	at := func(i int) time.Time { return testNow.Add(time.Duration(i) * 30 * time.Second) }
	start := func(v string, i int) uint64 {
		ms := c.Convert(parseText(t, "# TYPE x_total counter\nx_total "+v+"\n"), at(i))
		return ms[0].GetSum().DataPoints[0].StartTimeUnixNano
	}
	first := start("10", 0)
	if first != uint64(at(0).UnixNano()) {
		t.Fatalf("first start = %d", first)
	}
	if s := start("20", 1); s != first {
		t.Fatalf("start moved without reset: %d", s)
	}
	// The value dropped: a new cumulative run starts at the previous scrape.
	if s := start("3", 2); s != uint64(at(1).UnixNano()) {
		t.Fatalf("start after reset = %d, want %d", s, at(1).UnixNano())
	}
	if s := start("4", 3); s != uint64(at(1).UnixNano()) {
		t.Fatalf("start after reset moved: %d", s)
	}
}

func TestConvertCreatedIsStartTime(t *testing.T) {
	in := `# TYPE foo counter
foo_total 17.0 # {trace_id="0af7651916cd43dd8448eb211c80319c",span_id="b7ad6b7169203331",user="u1"} 1 1520879607.789
foo_created 1520872607.123
# EOF
`
	fams, err := Parse(strings.NewReader(in), FormatOpenMetrics, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	ms := NewConverter().Convert(fams, testNow)
	if len(ms) != 1 || ms[0].Name != "foo_total" {
		t.Fatalf("metrics = %v", ms)
	}
	dp := ms[0].GetSum().DataPoints[0]
	if want := uint64(1520872607.123 * 1e9); dp.StartTimeUnixNano != want {
		t.Fatalf("start = %d, want %d", dp.StartTimeUnixNano, want)
	}
	if len(dp.Exemplars) != 1 {
		t.Fatalf("exemplars = %v", dp.Exemplars)
	}
	ex := dp.Exemplars[0]
	if hex.EncodeToString(ex.TraceId) != "0af7651916cd43dd8448eb211c80319c" || hex.EncodeToString(ex.SpanId) != "b7ad6b7169203331" {
		t.Fatalf("exemplar ids = %x %x", ex.TraceId, ex.SpanId)
	}
	if len(ex.FilteredAttributes) != 1 || ex.FilteredAttributes[0].Key != "user" || ex.TimeUnixNano != 1520879607789*uint64(time.Millisecond) {
		t.Fatalf("exemplar = %v", ex)
	}
}

func TestConvertHistogramSeriesAndUnit(t *testing.T) {
	in := `# TYPE lat histogram
# UNIT lat seconds
lat_bucket{route="/a",le="1.0"} 1
lat_bucket{route="/a",le="+Inf"} 2
lat_count{route="/a"} 2
lat_sum{route="/a"} 3.5
lat_bucket{route="/b",le="1.0"} 5
lat_bucket{route="/b",le="+Inf"} 5
lat_count{route="/b"} 5
lat_sum{route="/b"} 1
# EOF
`
	fams, err := Parse(strings.NewReader(in), FormatOpenMetrics, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	ms := NewConverter().Convert(fams, testNow)
	h := ms[0]
	if h.Unit != "s" || len(h.GetHistogram().DataPoints) != 2 {
		t.Fatalf("histogram = %v", h)
	}
	b := h.GetHistogram().DataPoints[1]
	if len(b.Attributes) != 1 || b.Attributes[0].Value.GetStringValue() != "/b" || !slices.Equal(b.BucketCounts, []uint64{5, 0}) {
		t.Fatalf("route /b = %v", b)
	}
}

func TestConvertForgetsVanishedSeries(t *testing.T) {
	c := NewConverter()
	c.Convert(parseText(t, "# TYPE a counter\na{x=\"1\"} 1\na{x=\"2\"} 1\n"), testNow)
	c.Convert(parseText(t, "# TYPE a counter\na{x=\"1\"} 2\n"), testNow.Add(time.Minute))
	if len(c.series) != 1 {
		t.Fatalf("series kept = %d", len(c.series))
	}
}

func TestConvertStateSetAndInfoAreGauges(t *testing.T) {
	in := "# TYPE door stateset\ndoor{door=\"open\"} 1\ndoor{door=\"closed\"} 0\n# TYPE b info\nb_info{v=\"1\"} 1\n# EOF\n"
	fams, err := Parse(strings.NewReader(in), FormatOpenMetrics, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	ms := metricsByName(NewConverter().Convert(fams, testNow))
	if g := ms["door"].GetGauge(); g == nil || len(g.DataPoints) != 2 {
		t.Fatalf("stateset = %v", ms["door"])
	}
	if g := ms["b_info"].GetGauge(); g == nil || g.DataPoints[0].GetAsDouble() != 1 {
		t.Fatalf("info = %v", ms["b_info"])
	}
}
