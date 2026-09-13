package auth

import (
	"context"
	"time"

	"github.com/onuragtas/openlog/internal/tenant"
)

// Organization is a tenant. TenantID is the ClickHouse tenant key.
type Organization struct {
	ID        string
	TenantID  string
	Name      string
	CreatedAt time.Time
}

// User is an account. PasswordHash is never serialized to clients.
type User struct {
	ID           string
	Email        string
	Name         string
	PasswordHash string
	CreatedAt    time.Time
	LastLoginAt  *time.Time
	DisabledAt   *time.Time
}

// Membership is one of a user's organizations.
type Membership struct {
	Org       Organization
	Role      Role
	CreatedAt time.Time
}

// Member is a user in an organization.
type Member struct {
	UserID   string
	Email    string
	Name     string
	Role     Role
	JoinedAt time.Time
}

// Session is a browser session. TokenHash = sha256(cookie value).
type Session struct {
	ID         string
	UserID     string
	TokenHash  []byte
	CSRFToken  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	IP         string
	UserAgent  string
}

// Active reports whether the session is usable at now: not revoked, not past
// its absolute expiry and (idle > 0) used within the idle timeout.
func (s Session) Active(now time.Time, idle time.Duration) bool {
	if s.RevokedAt != nil || !now.Before(s.ExpiresAt) {
		return false
	}
	return idle <= 0 || now.Sub(s.LastSeenAt) < idle
}

// LicenseKey is an ingest key. The plaintext is never stored.
type LicenseKey struct {
	ID             string
	OrgID          string
	Name           string
	Prefix         string
	Hash           []byte
	CreatedBy      string
	CreatedByEmail string
	CreatedAt      time.Time
	LastUsedAt     *time.Time
	RevokedAt      *time.Time
}

// APIKey is a read-only Query API key. The plaintext is never stored.
type APIKey struct {
	ID             string
	OrgID          string
	Name           string
	Prefix         string
	Hash           []byte
	Scope          string
	CreatedBy      string
	CreatedByEmail string
	CreatedAt      time.Time
	LastUsedAt     *time.Time
	ExpiresAt      *time.Time
	RevokedAt      *time.Time
}

// Usable reports whether the key authenticates at now.
func (k APIKey) Usable(now time.Time) bool {
	return k.RevokedAt == nil && (k.ExpiresAt == nil || now.Before(*k.ExpiresAt))
}

// Invitation invites an email address into an organization with a role.
type Invitation struct {
	ID             string
	OrgID          string
	Email          string
	Role           Role
	TokenHash      []byte
	InvitedBy      string
	InvitedByEmail string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	AcceptedAt     *time.Time
	RevokedAt      *time.Time
}

// Pending reports whether the invitation can still be accepted at now.
func (i Invitation) Pending(now time.Time) bool {
	return i.AcceptedAt == nil && i.RevokedAt == nil && now.Before(i.ExpiresAt)
}

// AuditEvent is an audit log entry. OrgID is empty for user-level events.
type AuditEvent struct {
	ID          int64
	OrgID       string
	ActorUserID string
	ActorEmail  string
	Action      string
	TargetType  string
	TargetID    string
	Details     map[string]any
	IP          string
	CreatedAt   time.Time
}

// Store persists auth data. Implementations return ErrNotFound,
// ErrAlreadyExists and ErrLastOwner as documented; any other error is treated
// as the store being unavailable. Create methods fill in ID and CreatedAt
// when they are empty.
type Store interface {
	tenant.KeyStore

	CreateOrganization(ctx context.Context, org *Organization, owner *User) error // creates owner too when owner.ID == ""
	GetOrganization(ctx context.Context, id string) (Organization, error)
	GetOrganizationByTenant(ctx context.Context, tenantID string) (Organization, error)
	UpdateOrganizationName(ctx context.Context, id, name string) error

	CreateUser(ctx context.Context, u *User) error
	GetUser(ctx context.Context, id string) (User, error)
	GetUserByEmail(ctx context.Context, email string) (User, error)
	SetUserPassword(ctx context.Context, userID, hash string) error
	SetUserLastLogin(ctx context.Context, userID string, at time.Time) error

	AddMember(ctx context.Context, orgID, userID string, role Role) error
	GetMembership(ctx context.Context, orgID, userID string) (Membership, error)
	ListMemberships(ctx context.Context, userID string) ([]Membership, error) // oldest first
	ListMembers(ctx context.Context, orgID string) ([]Member, error)
	UpdateMemberRole(ctx context.Context, orgID, userID string, role Role) error // ErrLastOwner
	RemoveMember(ctx context.Context, orgID, userID string) error                // ErrLastOwner

	CreateSession(ctx context.Context, s *Session) error
	GetSessionByTokenHash(ctx context.Context, hash []byte) (Session, User, error)
	TouchSession(ctx context.Context, id string, at time.Time) error
	ListSessions(ctx context.Context, userID string, now time.Time) ([]Session, error) // not revoked, not expired
	RevokeSession(ctx context.Context, userID, id string, at time.Time) error
	RevokeUserSessions(ctx context.Context, userID, exceptID string, at time.Time) error

	CreateLicenseKey(ctx context.Context, k *LicenseKey) error
	ListLicenseKeys(ctx context.Context, orgID string) ([]LicenseKey, error)
	RevokeLicenseKey(ctx context.Context, orgID, id, by string, at time.Time) (LicenseKey, error)

	CreateAPIKey(ctx context.Context, k *APIKey) error
	ListAPIKeys(ctx context.Context, orgID string) ([]APIKey, error)
	GetAPIKey(ctx context.Context, orgID, id string) (APIKey, error)
	RevokeAPIKey(ctx context.Context, orgID, id, by string, at time.Time) (APIKey, error)
	LookupAPIKey(ctx context.Context, hash []byte) (APIKey, Organization, error)
	TouchAPIKey(ctx context.Context, id string, at time.Time) error

	CreateInvitation(ctx context.Context, inv *Invitation) error // ErrAlreadyExists when one is pending for the email
	ListInvitations(ctx context.Context, orgID string, now time.Time) ([]Invitation, error)
	RevokeInvitation(ctx context.Context, orgID, id string, at time.Time) (Invitation, error)
	GetInvitationByTokenHash(ctx context.Context, hash []byte) (Invitation, Organization, error)
	// AcceptInvitation marks the pending invitation accepted and adds the
	// membership atomically; it creates user first when user.ID == "".
	AcceptInvitation(ctx context.Context, invID string, user *User, at time.Time) error

	AddAuditEvent(ctx context.Context, e *AuditEvent) error
	ListAuditEvents(ctx context.Context, orgID string, limit int) ([]AuditEvent, error)

	CountLoginFailures(ctx context.Context, key []byte, since time.Time) (int, error)
	AddLoginFailure(ctx context.Context, key []byte, at time.Time) error
	ClearLoginFailures(ctx context.Context, key []byte) error
}
