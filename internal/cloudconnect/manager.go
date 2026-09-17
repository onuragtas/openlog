package cloudconnect

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/onuragtas/openlog/internal/alert/secrets"
)

// Manager implements the management API operations (internal/api/cloudconnect.go). Encryption lives here and
// nowhere else, like intsettings.Manager: the handlers never see a key, and a credential value never reaches
// the store in plaintext or a response in any form.

// Tester runs a provider's credential check (collector.go).
type Tester interface {
	Test(ctx context.Context, provider string, creds Credentials, scope string) error
}

// ManagerOptions configure a Manager.
type ManagerOptions struct {
	// Keys encrypts and decrypts the stored credentials; without a configured keyring a connection cannot be
	// saved with credentials and nothing can be polled.
	Keys *secrets.Keyring
	// Tester performs the "test connection" action; nil disables it.
	Tester Tester
	Log    *slog.Logger
}

// Manager wraps a Store with credential handling.
type Manager struct {
	store Store
	o     ManagerOptions
}

// NewManager creates a manager.
func NewManager(store Store, o ManagerOptions) *Manager {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Manager{store: store, o: o}
}

// SecretsConfigured reports whether credentials can be stored.
func (m *Manager) SecretsConfigured() bool { return m.o.Keys.Configured() }

// TestSupported reports whether the "test connection" action is available.
func (m *Manager) TestSupported() bool { return m.o.Tester != nil }

// List returns an organization's connections, ordered by name.
func (m *Manager) List(ctx context.Context, orgID string) ([]Connection, error) {
	return m.store.List(ctx, orgID)
}

// Get returns one connection.
func (m *Manager) Get(ctx context.Context, orgID, id string) (*Connection, error) {
	return m.store.Get(ctx, orgID, id)
}

// Runs returns the recent polls of a connection.
func (m *Manager) Runs(ctx context.Context, orgID, id, scope string, limit int) ([]Run, error) {
	return m.store.Runs(ctx, orgID, id, scope, limit)
}

// encrypt seals a credential document for one connection.
func (m *Manager) encrypt(orgID, id string, c Credentials) (string, string, error) {
	if !m.o.Keys.Configured() {
		return "", "", secrets.ErrNoKey
	}
	raw, err := c.Encode()
	if err != nil {
		return "", "", err
	}
	return m.o.Keys.Encrypt(raw, CredentialsAAD(orgID, id))
}

// decrypt opens the stored credentials of one connection.
func (m *Manager) decrypt(orgID, id, enc string) (Credentials, error) {
	if enc == "" {
		return Credentials{}, ErrNoCredentials
	}
	if !m.o.Keys.Configured() {
		return Credentials{}, secrets.ErrNoKey
	}
	raw, err := m.o.Keys.Decrypt(enc, CredentialsAAD(orgID, id))
	if err != nil {
		return Credentials{}, err
	}
	return DecodeCredentials(raw)
}

// Create validates and stores a new connection. The id is generated here because it binds the ciphertext to
// the row (CredentialsAAD).
func (m *Manager) Create(ctx context.Context, orgID string, in Input, actor Actor) (*Connection, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	id := uuid.NewString()
	enc, keyID := "", ""
	if in.Credentials != nil {
		var err error
		if enc, keyID, err = m.encrypt(orgID, id, *in.Credentials); err != nil {
			return nil, err
		}
	}
	return m.store.Create(ctx, orgID, id, in, enc, keyID, actor)
}

// Update replaces a connection's fields. Omitted credentials (in.Credentials == nil) keep the stored ones, so
// an edit of the name or the services never has to re-send a secret.
func (m *Manager) Update(ctx context.Context, orgID, id string, in Input, actor Actor) (*Connection, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	old, err := m.store.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	// Changing the provider makes the stored credentials meaningless: they belong to the old provider's
	// fields, so new ones are required rather than silently kept.
	if old.Provider != in.Provider && in.Credentials == nil {
		return nil, invalid("credentials", "required when the provider changes")
	}
	var encPtr *string
	keyID := ""
	if in.Credentials != nil {
		enc, kid, err := m.encrypt(orgID, id, *in.Credentials)
		if err != nil {
			return nil, err
		}
		encPtr, keyID = &enc, kid
	}
	return m.store.Update(ctx, orgID, id, in, encPtr, keyID, actor)
}

// Delete removes a connection.
func (m *Manager) Delete(ctx context.Context, orgID, id string, actor Actor) error {
	return m.store.Delete(ctx, orgID, id, actor)
}

// TestRequest is the "test connection" action. Credentials are the ones being entered in the form; when they
// are omitted the stored credentials of ConnectionID are used, so an edit can be tested without re-typing a
// secret.
type TestRequest struct {
	ConnectionID string       `json:"connection_id"`
	Provider     string       `json:"provider"`
	Scope        string       `json:"scope"`
	Credentials  *Credentials `json:"credentials"`
}

// Test makes one cheap provider call and reports whether the credentials can read metrics. It stores nothing
// and is the only path that decrypts credentials outside the poller.
func (m *Manager) Test(ctx context.Context, orgID string, req TestRequest) error {
	if m.o.Tester == nil {
		return invalid("", "testing a connection is not available on this installation")
	}
	provider, scope, creds := req.Provider, req.Scope, Credentials{}

	if req.ConnectionID != "" {
		conn, err := m.store.Get(ctx, orgID, req.ConnectionID)
		if err != nil {
			return err
		}
		if provider == "" {
			provider = conn.Provider
		}
		if scope == "" && len(conn.Scopes) > 0 {
			scope = conn.Scopes[0]
		}
		if req.Credentials == nil {
			enc, err := m.store.CredentialsEnc(ctx, orgID, req.ConnectionID)
			if err != nil {
				return err
			}
			if creds, err = m.decrypt(orgID, req.ConnectionID, enc); err != nil {
				return err
			}
		}
	}
	if req.Credentials != nil {
		creds = *req.Credentials
	}
	if _, ok := credentialFields[provider]; !ok {
		return invalid("provider", "must be one of "+providerList())
	}
	if scope == "" {
		return invalid("scope", "required: the "+scopeLabel(provider)+" to test")
	}
	if creds.IsZero() {
		return ErrNoCredentials
	}
	if err := creds.Validate(provider); err != nil {
		return err
	}
	return m.o.Tester.Test(ctx, provider, creds, scope)
}

func providerList() string {
	out := ""
	for i, p := range Providers() {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
