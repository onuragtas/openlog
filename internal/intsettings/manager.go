package intsettings

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/onuragtas/openlog/internal/alert/secrets"
)

// AuditEntry is written to audit_log.
type AuditEntry struct {
	OrgID       string
	ActorUserID string
	ActorEmail  string
	// ActorAPIKeyID and ActorAPIKeyName name the API key that made the change (D-133); empty for users.
	ActorAPIKeyID   string
	ActorAPIKeyName string
	Action          string
	TargetType      string
	TargetID        string
	Details         map[string]any
	IP              string
	At              time.Time
}

// Store persists integration settings (PostgreSQL: PGStore).
type Store interface {
	// ListSettings returns every setting of an organization (any order).
	ListSettings(ctx context.Context, orgID string) ([]Setting, error)
	GetSetting(ctx context.Context, orgID, id string) (Setting, error)
	// CreateSetting inserts s (ID set by the caller). ErrConflict on a duplicate scope, integration and match.
	CreateSetting(ctx context.Context, s Setting) error
	// UpdateSetting replaces the stored row s.ID. ErrNotFound, ErrConflict.
	UpdateSetting(ctx context.Context, s Setting) error
	// DeleteSetting removes a row and returns it. ErrNotFound.
	DeleteSetting(ctx context.Context, orgID, id string) (Setting, error)
	AddAudit(ctx context.Context, e AuditEntry) error
}

// Actor is who performed a change (for the audit log): a signed-in user, or an API
// key acting with its own role (D-133), in which case UserID and Email are empty.
type Actor struct {
	UserID     string
	Email      string
	IP         string
	APIKeyID   string
	APIKeyName string
}

// ManagerOptions configure Manager.
type ManagerOptions struct {
	Keys *secrets.Keyring // nil or unconfigured: passwords cannot be set
	Log  *slog.Logger
	Now  func() time.Time
}

// Manager implements the management API operations.
type Manager struct {
	store Store
	o     ManagerOptions
}

// NewManager creates a manager.
func NewManager(store Store, o ManagerOptions) *Manager {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Manager{store: store, o: o}
}

// SecretsConfigured reports whether passwords can be stored.
func (m *Manager) SecretsConfigured() bool { return m.o.Keys.Configured() }

// List returns an organization's settings in API order (Sort).
func (m *Manager) List(ctx context.Context, orgID string) ([]Setting, error) {
	ss, err := m.store.ListSettings(ctx, orgID)
	if err != nil {
		return nil, err
	}
	Sort(ss)
	return ss, nil
}

// ForHost returns the settings that apply to a host (Effective) and their revision.
func (m *Manager) ForHost(ctx context.Context, orgID, hostID string) ([]Setting, string, error) {
	ss, err := m.store.ListSettings(ctx, orgID)
	if err != nil {
		return nil, "", err
	}
	eff := Effective(ss, hostID)
	return eff, Revision(eff), nil
}

func (m *Manager) audit(ctx context.Context, a Actor, action string, s Setting, details map[string]any) {
	details["integration"] = s.Integration
	details["host_id"] = nullable(s.HostID)
	e := AuditEntry{OrgID: s.OrgID, ActorUserID: a.UserID, ActorEmail: a.Email, ActorAPIKeyID: a.APIKeyID,
		ActorAPIKeyName: a.APIKeyName, Action: action, TargetType: "integration_setting",
		TargetID: s.ID, Details: details, IP: a.IP, At: m.o.Now()}
	if err := m.store.AddAudit(context.WithoutCancel(ctx), e); err != nil {
		m.o.Log.Error("cannot write audit log", "action", action, "org_id", s.OrgID, "err", err)
	}
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (m *Manager) encrypt(orgID, id, password string) (string, error) {
	if !m.o.Keys.Configured() {
		return "", ErrNoSecretsKey
	}
	enc, _, err := m.o.Keys.Encrypt([]byte(password), PasswordAAD(orgID, id))
	return enc, err
}

// Create validates and stores a new setting.
func (m *Manager) Create(ctx context.Context, orgID string, in Input, a Actor) (Setting, error) {
	s, err := normalize(in)
	if err != nil {
		return Setting{}, err
	}
	s.ID, s.OrgID = uuid.NewString(), orgID
	if in.Password != nil && *in.Password != "" {
		if s.PasswordEnc, err = m.encrypt(orgID, s.ID, *in.Password); err != nil {
			return Setting{}, err
		}
	}
	now := m.o.Now().UTC()
	s.CreatedAt, s.UpdatedAt, s.UpdatedBy, s.UpdatedByEmail = now, now, a.UserID, a.Email
	if err := m.store.CreateSetting(ctx, s); err != nil {
		return Setting{}, err
	}
	m.audit(ctx, a, "integration_setting.create", s, map[string]any{"changed": changedFields(Setting{}, s), "password_changed": s.PasswordEnc != ""})
	return s, nil
}

// Update replaces a setting's fields. The password is kept when in.Password is nil (and cleared when the new
// integration does not use one), cleared when "", and replaced otherwise.
func (m *Manager) Update(ctx context.Context, orgID, id string, in Input, a Actor) (Setting, error) {
	old, err := m.store.GetSetting(ctx, orgID, id)
	if err != nil {
		return Setting{}, err
	}
	s, err := normalize(in)
	if err != nil {
		return Setting{}, err
	}
	s.ID, s.OrgID, s.CreatedAt = old.ID, old.OrgID, old.CreatedAt
	switch {
	case in.Password == nil && AllowsPassword(s.Integration):
		s.PasswordEnc = old.PasswordEnc
	case in.Password != nil && *in.Password != "":
		if s.PasswordEnc, err = m.encrypt(orgID, s.ID, *in.Password); err != nil {
			return Setting{}, err
		}
	}
	s.UpdatedAt, s.UpdatedBy, s.UpdatedByEmail = m.o.Now().UTC(), a.UserID, a.Email
	if err := m.store.UpdateSetting(ctx, s); err != nil {
		return Setting{}, err
	}
	details := map[string]any{"changed": changedFields(old, s), "password_changed": s.PasswordEnc != old.PasswordEnc}
	if old.HostID != s.HostID {
		details["previous_host_id"] = nullable(old.HostID)
	}
	if old.Integration != s.Integration {
		details["previous_integration"] = old.Integration
	}
	m.audit(ctx, a, "integration_setting.update", s, details)
	return s, nil
}

// Delete removes a setting.
func (m *Manager) Delete(ctx context.Context, orgID, id string, a Actor) error {
	old, err := m.store.DeleteSetting(ctx, orgID, id)
	if err != nil {
		return err
	}
	m.audit(ctx, a, "integration_setting.delete", old, map[string]any{})
	return nil
}

// changedFields names the non-secret fields that differ (for the audit log).
func changedFields(a, b Setting) []string {
	out := []string{}
	add := func(name string, differs bool) {
		if differs {
			out = append(out, name)
		}
	}
	add("host_id", a.HostID != b.HostID)
	add("integration", a.Integration != b.Integration)
	add("match", !a.Match.equal(b.Match))
	add("enabled", a.Enabled != b.Enabled)
	add("endpoint", a.Endpoint != b.Endpoint)
	add("username", a.Username != b.Username)
	add("database", a.Database != b.Database)
	add("databases", !slices.Equal(a.Databases, b.Databases))
	return out
}
