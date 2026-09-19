package promscrape

import (
	"errors"
	"math"
	"strings"
	"testing"
)

const textExposition = `# HELP http_requests_total The total number of HTTP requests.
# TYPE http_requests_total counter
http_requests_total{method="post",code="200"} 1027 1395066363000
http_requests_total{method="post",code="400"}    3 1395066363000

# Escaping in label values:
msdos_file_access_time_seconds{path="C:\\DIR\\FILE.TXT",error="Cannot find file:\n\"FILE.TXT\""} 1.458255915e9

# Minimalistic line:
metric_without_timestamp_and_labels 12.47

# A weird metric from before the epoch:
something_weird{problem="division by zero"} +Inf -3982045

# A histogram, which has a pretty complex representation in the text format:
# HELP http_request_duration_seconds A histogram of the request duration.
# TYPE http_request_duration_seconds histogram
http_request_duration_seconds_bucket{le="0.05"} 24054
http_request_duration_seconds_bucket{le="0.1"} 33444
http_request_duration_seconds_bucket{le="0.2"} 100392
http_request_duration_seconds_bucket{le="0.5"} 129389
http_request_duration_seconds_bucket{le="1"} 133988
http_request_duration_seconds_bucket{le="+Inf"} 144320
http_request_duration_seconds_sum 53423
http_request_duration_seconds_count 144320

# Finally a summary, which has a complex representation, too:
# HELP rpc_duration_seconds A summary of the RPC duration in seconds.
# TYPE rpc_duration_seconds summary
rpc_duration_seconds{quantile="0.01"} 3102
rpc_duration_seconds{quantile="0.05"} 3272
rpc_duration_seconds{quantile="0.5"} 4773
rpc_duration_seconds{quantile="0.9"} 9001
rpc_duration_seconds{quantile="0.99"} 76656
rpc_duration_seconds_sum 1.7560473e+07
rpc_duration_seconds_count 2693
`

func parseText(t *testing.T, s string) []*Family {
	t.Helper()
	fams, err := Parse(strings.NewReader(s), FormatText, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return fams
}

func byName(fams []*Family) map[string]*Family {
	m := map[string]*Family{}
	for _, f := range fams {
		m[f.Name] = f
	}
	return m
}

func TestParseTextFormatReferenceExample(t *testing.T) {
	fams := parseText(t, textExposition)
	m := byName(fams)
	if len(fams) != 6 {
		t.Fatalf("families = %d, want 6: %v", len(fams), m)
	}
	c := m["http_requests_total"]
	if c.Type != TypeCounter || c.Help != "The total number of HTTP requests." || len(c.Samples) != 2 {
		t.Fatalf("counter = %+v", c)
	}
	if s := c.Samples[1]; s.Value != 3 || s.TimestampMs != 1395066363000 || s.Label("code") != "400" {
		t.Fatalf("sample = %+v", s)
	}
	if v := m["msdos_file_access_time_seconds"].Samples[0]; v.Label("path") != `C:\DIR\FILE.TXT` || v.Label("error") != "Cannot find file:\n\"FILE.TXT\"" {
		t.Fatalf("escaping: %+v", v.Labels)
	}
	if v := m["metric_without_timestamp_and_labels"]; v.Type != TypeUnknown || v.Samples[0].Value != 12.47 {
		t.Fatalf("untyped: %+v", v)
	}
	if v := m["something_weird"].Samples[0]; !math.IsInf(v.Value, 1) || v.TimestampMs != -3982045 {
		t.Fatalf("weird: %+v", v)
	}
	h := m["http_request_duration_seconds"]
	if h.Type != TypeHistogram || len(h.Samples) != 8 {
		t.Fatalf("histogram: %+v", h)
	}
	s := m["rpc_duration_seconds"]
	if s.Type != TypeSummary || len(s.Samples) != 7 {
		t.Fatalf("summary: %+v", s)
	}
}

func TestParseOpenMetrics(t *testing.T) {
	in := `# TYPE acme_http_router_request_seconds summary
# UNIT acme_http_router_request_seconds seconds
# HELP acme_http_router_request_seconds Latency though all of ACME's HTTP request router.
acme_http_router_request_seconds_sum{path="/api/v1",method="GET"} 9036.32
acme_http_router_request_seconds_count{path="/api/v1",method="GET"} 807283.0
acme_http_router_request_seconds_created{path="/api/v1",method="GET"} 1605281325.0
# TYPE foo counter
foo_total 17.0 1520879607.789 # {trace_id="0af7651916cd43dd8448eb211c80319c",span_id="b7ad6b7169203331"} 1 1520879607.789
foo_created 1520872607.123
# TYPE cpu info
cpu_info{model="xeon"} 1
# TYPE door stateset
door{door="open"} 1
door{door="closed"} 0
# TYPE q gaugehistogram
q_bucket{le="1"} 2
q_bucket{le="+Inf"} 3
q_gsum 4
q_gcount 3
# EOF
`
	fams, err := Parse(strings.NewReader(in), FormatOpenMetrics, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	m := byName(fams)
	s := m["acme_http_router_request_seconds"]
	if s.Unit != "seconds" || s.Type != TypeSummary || len(s.Samples) != 3 {
		t.Fatalf("summary = %+v", s)
	}
	foo := m["foo"]
	if foo.Type != TypeCounter || len(foo.Samples) != 2 {
		t.Fatalf("foo = %+v", foo)
	}
	fs := foo.Samples[0]
	if fs.TimestampMs != 1520879607789 || fs.Exemplar == nil || fs.Exemplar.Value != 1 || fs.Exemplar.TimestampMs != 1520879607789 {
		t.Fatalf("foo sample = %+v exemplar %+v", fs, fs.Exemplar)
	}
	if len(fs.Exemplar.Labels) != 2 || fs.Exemplar.Labels[0].Value != "0af7651916cd43dd8448eb211c80319c" {
		t.Fatalf("exemplar labels = %+v", fs.Exemplar.Labels)
	}
	for _, n := range []string{"cpu", "door", "q"} {
		if m[n] == nil {
			t.Fatalf("family %s missing", n)
		}
	}
	if m["cpu"].Type != TypeInfo || m["door"].Type != TypeStateSet || len(m["q"].Samples) != 4 {
		t.Fatalf("info/stateset/gaugehistogram: %+v %+v %+v", m["cpu"], m["door"], m["q"])
	}
}

func TestParseOpenMetricsRequiresEOF(t *testing.T) {
	_, err := Parse(strings.NewReader("# TYPE a gauge\na 1\n"), FormatOpenMetrics, Limits{})
	var pe *ParseError
	if !errors.As(err, &pe) || !strings.Contains(pe.Msg, "EOF") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Parse(strings.NewReader("a 1\n# EOF\nb 2\n"), FormatOpenMetrics, Limits{}); err == nil {
		t.Fatal("content after # EOF accepted")
	}
}

func TestParseUTF8Names(t *testing.T) {
	in := `# TYPE "my.dotted.metric" gauge
{"my.dotted.metric", "http.method"="GET", code="200"} 3
`
	fams := parseText(t, in)
	if len(fams) != 1 || fams[0].Name != "my.dotted.metric" || fams[0].Type != TypeGauge {
		t.Fatalf("fams = %+v", fams)
	}
	s := fams[0].Samples[0]
	if s.Label("http.method") != "GET" || s.Label("code") != "200" || s.Value != 3 {
		t.Fatalf("sample = %+v", s)
	}
}

func TestParseTextTolerance(t *testing.T) {
	// Spaces around labels, a trailing comma, tabs and a plain comment are accepted in the text format.
	in := "# just a comment\n\tfoo{ a = \"1\" , b=\"2\", }\t5\n"
	fams := parseText(t, in)
	if len(fams) != 1 || fams[0].Samples[0].Label("b") != "2" || fams[0].Samples[0].Value != 5 {
		t.Fatalf("fams = %+v", fams[0])
	}
}

func TestParseErrors(t *testing.T) {
	for name, in := range map[string]string{
		"bad value":       "foo abc\n",
		"no value":        "foo\n",
		"unquoted label":  "foo{a=1} 1\n",
		"unterminated":    "foo{a=\"1\" 1\n",
		"bad name":        "0foo 1\n",
		"bad type":        "# TYPE foo bogus\n",
		"om type in text": "# TYPE foo info\n",
		"type after data": "foo 1\n# TYPE foo gauge\n",
		"bad timestamp":   "foo 1 12.5\n",
		"too many fields": "foo 1 2 3\n",
	} {
		if _, err := Parse(strings.NewReader(in), FormatText, Limits{}); err == nil {
			t.Errorf("%s: accepted %q", name, in)
		}
	}
}

func TestParseSampleLimit(t *testing.T) {
	in := "a 1\nb 2\nc 3\n"
	if _, err := Parse(strings.NewReader(in), FormatText, Limits{MaxSamples: 3}); err != nil {
		t.Fatalf("at the limit: %v", err)
	}
	if _, err := Parse(strings.NewReader(in), FormatText, Limits{MaxSamples: 2}); !errors.Is(err, ErrSampleLimit) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseLineLimit(t *testing.T) {
	in := "a{l=\"" + strings.Repeat("x", 200) + "\"} 1\n"
	if _, err := Parse(strings.NewReader(in), FormatText, Limits{MaxLineBytes: 100}); err == nil {
		t.Fatal("long line accepted")
	}
}

func TestFormatOf(t *testing.T) {
	if FormatOf("application/openmetrics-text; version=1.0.0; charset=utf-8") != FormatOpenMetrics {
		t.Fatal("openmetrics not detected")
	}
	for _, ct := range []string{"text/plain; version=0.0.4", "", "application/json"} {
		if FormatOf(ct) != FormatText {
			t.Fatalf("%q not text", ct)
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add(textExposition, false)
	f.Add("# TYPE foo counter\nfoo_total 1 # {trace_id=\"ab\"} 1\n# EOF\n", true)
	f.Fuzz(func(t *testing.T, in string, om bool) {
		format := FormatText
		if om {
			format = FormatOpenMetrics
		}
		fams, err := Parse(strings.NewReader(in), format, Limits{MaxSamples: 1000})
		if err != nil {
			return
		}
		NewConverter().Convert(fams, testNow)
	})
}
