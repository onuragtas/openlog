// Package intsettingstest provides an in-memory intsettings.Store for tests.
package intsettingstest

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/onuragtas/openlog/internal/intsettings"
)

// MemStore is an in-memory intsettings.Store enforcing the unique scope index of the PostgreSQL table.
type MemStore struct {
	mu    sync.Mutex
	rows  map[string]intsettings.Setting // by id
	audit []intsettings.AuditEntry
}

var _ intsettings.Store = (*MemStore)(nil)

// NewMemStore creates an empty store.
func NewMemStore() *MemStore { return &MemStore{rows: map[string]intsettings.Setting{}} }

// Audit returns the audit entries in order.
func (s *MemStore) Audit() []intsettings.AuditEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.audit)
}

func scopeKey(st intsettings.Setting) string {
	port := 0
	if st.Match.Port != nil {
		port = *st.Match.Port
	}
	return fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%s\x00%s\x00%s", st.OrgID, st.HostID, st.Integration, port,
		st.Match.Container, st.Match.Endpoint, st.Match.Instance)
}

func clone(st intsettings.Setting) intsettings.Setting {
	st.Databases = slices.Clone(st.Databases)
	if st.Databases == nil {
		st.Databases = []string{}
	}
	if st.Match.Port != nil {
		p := *st.Match.Port
		st.Match.Port = &p
	}
	return st
}

func (s *MemStore) conflictLocked(st intsettings.Setting) bool {
	for id, r := range s.rows {
		if id != st.ID && scopeKey(r) == scopeKey(st) {
			return true
		}
	}
	return false
}

func (s *MemStore) ListSettings(_ context.Context, orgID string) ([]intsettings.Setting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []intsettings.Setting{}
	for _, r := range s.rows {
		if r.OrgID == orgID {
			out = append(out, clone(r))
		}
	}
	return out, nil
}

func (s *MemStore) GetSetting(_ context.Context, orgID, id string) (intsettings.Setting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok || r.OrgID != orgID {
		return intsettings.Setting{}, intsettings.ErrNotFound
	}
	return clone(r), nil
}

func (s *MemStore) CreateSetting(_ context.Context, st intsettings.Setting) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conflictLocked(st) {
		return intsettings.ErrConflict
	}
	s.rows[st.ID] = clone(st)
	return nil
}

func (s *MemStore) UpdateSetting(_ context.Context, st intsettings.Setting) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.rows[st.ID]; !ok || r.OrgID != st.OrgID {
		return intsettings.ErrNotFound
	}
	if s.conflictLocked(st) {
		return intsettings.ErrConflict
	}
	s.rows[st.ID] = clone(st)
	return nil
}

func (s *MemStore) DeleteSetting(_ context.Context, orgID, id string) (intsettings.Setting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok || r.OrgID != orgID {
		return intsettings.Setting{}, intsettings.ErrNotFound
	}
	delete(s.rows, id)
	return r, nil
}

func (s *MemStore) AddAudit(_ context.Context, e intsettings.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = append(s.audit, e)
	return nil
}
