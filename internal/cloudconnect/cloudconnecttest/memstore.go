// Package cloudconnecttest provides an in-memory cloudconnect.Store for the API tests, so they exercise the
// handlers (permissions, shapes, credential handling) without PostgreSQL.
package cloudconnecttest

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/onuragtas/openlog/internal/cloudconnect"
)

// MemStore is an in-memory cloudconnect.Store scoped by organization.
type MemStore struct {
	mu     sync.Mutex
	conns  map[string]cloudconnect.Connection // id -> connection
	creds  map[string]string                  // id -> ciphertext
	runs   map[string][]cloudconnect.Run      // id -> newest first
	nextID int
	// Now is the clock of created_at/updated_at (default time.Now).
	Now func() time.Time
}

var _ cloudconnect.Store = (*MemStore)(nil)

// New creates an empty store.
func New() *MemStore {
	return &MemStore{conns: map[string]cloudconnect.Connection{}, creds: map[string]string{},
		runs: map[string][]cloudconnect.Run{}, nextID: 1}
}

func (m *MemStore) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

// StoredCredentials returns the ciphertext stored for a connection (tests assert it was encrypted).
func (m *MemStore) StoredCredentials(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.creds[id]
}

// AddRun appends a run to a connection's history (newest first).
func (m *MemStore) AddRun(id string, r cloudconnect.Run) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs[id] = append([]cloudconnect.Run{r}, m.runs[id]...)
}

// List implements cloudconnect.Store.
func (m *MemStore) List(_ context.Context, orgID string) ([]cloudconnect.Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []cloudconnect.Connection{}
	for _, c := range m.conns {
		if c.OrgID == orgID {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Get implements cloudconnect.Store.
func (m *MemStore) Get(_ context.Context, orgID, id string) (*cloudconnect.Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.conns[id]
	if !ok || c.OrgID != orgID {
		return nil, cloudconnect.ErrNotFound
	}
	return &c, nil
}

// CredentialsEnc implements cloudconnect.Store.
func (m *MemStore) CredentialsEnc(_ context.Context, orgID, id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.conns[id]
	if !ok || c.OrgID != orgID {
		return "", cloudconnect.ErrNotFound
	}
	if m.creds[id] == "" {
		return "", cloudconnect.ErrNoCredentials
	}
	return m.creds[id], nil
}

// scheduleFor builds the schedule rows a saved connection starts with.
func scheduleFor(in cloudconnect.Input, now time.Time) []cloudconnect.ScopeStatus {
	out := []cloudconnect.ScopeStatus{}
	if !in.Polls() {
		return out
	}
	for _, s := range in.Scopes {
		out = append(out, cloudconnect.ScopeStatus{Scope: s, NextRunAt: now})
	}
	return out
}

// Create implements cloudconnect.Store.
func (m *MemStore) Create(_ context.Context, orgID, id string, in cloudconnect.Input, credentialsEnc, keyID string,
	_ cloudconnect.Actor) (*cloudconnect.Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.conns {
		if c.OrgID == orgID {
			n++
		}
	}
	if n >= cloudconnect.MaxPerOrg {
		return nil, cloudconnect.ErrLimit
	}
	now := m.now()
	m.nextID++
	// The stored definition never carries the submitted credentials.
	stored := in
	stored.Credentials = nil
	c := cloudconnect.Connection{ID: id, OrgID: orgID, Input: stored, CredentialsSet: credentialsEnc != "",
		CredentialsKeyID: keyID, CreatedAt: now, UpdatedAt: now, Status: scheduleFor(in, now)}
	m.conns[id] = c
	if credentialsEnc != "" {
		m.creds[id] = credentialsEnc
	}
	return &c, nil
}

// Update implements cloudconnect.Store.
func (m *MemStore) Update(_ context.Context, orgID, id string, in cloudconnect.Input, credentialsEnc *string,
	keyID string, _ cloudconnect.Actor) (*cloudconnect.Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.conns[id]
	if !ok || old.OrgID != orgID {
		return nil, cloudconnect.ErrNotFound
	}
	stored := in
	stored.Credentials = nil
	c := cloudconnect.Connection{ID: id, OrgID: orgID, Input: stored, CreatedAt: old.CreatedAt,
		UpdatedAt: m.now(), CredentialsKeyID: old.CredentialsKeyID, Status: scheduleFor(in, m.now())}
	if credentialsEnc != nil {
		m.creds[id] = *credentialsEnc
		c.CredentialsKeyID = keyID
	}
	c.CredentialsSet = m.creds[id] != ""
	m.conns[id] = c
	return &c, nil
}

// Delete implements cloudconnect.Store.
func (m *MemStore) Delete(_ context.Context, orgID, id string, _ cloudconnect.Actor) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.conns[id]
	if !ok || c.OrgID != orgID {
		return cloudconnect.ErrNotFound
	}
	delete(m.conns, id)
	delete(m.creds, id)
	delete(m.runs, id)
	return nil
}

// Runs implements cloudconnect.Store.
func (m *MemStore) Runs(_ context.Context, orgID, id, scope string, limit int) ([]cloudconnect.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.conns[id]
	if !ok || c.OrgID != orgID {
		return nil, cloudconnect.ErrNotFound
	}
	if limit <= 0 || limit > cloudconnect.MaxRunsPerResponse {
		limit = cloudconnect.MaxRunsPerResponse
	}
	out := []cloudconnect.Run{}
	for _, r := range m.runs[id] {
		if scope != "" && r.Scope != scope {
			continue
		}
		out = append(out, r)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// uuidLike builds a stable, valid-looking uuid from a counter, so the handlers' uuid checks pass.
func uuidLike(n int) string {
	const hex = "0123456789abcdef"
	b := []byte("53000000-0000-4000-8000-000000000000")
	for i, pos := 0, len(b)-1; i < 8 && pos >= 0; i, pos = i+1, pos-1 {
		b[pos] = hex[(n>>(4*i))&0xf]
	}
	return string(b)
}
