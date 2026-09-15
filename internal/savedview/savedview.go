// Package savedview stores the saved views of the Logs and Metrics Explorers (docs/contracts/api.md "Saved views",
// migrations/postgres/0080_saved_views.sql, D-118): a named explorer state, private to its creator or shared with the
// organization.
package savedview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Limits.
const (
	MaxNameRunes        = 200
	MaxDescriptionRunes = 2000
	MaxStateBytes       = 32 << 10
	MaxPerOrg           = 500
)

var (
	ErrNotFound  = errors.New("saved view not found")
	ErrForbidden = errors.New("not allowed to change this saved view")
	// ErrLimit: the organization already has MaxPerOrg views.
	ErrLimit = errors.New("saved view limit reached")
)

// ValidationError reports invalid input.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// View is one saved view.
type View struct {
	ID             string
	OrgID          string
	Signal         string
	Name           string
	Description    string
	Visibility     string // private | org
	State          json.RawMessage
	CreatedBy      string // "" when the creator was deleted
	CreatedByEmail string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Input is the writable part of a view.
type Input struct {
	Signal      string          `json:"signal"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Visibility  string          `json:"visibility"`
	State       json.RawMessage `json:"state"`
}

// Store persists views (PostgreSQL: PGStore).
type Store interface {
	// List returns the views of the organization viewerID may read (org-wide, own private ones, private ones of
	// deleted users when admin), optionally of one signal, ordered by name.
	List(ctx context.Context, orgID, viewerID string, admin bool, signal string) ([]View, error)
	Get(ctx context.Context, orgID, id string) (*View, error)
	// Create inserts v unless the organization already has maxPerOrg views (ErrLimit); it sets the timestamps.
	Create(ctx context.Context, v *View, maxPerOrg int) error
	// Update writes the writable fields of v and sets UpdatedAt.
	Update(ctx context.Context, v *View) error
	Delete(ctx context.Context, orgID, id string) error
}

// Viewer is the principal acting on views.
type Viewer struct {
	UserID   string // signed-in user; "" for API keys
	Admin    bool   // admin or owner
	CanWrite bool   // signed-in member or higher
}

func (v Viewer) creator(x *View) bool { return v.UserID != "" && x.CreatedBy == v.UserID }

// CanRead reports whether v may read x.
func (v Viewer) CanRead(x *View) bool {
	return x.Visibility == "org" || v.creator(x) || (x.CreatedBy == "" && v.Admin)
}

// CanEdit reports whether v may change or delete x: its creator, or an admin for org-wide and orphaned views.
func (v Viewer) CanEdit(x *View) bool {
	if !v.CanWrite || !v.CanRead(x) {
		return false
	}
	return v.creator(x) || (v.Admin && (x.Visibility == "org" || x.CreatedBy == ""))
}

// Manager implements the saved view API operations.
type Manager struct {
	store Store
}

// NewManager creates a manager.
func NewManager(store Store) *Manager { return &Manager{store: store} }

// ValidSignal reports whether s is a saved view signal ("" is not).
func ValidSignal(s string) bool { return s == "logs" || s == "metrics" || s == "traces" }

// CanonicalID returns the canonical form of a UUID.
func CanonicalID(id string) (string, bool) {
	u, err := uuid.Parse(id)
	if err != nil || len(id) != 36 {
		return "", false
	}
	return u.String(), true
}

func (in *Input) validate() error {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	switch {
	case !ValidSignal(in.Signal):
		return invalid("signal must be one of logs, metrics, traces")
	case in.Name == "" || utf8.RuneCountInString(in.Name) > MaxNameRunes || !utf8.ValidString(in.Name):
		return invalid("name must be 1 to 200 characters")
	case utf8.RuneCountInString(in.Description) > MaxDescriptionRunes || !utf8.ValidString(in.Description):
		return invalid("description must be at most 2000 characters")
	case in.Visibility != "private" && in.Visibility != "org":
		return invalid("visibility must be private or org")
	case len(in.State) > MaxStateBytes:
		return invalid("state must be at most 32 KiB")
	}
	trimmed := bytes.TrimSpace(in.State)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return invalid("state must be a JSON object")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return invalid("state must be a JSON object")
	}
	in.State = compact.Bytes()
	return nil
}

// List returns the views v may read, optionally of one signal.
func (m *Manager) List(ctx context.Context, orgID string, v Viewer, signal string) ([]View, error) {
	if signal != "" && !ValidSignal(signal) {
		return nil, invalid("signal must be one of logs, metrics, traces")
	}
	return m.store.List(ctx, orgID, v.UserID, v.Admin, signal)
}

// Get returns a view v may read (ErrNotFound otherwise).
func (m *Manager) Get(ctx context.Context, orgID, id string, v Viewer) (*View, error) {
	cid, ok := CanonicalID(id)
	if !ok {
		return nil, ErrNotFound
	}
	x, err := m.store.Get(ctx, orgID, cid)
	if err != nil {
		return nil, err
	}
	if !v.CanRead(x) {
		return nil, ErrNotFound
	}
	return x, nil
}

// Create stores a new view owned by v.
func (m *Manager) Create(ctx context.Context, orgID string, v Viewer, in Input) (*View, error) {
	if !v.CanWrite {
		return nil, ErrForbidden
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	x := &View{ID: uuid.NewString(), OrgID: orgID, Signal: in.Signal, Name: in.Name, Description: in.Description,
		Visibility: in.Visibility, State: in.State, CreatedBy: v.UserID}
	if err := m.store.Create(ctx, x, MaxPerOrg); err != nil {
		return nil, err
	}
	return m.store.Get(ctx, orgID, x.ID)
}

// Update replaces the writable fields of a view v may edit.
func (m *Manager) Update(ctx context.Context, orgID, id string, v Viewer, in Input) (*View, error) {
	x, err := m.Get(ctx, orgID, id, v)
	if err != nil {
		return nil, err
	}
	if !v.CanEdit(x) {
		return nil, ErrForbidden
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	x.Signal, x.Name, x.Description, x.Visibility, x.State = in.Signal, in.Name, in.Description, in.Visibility, in.State
	if err := m.store.Update(ctx, x); err != nil {
		return nil, err
	}
	return m.store.Get(ctx, orgID, x.ID)
}

// Delete removes a view v may edit and returns it (for the audit log).
func (m *Manager) Delete(ctx context.Context, orgID, id string, v Viewer) (*View, error) {
	x, err := m.Get(ctx, orgID, id, v)
	if err != nil {
		return nil, err
	}
	if !v.CanEdit(x) {
		return nil, ErrForbidden
	}
	return x, m.store.Delete(ctx, orgID, x.ID)
}
