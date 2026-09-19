// Package promscrape scrapes Prometheus and OpenMetrics endpoints and converts the samples to OTLP metrics
// (semantic-conventions §6.9). Targets come from config.yaml, from container labels and from the annotations of
// the pods of the node; every target is scraped on its own goroutine with a timeout, a body size limit and a
// series limit, so a slow or oversized exporter never affects the rest of the agent.
package promscrape

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Metric types of the exposition formats. Text format 0.0.4 knows counter, gauge, histogram, summary and
// untyped; OpenMetrics 1.0 adds gaugehistogram, info, stateset and calls untyped unknown.
const (
	TypeCounter        = "counter"
	TypeGauge          = "gauge"
	TypeHistogram      = "histogram"
	TypeGaugeHistogram = "gaugehistogram"
	TypeSummary        = "summary"
	TypeInfo           = "info"
	TypeStateSet       = "stateset"
	TypeUnknown        = "unknown"
)

// Format is the exposition format of a response.
type Format int

const (
	FormatText        Format = iota // text/plain; version=0.0.4
	FormatOpenMetrics               // application/openmetrics-text; version=1.0.0
)

// FormatOf returns the format announced by a Content-Type header. Anything that is not OpenMetrics is parsed
// as the text format, which is what Prometheus itself does.
func FormatOf(contentType string) Format {
	mt, _, _ := strings.Cut(contentType, ";")
	if strings.EqualFold(strings.TrimSpace(mt), "application/openmetrics-text") {
		return FormatOpenMetrics
	}
	return FormatText
}

// Label is one label pair.
type Label struct{ Name, Value string }

// Exemplar is an OpenMetrics exemplar of a counter or histogram bucket sample.
type Exemplar struct {
	Labels []Label
	Value  float64
	// TimestampMs is 0 when the exemplar carries no timestamp.
	TimestampMs int64
}

// Sample is one line of a family.
type Sample struct {
	// Name is the full sample name (with _total, _bucket, _sum, _count, _created, … suffixes).
	Name   string
	Labels []Label
	Value  float64
	// TimestampMs is the optional sample timestamp in milliseconds (0: none).
	TimestampMs int64
	Exemplar    *Exemplar
}

// Label returns the value of a label ("" when absent).
func (s *Sample) Label(name string) string {
	for _, l := range s.Labels {
		if l.Name == name {
			return l.Value
		}
	}
	return ""
}

// Family is a metric family: the samples that belong to one # TYPE.
type Family struct {
	Name    string
	Type    string
	Help    string
	Unit    string
	Samples []Sample
}

// Limits bound what one parse may allocate.
type Limits struct {
	// MaxSamples stops the parse with ErrSampleLimit after this many samples (0: unlimited).
	MaxSamples int
	// MaxLineBytes rejects longer lines (0: 1 MiB).
	MaxLineBytes int
}

// ErrSampleLimit reports that a target exposes more samples than sample_limit; nothing of the scrape is kept,
// like Prometheus does, because a truncated exposition would produce partial histograms and summaries.
var ErrSampleLimit = errors.New("sample limit exceeded")

// ParseError reports a syntax error with its line number.
type ParseError struct {
	Line int
	Msg  string
}

func (e *ParseError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// Parse reads an exposition. Families are returned in the order of their first appearance. Samples without a
// preceding # TYPE form an unknown (untyped) family of their own name.
func Parse(r io.Reader, f Format, lim Limits) ([]*Family, error) {
	maxLine := lim.MaxLineBytes
	if maxLine <= 0 {
		maxLine = 1 << 20
	}
	sc := bufio.NewScanner(r)
	// The larger of max and cap(buf) is the limit, so the initial buffer must not exceed it.
	sc.Buffer(make([]byte, 0, min(64<<10, maxLine)), maxLine)
	p := &parser{format: f, byName: map[string]*Family{}}
	samples := 0
	sawEOF := false
	for sc.Scan() {
		p.line++
		line := sc.Bytes()
		if sawEOF {
			if len(bytes.TrimSpace(line)) > 0 {
				return nil, p.errf("content after # EOF")
			}
			continue
		}
		if f == FormatText {
			line = bytes.TrimSpace(line)
		}
		if len(line) == 0 {
			if f == FormatOpenMetrics {
				return nil, p.errf("empty line")
			}
			continue
		}
		if line[0] == '#' {
			eof, err := p.comment(string(line))
			if err != nil {
				return nil, err
			}
			sawEOF = eof
			continue
		}
		if err := p.sample(string(line)); err != nil {
			return nil, err
		}
		samples++
		if lim.MaxSamples > 0 && samples > lim.MaxSamples {
			return nil, ErrSampleLimit
		}
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, p.errf("line longer than %d bytes", maxLine)
		}
		return nil, err
	}
	if f == FormatOpenMetrics && !sawEOF {
		return nil, p.errf("missing # EOF")
	}
	return p.families, nil
}

type parser struct {
	format   Format
	line     int
	families []*Family
	byName   map[string]*Family
	// cur is the family of the last # TYPE/HELP/UNIT; samples of other names end it.
	cur *Family
}

func (p *parser) errf(format string, a ...any) error {
	return &ParseError{Line: p.line, Msg: fmt.Sprintf(format, a...)}
}

func (p *parser) family(name string) *Family {
	if f, ok := p.byName[name]; ok {
		return f
	}
	f := &Family{Name: name, Type: TypeUnknown}
	p.byName[name] = f
	p.families = append(p.families, f)
	return f
}

// comment handles # HELP, # TYPE, # UNIT and # EOF; other comments are ignored (text format only).
func (p *parser) comment(line string) (eof bool, err error) {
	rest := strings.TrimPrefix(line, "#")
	if p.format == FormatOpenMetrics {
		if rest == " EOF" {
			return true, nil
		}
		if !strings.HasPrefix(rest, " ") {
			return false, p.errf("invalid comment")
		}
	}
	fields := strings.SplitN(strings.TrimLeft(rest, " \t"), " ", 3)
	if len(fields) < 2 {
		if p.format == FormatOpenMetrics {
			return false, p.errf("invalid descriptor")
		}
		return false, nil
	}
	kind := fields[0]
	switch kind {
	case "HELP", "TYPE", "UNIT":
	default:
		if p.format == FormatOpenMetrics {
			return false, p.errf("unknown descriptor %q", kind)
		}
		return false, nil // plain comment
	}
	name := fields[1]
	if strings.HasPrefix(name, `"`) {
		// UTF-8 metric name (Prometheus 3): # TYPE "my.metric" counter
		n, after, err := unquotePrefix(strings.TrimLeft(rest, " \t")[len(kind)+1:])
		if err != nil {
			return false, p.errf("invalid quoted metric name: %v", err)
		}
		name = n
		fields = []string{kind, name, strings.TrimLeft(after, " ")}
		if fields[2] == "" {
			fields = fields[:2]
		}
	} else if !validMetricName(name) {
		return false, p.errf("invalid metric name %q", name)
	}
	arg := ""
	if len(fields) == 3 {
		arg = fields[2]
	}
	f := p.family(name)
	p.cur = f
	switch kind {
	case "HELP":
		if p.format == FormatText {
			f.Help = unescapeHelp(arg)
		} else {
			f.Help = unescapeLabelValue(arg)
		}
	case "UNIT":
		f.Unit = arg
	case "TYPE":
		t := strings.ToLower(strings.TrimSpace(arg))
		switch t {
		case "untyped":
			t = TypeUnknown
		case TypeCounter, TypeGauge, TypeHistogram, TypeSummary, TypeUnknown:
		case TypeGaugeHistogram, TypeInfo, TypeStateSet:
			if p.format == FormatText {
				return false, p.errf("type %q is not valid in the text format", t)
			}
		default:
			return false, p.errf("unknown metric type %q", arg)
		}
		if len(f.Samples) > 0 {
			return false, p.errf("# TYPE %s after its samples", name)
		}
		f.Type = t
	}
	return false, nil
}

// suffixes a sample name may carry per family type.
var typeSuffixes = map[string][]string{
	TypeCounter:        {"_total", "_created"},
	TypeHistogram:      {"_bucket", "_sum", "_count", "_created"},
	TypeGaugeHistogram: {"_bucket", "_gsum", "_gcount"},
	TypeSummary:        {"_sum", "_count", "_created"},
	TypeInfo:           {"_info"},
}

// familyOf returns the family a sample name belongs to: the current family when the name is its name or its
// name plus a suffix of its type, otherwise the family of the sample name itself.
func (p *parser) familyOf(sample string) *Family {
	if f := p.cur; f != nil {
		if sample == f.Name {
			return f
		}
		for _, s := range typeSuffixes[f.Type] {
			if sample == f.Name+s {
				return f
			}
		}
	}
	for _, t := range []string{TypeHistogram, TypeSummary, TypeCounter, TypeGaugeHistogram, TypeInfo} {
		for _, s := range typeSuffixes[t] {
			if base, ok := strings.CutSuffix(sample, s); ok {
				if f, ok := p.byName[base]; ok && f.Type == t {
					return f
				}
			}
		}
	}
	f := p.family(sample)
	p.cur = f
	return f
}

func (p *parser) sample(line string) error {
	var s Sample
	rest := line
	var err error
	if strings.HasPrefix(rest, "{") {
		// {"utf8.metric.name", label="v"} value
		s.Name, s.Labels, rest, err = parseLabels(rest, true)
		if err != nil {
			return p.errf("%v", err)
		}
		if s.Name == "" {
			return p.errf("sample without a metric name")
		}
	} else {
		i := strings.IndexAny(rest, "{ \t")
		if i < 0 {
			return p.errf("sample without a value")
		}
		s.Name = rest[:i]
		if !validMetricName(s.Name) {
			return p.errf("invalid metric name %q", s.Name)
		}
		rest = rest[i:]
		if strings.HasPrefix(rest, "{") {
			_, s.Labels, rest, err = parseLabels(rest, false)
			if err != nil {
				return p.errf("%v", err)
			}
		}
	}
	if p.format == FormatOpenMetrics {
		if !strings.HasPrefix(rest, " ") {
			return p.errf("expected a single space before the value")
		}
		rest = rest[1:]
	} else {
		rest = strings.TrimLeft(rest, " \t")
	}
	var exemplarPart string
	if i := strings.Index(rest, " # "); i >= 0 && p.format == FormatOpenMetrics {
		rest, exemplarPart = rest[:i], rest[i+3:]
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 || len(fields) > 2 {
		return p.errf("expected a value and an optional timestamp")
	}
	if s.Value, err = parseFloat(fields[0]); err != nil {
		return p.errf("invalid value %q", fields[0])
	}
	if len(fields) == 2 {
		if s.TimestampMs, err = p.parseTimestamp(fields[1]); err != nil {
			return p.errf("invalid timestamp %q", fields[1])
		}
	}
	if exemplarPart != "" {
		ex, err := parseExemplar(exemplarPart)
		if err != nil {
			return p.errf("exemplar: %v", err)
		}
		s.Exemplar = ex
	}
	f := p.familyOf(s.Name)
	f.Samples = append(f.Samples, s)
	return nil
}

// parseTimestamp returns milliseconds: the text format uses integer milliseconds, OpenMetrics float seconds.
func (p *parser) parseTimestamp(v string) (int64, error) {
	if p.format == FormatText {
		return strconv.ParseInt(v, 10, 64)
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, errors.New("invalid")
	}
	return int64(math.Round(f * 1000)), nil
}

func parseExemplar(s string) (*Exemplar, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") {
		return nil, errors.New("expected labels")
	}
	_, labels, rest, err := parseLabels(s, false)
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 || len(fields) > 2 {
		return nil, errors.New("expected a value and an optional timestamp")
	}
	ex := &Exemplar{Labels: labels}
	if ex.Value, err = parseFloat(fields[0]); err != nil {
		return nil, fmt.Errorf("invalid value %q", fields[0])
	}
	if len(fields) == 2 {
		f, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return nil, fmt.Errorf("invalid timestamp %q", fields[1])
		}
		ex.TimestampMs = int64(math.Round(f * 1000))
	}
	return ex, nil
}

// parseLabels parses {…} at the start of s and returns the rest after the closing brace. When allowName is set,
// a quoted string without "=" is the metric name (UTF-8 names, Prometheus 3).
func parseLabels(s string, allowName bool) (name string, labels []Label, rest string, err error) {
	s = s[1:] // {
	for {
		s = strings.TrimLeft(s, " \t")
		if s == "" {
			return "", nil, "", errors.New("unterminated label set")
		}
		if s[0] == '}' {
			return name, labels, s[1:], nil
		}
		var ln string
		if s[0] == '"' {
			var after string
			ln, after, err = unquotePrefix(s)
			if err != nil {
				return "", nil, "", err
			}
			after = strings.TrimLeft(after, " \t")
			if allowName && name == "" && len(labels) == 0 && (strings.HasPrefix(after, ",") || strings.HasPrefix(after, "}")) {
				name = ln
				s = strings.TrimPrefix(after, ",")
				continue
			}
			s = after
		} else {
			i := 0
			for i < len(s) && isLabelChar(s[i], i == 0) {
				i++
			}
			if i == 0 {
				return "", nil, "", fmt.Errorf("invalid label name at %q", truncate(s, 20))
			}
			ln, s = s[:i], strings.TrimLeft(s[i:], " \t")
		}
		if !strings.HasPrefix(s, "=") {
			return "", nil, "", fmt.Errorf("expected = after label %q", ln)
		}
		s = strings.TrimLeft(s[1:], " \t")
		if !strings.HasPrefix(s, `"`) {
			return "", nil, "", fmt.Errorf("label %q: value must be quoted", ln)
		}
		v, after, err := unquotePrefix(s)
		if err != nil {
			return "", nil, "", fmt.Errorf("label %q: %v", ln, err)
		}
		labels = append(labels, Label{Name: ln, Value: v})
		s = strings.TrimLeft(after, " \t")
		switch {
		case strings.HasPrefix(s, ","):
			s = s[1:]
		case strings.HasPrefix(s, "}"):
		default:
			return "", nil, "", fmt.Errorf("expected , or } after label %q", ln)
		}
	}
}

// unquotePrefix reads a double-quoted string with the \\, \" and \n escapes at the start of s.
func unquotePrefix(s string) (string, string, error) {
	if !strings.HasPrefix(s, `"`) {
		return "", s, errors.New("expected a quoted string")
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			v := b.String()
			if !utf8.ValidString(v) {
				return "", "", errors.New("invalid UTF-8")
			}
			return v, s[i+1:], nil
		case '\\':
			if i+1 >= len(s) {
				return "", "", errors.New("unterminated escape")
			}
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			default:
				// Prometheus keeps unknown escapes verbatim.
				b.WriteByte('\\')
				b.WriteByte(s[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", "", errors.New("unterminated string")
}

func unescapeHelp(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				b.WriteByte('\n')
				i++
				continue
			case '\\':
				b.WriteByte('\\')
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func unescapeLabelValue(s string) string {
	v, _, err := unquotePrefix(`"` + strings.ReplaceAll(s, `"`, `\"`) + `"`)
	if err != nil {
		return s
	}
	return v
}

func parseFloat(s string) (float64, error) {
	switch s {
	case "+Inf", "Inf", "+inf", "inf":
		return math.Inf(1), nil
	case "-Inf", "-inf":
		return math.Inf(-1), nil
	case "NaN", "nan":
		return math.NaN(), nil
	}
	return strconv.ParseFloat(s, 64)
}

func validMetricName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c == '_' || c == ':' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func isLabelChar(c byte, first bool) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || !first && c >= '0' && c <= '9'
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
