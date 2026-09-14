package sso

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/tenant"
)

// TXTResolver looks up DNS TXT records (net.Resolver; replaced in tests).
type TXTResolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// Config configures the Service (docs/contracts/config.md "Single sign-on").
type Config struct {
	// PublicURL is OPENLOG_PUBLIC_URL: redirect URIs, SAML entity ids and links. Required for SSO.
	PublicURL    string
	CookieSecure bool
	// LoginTTL bounds a sign-in from start to completion [10m].
	LoginTTL time.Duration
	// ClockSkew tolerated for ID token and SAML assertion times [2m].
	ClockSkew time.Duration
	// AllowPrivateNetworks lets IdP URLs resolve to private addresses and use http (self-hosted IdPs).
	AllowPrivateNetworks bool
	HTTPTimeout          time.Duration // requests to identity providers [10s]
	SecretBox            *SecretBox
	KeyHasher            *tenant.KeyHasher // SCIM tokens (like API keys, D-044)
	Mailer               auth.Mailer       // domain verification e-mails; nil disables that method
	Resolver             TXTResolver       // nil: net.DefaultResolver
	HTTPClient           *http.Client      // nil: NewHTTPClient(AllowPrivateNetworks, HTTPTimeout)
	SCIMEnabled          bool
}

func (c *Config) defaults() {
	if c.LoginTTL <= 0 {
		c.LoginTTL = 10 * time.Minute
	}
	if c.ClockSkew <= 0 {
		c.ClockSkew = 2 * time.Minute
	}
	if c.HTTPTimeout <= 0 {
		c.HTTPTimeout = 10 * time.Second
	}
	if c.SecretBox == nil {
		c.SecretBox = &SecretBox{}
	}
	if c.Resolver == nil {
		c.Resolver = net.DefaultResolver
	}
	c.PublicURL = strings.TrimRight(strings.TrimSpace(c.PublicURL), "/")
}

// Service implements SSO administration, sign-in and the session policy. Like auth.Service it keeps no
// per-user state in memory: login states, replay cache and settings live in the Store. The only process state is
// a cache of OIDC discovery documents and JWKS (refetched hourly and on unknown key ids).
type Service struct {
	auth   *auth.Service
	users  auth.Store
	store  Store
	cfg    Config
	log    *slog.Logger
	now    func() time.Time
	client *http.Client

	mu        sync.Mutex
	providers map[string]*oidcClient
	metrics   *refreshMetrics
}

// NewService creates a Service on top of the auth service (users, memberships, sessions, audit log).
func NewService(authSvc *auth.Service, store Store, cfg Config, log *slog.Logger) *Service {
	cfg.defaults()
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = NewHTTPClient(cfg.AllowPrivateNetworks, cfg.HTTPTimeout)
	}
	configureSAML(cfg.ClockSkew)
	return &Service{auth: authSvc, users: authSvc.Store(), store: store, cfg: cfg, log: log, now: time.Now,
		client: client, providers: map[string]*oidcClient{}}
}

// SetClock replaces the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// Config returns the effective configuration.
func (s *Service) Config() Config { return s.cfg }

// Available reports whether SSO can be used (OPENLOG_PUBLIC_URL is set).
func (s *Service) Available() bool { return s != nil && s.cfg.PublicURL != "" }

// SCIMEnabled reports whether the SCIM endpoints are served.
func (s *Service) SCIMEnabled() bool { return s != nil && s.cfg.SCIMEnabled }

// Store returns the SSO store (internal/scim).
func (s *Service) Store() Store { return s.store }

// Users returns the auth store (internal/scim).
func (s *Service) Users() auth.Store { return s.users }

// Now returns the service clock.
func (s *Service) Now() time.Time { return s.now() }

// ---- URLs ----

// OIDCRedirectURL is the redirect URI to register at the OIDC provider.
func (s *Service) OIDCRedirectURL() string { return s.cfg.PublicURL + "/api/v1/sso/oidc/callback" }

// SAMLEntityID is the SP entity id (also the metadata URL) of a connection.
func (s *Service) SAMLEntityID(connectionID string) string {
	return s.cfg.PublicURL + "/api/v1/sso/saml/" + connectionID + "/metadata"
}

// SAMLACSURL is the assertion consumer service URL of a connection.
func (s *Service) SAMLACSURL(connectionID string) string {
	return s.cfg.PublicURL + "/api/v1/sso/saml/" + connectionID + "/acs"
}

// SAMLSLOURL is the single logout service URL of a connection (LogoutRequest and LogoutResponse, both bindings).
func (s *Service) SAMLSLOURL(connectionID string) string {
	return s.cfg.PublicURL + "/api/v1/sso/saml/" + connectionID + "/slo"
}

// OIDCPostLogoutRedirectURL is the post_logout_redirect_uri to register at the OIDC provider.
func (s *Service) OIDCPostLogoutRedirectURL() string {
	return s.cfg.PublicURL + "/api/v1/sso/oidc/logout/callback"
}

// SCIMBaseURL is the SCIM 2.0 base URL.
func (s *Service) SCIMBaseURL() string { return s.cfg.PublicURL + "/api/scim/v2" }

// ---- errors ----

func newErr(code auth.Code, format string, args ...any) error {
	return &auth.Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func invalid(format string, args ...any) error {
	return newErr(auth.CodeInvalidArgument, format, args...)
}

func denied(msg string) error { return &auth.Error{Code: auth.CodePermissionDenied, Message: msg} }

func notFound(msg string) error { return &auth.Error{Code: auth.CodeNotFound, Message: msg} }

func precondition(format string, args ...any) error {
	return newErr(auth.CodeFailedPrecondition, format, args...)
}

// fail passes *auth.Error values through and reports anything else as unavailable.
func (s *Service) fail(err error) error {
	var e *auth.Error
	if errors.As(err, &e) {
		return err
	}
	s.log.Error("sso store error", "err", err)
	return &auth.Error{Code: auth.CodeUnavailable, Message: "single sign-on backend unavailable"}
}

var errUnavailableNoURL = &auth.Error{Code: auth.CodeFailedPrecondition,
	Message: "single sign-on needs OPENLOG_PUBLIC_URL (the redirect and SAML URLs are derived from it)"}

// gate requires a signed-in user in an organization with at least role min.
func (s *Service) gate(p *auth.Principal, min auth.Role) error {
	if p == nil || p.Kind != auth.KindSession {
		return denied("this operation requires a signed-in user; API keys are read-only")
	}
	if !p.HasOrg() {
		return denied("you are not a member of any organization")
	}
	if !p.Role.AtLeast(min) {
		return denied("your role (" + string(p.Role) + ") does not allow this operation")
	}
	return nil
}

// ---- helpers ----

func hashToken(v string) []byte {
	h := sha256.Sum256([]byte(v))
	return h[:]
}

func emailDomain(email string) string {
	_, d, ok := strings.Cut(email, "@")
	if !ok {
		return ""
	}
	return strings.ToLower(d)
}

// audit writes an organization audit event without a principal (sign-in, SCIM); best effort.
func (s *Service) audit(ctx context.Context, orgID, actorID, actorEmail, ip, action, targetType, targetID string, details map[string]any) {
	e := &auth.AuditEvent{OrgID: orgID, ActorUserID: actorID, ActorEmail: actorEmail, Action: action, TargetType: targetType,
		TargetID: targetID, Details: details, IP: ip, CreatedAt: s.now()}
	if err := s.users.AddAuditEvent(context.WithoutCancel(ctx), e); err != nil {
		s.log.Error("cannot write audit log", "action", action, "org_id", orgID, "err", err)
	}
}

// Audit writes an organization audit event for SCIM (internal/scim).
func (s *Service) Audit(ctx context.Context, orgID, actor, ip, action, targetType, targetID string, details map[string]any) {
	s.audit(ctx, orgID, "", actor, ip, action, targetType, targetID, details)
}

// validRedirect returns target when it is a same-origin relative UI path, else "/".
func validRedirect(target string) string {
	if target == "" || len(target) > 2048 || !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") ||
		strings.ContainsAny(target, "\\\r\n\t") || strings.HasPrefix(target, "/api/") {
		return "/"
	}
	return target
}

// roleFor maps groups to the highest mapped role; ok=false when no mapping matches.
func roleFor(ms []RoleMapping, groups []string) (auth.Role, bool) {
	set := map[string]bool{}
	for _, g := range groups {
		set[strings.TrimSpace(g)] = true
	}
	var best auth.Role
	for _, m := range ms {
		if set[m.Group] && (best == "" || m.Role.AtLeast(best)) {
			best = m.Role
		}
	}
	return best, best != ""
}

// RoleForGroups returns the role the organization-wide mappings give groups, or def when none matches
// (internal/scim).
func (s *Service) RoleForGroups(ctx context.Context, orgID string, groups []string, def auth.Role) (auth.Role, error) {
	ms, err := s.store.ListRoleMappings(ctx, orgID, "")
	if err != nil {
		return "", s.fail(err)
	}
	if r, ok := roleFor(ms, groups); ok {
		return r, nil
	}
	return def, nil
}
