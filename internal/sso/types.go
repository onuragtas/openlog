// Package sso implements single sign-on for organizations (docs/contracts/api.md "Single sign-on", D-077, D-088,
// D-089): OIDC and SAML 2.0 connections (several per organization, routed by claimed e-mail domain), claimed
// domains, IdP group → role mappings, just-in-time provisioning, SSO-bound sessions, enforcement, single logout
// (SAML SLO, OIDC RP-initiated logout), the background refresh of IdP metadata and the persistence used by SCIM
// provisioning (internal/scim, D-078). Users, memberships, sessions and the audit log stay in internal/auth.
package sso

import (
	"context"
	"encoding/json"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
)

// Protocol is the protocol of a connection.
type Protocol string

// Protocols.
const (
	ProtocolOIDC Protocol = "oidc"
	ProtocolSAML Protocol = "saml"
)

// OIDCConfig is the stored OIDC configuration (sso_connections.config). The client secret is stored encrypted.
type OIDCConfig struct {
	Issuer   string   `json:"issuer"`
	ClientID string   `json:"client_id"`
	Scopes   []string `json:"scopes,omitempty"` // requested besides openid, email, profile
	// RequireEmailVerified refuses ID tokens without email_verified=true [true].
	RequireEmailVerified bool `json:"require_email_verified"`
}

// SAMLConfig is the stored SAML configuration (sso_connections.config). The SP private key is stored encrypted.
type SAMLConfig struct {
	IdPMetadataURL string `json:"idp_metadata_url,omitempty"`
	// IdPMetadataXML is the pasted metadata or the copy fetched from IdPMetadataURL when the connection was saved.
	IdPMetadataXML string `json:"idp_metadata_xml"`
	// Derived from the metadata for display.
	IdPEntityID string `json:"idp_entity_id"`
	IdPSSOURL   string `json:"idp_sso_url"`
	// IdPSLOURL is the IdP SingleLogoutService (HTTP-Redirect preferred, else HTTP-POST); "" = no single logout.
	IdPSLOURL       string   `json:"idp_slo_url,omitempty"`
	IdPSLOBinding   string   `json:"idp_slo_binding,omitempty"`
	IdPCertificates []string `json:"idp_certificates"` // SHA-256 fingerprints
	IdPCertNotAfter string   `json:"idp_cert_not_after,omitempty"`

	AllowIdPInitiated   bool     `json:"allow_idp_initiated"`
	RelayStateAllowlist []string `json:"relay_state_allowlist,omitempty"` // relative UI paths for IdP-initiated sign-in
	SignAuthnRequests   bool     `json:"sign_authn_requests"`
	SPCertificatePEM    string   `json:"sp_certificate_pem"`
}

// Connection is an SSO connection of an organization. The oldest connection of an organization is its default
// connection: claimed domains without an explicit connection route to it.
type Connection struct {
	ID       string
	OrgID    string
	Protocol Protocol
	Name     string
	Enabled  bool
	OIDC     *OIDCConfig
	SAML     *SAMLConfig
	// SecretEnc is the sealed OIDC client secret (nil: public client); SPKeyEnc the sealed SAML SP key (PKCS#8).
	SecretEnc []byte
	SPKeyEnc  []byte

	EmailAttribute  string
	NameAttribute   string
	GroupsAttribute string
	JITEnabled      bool
	DefaultRole     auth.Role
	SessionMaxAge   time.Duration

	Enforce           bool
	BreakGlassUserIDs []string
	// LogoutRedirectAllowlist lists the relative UI paths a single logout may return to besides /login.
	LogoutRedirectAllowlist []string
	// AllowExternalInvitations lets members of the domains routed to this connection accept invitations of other
	// organizations with a password (claimed-domain redirection, D-089).
	AllowExternalInvitations bool

	ConfigVersion   int
	TestedVersion   int
	LastTestAt      *time.Time
	LastTestOK      bool
	LastTestError   string
	LastTestDetails map[string]any

	CreatedBy string
	UpdatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time

	// Refresh is the state of the background IdP metadata refresh (refresh.go).
	Refresh RefreshState
}

// RefreshState is the result of the last background refresh of a connection's IdP documents.
type RefreshState struct {
	RefreshedAt *time.Time
	OK          *bool // nil: never refreshed
	Error       string
	Failures    int // consecutive failures
	NextAt      *time.Time
	Cache       IdPCache
}

// IdPCache is the last successfully fetched IdP data (sso_connections.idp_cache).
type IdPCache struct {
	FetchedAt      *time.Time      `json:"fetched_at,omitempty"`
	Issuer         string          `json:"issuer,omitempty"`
	OIDCDiscovery  json.RawMessage `json:"oidc_discovery,omitempty"`
	OIDCJWKS       json.RawMessage `json:"oidc_jwks,omitempty"`
	SAMLValidUntil *time.Time      `json:"saml_valid_until,omitempty"`
}

// Tested reports whether the current settings passed a test sign-in.
func (c Connection) Tested() bool { return c.LastTestOK && c.TestedVersion == c.ConfigVersion }

// Domain is an e-mail domain claimed by an organization.
type Domain struct {
	ID                 string
	OrgID              string
	Domain             string
	DNSToken           string
	EmailTokenHash     []byte
	EmailAddress       string
	EmailExpiresAt     *time.Time
	VerifiedAt         *time.Time
	VerificationMethod string // "", "dns_txt", "email"
	LastCheckedAt      *time.Time
	// ConnectionID routes sign-ins of the domain to a connection; "" = the organization's default connection.
	ConnectionID string
	CreatedBy    string
	CreatedAt    time.Time
}

// RoleMapping maps an IdP group name to a role.
type RoleMapping struct {
	Group string
	Role  auth.Role
}

// Login state purposes.
const (
	PurposeLogin  = "login"
	PurposeTest   = "test"
	PurposeLogout = "logout"
)

// Identity is a verified identity asserted by the IdP.
type Identity struct {
	Subject       string   `json:"subject"`
	Issuer        string   `json:"issuer"`
	Email         string   `json:"email"`
	EmailVerified bool     `json:"email_verified"`
	Name          string   `json:"name"`
	Groups        []string `json:"groups"`
	GroupsPresent bool     `json:"groups_present"`
	// SAML NameID details and the AuthnStatement SessionIndex (OIDC: the sid claim), kept for single logout.
	NameIDFormat    string `json:"name_id_format,omitempty"`
	NameQualifier   string `json:"name_qualifier,omitempty"`
	SPNameQualifier string `json:"sp_name_qualifier,omitempty"`
	SessionIndex    string `json:"session_index,omitempty"`
	// IDToken is the raw OIDC ID token (id_token_hint); never serialized.
	IDToken string `json:"-"`
}

// SSOSession links an openlog session to the IdP session that created it (sso_sessions).
type SSOSession struct {
	SessionID       string
	ConnectionID    string
	OrgID           string
	UserID          string
	Subject         string
	NameIDFormat    string
	NameQualifier   string
	SPNameQualifier string
	SessionIndex    string
	IDTokenEnc      []byte
	CreatedAt       time.Time
}

// LoginState is an in-flight sign-in.
type LoginState struct {
	ID            string
	StateHash     []byte
	BindingHash   []byte // nil: IdP-initiated SAML (no browser binding)
	OrgID         string
	ConnectionID  string
	Purpose       string
	IdPInitiated  bool
	Nonce         string
	PKCEVerifier  string
	SAMLRequestID string
	RedirectTo    string
	ActorUserID   string
	ConfigVersion int
	Result        *Identity
	CreatedAt     time.Time
	ExpiresAt     time.Time
	ConsumedAt    *time.Time
}

// OrgPolicy is what the session policy needs to know about an organization on every request.
type OrgPolicy struct {
	// The connection passed to GetOrgPolicy, when it belongs to the organization ("" = not found).
	ConnectionID  string
	Enabled       bool
	SessionMaxAge time.Duration
	// DomainVerified: the e-mail domain passed to GetOrgPolicy is a verified domain of the organization.
	DomainVerified bool
	// Enforce: the connection the domain routes to is enabled and enforces SSO; BreakGlassUserIDs are its
	// break-glass owners.
	Enforce           bool
	BreakGlassUserIDs []string
}

// SCIMToken is a SCIM bearer token. The plaintext is never stored.
type SCIMToken struct {
	ID             string
	OrgID          string
	Name           string
	Prefix         string
	Hash           []byte
	CreatedBy      string
	CreatedByEmail string
	CreatedAt      time.Time
	LastUsedAt     *time.Time
	ExpiresAt      *time.Time
	RevokedAt      *time.Time
}

// Usable reports whether the token authenticates at now.
func (t SCIMToken) Usable(now time.Time) bool {
	return t.RevokedAt == nil && (t.ExpiresAt == nil || now.Before(*t.ExpiresAt))
}

// SCIMUser is the SCIM view of an organization member.
type SCIMUser struct {
	OrgID       string
	UserID      string
	UserName    string
	ExternalID  string
	Active      bool
	DisplayName string
	GivenName   string
	FamilyName  string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// SCIMGroup is a provisioned group. Members are user ids.
type SCIMGroup struct {
	ID          string
	OrgID       string
	DisplayName string
	ExternalID  string
	Members     []string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// SCIMFilter is the supported SCIM filter subset (attribute eq value). Empty fields do not filter.
type SCIMFilter struct {
	UserName    string
	ExternalID  string
	DisplayName string
}

// Store persists SSO and SCIM data. Implementations return auth.ErrNotFound and auth.ErrAlreadyExists as
// documented; other errors mean the store is unavailable.
type Store interface {
	// CreateConnection inserts c (c.ID must be preset).
	CreateConnection(ctx context.Context, c *Connection) error
	// ListConnections returns the organization's connections, the default (oldest) first.
	ListConnections(ctx context.Context, orgID string) ([]Connection, error)
	// GetConnection returns connection id of the organization; id "" = the default connection.
	GetConnection(ctx context.Context, orgID, id string) (Connection, error)
	GetConnectionByID(ctx context.Context, id string) (Connection, error)
	// UpdateConnection replaces all settings of c.ID (not the test result and refresh fields).
	UpdateConnection(ctx context.Context, c *Connection) error
	DeleteConnection(ctx context.Context, orgID, id string) (Connection, error)
	// RecordTest stores a test sign-in result; tested_version is set only when ok and version is still current.
	RecordTest(ctx context.Context, connectionID string, version int, ok bool, errMsg string, details map[string]any, at time.Time) error
	// GetOrgPolicy returns the session connection (connectionID, may be "") and the domain policy (emailDomain,
	// may be "") of an organization in one read.
	GetOrgPolicy(ctx context.Context, orgID, connectionID, emailDomain string) (OrgPolicy, error)
	// ConnectionForDomain returns the verified claim of domain and the connection it routes to (its connection_id
	// or the organization's default connection); ErrNotFound when the domain is not verified or has no connection.
	ConnectionForDomain(ctx context.Context, domain string) (Connection, Domain, error)

	// RecordRefresh stores the result of a background refresh; cache is replaced only when ok.
	RecordRefresh(ctx context.Context, connectionID string, ok bool, errMsg string, cache *IdPCache, next, at time.Time) error
	// ListRefreshDue returns enabled connections whose next refresh is due (never refreshed first).
	ListRefreshDue(ctx context.Context, now time.Time, limit int) ([]Connection, error)
	// UpdateSAMLMetadata replaces the SAML settings of a connection whose config_version is still version,
	// without changing the version (refreshed IdP metadata); ErrNotFound otherwise.
	UpdateSAMLMetadata(ctx context.Context, connectionID string, version int, cfg SAMLConfig) error
	// CountRefreshFailing counts enabled connections whose last refresh failed.
	CountRefreshFailing(ctx context.Context) (int, error)

	CreateSSOSession(ctx context.Context, ss *SSOSession) error
	GetSSOSession(ctx context.Context, sessionID string) (SSOSession, error)
	// ListSSOSessions returns the active (not revoked, not expired) sessions of a connection with subject
	// (subject "" and userID != "": all active sessions of the user on the connection).
	ListSSOSessions(ctx context.Context, connectionID, subject, userID string, now time.Time) ([]SSOSession, error)

	CreateDomain(ctx context.Context, d *Domain) error // ErrAlreadyExists for (org, domain)
	ListDomains(ctx context.Context, orgID string) ([]Domain, error)
	GetDomain(ctx context.Context, orgID, id string) (Domain, error)
	DeleteDomain(ctx context.Context, orgID, id string) (Domain, error)
	// MarkDomainVerified verifies the domain; ErrAlreadyExists when another organization verified it first.
	MarkDomainVerified(ctx context.Context, orgID, id, method string, at time.Time) (Domain, error)
	SetDomainChecked(ctx context.Context, id string, at time.Time) error
	SetDomainEmailToken(ctx context.Context, orgID, id string, tokenHash []byte, address string, expires time.Time) error
	// ConsumeDomainEmailToken verifies the domain of an unexpired e-mail token (and clears the token).
	ConsumeDomainEmailToken(ctx context.Context, tokenHash []byte, at time.Time) (Domain, error)
	// FindVerifiedDomain returns the verified claim of domain (any organization).
	FindVerifiedDomain(ctx context.Context, domain string) (Domain, error)
	// SetDomainConnection routes a domain to connectionID ("" = default); ErrNotFound when the domain or the
	// connection is not the organization's.
	SetDomainConnection(ctx context.Context, orgID, domainID, connectionID string) (Domain, error)

	// ListRoleMappings returns the mappings of a connection (connectionID "" = organization-wide).
	ListRoleMappings(ctx context.Context, orgID, connectionID string) ([]RoleMapping, error)
	ReplaceRoleMappings(ctx context.Context, orgID, connectionID string, ms []RoleMapping) error

	CreateLoginState(ctx context.Context, st *LoginState) error
	// GetLoginState returns an unconsumed, unexpired state.
	GetLoginState(ctx context.Context, stateHash []byte, now time.Time) (LoginState, error)
	// SetLoginResult stores the identity of a state that has none yet; ErrNotFound otherwise.
	SetLoginResult(ctx context.Context, id string, result Identity, now time.Time) error
	// ConsumeLoginState marks an unconsumed, unexpired state consumed and returns it; ErrNotFound otherwise.
	ConsumeLoginState(ctx context.Context, stateHash []byte, now time.Time) (LoginState, error)
	// RecordAssertion stores a SAML assertion id; fresh=false when it was seen before (replay).
	RecordAssertion(ctx context.Context, connectionID, assertionID string, expires time.Time) (fresh bool, err error)
	// Cleanup deletes expired login states, assertion ids and domain e-mail tokens.
	Cleanup(ctx context.Context, now time.Time) error

	CreateSCIMToken(ctx context.Context, t *SCIMToken) error
	ListSCIMTokens(ctx context.Context, orgID string) ([]SCIMToken, error)
	RevokeSCIMToken(ctx context.Context, orgID, id string, at time.Time) (SCIMToken, error)
	// LookupSCIMToken returns the token (even revoked or expired) with one of hashes (current format first).
	LookupSCIMToken(ctx context.Context, hashes [][]byte) (SCIMToken, auth.Organization, error)
	TouchSCIMToken(ctx context.Context, id string, at time.Time) error

	CreateSCIMUser(ctx context.Context, u *SCIMUser) error // ErrAlreadyExists: user name, external id or user
	GetSCIMUser(ctx context.Context, orgID, userID string) (SCIMUser, error)
	ListSCIMUsers(ctx context.Context, orgID string, f SCIMFilter, offset, limit int) ([]SCIMUser, int, error)
	UpdateSCIMUser(ctx context.Context, u *SCIMUser) error
	DeleteSCIMUser(ctx context.Context, orgID, userID string) error

	CreateSCIMGroup(ctx context.Context, g *SCIMGroup) error // ErrAlreadyExists: display name
	GetSCIMGroup(ctx context.Context, orgID, id string) (SCIMGroup, error)
	ListSCIMGroups(ctx context.Context, orgID string, f SCIMFilter, offset, limit int) ([]SCIMGroup, int, error)
	// UpdateSCIMGroup changes the name and external id and, when members != nil, replaces the members.
	UpdateSCIMGroup(ctx context.Context, g *SCIMGroup, members []string) error
	AddSCIMGroupMembers(ctx context.Context, orgID, groupID string, userIDs []string) error
	RemoveSCIMGroupMembers(ctx context.Context, orgID, groupID string, userIDs []string) error
	DeleteSCIMGroup(ctx context.Context, orgID, id string) (SCIMGroup, error)
	// SCIMGroupNames returns the display names of the organization's groups userID belongs to.
	SCIMGroupNames(ctx context.Context, orgID, userID string) ([]string, error)
}
