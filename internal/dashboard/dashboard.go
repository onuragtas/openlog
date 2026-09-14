// Package dashboard implements custom dashboards of OQL widgets (docs/contracts/api.md "Dashboards",
// postgres.md, D-064): validation, permissions, import/export and the PostgreSQL store.
package dashboard

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/onuragtas/openlog/internal/oql"
)

// Errors returned by Manager.
var (
	ErrNotFound  = errors.New("dashboard not found")
	ErrConflict  = errors.New("dashboard was changed")
	ErrForbidden = errors.New("not allowed")
)

// ValidationError is an invalid input (400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// Limits (api.md "Dashboards").
const (
	MaxPages          = 20
	MaxWidgetsPerPage = 100
	MaxVariables      = 10
	MaxVariableValues = 200
	MaxThresholds     = 10
	GridColumns       = 12
	maxMarkdown       = 20000
	ExportVersion     = 1
)

// Visualizations lists the widget visualizations.
var Visualizations = []string{"line", "area", "bar", "table", "billboard", "pie", "heatmap", "markdown"}

var (
	visualizations = map[string]bool{}
	units          = map[string]bool{"": true, "number": true, "percent": true, "bytes": true, "bytesPerSec": true, "ms": true, "s": true}
	variableNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
)

func init() {
	for _, v := range Visualizations {
		visualizations[v] = true
	}
}

// Layout is a widget position on the 12-column grid.
type Layout struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// Threshold colors a widget value.
type Threshold struct {
	Value    float64 `json:"value"`
	Severity string  `json:"severity"`
}

// WidgetOptions are visualization options.
type WidgetOptions struct {
	Stacked bool  `json:"stacked,omitempty"`
	Legend  *bool `json:"legend,omitempty"`
}

// Widget is one dashboard widget.
type Widget struct {
	ID            string        `json:"id,omitempty"`
	Title         string        `json:"title"`
	Visualization string        `json:"visualization"`
	Layout        Layout        `json:"layout"`
	Query         string        `json:"query"`
	Markdown      string        `json:"markdown"`
	Unit          string        `json:"unit"`
	Thresholds    []Threshold   `json:"thresholds"`
	Options       WidgetOptions `json:"options"`
}

// Page is a dashboard page.
type Page struct {
	ID      string   `json:"id,omitempty"`
	Name    string   `json:"name"`
	Widgets []Widget `json:"widgets"`
}

// Variable is a dashboard template variable ({{name}} in widget queries).
type Variable struct {
	Name       string   `json:"name"`
	Label      string   `json:"label"`
	Type       string   `json:"type"`
	Query      string   `json:"query"`
	Values     []string `json:"values"`
	Default    []string `json:"default"`
	Multi      bool     `json:"multi"`
	IncludeAll bool     `json:"include_all"`
}

// Dashboard is a stored dashboard.
type Dashboard struct {
	ID             string
	OrgID          string
	Name           string
	Description    string
	Visibility     string
	Variables      []Variable
	Pages          []Page
	Version        int
	CreatedBy      string // "" = deleted user
	CreatedByEmail string
	UpdatedBy      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Summary is a list entry.
type Summary struct {
	ID             string
	Name           string
	Description    string
	Visibility     string
	PageCount      int
	WidgetCount    int
	CreatedBy      string
	CreatedByEmail string
	UpdatedAt      time.Time
}

// Input is the API representation of a dashboard document.
type Input struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Visibility  string     `json:"visibility"`
	Variables   []Variable `json:"variables"`
	Pages       []Page     `json:"pages"`
	Version     int        `json:"version"`
}

// Export is the portable dashboard document (import/export).
type Export struct {
	OpenlogDashboard int        `json:"openlog_dashboard"`
	Name             string     `json:"name"`
	Description      string     `json:"description"`
	Visibility       string     `json:"visibility,omitempty"`
	Variables        []Variable `json:"variables"`
	Pages            []Page     `json:"pages"`
}

func checkText(field, v string, lo, hi int) (string, error) {
	v = strings.TrimSpace(v)
	n := utf8.RuneCountInString(v)
	if n < lo || n > hi {
		if lo > 0 {
			return "", invalid("%s must be %d-%d characters", field, lo, hi)
		}
		return "", invalid("%s must be at most %d characters", field, hi)
	}
	if strings.ContainsFunc(v, func(r rune) bool { return (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f }) {
		return "", invalid("%s contains control characters", field)
	}
	return v, nil
}

// normalize validates in and returns the document (ids are assigned by the manager).
func normalize(in Input, now time.Time) (*Dashboard, error) {
	d := &Dashboard{Visibility: in.Visibility}
	var err error
	if d.Name, err = checkText("name", in.Name, 1, 200); err != nil {
		return nil, err
	}
	if strings.Contains(d.Name, "\n") {
		return nil, invalid("name must be a single line")
	}
	if d.Description, err = checkText("description", in.Description, 0, 2000); err != nil {
		return nil, err
	}
	switch d.Visibility {
	case "":
		d.Visibility = "org"
	case "org", "private":
	default:
		return nil, invalid("visibility must be org or private")
	}
	if len(in.Variables) > MaxVariables {
		return nil, invalid("at most %d variables", MaxVariables)
	}
	seen := map[string]bool{}
	d.Variables = []Variable{}
	for i, v := range in.Variables {
		nv, err := normalizeVariable(fmt.Sprintf("variables[%d]", i), v, now)
		if err != nil {
			return nil, err
		}
		if seen[nv.Name] {
			return nil, invalid("variables[%d].name: duplicate variable %q", i, nv.Name)
		}
		seen[nv.Name] = true
		d.Variables = append(d.Variables, nv)
	}
	pages := in.Pages
	if len(pages) == 0 {
		pages = []Page{{Name: "Page 1"}}
	}
	if len(pages) > MaxPages {
		return nil, invalid("at most %d pages", MaxPages)
	}
	for i, pg := range pages {
		field := fmt.Sprintf("pages[%d]", i)
		np := Page{ID: pg.ID, Widgets: []Widget{}}
		if np.Name, err = checkText(field+".name", pg.Name, 1, 100); err != nil {
			return nil, err
		}
		if len(pg.Widgets) > MaxWidgetsPerPage {
			return nil, invalid("%s: at most %d widgets", field, MaxWidgetsPerPage)
		}
		for j, w := range pg.Widgets {
			nw, err := normalizeWidget(fmt.Sprintf("%s.widgets[%d]", field, j), w, now)
			if err != nil {
				return nil, err
			}
			np.Widgets = append(np.Widgets, nw)
		}
		d.Pages = append(d.Pages, np)
	}
	return d, nil
}

func normalizeWidget(field string, w Widget, now time.Time) (Widget, error) {
	var err error
	nw := Widget{ID: w.ID, Visualization: w.Visualization, Layout: w.Layout, Unit: w.Unit, Options: w.Options, Thresholds: []Threshold{}}
	if nw.Title, err = checkText(field+".title", w.Title, 0, 200); err != nil {
		return nw, err
	}
	if !visualizations[nw.Visualization] {
		return nw, invalid("%s.visualization must be one of %s", field, strings.Join(Visualizations, ", "))
	}
	l := nw.Layout
	if l.X < 0 || l.W < 1 || l.W > GridColumns || l.X+l.W > GridColumns || l.Y < 0 || l.Y > 10000 || l.H < 1 || l.H > 50 {
		return nw, invalid("%s.layout must fit the 12-column grid (0 <= x, 1 <= w <= 12, x + w <= 12, 0 <= y <= 10000, 1 <= h <= 50)", field)
	}
	if !units[nw.Unit] {
		return nw, invalid("%s.unit must be empty or one of number, percent, bytes, bytesPerSec, ms, s", field)
	}
	if len(w.Thresholds) > MaxThresholds {
		return nw, invalid("%s: at most %d thresholds", field, MaxThresholds)
	}
	for k, t := range w.Thresholds {
		if math.IsNaN(t.Value) || math.IsInf(t.Value, 0) || (t.Severity != "warning" && t.Severity != "critical") {
			return nw, invalid("%s.thresholds[%d] needs a finite value and severity warning or critical", field, k)
		}
		nw.Thresholds = append(nw.Thresholds, t)
	}
	if nw.Visualization == "markdown" {
		if utf8.RuneCountInString(w.Markdown) > maxMarkdown {
			return nw, invalid("%s.markdown must be at most %d characters", field, maxMarkdown)
		}
		if strings.TrimSpace(w.Query) != "" {
			return nw, invalid("%s: markdown widgets have no query", field)
		}
		nw.Markdown = w.Markdown
		return nw, nil
	}
	if w.Markdown != "" {
		return nw, invalid("%s: only markdown widgets have markdown", field)
	}
	nw.Query = strings.TrimSpace(w.Query)
	if nw.Query == "" {
		return nw, invalid("%s.query is required", field)
	}
	if _, err := oql.Compile(nw.Query, oql.Options{Now: now}); err != nil {
		return nw, invalid("%s.query: %s", field, oql.Describe(nw.Query, err))
	}
	return nw, nil
}

func normalizeVariable(field string, v Variable, now time.Time) (Variable, error) {
	nv := Variable{Name: v.Name, Type: v.Type, Multi: v.Multi, IncludeAll: v.IncludeAll, Values: []string{}, Default: []string{}}
	if !variableNameRe.MatchString(nv.Name) {
		return nv, invalid("%s.name must match [A-Za-z_][A-Za-z0-9_]{0,63}", field)
	}
	var err error
	if nv.Label, err = checkText(field+".label", v.Label, 0, 100); err != nil {
		return nv, err
	}
	if nv.Label == "" {
		nv.Label = nv.Name
	}
	checkValues := func(name string, vals []string) ([]string, error) {
		if len(vals) > MaxVariableValues {
			return nil, invalid("%s.%s: at most %d values", field, name, MaxVariableValues)
		}
		out := []string{}
		for _, s := range vals {
			if len(s) > 1024 || strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
				return nil, invalid("%s.%s: values must be at most 1024 bytes without control characters", field, name)
			}
			out = append(out, s)
		}
		return out, nil
	}
	if nv.Default, err = checkValues("default", v.Default); err != nil {
		return nv, err
	}
	switch nv.Type {
	case "query":
		nv.Query = strings.TrimSpace(v.Query)
		p, err := oql.Compile(nv.Query, oql.Options{Now: now})
		if err != nil {
			return nv, invalid("%s.query: %s", field, oql.Describe(nv.Query, err))
		}
		if len(p.Facets) == 0 || p.Kind == oql.KindTimeseries {
			return nv, invalid("%s.query needs FACET (the values are the first facet) and no TIMESERIES", field)
		}
		if len(p.Variables) > 0 {
			return nv, invalid("%s.query cannot use variables", field)
		}
	case "list":
		if nv.Values, err = checkValues("values", v.Values); err != nil {
			return nv, err
		}
	case "text":
	default:
		return nv, invalid("%s.type must be query, list or text", field)
	}
	if nv.Type != "list" && len(v.Values) > 0 {
		return nv, invalid("%s.values are only used by list variables", field)
	}
	if nv.Type != "query" && strings.TrimSpace(v.Query) != "" {
		return nv, invalid("%s.query is only used by query variables", field)
	}
	if !nv.Multi && len(nv.Default) > 1 {
		return nv, invalid("%s.default: a single-value variable has at most one default", field)
	}
	return nv, nil
}

// ToExport renders d as a portable document (no ids or users).
func (d *Dashboard) ToExport() *Export {
	ex := &Export{OpenlogDashboard: ExportVersion, Name: d.Name, Description: d.Description, Variables: d.Variables, Pages: []Page{}}
	if ex.Variables == nil {
		ex.Variables = []Variable{}
	}
	for _, p := range d.Pages {
		np := Page{Name: p.Name, Widgets: []Widget{}}
		for _, w := range p.Widgets {
			w.ID = ""
			np.Widgets = append(np.Widgets, w)
		}
		ex.Pages = append(ex.Pages, np)
	}
	return ex
}

// input converts a stored dashboard back to an input document (ids kept).
func (d *Dashboard) input() Input {
	return Input{Name: d.Name, Description: d.Description, Visibility: d.Visibility, Variables: d.Variables, Pages: d.Pages, Version: d.Version}
}
