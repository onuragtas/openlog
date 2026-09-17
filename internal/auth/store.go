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
	// Locale is the organization's default e-mail language ("" = none, "en", "tr"; 0060_language_preferences, D-095).
	Locale string
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
	// EmailVerifiedAt is nil only for self-service sign-ups that have not confirmed their address yet
	// (OPENLOG_SIGNUP_REQUIRE_VERIFICATION); every other way of creating a user sets it.
	EmailVerifiedAt *time.Time
	// Locale is the e-mail language preferred when the user was created (Accept-Language; "" = English), or, with
	// LocaleExplicit, the language the user chose in their profile (D-095).
	Locale         string
	LocaleExplicit bool
}

// Preference returns the user's chosen language ("" = none: automatic).
func (u User) Preference() string {
	if u.LocaleExplicit {
		return u.Locale
	}
	return ""
}

// EmailVerification is a one-time e-mail address confirmation token. TokenHash = sha256(token).
type EmailVerification struct {
	ID        string
	UserID    string
	Email     string
	TokenHash []byte
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
	Locale    string // language of the e-mail that carried the token
}

// AuditFilter selects audit events (newest first). Zero values do not filter.
type AuditFilter struct {
	Limit  int
	Actor  string    // case-insensitive substring of actor_email
	Action string    // action prefix, e.g. "member." or "member.remove"
	From   time.Time // created_at >= From
	To     time.Time // created_at < To
	// Before continues a listing after the last event of the previous page (keyset on created_at, id).
	Before *AuditCursor
}

// AuditCursor is the position of an audit event in the newest-first order.
type AuditCursor struct {
	CreatedAt time.Time
	ID        int64
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
	// AuthMethod is how the session was created: MethodPassword (also sign-up and invitations; "" = password),
	// MethodOIDC or MethodSAML (external.go).
	AuthMethod string
	// OrgID binds a single sign-on session to one organization; ConnectionID is its SSO connection. Empty for
	// password sessions.
	OrgID        string
	ConnectionID string
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
	ID     string
	OrgID  string
	Name   string
	Prefix string
	Hash   []byte
	// LegacyHashes (create only, not stored): other hashes of the same value (KeyHasher.Legacy); creation fails
	// with ErrAlreadyExists when any key has one of them, so a value stays unusable after a secret is introduced.
	LegacyHashes   [][]byte
	Custom         bool // operator-chosen value (imported or bootstrap), not generated
	CreatedBy      string
	CreatedByEmail string
	CreatedAt      time.Time
	LastUsedAt     *time.Time
	RevokedAt      *time.Time
}

// BrowserKey is a public key of the browser SDK (docs/contracts/rum.md §3, D-136). Unlike a LicenseKey it is
// **not** a secret: it ships inside a web page, so everyone who can open the page has it. What bounds it is
// not confidentiality but capability — it authenticates one endpoint (POST /v1/rum), it may only be used
// from Origins, it carries its own RateLimitPerMinute, and every payload it delivers is rewritten to
// ServiceName/Environment before storage (internal/rum.Sanitize).
//
// The value is still stored hashed like every other credential: a database dump must not become the ability
// to write telemetry, and the unique hash is what stops one value belonging to two organizations.
type BrowserKey struct {
	ID     string
	OrgID  string
	Name   string
	Prefix string
	Hash   []byte
	// ServiceName is the application every payload of this key is stored under; Environment is the
	// optional deployment.environment.name. Both are forced server-side, never taken from the payload.
	ServiceName string
	Environment string
	// Origins is the allowlist, already normalized by rum.ParseOrigins (exact origins and subdomain
	// wildcards). Never empty: the API refuses to create a key without one.
	Origins []string
	// RateLimitPerMinute bounds the RUM events this key may produce per ingest pod.
	RateLimitPerMinute int
	// SampleRate is the share of sessions the SDK keeps (0 < x <= 1); the stored sampling weight is
	// derived from it, so a page cannot inflate its own traffic.
	SampleRate     float64
	CreatedBy      string
	CreatedByEmail string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastUsedAt     *time.Time
	RevokedAt      *time.Time
}

// BrowserKeyInput is the editable part of a browser key (create and update).
type BrowserKeyInput struct {
	Name               string
	ServiceName        string
	Environment        string
	Origins            []string
	RateLimitPerMinute int
	SampleRate         float64
}

// APIKey is a Query and management API key. The plaintext is never stored.
type APIKey struct {
	ID     string
	OrgID  string
	Name   string
	Prefix string
	Hash   []byte
	// Scope is derived from Role at creation and kept for older binaries: "read" for a viewer key,
	// "write" for one that may change configuration.
	Scope string
	// Role is what the key may do in its organization: viewer (read-only, the default and what every
	// key created before 0091_api_key_roles has), member or admin (D-133).
	Role           Role
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
	LastSentAt     *time.Time // last invitation e-mail (nil: never e-mailed)
	SendCount      int        // invitation e-mails sent
	Locale         string     // e-mail language: the inviter's Accept-Language at creation ("" = English)
}

// Expired reports whether a not yet accepted or revoked invitation has passed its expiry at now.
func (i Invitation) Expired(now time.Time) bool {
	return i.AcceptedAt == nil && i.RevokedAt == nil && !now.Before(i.ExpiresAt)
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
	// ActorAPIKeyID and ActorAPIKeyName name the API key that made the change; both are empty for
	// changes made by a user, and ActorUserID/ActorEmail are empty for changes made by a key (D-133).
	ActorAPIKeyID   string
	ActorAPIKeyName string
	Action          string
	TargetType      string
	TargetID        string
	Details         map[string]any
	IP              string
	CreatedAt       time.Time
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
	// SetOrganizationLocale sets the organization's default e-mail language ("" = none).
	SetOrganizationLocale(ctx context.Context, id, locale string) error

	CreateUser(ctx context.Context, u *User) error
	GetUser(ctx context.Context, id string) (User, error)
	GetUserByEmail(ctx context.Context, email string) (User, error)
	SetUserPassword(ctx context.Context, userID, hash string) error
	SetUserLastLogin(ctx context.Context, userID string, at time.Time) error
	// SetUserEmail changes a user's (normalized) e-mail address; ErrAlreadyExists when another account uses it
	// (SCIM e-mail change, D-089).
	SetUserEmail(ctx context.Context, userID, email string) error
	// SetUserLocale stores the user's language; explicit=false (automatic) keeps locale only as the request language.
	SetUserLocale(ctx context.Context, userID, locale string, explicit bool) error

	AddMember(ctx context.Context, orgID, userID string, role Role) error
	GetMembership(ctx context.Context, orgID, userID string) (Membership, error)
	ListMemberships(ctx context.Context, userID string) ([]Membership, error) // oldest first
	ListMembers(ctx context.Context, orgID string) ([]Member, error)
	UpdateMemberRole(ctx context.Context, orgID, userID string, role Role) error // ErrLastOwner
	RemoveMember(ctx context.Context, orgID, userID string) error                // ErrLastOwner; see MemberRemover

	CreateSession(ctx context.Context, s *Session) error
	GetSessionByTokenHash(ctx context.Context, hash []byte) (Session, User, error)
	TouchSession(ctx context.Context, id string, at time.Time) error
	ListSessions(ctx context.Context, userID string, now time.Time) ([]Session, error) // not revoked, not expired
	RevokeSession(ctx context.Context, userID, id string, at time.Time) error
	RevokeUserSessions(ctx context.Context, userID, exceptID string, at time.Time) error

	CreateLicenseKey(ctx context.Context, k *LicenseKey) error
	ListLicenseKeys(ctx context.Context, orgID string) ([]LicenseKey, error)
	RevokeLicenseKey(ctx context.Context, orgID, id, by string, at time.Time) (LicenseKey, error)

	// Browser keys (rum.md §3). The ingest-side lookup is not part of this interface: it is served by
	// rum.Store, which internal/store/postgres implements on the same pool, exactly like tenant.KeyStore.
	CreateBrowserKey(ctx context.Context, k *BrowserKey) error
	ListBrowserKeys(ctx context.Context, orgID string) ([]BrowserKey, error)
	GetBrowserKey(ctx context.Context, orgID, id string) (BrowserKey, error)
	UpdateBrowserKey(ctx context.Context, orgID, id string, in BrowserKeyInput, by string, at time.Time) (BrowserKey, error)
	RevokeBrowserKey(ctx context.Context, orgID, id, by string, at time.Time) (BrowserKey, error)

	CreateAPIKey(ctx context.Context, k *APIKey) error
	ListAPIKeys(ctx context.Context, orgID string) ([]APIKey, error)
	GetAPIKey(ctx context.Context, orgID, id string) (APIKey, error)
	RevokeAPIKey(ctx context.Context, orgID, id, by string, at time.Time) (APIKey, error)
	// RevokeAPIKeysCreatedBy revokes the organization's active API keys created by userID and returns how many.
	RevokeAPIKeysCreatedBy(ctx context.Context, orgID, userID, by string, at time.Time) (int, error)
	// LookupAPIKey returns the key (even revoked or expired) whose hash is one of hashes (current format first);
	// an active key found by a later candidate is rewritten to hashes[0].
	LookupAPIKey(ctx context.Context, hashes [][]byte) (APIKey, Organization, error)
	TouchAPIKey(ctx context.Context, id string, at time.Time) error

	CreateInvitation(ctx context.Context, inv *Invitation) error // ErrAlreadyExists when one is pending for the email
	// ListInvitations lists invitations that are neither accepted nor revoked and, unless includeExpired, not expired.
	ListInvitations(ctx context.Context, orgID string, now time.Time, includeExpired bool) ([]Invitation, error)
	RevokeInvitation(ctx context.Context, orgID, id string, at time.Time) (Invitation, error)
	// RenewInvitation replaces the token and expiry of an invitation that is neither accepted nor revoked
	// (expired ones included); ErrNotFound otherwise.
	RenewInvitation(ctx context.Context, orgID, id string, tokenHash []byte, expiresAt time.Time) (Invitation, error)
	// MarkInvitationSent records an invitation e-mail (last_sent_at, send_count).
	MarkInvitationSent(ctx context.Context, id string, at time.Time) error
	GetInvitationByTokenHash(ctx context.Context, hash []byte) (Invitation, Organization, error)
	// AcceptInvitation marks the pending invitation accepted and adds the
	// membership atomically; it creates user first when user.ID == "".
	AcceptInvitation(ctx context.Context, invID string, user *User, at time.Time) error

	AddAuditEvent(ctx context.Context, e *AuditEvent) error
	ListAuditEvents(ctx context.Context, orgID string, f AuditFilter) ([]AuditEvent, error)

	CreateEmailVerification(ctx context.Context, v *EmailVerification) error
	// ConsumeEmailVerification marks the unused, unexpired token used and sets the user's email_verified_at
	// (when the user still has the token's email) in one statement; ErrNotFound otherwise.
	ConsumeEmailVerification(ctx context.Context, tokenHash []byte, at time.Time) (User, error)

	// Rate limiting counters (login_failures table; keys are hashes of a purpose label plus the counted subject).
	CountLoginFailures(ctx context.Context, key []byte, since time.Time) (int, error)
	AddLoginFailure(ctx context.Context, key []byte, at time.Time) error
	ClearLoginFailures(ctx context.Context, key []byte) error
}
