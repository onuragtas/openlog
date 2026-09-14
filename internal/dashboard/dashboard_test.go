package dashboard_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/onuragtas/openlog/internal/dashboard"
	"github.com/onuragtas/openlog/internal/dashboard/dashboardtest"
)

const (
	org      = "11111111-1111-1111-1111-111111111111"
	otherOrg = "22222222-2222-2222-2222-222222222222"
	alice    = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	bob      = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	carol    = "cccccccc-cccc-cccc-cccc-cccccccccccc"
)

var (
	ctx      = context.Background()
	memberA  = dashboard.Viewer{UserID: alice, CanWrite: true}
	memberB  = dashboard.Viewer{UserID: bob, CanWrite: true}
	adminC   = dashboard.Viewer{UserID: carol, CanWrite: true, Admin: true}
	viewer   = dashboard.Viewer{UserID: "dddddddd-dddd-dddd-dddd-dddddddddddd"}
	apiKey   = dashboard.Viewer{Admin: true}
	boolTrue = true
)

func sample() dashboard.Input {
	return dashboard.Input{
		Name: "Checkout",
		Variables: []dashboard.Variable{
			{Name: "host", Type: "query", Query: "SELECT count(*) FROM Log FACET host.name LIMIT 100", Multi: true, IncludeAll: true},
			{Name: "env", Label: "Environment", Type: "list", Values: []string{"prod", "staging"}, Default: []string{"prod"}},
		},
		Pages: []dashboard.Page{{Name: "Overview", Widgets: []dashboard.Widget{
			{Title: "Errors", Visualization: "line", Layout: dashboard.Layout{X: 0, Y: 0, W: 6, H: 3},
				Query: "SELECT count(*) FROM Log WHERE severity = 'ERROR' AND host.name IN ({{host}}) TIMESERIES", Unit: "number",
				Thresholds: []dashboard.Threshold{{Value: 10, Severity: "warning"}}, Options: dashboard.WidgetOptions{Legend: &boolTrue}},
			{Title: "Notes", Visualization: "markdown", Layout: dashboard.Layout{X: 6, Y: 0, W: 6, H: 3}, Markdown: "# Hello"},
		}}},
	}
}

func TestValidation(t *testing.T) {
	m := dashboard.NewManager(dashboardtest.New())
	mutate := func(f func(in *dashboard.Input)) dashboard.Input {
		in := sample()
		f(&in)
		return in
	}
	w := func(in *dashboard.Input) *dashboard.Widget { return &in.Pages[0].Widgets[0] }
	cases := map[string]dashboard.Input{
		"name must be 1-200":               mutate(func(in *dashboard.Input) { in.Name = "  " }),
		"visibility must be":               mutate(func(in *dashboard.Input) { in.Visibility = "public" }),
		"at most 20 pages":                 mutate(func(in *dashboard.Input) { in.Pages = make([]dashboard.Page, 21) }),
		"pages[0].name":                    mutate(func(in *dashboard.Input) { in.Pages[0].Name = "" }),
		"layout must fit":                  mutate(func(in *dashboard.Input) { w(in).Layout = dashboard.Layout{X: 8, W: 6, H: 2} }),
		"visualization must be":            mutate(func(in *dashboard.Input) { w(in).Visualization = "gauge" }),
		"unit must be":                     mutate(func(in *dashboard.Input) { w(in).Unit = "furlongs" }),
		"severity warning or critical":     mutate(func(in *dashboard.Input) { w(in).Thresholds[0].Severity = "info" }),
		"query is required":                mutate(func(in *dashboard.Input) { w(in).Query = "" }),
		"line 1, column 22: unknown event": mutate(func(in *dashboard.Input) { w(in).Query = "SELECT count(*) FROM Logz" }),
		"markdown widgets have no query":   mutate(func(in *dashboard.Input) { in.Pages[0].Widgets[1].Query = "SELECT count(*) FROM Log" }),
		"needs FACET":                      mutate(func(in *dashboard.Input) { in.Variables[0].Query = "SELECT count(*) FROM Log" }),
		"duplicate variable":               mutate(func(in *dashboard.Input) { in.Variables[1].Name = "host" }),
		"type must be query, list":         mutate(func(in *dashboard.Input) { in.Variables[1].Type = "range" }),
		"name must match":                  mutate(func(in *dashboard.Input) { in.Variables[1].Name = "1x" }),
		"at most one default":              mutate(func(in *dashboard.Input) { in.Variables[1].Default = []string{"prod", "staging"} }),
		"only used by list variables":      mutate(func(in *dashboard.Input) { in.Variables[0].Values = []string{"x"} }),
	}
	for want, in := range cases {
		_, err := m.Create(ctx, org, in, memberA)
		var ve *dashboard.ValidationError
		if !errors.As(err, &ve) || !strings.Contains(ve.Msg, want) {
			t.Errorf("%s: got %v", want, err)
		}
	}
	d, err := m.Create(ctx, org, dashboard.Input{Name: "Empty"}, memberA)
	if err != nil || len(d.Pages) != 1 || d.Pages[0].Name != "Page 1" || d.Visibility != "org" || d.Version != 1 {
		t.Fatalf("defaults: %+v %v", d, err)
	}
}

func TestPermissions(t *testing.T) {
	store := dashboardtest.New()
	m := dashboard.NewManager(store)
	orgD, err := m.Create(ctx, org, sample(), memberA)
	if err != nil {
		t.Fatal(err)
	}
	priv := sample()
	priv.Name, priv.Visibility = "Mine", "private"
	privD, err := m.Create(ctx, org, priv, memberA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(ctx, org, sample(), viewer); !errors.Is(err, dashboard.ErrForbidden) {
		t.Errorf("viewer create: %v", err)
	}
	if _, err := m.Create(ctx, org, sample(), apiKey); !errors.Is(err, dashboard.ErrForbidden) {
		t.Errorf("api key create: %v", err)
	}
	for _, tc := range []struct {
		name     string
		v        dashboard.Viewer
		d        *dashboard.Dashboard
		read     bool
		edit     bool
		listSize int
	}{
		{"creator org", memberA, orgD, true, true, 2},
		{"creator private", memberA, privD, true, true, 2},
		{"member org", memberB, orgD, true, false, 1},
		{"member private", memberB, privD, false, false, 1},
		{"admin org", adminC, orgD, true, true, 1},
		{"admin private of live user", adminC, privD, false, false, 1},
		{"viewer org", viewer, orgD, true, false, 1},
		{"api key org", apiKey, orgD, true, false, 1},
		{"api key private", apiKey, privD, false, false, 1},
	} {
		_, err := m.Get(ctx, org, tc.d.ID, tc.v)
		if (err == nil) != tc.read {
			t.Errorf("%s: read err %v", tc.name, err)
		}
		in := sample()
		in.Version = 99
		if tc.d.Visibility == "private" {
			in.Visibility = "private"
		}
		_, err = m.Update(ctx, org, tc.d.ID, in, tc.v)
		switch {
		case !tc.read && !errors.Is(err, dashboard.ErrNotFound):
			t.Errorf("%s: update of unreadable: %v", tc.name, err)
		case tc.read && !tc.edit && !errors.Is(err, dashboard.ErrForbidden):
			t.Errorf("%s: update without permission: %v", tc.name, err)
		case tc.edit && !errors.Is(err, dashboard.ErrConflict):
			t.Errorf("%s: update with stale version: %v", tc.name, err)
		}
		list, _ := m.List(ctx, org, tc.v, "")
		if len(list) != tc.listSize {
			t.Errorf("%s: list %d", tc.name, len(list))
		}
		if tc.read && tc.v.CanEdit(tc.d) != tc.edit {
			t.Errorf("%s: CanEdit", tc.name)
		}
	}
	// Other organizations never see it.
	if _, err := m.Get(ctx, otherOrg, orgD.ID, adminC); !errors.Is(err, dashboard.ErrNotFound) {
		t.Errorf("cross-org get: %v", err)
	}
	if _, err := m.Delete(ctx, otherOrg, orgD.ID, adminC); !errors.Is(err, dashboard.ErrNotFound) {
		t.Errorf("cross-org delete: %v", err)
	}
	// An admin cannot make someone else's dashboard private.
	in := sample()
	in.Version, in.Visibility = orgD.Version, "private"
	if _, err := m.Update(ctx, org, orgD.ID, in, adminC); err == nil || !strings.Contains(err.Error(), "only the creator") {
		t.Errorf("admin visibility change: %v", err)
	}
	// Private dashboards of deleted users become visible and editable for admins.
	store.ForgetCreator(alice)
	if d, err := m.Get(ctx, org, privD.ID, adminC); err != nil || !adminC.CanEdit(d) {
		t.Errorf("orphan private: %v", err)
	}
	if _, err := m.Get(ctx, org, privD.ID, memberB); !errors.Is(err, dashboard.ErrNotFound) {
		t.Errorf("orphan private for member: %v", err)
	}
	if _, err := m.Delete(ctx, org, privD.ID, adminC); err != nil {
		t.Errorf("admin delete orphan: %v", err)
	}
}

func TestUpdateKeepsIDsAndVersions(t *testing.T) {
	m := dashboard.NewManager(dashboardtest.New())
	d, err := m.Create(ctx, org, sample(), memberA)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := m.Create(ctx, org, sample(), memberA)
	in := dashboard.Input{Name: d.Name, Version: d.Version, Variables: d.Variables, Pages: d.Pages}
	in.Pages[0].Widgets = append(in.Pages[0].Widgets,
		dashboard.Widget{ID: other.Pages[0].Widgets[0].ID, Title: "foreign id", Visualization: "table", Layout: dashboard.Layout{Y: 3, W: 12, H: 2}, Query: "SELECT count(*) FROM Span FACET name"},
		dashboard.Widget{ID: d.Pages[0].Widgets[0].ID, Title: "reused id", Visualization: "billboard", Layout: dashboard.Layout{Y: 5, W: 3, H: 2}, Query: "SELECT count(*) FROM Span"})
	u, err := m.Update(ctx, org, d.ID, in, memberA)
	if err != nil {
		t.Fatal(err)
	}
	ws := u.Pages[0].Widgets
	if u.Version != 2 || u.Pages[0].ID != d.Pages[0].ID || ws[0].ID != d.Pages[0].Widgets[0].ID || ws[1].ID != d.Pages[0].Widgets[1].ID {
		t.Errorf("ids not kept: %+v", u.Pages)
	}
	if ws[2].ID == other.Pages[0].Widgets[0].ID || ws[3].ID == ws[0].ID || ws[2].ID == "" {
		t.Errorf("foreign or duplicate ids kept: %s %s", ws[2].ID, ws[3].ID)
	}
	if _, err := m.Update(ctx, org, d.ID, in, memberA); !errors.Is(err, dashboard.ErrConflict) {
		t.Errorf("stale version: %v", err)
	}
	in.Version = 0
	if _, err := m.Update(ctx, org, d.ID, in, memberA); err == nil || !strings.Contains(err.Error(), "version is required") {
		t.Errorf("missing version: %v", err)
	}
}

func TestAddWidgetDuplicateExportImport(t *testing.T) {
	m := dashboard.NewManager(dashboardtest.New())
	d, err := m.Create(ctx, org, sample(), memberA)
	if err != nil {
		t.Fatal(err)
	}
	u, err := m.AddWidget(ctx, org, d.ID, "", dashboard.Widget{Title: "From console", Visualization: "bar", Query: "SELECT count(*) FROM Log FACET service.name"}, memberA)
	if err != nil {
		t.Fatal(err)
	}
	nw := u.Pages[0].Widgets[2]
	if nw.Layout != (dashboard.Layout{X: 0, Y: 3, W: 6, H: 3}) || u.Version != 2 {
		t.Errorf("added widget %+v version %d", nw.Layout, u.Version)
	}
	if _, err := m.AddWidget(ctx, org, d.ID, "33333333-3333-3333-3333-333333333333", nw, memberA); err == nil {
		t.Error("unknown page accepted")
	}
	if _, err := m.AddWidget(ctx, org, d.ID, "", nw, memberB); !errors.Is(err, dashboard.ErrForbidden) {
		t.Errorf("other member add widget: %v", err)
	}

	cp, err := m.Duplicate(ctx, org, d.ID, "", memberB)
	if err != nil {
		t.Fatal(err)
	}
	if cp.Name != "Checkout (copy)" || cp.CreatedBy != bob || cp.ID == d.ID || cp.Pages[0].Widgets[0].ID == u.Pages[0].Widgets[0].ID || !memberB.CanEdit(cp) {
		t.Errorf("duplicate %+v", cp)
	}

	ex, err := m.Export(ctx, org, d.ID, viewer)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ex.Pages {
		for _, w := range p.Widgets {
			if w.ID != "" {
				t.Error("export contains widget ids")
			}
		}
	}
	ex.Visibility = "private"
	im, err := m.Import(ctx, org, *ex, memberB)
	if err != nil {
		t.Fatal(err)
	}
	ex2 := im.ToExport()
	ex2.Visibility = "private"
	if !reflect.DeepEqual(ex, ex2) || im.Visibility != "private" {
		t.Errorf("import roundtrip\n%+v\n%+v", ex, ex2)
	}
	ex.OpenlogDashboard = 2
	if _, err := m.Import(ctx, org, *ex, memberB); err == nil {
		t.Error("unknown export version accepted")
	}
}
