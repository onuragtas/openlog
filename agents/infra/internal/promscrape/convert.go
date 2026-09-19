package promscrape

import (
	"encoding/hex"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// Label names the exposition formats reserve for bucket bounds and quantiles.
const (
	labelLE       = "le"
	labelQuantile = "quantile"
)

// seriesState is what a target remembers about one cumulative series between scrapes: the start time reported
// with every point and the last value, so that a counter reset starts a new cumulative run (like the OTel
// Collector's prometheus receiver does).
type seriesState struct {
	start uint64
	last  float64
	seen  bool // seen in the current scrape (garbage collection)
}

// Converter turns the families of successive scrapes of one target into OTLP metrics. It is not safe for
// concurrent use; every target owns one.
type Converter struct {
	series map[string]*seriesState
	prev   time.Time // time of the previous scrape (start of a series that reset)
}

// NewConverter returns a converter with no history.
func NewConverter() *Converter { return &Converter{series: map[string]*seriesState{}} }

// Convert builds the metrics of one scrape taken at now. Counters, histograms and summaries are cumulative
// with a start time from _created when the target exposes it, else from the first scrape that saw the series.
func (c *Converter) Convert(fams []*Family, now time.Time) []*metricspb.Metric {
	for _, s := range c.series {
		s.seen = false
	}
	out := make([]*metricspb.Metric, 0, len(fams))
	for _, f := range fams {
		created := createdTimes(f)
		switch f.Type {
		case TypeCounter:
			if m := c.counter(f, created, now); m != nil {
				out = append(out, m)
			}
		case TypeHistogram:
			if m := c.histogram(f, created, now); m != nil {
				out = append(out, m)
			}
		case TypeSummary:
			if m := c.summary(f, created, now); m != nil {
				out = append(out, m)
			}
		default:
			// gauge, unknown, info, stateset and gaugehistogram samples are point-in-time values: one gauge per
			// sample name (a gauge histogram becomes x_bucket, x_gsum and x_gcount gauges).
			out = append(out, gauges(f, now)...)
		}
	}
	for k, s := range c.series {
		if !s.seen {
			delete(c.series, k)
		}
	}
	c.prev = now
	return out
}

// start returns the start time of a cumulative series and records its value; a smaller value than the last one
// is a reset and starts a new run at the previous scrape.
func (c *Converter) start(key string, value float64, created uint64, now time.Time) uint64 {
	st := c.series[key]
	switch {
	case st == nil:
		st = &seriesState{start: uint64(now.UnixNano())}
		c.series[key] = st
	case value < st.last:
		st.start = uint64(now.UnixNano()) - 1
		if !c.prev.IsZero() {
			st.start = uint64(c.prev.UnixNano())
		}
	}
	if created > 0 {
		st.start = created
	}
	st.last, st.seen = value, true
	return st.start
}

// createdTimes maps series keys (sample labels without le/quantile) to the _created timestamp in ns.
func createdTimes(f *Family) map[string]uint64 {
	var out map[string]uint64
	for i := range f.Samples {
		s := &f.Samples[i]
		if !strings.HasSuffix(s.Name, "_created") || s.Name == f.Name {
			continue
		}
		if math.IsNaN(s.Value) || s.Value <= 0 {
			continue
		}
		if out == nil {
			out = map[string]uint64{}
		}
		// Rounded to microseconds: seconds as float64 cannot hold nanoseconds of today's dates exactly.
		out[labelKey(s.Labels, "")] = uint64(math.Round(s.Value*1e6)) * 1e3
	}
	return out
}

func sampleTime(s *Sample, now time.Time) uint64 {
	if s.TimestampMs != 0 {
		return uint64(s.TimestampMs) * uint64(time.Millisecond)
	}
	return uint64(now.UnixNano())
}

func (c *Converter) counter(f *Family, created map[string]uint64, now time.Time) *metricspb.Metric {
	// The metric keeps the sample name (http_requests_total), which is how a Prometheus user queries it.
	var byName = map[string]*metricspb.Metric{}
	var order []*metricspb.Metric
	for i := range f.Samples {
		s := &f.Samples[i]
		if strings.HasSuffix(s.Name, "_created") && s.Name != f.Name {
			continue
		}
		m := byName[s.Name]
		if m == nil {
			m = &metricspb.Metric{Name: s.Name, Unit: unit(f), Description: f.Help, Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
				AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE, IsMonotonic: true}}}
			byName[s.Name] = m
			order = append(order, m)
		}
		lk := labelKey(s.Labels, "")
		dp := &metricspb.NumberDataPoint{
			Attributes:        attrs(s.Labels, ""),
			StartTimeUnixNano: c.start(s.Name+"\xff"+lk, s.Value, created[lk], now),
			TimeUnixNano:      sampleTime(s, now),
			Value:             &metricspb.NumberDataPoint_AsDouble{AsDouble: s.Value},
		}
		if s.Exemplar != nil {
			dp.Exemplars = []*metricspb.Exemplar{exemplar(s.Exemplar, now)}
		}
		m.GetSum().DataPoints = append(m.GetSum().DataPoints, dp)
	}
	if len(order) == 0 {
		return nil
	}
	// A counter family has one sample name (x_total, or x in the text format); a malformed family with more
	// keeps the first one.
	return order[0]
}

type histSeries struct {
	labels   []Label
	buckets  map[float64]float64
	sum      float64
	count    float64
	hasCount bool
	ts       uint64
	ex       map[float64]*Exemplar
}

func (c *Converter) histogram(f *Family, created map[string]uint64, now time.Time) *metricspb.Metric {
	series := map[string]*histSeries{}
	var order []string
	get := func(s *Sample) *histSeries {
		k := labelKey(s.Labels, labelLE)
		h := series[k]
		if h == nil {
			h = &histSeries{labels: withoutLabel(s.Labels, labelLE), buckets: map[float64]float64{}}
			series[k] = h
			order = append(order, k)
		}
		if t := sampleTime(s, now); t > h.ts {
			h.ts = t
		}
		return h
	}
	for i := range f.Samples {
		s := &f.Samples[i]
		switch s.Name {
		case f.Name + "_bucket":
			le, err := parseFloat(s.Label(labelLE))
			if err != nil || math.IsNaN(le) {
				continue
			}
			h := get(s)
			h.buckets[le] = s.Value
			if s.Exemplar != nil {
				if h.ex == nil {
					h.ex = map[float64]*Exemplar{}
				}
				h.ex[le] = s.Exemplar
			}
		case f.Name + "_sum":
			get(s).sum = s.Value
		case f.Name + "_count":
			h := get(s)
			h.count, h.hasCount = s.Value, true
		}
	}
	if len(order) == 0 {
		return nil
	}
	h := &metricspb.Histogram{AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE}
	for _, k := range order {
		hs := series[k]
		bounds := make([]float64, 0, len(hs.buckets))
		for le := range hs.buckets {
			if !math.IsInf(le, 1) {
				bounds = append(bounds, le)
			}
		}
		sort.Float64s(bounds)
		count := hs.count
		if !hs.hasCount {
			inf, ok := hs.buckets[math.Inf(1)]
			if !ok && len(bounds) > 0 {
				inf = hs.buckets[bounds[len(bounds)-1]]
			}
			count = inf
		}
		// Prometheus buckets are cumulative (le); OTLP bucket counts are per bucket, the last one being (max, +Inf].
		counts := make([]uint64, len(bounds)+1)
		var prev float64
		for i, b := range bounds {
			v := hs.buckets[b]
			counts[i] = nonNegative(v - prev)
			prev = max(prev, v)
		}
		counts[len(bounds)] = nonNegative(count - prev)
		sum := hs.sum
		dp := &metricspb.HistogramDataPoint{
			Attributes:        attrs(hs.labels, ""),
			StartTimeUnixNano: c.start(f.Name+"\xff"+k, count, created[k], now),
			TimeUnixNano:      hs.ts,
			Count:             nonNegative(count),
			ExplicitBounds:    bounds,
			BucketCounts:      counts,
		}
		if !math.IsNaN(sum) {
			dp.Sum = &sum
		}
		for _, le := range sortedKeys(hs.ex) {
			dp.Exemplars = append(dp.Exemplars, exemplar(hs.ex[le], now))
		}
		h.DataPoints = append(h.DataPoints, dp)
	}
	return &metricspb.Metric{Name: f.Name, Unit: unit(f), Description: f.Help, Data: &metricspb.Metric_Histogram{Histogram: h}}
}

type summarySeries struct {
	labels    []Label
	quantiles map[float64]float64
	sum       float64
	count     float64
	ts        uint64
}

func (c *Converter) summary(f *Family, created map[string]uint64, now time.Time) *metricspb.Metric {
	series := map[string]*summarySeries{}
	var order []string
	get := func(s *Sample) *summarySeries {
		k := labelKey(s.Labels, labelQuantile)
		ss := series[k]
		if ss == nil {
			ss = &summarySeries{labels: withoutLabel(s.Labels, labelQuantile), quantiles: map[float64]float64{}}
			series[k] = ss
			order = append(order, k)
		}
		if t := sampleTime(s, now); t > ss.ts {
			ss.ts = t
		}
		return ss
	}
	for i := range f.Samples {
		s := &f.Samples[i]
		switch s.Name {
		case f.Name:
			q, err := parseFloat(s.Label(labelQuantile))
			if err != nil || math.IsNaN(q) || q < 0 || q > 1 {
				continue
			}
			get(s).quantiles[q] = s.Value
		case f.Name + "_sum":
			get(s).sum = s.Value
		case f.Name + "_count":
			get(s).count = s.Value
		}
	}
	if len(order) == 0 {
		return nil
	}
	sm := &metricspb.Summary{}
	for _, k := range order {
		ss := series[k]
		dp := &metricspb.SummaryDataPoint{
			Attributes:        attrs(ss.labels, ""),
			StartTimeUnixNano: c.start(f.Name+"\xff"+k, ss.count, created[k], now),
			TimeUnixNano:      ss.ts,
			Count:             nonNegative(ss.count),
			Sum:               ss.sum,
		}
		for _, q := range sortedKeys(ss.quantiles) {
			dp.QuantileValues = append(dp.QuantileValues, &metricspb.SummaryDataPoint_ValueAtQuantile{Quantile: q, Value: ss.quantiles[q]})
		}
		sm.DataPoints = append(sm.DataPoints, dp)
	}
	return &metricspb.Metric{Name: f.Name, Unit: unit(f), Description: f.Help, Data: &metricspb.Metric_Summary{Summary: sm}}
}

func gauges(f *Family, now time.Time) []*metricspb.Metric {
	byName := map[string]*metricspb.Metric{}
	var out []*metricspb.Metric
	for i := range f.Samples {
		s := &f.Samples[i]
		m := byName[s.Name]
		if m == nil {
			m = &metricspb.Metric{Name: s.Name, Unit: unit(f), Description: f.Help, Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{}}}
			byName[s.Name] = m
			out = append(out, m)
		}
		m.GetGauge().DataPoints = append(m.GetGauge().DataPoints, &metricspb.NumberDataPoint{
			Attributes:   attrs(s.Labels, ""),
			TimeUnixNano: sampleTime(s, now),
			Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: s.Value},
		})
	}
	return out
}

// unit maps the OpenMetrics UNIT (base units, plural) to UCUM, the unit syntax of OTLP.
func unit(f *Family) string {
	switch f.Unit {
	case "":
		return ""
	case "seconds":
		return "s"
	case "milliseconds":
		return "ms"
	case "bytes":
		return "By"
	case "ratio":
		return "1"
	case "celsius":
		return "Cel"
	case "meters":
		return "m"
	case "volts":
		return "V"
	case "amperes":
		return "A"
	case "joules":
		return "J"
	case "grams":
		return "g"
	case "hertz":
		return "Hz"
	}
	return f.Unit
}

func exemplar(e *Exemplar, now time.Time) *metricspb.Exemplar {
	ex := &metricspb.Exemplar{Value: &metricspb.Exemplar_AsDouble{AsDouble: e.Value}, TimeUnixNano: uint64(now.UnixNano())}
	if e.TimestampMs != 0 {
		ex.TimeUnixNano = uint64(e.TimestampMs) * uint64(time.Millisecond)
	}
	for _, l := range e.Labels {
		switch l.Name {
		case "trace_id", "traceID", "TraceID":
			if b, err := hex.DecodeString(l.Value); err == nil && len(b) == 16 {
				ex.TraceId = b
				continue
			}
		case "span_id", "spanID", "SpanID":
			if b, err := hex.DecodeString(l.Value); err == nil && len(b) == 8 {
				ex.SpanId = b
				continue
			}
		}
		ex.FilteredAttributes = append(ex.FilteredAttributes, otlputil.Str(l.Name, l.Value))
	}
	return ex
}

func attrs(ls []Label, skip string) []*commonpb.KeyValue {
	out := make([]*commonpb.KeyValue, 0, len(ls))
	for _, l := range ls {
		if l.Name == skip {
			continue
		}
		out = append(out, otlputil.Str(l.Name, l.Value))
	}
	return out
}

func withoutLabel(ls []Label, name string) []Label {
	out := make([]Label, 0, len(ls))
	for _, l := range ls {
		if l.Name != name {
			out = append(out, l)
		}
	}
	return out
}

// labelKey is an order-independent key of a label set without one label.
func labelKey(ls []Label, skip string) string {
	cp := withoutLabel(ls, skip)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Name < cp[j].Name })
	var b strings.Builder
	for _, l := range cp {
		b.WriteString(l.Name)
		b.WriteByte(0)
		b.WriteString(l.Value)
		b.WriteByte(0)
	}
	return b.String()
}

func nonNegative(v float64) uint64 {
	if math.IsNaN(v) || v <= 0 {
		return 0
	}
	if v >= math.MaxUint64 {
		return math.MaxUint64
	}
	return uint64(v)
}

func sortedKeys[V any](m map[float64]V) []float64 {
	out := make([]float64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Float64s(out)
	return out
}
