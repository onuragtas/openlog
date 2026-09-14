// Package report sends scheduled dashboard reports by e-mail (api.md "Dashboards" › "Scheduled reports", D-087):
// every widget query runs server-side for the report period and its values are rendered as simple HTML tables with a
// link to the dashboard. With a renderer (D-097, images.go) widgets are shown as inline PNG images instead.
package report

import (
	"bytes"
	"fmt"
	"html/template"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/dashboard"
	"github.com/onuragtas/openlog/internal/oql"
)

// MaxRows bounds the rows of one widget table in a report.
const MaxRows = 20

// WidgetResult is the outcome of one widget query.
type WidgetResult struct {
	Widget dashboard.Widget
	Page   string
	Result *oql.Result
	Err    string
}

// Content is everything a report e-mail shows.
type Content struct {
	Dashboard *dashboard.Dashboard
	Report    *dashboard.Report
	From, To  time.Time
	Link      string // "" when OPENLOG_PUBLIC_URL is not set
	Widgets   []WidgetResult
	Skipped   int // widgets beyond the report's widget limit
	// Images are PNG renderings by widget id (images.go, D-097); widgets without one are shown as tables.
	Images map[string]Image
}

type messages struct {
	Subject, Intro, Period, Open, Error, Empty, NoData, Truncated, Skipped, Footer, Value, Series, Average, Min, Max, Last, Bucket, Count string
	Daily, Weekly                                                                                                                         string
}

var texts = map[string]messages{
	"en": {
		Subject: "[openlog] %s report: %s", Intro: "Scheduled report of the dashboard %q.", Period: "Period: %s – %s (%s)",
		Open: "Open the dashboard", Error: "The query failed: %s", Empty: "This dashboard has no widgets with queries.", NoData: "No data",
		Truncated: "First %d rows.", Skipped: "%d more widgets are not included.", Footer: "You receive this e-mail because you are a recipient of this scheduled report. Its owner or an organization admin can change or delete it in the dashboard's report settings.",
		Value: "Value", Series: "Series", Average: "Average", Min: "Min", Max: "Max", Last: "Last", Bucket: "Bucket", Count: "Count",
		Daily: "Daily", Weekly: "Weekly",
	},
	"tr": {
		Subject: "[openlog] %s rapor: %s", Intro: "%q panosunun zamanlanmış raporu.", Period: "Dönem: %s – %s (%s)",
		Open: "Panoyu aç", Error: "Sorgu başarısız oldu: %s", Empty: "Bu panoda sorgulu widget yok.", NoData: "Veri yok",
		Truncated: "İlk %d satır.", Skipped: "%d widget daha rapora eklenmedi.", Footer: "Bu e-postayı zamanlanmış bu raporun alıcısı olduğunuz için alıyorsunuz. Raporu sahibi veya bir organizasyon yöneticisi panonun rapor ayarlarından değiştirebilir ya da silebilir.",
		Value: "Değer", Series: "Seri", Average: "Ortalama", Min: "En düşük", Max: "En yüksek", Last: "Son", Bucket: "Aralık", Count: "Adet",
		Daily: "Günlük", Weekly: "Haftalık",
	},
}

func textsFor(lang string) messages {
	if m, ok := texts[lang]; ok {
		return m
	}
	return texts["en"]
}

// Table is a rendered widget table.
type Table struct {
	Title     string
	Page      string
	Error     string
	Head      []string
	Numeric   []bool // per column: right-aligned
	Rows      [][]string
	Truncated bool
	Empty     bool
	Image     *TableImage // shown instead of the table in the HTML body (images.go)
}

// Tables renders the widget results as tables.
func Tables(c Content) []Table {
	m := textsFor(c.Report.Language)
	out := make([]Table, 0, len(c.Widgets))
	for i, w := range c.Widgets {
		t := Table{Title: w.Widget.Title, Page: w.Page, Image: tableImage(c, i)}
		if t.Title == "" {
			t.Title = w.Widget.Query
		}
		switch {
		case w.Err != "":
			t.Error = fmt.Sprintf(m.Error, w.Err)
		case w.Result != nil:
			fillTable(&t, w.Result, w.Widget.Unit, m)
		}
		out = append(out, t)
	}
	return out
}

func fillTable(t *Table, r *oql.Result, unit string, m messages) {
	cols := make([]string, len(r.Columns))
	for i, c := range r.Columns {
		cols[i] = c.Name
	}
	add := func(row []string) {
		if len(t.Rows) == MaxRows {
			t.Truncated = true
			return
		}
		t.Rows = append(t.Rows, row)
	}
	switch r.Kind {
	case oql.KindSingle:
		t.Head, t.Numeric = []string{"", m.Value}, []bool{false, true}
		if len(r.Rows) > 0 {
			for i, c := range cols {
				add([]string{c, formatValue(r.Rows[0].Values[i], unit)})
			}
		}
	case oql.KindFacets:
		t.Head = append(append([]string{}, r.Facets...), cols...)
		for range r.Facets {
			t.Numeric = append(t.Numeric, false)
		}
		for _, c := range r.Columns {
			t.Numeric = append(t.Numeric, c.Type != "string")
		}
		for _, row := range r.Rows {
			cells := append([]string{}, row.Facets...)
			for _, v := range row.Values {
				cells = append(cells, formatValue(v, unit))
			}
			add(cells)
		}
		t.Truncated = t.Truncated || r.Metadata.Truncated
	case oql.KindTimeseries:
		t.Head, t.Numeric = []string{m.Series, m.Average, m.Min, m.Max, m.Last}, []bool{false, true, true, true, true}
		for _, s := range r.Series {
			label := strings.Join(s.Facets, " · ")
			if len(cols) > 1 || label == "" {
				if label != "" {
					label += " · "
				}
				label += cols[s.Column]
			}
			avg, lo, hi, last, ok := stats(s.Points)
			if !ok {
				add([]string{label, "–", "–", "–", "–"})
				continue
			}
			add([]string{label, formatValue(avg, unit), formatValue(lo, unit), formatValue(hi, unit), formatValue(last, unit)})
		}
	case oql.KindHistogram:
		t.Head, t.Numeric = []string{m.Bucket, m.Count}, []bool{false, true}
		for _, b := range r.Buckets {
			if b.Count > 0 {
				add([]string{formatValue(b.From, unit) + " – " + formatValue(b.To, unit), formatValue(b.Count, "number")})
			}
		}
	}
	t.Empty = len(t.Rows) == 0
}

func stats(points []oql.Point) (avg, lo, hi, last float64, ok bool) {
	lo, hi = math.Inf(1), math.Inf(-1)
	var sum float64
	n := 0
	for _, p := range points {
		v, isNum := p[1].(float64)
		if !isNum || math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		sum += v
		n++
		lo, hi, last = math.Min(lo, v), math.Max(hi, v), v
	}
	if n == 0 {
		return 0, 0, 0, 0, false
	}
	return sum / float64(n), lo, hi, last, true
}

// formatValue formats a result value for a unit (number, percent 0–1, bytes, bytesPerSec, ms, s).
func formatValue(v any, unit string) string {
	switch x := v.(type) {
	case nil:
		return "–"
	case string:
		return x
	case float64:
		return formatNumber(x, unit)
	case int64:
		return formatNumber(float64(x), unit)
	case int:
		return formatNumber(float64(x), unit)
	}
	return fmt.Sprint(v)
}

func formatNumber(f float64, unit string) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "–"
	}
	switch unit {
	case "percent":
		return trimFloat(f*100) + "%"
	case "bytes", "bytesPerSec":
		suffix := ""
		if unit == "bytesPerSec" {
			suffix = "/s"
		}
		units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
		i := 0
		for math.Abs(f) >= 1024 && i < len(units)-1 {
			f /= 1024
			i++
		}
		return trimFloat(f) + " " + units[i] + suffix
	case "ms":
		return trimFloat(f) + " ms"
	case "s":
		return trimFloat(f) + " s"
	}
	return trimFloat(f)
}

func trimFloat(f float64) string {
	switch a := math.Abs(f); {
	case a >= 1e6:
		return strconv.FormatFloat(f, 'f', 0, 64)
	case a >= 100:
		return strconv.FormatFloat(f, 'f', 1, 64)
	default:
		s := strconv.FormatFloat(f, 'f', 2, 64)
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
		if s == "-0" {
			return "0"
		}
		return s
	}
}

var mailTemplate = template.Must(template.New("report").Parse(`<!doctype html>
<html><body style="margin:0;padding:16px;background:#f6f7f9;font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;color:#111827">
<div style="max-width:760px;margin:0 auto;background:#ffffff;border:1px solid #e5e7eb;border-radius:8px;padding:20px">
<h1 style="margin:0 0 4px;font-size:20px">{{.Name}}</h1>
<p style="margin:0 0 4px;color:#4b5563;font-size:14px">{{.Intro}}</p>
<p style="margin:0 0 16px;color:#4b5563;font-size:13px">{{.Period}}</p>
{{if .Link}}<p style="margin:0 0 20px"><a href="{{.Link}}" style="display:inline-block;background:#2563eb;color:#ffffff;text-decoration:none;padding:8px 14px;border-radius:6px;font-size:14px">{{.Open}}</a></p>{{end}}
{{if not .Tables}}<p style="color:#4b5563">{{.EmptyText}}</p>{{end}}
{{range .Tables}}
<h2 style="margin:20px 0 6px;font-size:15px">{{.Title}}{{if $.MultiPage}} <span style="color:#6b7280;font-weight:normal">· {{.Page}}</span>{{end}}</h2>
{{if .Image}}<img src="{{.Image.Src}}" width="{{.Image.Width}}" height="{{.Image.Height}}" alt="{{.Title}}" style="display:block;width:100%;max-width:{{.Image.Width}}px;height:auto;border:0">
{{else if .Error}}<p style="margin:0;color:#b91c1c;font-size:13px">{{.Error}}</p>
{{else if .Empty}}<p style="margin:0;color:#6b7280;font-size:13px">{{$.NoData}}</p>
{{else}}<table style="border-collapse:collapse;width:100%;font-size:13px">
{{if .Head}}<tr>{{range .Head}}<th style="text-align:left;border-bottom:1px solid #e5e7eb;padding:4px 6px;color:#374151">{{.}}</th>{{end}}</tr>{{end}}
{{$t := .}}{{range .Rows}}<tr>{{range $i, $c := .}}<td style="border-bottom:1px solid #f3f4f6;padding:4px 6px;{{if $t.IsNumeric $i}}text-align:right;font-variant-numeric:tabular-nums;{{end}}">{{$c}}</td>{{end}}</tr>{{end}}
</table>
{{if .Truncated}}<p style="margin:4px 0 0;color:#6b7280;font-size:12px">{{$.TruncatedText}}</p>{{end}}
{{end}}
{{end}}
{{if .SkippedText}}<p style="margin:16px 0 0;color:#6b7280;font-size:12px">{{.SkippedText}}</p>{{end}}
<p style="margin:24px 0 0;color:#9ca3af;font-size:11px">{{.Footer}}</p>
</div></body></html>`))

// IsNumeric reports whether column i is right-aligned.
func (t Table) IsNumeric(i int) bool { return i < len(t.Numeric) && t.Numeric[i] }

type mailData struct {
	Name, Intro, Period, Link, Open, EmptyText, NoData, TruncatedText, SkippedText, Footer string
	MultiPage                                                                              bool
	Tables                                                                                 []Table
}

// Render returns the subject, plain text and HTML body of a report e-mail.
func Render(c Content) (subject, text, htmlBody string) {
	m := textsFor(c.Report.Language)
	loc := c.Report.Location()
	freq := m.Daily
	if c.Report.Frequency == "weekly" {
		freq = m.Weekly
	}
	name := c.Dashboard.Name
	if c.Report.Name != "" {
		name = c.Report.Name
	}
	subject = fmt.Sprintf(m.Subject, freq, name)
	period := fmt.Sprintf(m.Period, c.From.In(loc).Format("2006-01-02 15:04"), c.To.In(loc).Format("2006-01-02 15:04"), loc.String())
	tables := Tables(c)
	pages := map[string]bool{}
	for _, t := range tables {
		pages[t.Page] = true
	}
	data := mailData{Name: name, Intro: fmt.Sprintf(m.Intro, c.Dashboard.Name), Period: period, Link: c.Link, Open: m.Open,
		EmptyText: m.Empty, NoData: m.NoData, TruncatedText: fmt.Sprintf(m.Truncated, MaxRows), Footer: m.Footer,
		MultiPage: len(pages) > 1, Tables: tables}
	if c.Skipped > 0 {
		data.SkippedText = fmt.Sprintf(m.Skipped, c.Skipped)
	}
	var hb bytes.Buffer
	if err := mailTemplate.Execute(&hb, data); err == nil {
		htmlBody = hb.String()
	}

	var tb strings.Builder
	tb.WriteString(name + "\n" + data.Intro + "\n" + period + "\n")
	if c.Link != "" {
		tb.WriteString(m.Open + ": " + c.Link + "\n")
	}
	if len(tables) == 0 {
		tb.WriteString("\n" + m.Empty + "\n")
	}
	for _, t := range tables {
		tb.WriteString("\n== " + t.Title + " ==\n")
		switch {
		case t.Error != "":
			tb.WriteString(t.Error + "\n")
		case t.Empty:
			tb.WriteString(m.NoData + "\n")
		default:
			if len(t.Head) > 0 {
				tb.WriteString(strings.Join(t.Head, " | ") + "\n")
			}
			for _, row := range t.Rows {
				tb.WriteString(strings.Join(row, " | ") + "\n")
			}
			if t.Truncated {
				tb.WriteString(data.TruncatedText + "\n")
			}
		}
	}
	if data.SkippedText != "" {
		tb.WriteString("\n" + data.SkippedText + "\n")
	}
	tb.WriteString("\n-- \n" + m.Footer + "\n")
	return subject, tb.String(), htmlBody
}

// sortedVariables renders locked variables for logs.
func sortedVariables(v map[string][]string) string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+strings.Join(v[k], ","))
	}
	return strings.Join(parts, " ")
}
