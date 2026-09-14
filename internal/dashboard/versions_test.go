package dashboard_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/onuragtas/openlog/internal/dashboard"
	"github.com/onuragtas/openlog/internal/dashboard/dashboardtest"
)

func inputOf(d *dashboard.Dashboard) dashboard.Input {
	pages := make([]dashboard.Page, len(d.Pages))
	for i, p := range d.Pages {
		pages[i] = dashboard.Page{ID: p.ID, Name: p.Name, Widgets: append([]dashboard.Widget{}, p.Widgets...)}
	}
	return dashboard.Input{Name: d.Name, Description: d.Description, Visibility: d.Visibility, Variables: d.Variables, Pages: pages, Version: d.Version}
}

func widgetIDs(d *dashboard.Dashboard) []string {
	var out []string
	for _, p := range d.Pages {
		for _, w := range p.Widgets {
			out = append(out, w.ID)
		}
	}
	return out
}

func TestVersionHistory(t *testing.T) {
	store := dashboardtest.New()
	store.Emails[alice] = "alice@example.com"
	m := dashboard.NewManager(store)
	d, err := m.Create(ctx, org, sample(), memberA)
	if err != nil {
		t.Fatal(err)
	}
	in := inputOf(d)
	in.Name = "Checkout v2"
	in.Pages[0].Widgets[0].Query = "SELECT count(*) FROM Log TIMESERIES"
	in.Pages[0].Widgets = append(in.Pages[0].Widgets, dashboard.Widget{Title: "New", Visualization: "table",
		Layout: dashboard.Layout{X: 0, Y: 3, W: 6, H: 3}, Query: "SELECT count(*) FROM Log FACET host.name"})
	d2, err := m.Update(ctx, org, d.ID, in, memberA)
	if err != nil {
		t.Fatal(err)
	}

	list, err := m.Versions(ctx, org, d.ID, viewer)
	if err != nil || len(list) != 2 || list[0].Version != 2 || list[1].Version != 1 || list[0].AuthorEmail != "alice@example.com" ||
		list[0].WidgetCount != 3 || list[0].PageCount != 1 {
		t.Fatalf("versions %+v %v", list, err)
	}
	det, err := m.VersionDetail(ctx, org, d.ID, 2, viewer)
	if err != nil || det.Previous != 1 || det.Changes == nil || !det.Changes.Name || len(det.Changes.WidgetsAdded) != 1 ||
		det.Changes.WidgetsAdded[0].Title != "New" || len(det.Changes.WidgetsChanged) != 1 ||
		!reflect.DeepEqual(det.Changes.WidgetsChanged[0].Fields, []string{"query"}) || !det.FromCurrent.Empty() || det.Current != 2 {
		t.Fatalf("detail of v2 %+v %v", det, err)
	}
	det1, err := m.VersionDetail(ctx, org, d.ID, 1, viewer)
	if err != nil || det1.Changes != nil || det1.Previous != 0 || len(det1.FromCurrent.WidgetsRemoved) != 1 || !det1.FromCurrent.Name {
		t.Fatalf("detail of v1 %+v %v", det1, err)
	}
	if _, err := m.VersionDetail(ctx, org, d.ID, 9, viewer); !errors.Is(err, dashboard.ErrNotFound) {
		t.Errorf("unknown version: %v", err)
	}
	if _, err := m.Versions(ctx, otherOrg, d.ID, memberA); !errors.Is(err, dashboard.ErrNotFound) {
		t.Errorf("other org: %v", err)
	}

	if _, err := m.Restore(ctx, org, d.ID, 1, 1, memberA); !errors.Is(err, dashboard.ErrConflict) {
		t.Errorf("stale restore: %v", err)
	}
	if _, err := m.Restore(ctx, org, d.ID, 1, d2.Version, memberB); !errors.Is(err, dashboard.ErrForbidden) {
		t.Errorf("restore by a member who cannot edit: %v", err)
	}
	r, err := m.Restore(ctx, org, d.ID, 1, d2.Version, memberA)
	if err != nil || r.Version != 3 || r.Name != "Checkout" || !reflect.DeepEqual(widgetIDs(r), widgetIDs(d)) {
		t.Fatalf("restore %+v %v", r, err)
	}
	list, _ = m.Versions(ctx, org, d.ID, memberA)
	if len(list) != 3 || list[0].RestoredFrom != 1 || list[1].RestoredFrom != 0 {
		t.Fatalf("after restore %+v", list)
	}

	cur := r
	for i := 0; i < dashboard.MaxVersions+5; i++ {
		in := inputOf(cur)
		in.Name = fmt.Sprintf("Checkout %d", i)
		if cur, err = m.Update(ctx, org, d.ID, in, memberA); err != nil {
			t.Fatal(err)
		}
	}
	list, _ = m.Versions(ctx, org, d.ID, memberA)
	if len(list) != dashboard.MaxVersions || list[0].Version != cur.Version || list[len(list)-1].Version != cur.Version-dashboard.MaxVersions+1 {
		t.Fatalf("retention: %d versions, newest %d, oldest %d", len(list), list[0].Version, list[len(list)-1].Version)
	}
	if _, err := m.VersionDetail(ctx, org, d.ID, 1, memberA); !errors.Is(err, dashboard.ErrNotFound) {
		t.Errorf("pruned version: %v", err)
	}
}

func TestDiffSnapshots(t *testing.T) {
	w := func(id, title string) dashboard.Widget {
		return dashboard.Widget{ID: id, Title: title, Visualization: "line", Layout: dashboard.Layout{W: 6, H: 3}, Query: "q"}
	}
	from := dashboard.Snapshot{Name: "A", Pages: []dashboard.Page{
		{ID: "p1", Name: "One", Widgets: []dashboard.Widget{w("w1", "x"), w("w2", "y")}},
		{ID: "p2", Name: "Two", Widgets: []dashboard.Widget{w("w3", "z")}},
	}}
	moved := w("w2", "y")
	moved.Layout.X = 6
	to := dashboard.Snapshot{Name: "A", Variables: []dashboard.Variable{{Name: "v", Type: "text"}}, Pages: []dashboard.Page{
		{ID: "p1", Name: "Uno", Widgets: []dashboard.Widget{moved}},
		{ID: "p3", Name: "Three", Widgets: []dashboard.Widget{w("w3", "z"), w("w4", "new")}},
	}}
	d := dashboard.DiffSnapshots(from, to)
	if d.Name || !d.Variables || len(d.PagesAdded) != 1 || len(d.PagesRemoved) != 1 || len(d.PagesRenamed) != 1 ||
		len(d.WidgetsAdded) != 1 || len(d.WidgetsRemoved) != 1 || len(d.WidgetsChanged) != 2 {
		t.Fatalf("diff %+v", d)
	}
	if !reflect.DeepEqual(d.WidgetsChanged[0].Fields, []string{"layout"}) || !reflect.DeepEqual(d.WidgetsChanged[1].Fields, []string{"page"}) {
		t.Errorf("changed fields %+v", d.WidgetsChanged)
	}
	if !dashboard.DiffSnapshots(to, to).Empty() {
		t.Error("identical snapshots must not differ")
	}
}
