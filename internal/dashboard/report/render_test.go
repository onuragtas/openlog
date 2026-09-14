package report

import (
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/dashboard"
	"github.com/onuragtas/openlog/internal/oql"
)

func TestFormatNumber(t *testing.T) {
	for _, tc := range []struct {
		v    float64
		unit string
		want string
	}{
		{0.1234, "percent", "12.34%"},
		{1536, "bytes", "1.5 KiB"},
		{2048, "bytesPerSec", "2 KiB/s"},
		{12, "ms", "12 ms"},
		{1234.56, "", "1234.6"},
		{0.5, "", "0.5"},
		{-0.001, "", "0"},
		{12345678, "number", "12345678"},
	} {
		if got := formatNumber(tc.v, tc.unit); got != tc.want {
			t.Errorf("formatNumber(%v, %q) = %q, want %q", tc.v, tc.unit, got, tc.want)
		}
	}
}

func TestRenderEnglish(t *testing.T) {
	rows := make([]oql.Row, MaxRows+3)
	for i := range rows {
		rows[i] = oql.Row{Facets: []string{"h"}, Values: []any{float64(i)}}
	}
	c := Content{
		Dashboard: &dashboard.Dashboard{ID: "d1", Name: "Ops"},
		Report:    &dashboard.Report{Frequency: "weekly", Language: "en", Timezone: "UTC"},
		From:      time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC), To: time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC),
		Widgets: []WidgetResult{
			{Widget: dashboard.Widget{Title: "Hosts"}, Page: "A", Result: &oql.Result{Kind: oql.KindFacets, Facets: []string{"host.name"},
				Columns: []oql.ResultColumn{{Name: "count(*)", Type: "number"}}, Rows: rows}},
			{Widget: dashboard.Widget{Title: "Broken"}, Page: "B", Err: `unknown attribute "x"`},
			{Widget: dashboard.Widget{Title: "Nothing"}, Page: "B", Result: &oql.Result{Kind: oql.KindFacets}},
		},
		Skipped: 2,
	}
	subject, text, html := Render(c)
	if subject != "[openlog] Weekly report: Ops" {
		t.Errorf("subject %q", subject)
	}
	for _, want := range []string{"First 20 rows.", "The query failed: unknown attribute", "No data", "2 more widgets", "2026-09-07 09:00 – 2026-09-14 09:00 (UTC)"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	if strings.Count(html, "<tr>") != MaxRows+1 || !strings.Contains(html, "· B") || strings.Contains(html, "Open the dashboard") {
		t.Errorf("html:\n%s", html)
	}
}
