// Package dashboardtest provides an in-memory dashboard.Store for tests.
package dashboardtest

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/onuragtas/openlog/internal/dashboard"
)

// MemStore implements dashboard.Store in memory with the visibility and version semantics of the PostgreSQL store.
type MemStore struct {
	mu     sync.Mutex
	items  map[string]*dashboard.Dashboard
	Emails map[string]string // user id → e-mail
	Now    func() time.Time
}

// New returns an empty store.
func New() *MemStore {
	return &MemStore{items: map[string]*dashboard.Dashboard{}, Emails: map[string]string{}, Now: time.Now}
}

func clone(d *dashboard.Dashboard) *dashboard.Dashboard {
	c := *d
	c.Variables = append([]dashboard.Variable{}, d.Variables...)
	c.Pages = make([]dashboard.Page, len(d.Pages))
	for i, p := range d.Pages {
		c.Pages[i] = dashboard.Page{ID: p.ID, Name: p.Name, Widgets: append([]dashboard.Widget{}, p.Widgets...)}
	}
	return &c
}

func (s *MemStore) List(_ context.Context, orgID, viewerID string, admin bool, q string) ([]dashboard.Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []dashboard.Summary{}
	for _, d := range s.items {
		if d.OrgID != orgID {
			continue
		}
		if !(d.Visibility == "org" || (d.CreatedBy != "" && d.CreatedBy == viewerID) || (d.CreatedBy == "" && admin)) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(d.Name), strings.ToLower(q)) {
			continue
		}
		n := 0
		for _, p := range d.Pages {
			n += len(p.Widgets)
		}
		out = append(out, dashboard.Summary{ID: d.ID, Name: d.Name, Description: d.Description, Visibility: d.Visibility,
			PageCount: len(d.Pages), WidgetCount: n, CreatedBy: d.CreatedBy, CreatedByEmail: s.Emails[d.CreatedBy], UpdatedAt: d.UpdatedAt})
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name); a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *MemStore) Get(_ context.Context, orgID, id string) (*dashboard.Dashboard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.items[strings.ToLower(id)]
	if !ok || d.OrgID != orgID {
		return nil, dashboard.ErrNotFound
	}
	c := clone(d)
	c.CreatedByEmail = s.Emails[d.CreatedBy]
	return c, nil
}

func (s *MemStore) Create(_ context.Context, d *dashboard.Dashboard) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := clone(d)
	c.Version = 1
	c.CreatedAt = s.Now()
	c.UpdatedAt = c.CreatedAt
	s.items[c.ID] = c
	return nil
}

func (s *MemStore) Replace(_ context.Context, d *dashboard.Dashboard, expectedVersion int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.items[d.ID]
	if !ok || old.OrgID != d.OrgID {
		return dashboard.ErrNotFound
	}
	if old.Version != expectedVersion {
		return dashboard.ErrConflict
	}
	c := clone(d)
	c.Version = old.Version + 1
	c.CreatedAt = old.CreatedAt
	c.CreatedBy = old.CreatedBy
	c.UpdatedAt = s.Now()
	s.items[c.ID] = c
	return nil
}

func (s *MemStore) Delete(_ context.Context, orgID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.items[strings.ToLower(id)]
	if !ok || d.OrgID != orgID {
		return dashboard.ErrNotFound
	}
	delete(s.items, d.ID)
	return nil
}

// ForgetCreator simulates the deletion of a user (created_by SET NULL).
func (s *MemStore) ForgetCreator(userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.items {
		if d.CreatedBy == userID {
			d.CreatedBy = ""
		}
	}
}
