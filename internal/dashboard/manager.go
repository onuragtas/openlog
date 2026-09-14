package dashboard

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Store persists dashboards (PostgreSQL: PGStore).
type Store interface {
	// List returns the summaries of dashboards viewerID may read (org-wide, own private ones, private ones of deleted
	// users when admin), ordered by name; q filters by name substring (case-insensitive).
	List(ctx context.Context, orgID, viewerID string, admin bool, q string) ([]Summary, error)
	Get(ctx context.Context, orgID, id string) (*Dashboard, error)
	// Create inserts d (ids set by the caller) with version 1.
	Create(ctx context.Context, d *Dashboard) error
	// Replace overwrites the document of d.ID when its version is expectedVersion (ErrConflict otherwise) and
	// increments the version.
	Replace(ctx context.Context, d *Dashboard, expectedVersion int) error
	Delete(ctx context.Context, orgID, id string) error
}

// Viewer is the principal acting on dashboards.
type Viewer struct {
	UserID   string // signed-in user; "" for API keys
	Admin    bool   // admin or owner
	CanWrite bool   // signed-in member or higher
}

func (v Viewer) creator(d *Dashboard) bool { return v.UserID != "" && d.CreatedBy == v.UserID }

// CanRead reports whether v may read d.
func (v Viewer) CanRead(d *Dashboard) bool {
	return d.Visibility == "org" || v.creator(d) || (d.CreatedBy == "" && v.Admin)
}

// CanEdit reports whether v may change or delete d.
func (v Viewer) CanEdit(d *Dashboard) bool {
	if !v.CanWrite || !v.CanRead(d) {
		return false
	}
	return v.creator(d) || (v.Admin && (d.Visibility == "org" || d.CreatedBy == ""))
}

// Manager implements the dashboard API operations.
type Manager struct {
	store Store
	now   func() time.Time
	newID func() string
}

// NewManager creates a manager.
func NewManager(store Store) *Manager {
	return &Manager{store: store, now: time.Now, newID: uuid.NewString}
}

// List returns the dashboards v may read.
func (m *Manager) List(ctx context.Context, orgID string, v Viewer, q string) ([]Summary, error) {
	q = strings.TrimSpace(q)
	if len(q) > 200 {
		return nil, invalid("q must be at most 200 bytes")
	}
	return m.store.List(ctx, orgID, v.UserID, v.Admin, q)
}

// Get returns a dashboard v may read (ErrNotFound otherwise).
func (m *Manager) Get(ctx context.Context, orgID, id string, v Viewer) (*Dashboard, error) {
	d, err := m.store.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if !v.CanRead(d) {
		return nil, ErrNotFound
	}
	return d, nil
}

// Create validates and stores a new dashboard owned by v.
func (m *Manager) Create(ctx context.Context, orgID string, in Input, v Viewer) (*Dashboard, error) {
	if !v.CanWrite {
		return nil, ErrForbidden
	}
	d, err := normalize(in, m.now())
	if err != nil {
		return nil, err
	}
	d.ID, d.OrgID, d.CreatedBy, d.UpdatedBy = m.newID(), orgID, v.UserID, v.UserID
	m.assignIDs(d, nil)
	if err := m.store.Create(ctx, d); err != nil {
		return nil, err
	}
	return m.store.Get(ctx, orgID, d.ID)
}

// Update replaces a dashboard document (optimistic concurrency on in.Version).
func (m *Manager) Update(ctx context.Context, orgID, id string, in Input, v Viewer) (*Dashboard, error) {
	old, err := m.Get(ctx, orgID, id, v)
	if err != nil {
		return nil, err
	}
	return m.replace(ctx, old, in, v)
}

func (m *Manager) replace(ctx context.Context, old *Dashboard, in Input, v Viewer) (*Dashboard, error) {
	if !v.CanEdit(old) {
		return nil, ErrForbidden
	}
	if in.Version <= 0 {
		return nil, invalid("version is required (the version of the dashboard that was read)")
	}
	d, err := normalize(in, m.now())
	if err != nil {
		return nil, err
	}
	if d.Visibility != old.Visibility && !v.creator(old) && old.CreatedBy != "" {
		return nil, invalid("only the creator can change the visibility of a dashboard")
	}
	d.ID, d.OrgID, d.CreatedBy, d.UpdatedBy = old.ID, old.OrgID, old.CreatedBy, v.UserID
	m.assignIDs(d, old)
	if err := m.store.Replace(ctx, d, in.Version); err != nil {
		return nil, err
	}
	return m.store.Get(ctx, old.OrgID, old.ID)
}

// Delete removes a dashboard and returns it.
func (m *Manager) Delete(ctx context.Context, orgID, id string, v Viewer) (*Dashboard, error) {
	old, err := m.Get(ctx, orgID, id, v)
	if err != nil {
		return nil, err
	}
	if !v.CanEdit(old) {
		return nil, ErrForbidden
	}
	if err := m.store.Delete(ctx, orgID, id); err != nil {
		return nil, err
	}
	return old, nil
}

// AddWidget appends w below the widgets of a page (default: the first page).
func (m *Manager) AddWidget(ctx context.Context, orgID, id, pageID string, w Widget, v Viewer) (*Dashboard, error) {
	old, err := m.Get(ctx, orgID, id, v)
	if err != nil {
		return nil, err
	}
	if !v.CanEdit(old) {
		return nil, ErrForbidden
	}
	in := old.input()
	in.Pages = make([]Page, len(old.Pages))
	idx := -1
	for i, p := range old.Pages {
		in.Pages[i] = Page{ID: p.ID, Name: p.Name, Widgets: append([]Widget(nil), p.Widgets...)}
		if (pageID == "" && i == 0) || strings.EqualFold(p.ID, pageID) {
			idx = i
		}
	}
	if idx < 0 {
		return nil, invalid("page_id: no such page")
	}
	bottom := 0
	for _, e := range in.Pages[idx].Widgets {
		bottom = max(bottom, e.Layout.Y+e.Layout.H)
	}
	w.ID = ""
	if w.Layout.W == 0 {
		w.Layout.W = 6
	}
	if w.Layout.H == 0 {
		w.Layout.H = 3
	}
	w.Layout.Y = bottom
	in.Pages[idx].Widgets = append(in.Pages[idx].Widgets, w)
	return m.replace(ctx, old, in, v)
}

// Duplicate copies a readable dashboard as a new dashboard of v.
func (m *Manager) Duplicate(ctx context.Context, orgID, id, name string, v Viewer) (*Dashboard, error) {
	old, err := m.Get(ctx, orgID, id, v)
	if err != nil {
		return nil, err
	}
	in := old.input()
	in.Name = strings.TrimSpace(name)
	if in.Name == "" {
		in.Name = old.Name
		if utf8.RuneCountInString(in.Name) > 193 {
			in.Name = string([]rune(in.Name)[:193])
		}
		in.Name += " (copy)"
	}
	return m.Create(ctx, orgID, in, v)
}

// Export returns the portable document of a readable dashboard.
func (m *Manager) Export(ctx context.Context, orgID, id string, v Viewer) (*Export, error) {
	d, err := m.Get(ctx, orgID, id, v)
	if err != nil {
		return nil, err
	}
	return d.ToExport(), nil
}

// Import creates a dashboard from an exported document.
func (m *Manager) Import(ctx context.Context, orgID string, ex Export, v Viewer) (*Dashboard, error) {
	if ex.OpenlogDashboard != ExportVersion {
		return nil, invalid("openlog_dashboard must be %d", ExportVersion)
	}
	return m.Create(ctx, orgID, Input{Name: ex.Name, Description: ex.Description, Visibility: ex.Visibility, Variables: ex.Variables, Pages: ex.Pages}, v)
}

func canonicalID(id string) (string, bool) {
	u, err := uuid.Parse(id)
	if err != nil || len(id) != 36 {
		return "", false
	}
	return u.String(), true
}

// assignIDs keeps page and widget ids that belong to old and are used once; every other page or widget gets a new id.
func (m *Manager) assignIDs(d *Dashboard, old *Dashboard) {
	known := map[string]bool{}
	if old != nil {
		for _, p := range old.Pages {
			known[p.ID] = true
			for _, w := range p.Widgets {
				known[w.ID] = true
			}
		}
	}
	used := map[string]bool{}
	pick := func(id string) string {
		if c, ok := canonicalID(id); ok && known[c] && !used[c] {
			used[c] = true
			return c
		}
		n := m.newID()
		used[n] = true
		return n
	}
	for i := range d.Pages {
		d.Pages[i].ID = pick(d.Pages[i].ID)
		for j := range d.Pages[i].Widgets {
			d.Pages[i].Widgets[j].ID = pick(d.Pages[i].Widgets[j].ID)
		}
	}
}
