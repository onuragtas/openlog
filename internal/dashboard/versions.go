package dashboard

import (
	"context"
	"encoding/json"
	"time"
)

// MaxVersions is the number of versions kept per dashboard (older ones are pruned on save).
const MaxVersions = 50

// Snapshot is the stored document of one dashboard version (ids kept).
type Snapshot struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Visibility  string     `json:"visibility"`
	Variables   []Variable `json:"variables"`
	Pages       []Page     `json:"pages"`
}

// VersionInfo is a version list entry.
type VersionInfo struct {
	Version      int
	AuthorID     string // "" = deleted user or unknown
	AuthorEmail  string
	CreatedAt    time.Time
	RestoredFrom int // 0 = a normal save
	PageCount    int
	WidgetCount  int
}

// Version is a stored version with its document.
type Version struct {
	VersionInfo
	Document Snapshot
}

// Snapshot returns the document of d.
func (d *Dashboard) Snapshot() Snapshot {
	s := Snapshot{Name: d.Name, Description: d.Description, Visibility: d.Visibility, Variables: d.Variables, Pages: d.Pages}
	if s.Variables == nil {
		s.Variables = []Variable{}
	}
	if s.Pages == nil {
		s.Pages = []Page{}
	}
	return s
}

// PageRef names a page in a diff.
type PageRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// WidgetRef names a widget in a diff; Fields lists the changed fields of a changed widget.
type WidgetRef struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Page   string   `json:"page"`
	Fields []string `json:"fields,omitempty"`
}

// Diff summarizes the changes from one document to another (widgets and pages matched by id).
type Diff struct {
	Name           bool        `json:"name"`
	Description    bool        `json:"description"`
	Visibility     bool        `json:"visibility"`
	Variables      bool        `json:"variables"`
	PagesAdded     []PageRef   `json:"pages_added"`
	PagesRemoved   []PageRef   `json:"pages_removed"`
	PagesRenamed   []PageRef   `json:"pages_renamed"`
	WidgetsAdded   []WidgetRef `json:"widgets_added"`
	WidgetsRemoved []WidgetRef `json:"widgets_removed"`
	WidgetsChanged []WidgetRef `json:"widgets_changed"`
}

// Empty reports whether nothing changed.
func (d Diff) Empty() bool {
	return !d.Name && !d.Description && !d.Visibility && !d.Variables && len(d.PagesAdded)+len(d.PagesRemoved)+len(d.PagesRenamed)+
		len(d.WidgetsAdded)+len(d.WidgetsRemoved)+len(d.WidgetsChanged) == 0
}

func jsonEqual(a, b any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// DiffSnapshots returns the changes that turn from into to.
func DiffSnapshots(from, to Snapshot) Diff {
	d := Diff{Name: from.Name != to.Name, Description: from.Description != to.Description, Visibility: from.Visibility != to.Visibility,
		PagesAdded: []PageRef{}, PagesRemoved: []PageRef{}, PagesRenamed: []PageRef{},
		WidgetsAdded: []WidgetRef{}, WidgetsRemoved: []WidgetRef{}, WidgetsChanged: []WidgetRef{}}
	d.Variables = !jsonEqual(nonNilVars(from.Variables), nonNilVars(to.Variables))
	type located struct {
		w      Widget
		pageID string
	}
	index := func(s Snapshot) (map[string]Page, map[string]located) {
		pages, widgets := map[string]Page{}, map[string]located{}
		for _, p := range s.Pages {
			pages[p.ID] = p
			for _, w := range p.Widgets {
				widgets[w.ID] = located{w, p.ID}
			}
		}
		return pages, widgets
	}
	fromPages, fromWidgets := index(from)
	toPages, toWidgets := index(to)
	for _, p := range to.Pages {
		old, ok := fromPages[p.ID]
		switch {
		case !ok:
			d.PagesAdded = append(d.PagesAdded, PageRef{p.ID, p.Name})
		case old.Name != p.Name:
			d.PagesRenamed = append(d.PagesRenamed, PageRef{p.ID, p.Name})
		}
		for _, w := range p.Widgets {
			o, ok := fromWidgets[w.ID]
			if !ok {
				d.WidgetsAdded = append(d.WidgetsAdded, WidgetRef{ID: w.ID, Title: w.Title, Page: p.Name})
				continue
			}
			if fields := widgetFields(o.w, w, o.pageID != p.ID); len(fields) > 0 {
				d.WidgetsChanged = append(d.WidgetsChanged, WidgetRef{ID: w.ID, Title: w.Title, Page: p.Name, Fields: fields})
			}
		}
	}
	for _, p := range from.Pages {
		if _, ok := toPages[p.ID]; !ok {
			d.PagesRemoved = append(d.PagesRemoved, PageRef{p.ID, p.Name})
		}
		for _, w := range p.Widgets {
			if _, ok := toWidgets[w.ID]; !ok {
				d.WidgetsRemoved = append(d.WidgetsRemoved, WidgetRef{ID: w.ID, Title: w.Title, Page: p.Name})
			}
		}
	}
	return d
}

func nonNilVars(v []Variable) []Variable {
	if v == nil {
		return []Variable{}
	}
	return v
}

func widgetFields(a, b Widget, moved bool) []string {
	var out []string
	add := func(changed bool, name string) {
		if changed {
			out = append(out, name)
		}
	}
	add(a.Title != b.Title, "title")
	add(a.Visualization != b.Visualization, "visualization")
	add(a.Layout != b.Layout, "layout")
	add(a.Query != b.Query, "query")
	add(a.Markdown != b.Markdown, "markdown")
	add(a.Unit != b.Unit, "unit")
	add(!jsonEqual(nonNilThresholds(a.Thresholds), nonNilThresholds(b.Thresholds)), "thresholds")
	add(!jsonEqual(a.Options, b.Options), "options")
	add(moved, "page")
	return out
}

func nonNilThresholds(t []Threshold) []Threshold {
	if t == nil {
		return []Threshold{}
	}
	return t
}

// VersionDetail is one version with its differences.
type VersionDetail struct {
	Version
	// Current is the dashboard's current version number.
	Current int
	// Previous is the stored version before this one (0 = none kept); Changes is the diff from it to this version.
	Previous int
	Changes  *Diff
	// ToCurrent is the diff from this version to the current document (what a restore would undo).
	FromCurrent Diff
}

// Versions lists the stored versions of a readable dashboard, newest first.
func (m *Manager) Versions(ctx context.Context, orgID, id string, v Viewer) ([]VersionInfo, error) {
	d, err := m.Get(ctx, orgID, id, v)
	if err != nil {
		return nil, err
	}
	return m.store.ListVersions(ctx, orgID, d.ID)
}

// VersionDetail returns one stored version of a readable dashboard with its changes.
func (m *Manager) VersionDetail(ctx context.Context, orgID, id string, version int, v Viewer) (*VersionDetail, error) {
	d, err := m.Get(ctx, orgID, id, v)
	if err != nil {
		return nil, err
	}
	ver, err := m.store.GetVersion(ctx, orgID, d.ID, version)
	if err != nil {
		return nil, err
	}
	out := &VersionDetail{Version: *ver, Current: d.Version, FromCurrent: DiffSnapshots(d.Snapshot(), ver.Document)}
	list, err := m.store.ListVersions(ctx, orgID, d.ID)
	if err != nil {
		return nil, err
	}
	for _, info := range list { // newest first: the first older entry is the previous version
		if info.Version < version {
			prev, err := m.store.GetVersion(ctx, orgID, d.ID, info.Version)
			if err != nil {
				return nil, err
			}
			ch := DiffSnapshots(prev.Document, ver.Document)
			out.Previous, out.Changes = info.Version, &ch
			break
		}
	}
	return out, nil
}

// Restore saves a stored version as the new current version (optimistic concurrency on expectedVersion). The
// dashboard keeps its current visibility; widget and page ids of the version are kept.
func (m *Manager) Restore(ctx context.Context, orgID, id string, version, expectedVersion int, v Viewer) (*Dashboard, error) {
	old, err := m.Get(ctx, orgID, id, v)
	if err != nil {
		return nil, err
	}
	if !v.CanEdit(old) {
		return nil, ErrForbidden
	}
	ver, err := m.store.GetVersion(ctx, orgID, old.ID, version)
	if err != nil {
		return nil, err
	}
	doc := ver.Document
	in := Input{Name: doc.Name, Description: doc.Description, Visibility: old.Visibility, Variables: doc.Variables, Pages: doc.Pages, Version: expectedVersion}
	return m.replace(ctx, old, in, v, &ver.Document, version)
}
