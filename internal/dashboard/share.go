package dashboard

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Share links and the organization's dashboard sharing settings (migrations/postgres/0054_dashboard_shares.sql,
// api.md "Dashboards" › "Share links", D-087).

// ErrSharingDisabled is returned when share links are not enabled for the organization.
var ErrSharingDisabled = errors.New("share links are disabled for this organization")

// Share link limits.
const (
	MaxSharesPerDashboard = 20
	MinShareTTL           = 5 * time.Minute
	MaxShareTTL           = 90 * 24 * time.Hour
	MaxShareRange         = 31 * 24 * time.Hour
	MaxReportDomains      = 20
	shareTokenPrefix      = "olds_"
	shareTokenLen         = len(shareTokenPrefix) + 43 // 32 random bytes, base64url without padding
)

var (
	relativeRangeRe = regexp.MustCompile(`^([1-9][0-9]{0,3})([mhd])$`)
	domainRe        = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
)

// Settings are the dashboard sharing settings of an organization.
type Settings struct {
	SharesEnabled bool
	ReportDomains []string
	UpdatedBy     string
	UpdatedAt     time.Time // zero: never changed (defaults)
}

// ShareInput creates a share link.
type ShareInput struct {
	Label     string              `json:"label"`
	ExpiresAt time.Time           `json:"expires_at"`
	Range     string              `json:"range"`
	From      *time.Time          `json:"from"`
	To        *time.Time          `json:"to"`
	Variables map[string][]string `json:"variables"`
}

// Share is a read-only share link of a dashboard (the token itself is never stored).
type Share struct {
	ID             string
	OrgID          string
	DashboardID    string
	Label          string
	Range          string // relative range; "" = From/To
	From, To       time.Time
	Variables      map[string][]string
	CreatedBy      string
	CreatedByEmail string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	RevokedAt      *time.Time
	LastUsedAt     *time.Time
	UseCount       int64
}

// Active reports whether the link can be used at now.
func (s *Share) Active(now time.Time) bool { return s.RevokedAt == nil && now.Before(s.ExpiresAt) }

// TimeRange returns the query range of the share at now.
func (s *Share) TimeRange(now time.Time) (time.Time, time.Time) {
	if s.Range == "" {
		return s.From, s.To
	}
	d, _ := ParseRelativeRange(s.Range)
	return now.Add(-d), now
}

// SharedDashboard is a resolved share link.
type SharedDashboard struct {
	Share     *Share
	Dashboard *Dashboard
	TenantID  string
	// Audit is true when the link was not used in the last hour (the access is audited at most hourly).
	Audit bool
}

// ShareStore persists sharing settings and share links (PGStore; optional for other stores).
type ShareStore interface {
	GetSettings(ctx context.Context, orgID string) (Settings, error)
	PutSettings(ctx context.Context, orgID string, s Settings) error
	ListShares(ctx context.Context, orgID, dashboardID string) ([]Share, error)
	// CreateShare inserts sh unless the dashboard already has maxActive active links (invalid argument).
	CreateShare(ctx context.Context, sh *Share, tokenHash []byte, maxActive int) error
	// RevokeShare marks a link revoked (idempotent) and returns it.
	RevokeShare(ctx context.Context, orgID, dashboardID, shareID, userID string) (*Share, error)
	// ResolveShare returns the active link of a token hash and the organization's tenant when sharing is enabled for
	// the organization and the link's creator is still a member; it records the use and reports whether the previous
	// use was more than an hour ago. ErrNotFound otherwise.
	ResolveShare(ctx context.Context, tokenHash []byte, now time.Time) (*Share, string, bool, error)
}

// ParseRelativeRange parses "15m", "24h", "7d" (1 minute to 31 days).
func ParseRelativeRange(v string) (time.Duration, bool) {
	m := relativeRangeRe.FindStringSubmatch(v)
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	unit := map[string]time.Duration{"m": time.Minute, "h": time.Hour, "d": 24 * time.Hour}[m[2]]
	d := time.Duration(n) * unit
	return d, d >= time.Minute && d <= MaxShareRange
}

// NewShareToken returns a random share token.
func NewShareToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return shareTokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// HashShareToken returns the stored hash of a token, or false for strings that cannot be share tokens.
func HashShareToken(token string) ([]byte, bool) {
	if len(token) != shareTokenLen || !strings.HasPrefix(token, shareTokenPrefix) {
		return nil, false
	}
	if _, err := base64.RawURLEncoding.DecodeString(token[len(shareTokenPrefix):]); err != nil {
		return nil, false
	}
	sum := sha256.Sum256([]byte(token))
	return sum[:], true
}

func (m *Manager) shareStore() (ShareStore, error) {
	ss, ok := m.store.(ShareStore)
	if !ok {
		return nil, ErrNotFound
	}
	return ss, nil
}

// Settings returns the sharing settings of an organization (defaults when never changed).
func (m *Manager) Settings(ctx context.Context, orgID string) (Settings, error) {
	ss, err := m.shareStore()
	if err != nil {
		return Settings{}, err
	}
	return ss.GetSettings(ctx, orgID)
}

// UpdateSettings replaces the sharing settings (signed-in admins and owners).
func (m *Manager) UpdateSettings(ctx context.Context, orgID string, in Settings, v Viewer) (Settings, error) {
	ss, err := m.shareStore()
	if err != nil {
		return Settings{}, err
	}
	if !v.CanWrite || !v.Admin || v.UserID == "" {
		return Settings{}, ErrForbidden
	}
	if len(in.ReportDomains) > MaxReportDomains {
		return Settings{}, invalid("report_domains: at most %d domains", MaxReportDomains)
	}
	domains := []string{}
	for i, d := range in.ReportDomains {
		d = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(d), "@")))
		if len(d) > 253 || !domainRe.MatchString(d) {
			return Settings{}, invalid("report_domains[%d] must be a domain name such as example.com", i)
		}
		if !slices.Contains(domains, d) {
			domains = append(domains, d)
		}
	}
	out := Settings{SharesEnabled: in.SharesEnabled, ReportDomains: domains, UpdatedBy: v.UserID, UpdatedAt: m.now()}
	if err := ss.PutSettings(ctx, orgID, out); err != nil {
		return Settings{}, err
	}
	return ss.GetSettings(ctx, orgID)
}

// canManageShares: editors of the dashboard, and admins for dashboards they can read.
func (v Viewer) canManageShares(d *Dashboard) bool {
	return v.CanWrite && v.UserID != "" && (v.CanEdit(d) || (v.Admin && v.CanRead(d)))
}

// Shares lists the share links of a dashboard (active and inactive), newest first.
func (m *Manager) Shares(ctx context.Context, orgID, id string, v Viewer) ([]Share, error) {
	ss, err := m.shareStore()
	if err != nil {
		return nil, err
	}
	d, err := m.Get(ctx, orgID, id, v)
	if err != nil {
		return nil, err
	}
	if !v.canManageShares(d) {
		return nil, ErrForbidden
	}
	return ss.ListShares(ctx, orgID, d.ID)
}

// CreateShare creates a share link and returns it with its token (shown once).
func (m *Manager) CreateShare(ctx context.Context, orgID, id string, in ShareInput, v Viewer) (*Share, string, error) {
	ss, err := m.shareStore()
	if err != nil {
		return nil, "", err
	}
	d, err := m.Get(ctx, orgID, id, v)
	if err != nil {
		return nil, "", err
	}
	if !v.CanEdit(d) {
		return nil, "", ErrForbidden
	}
	settings, err := ss.GetSettings(ctx, orgID)
	if err != nil {
		return nil, "", err
	}
	if !settings.SharesEnabled {
		return nil, "", ErrSharingDisabled
	}
	now := m.now().UTC()
	sh := &Share{ID: m.newID(), OrgID: orgID, DashboardID: d.ID, CreatedBy: v.UserID, CreatedAt: now}
	if sh.Label, err = checkText("label", in.Label, 0, 100); err != nil {
		return nil, "", err
	}
	if strings.Contains(sh.Label, "\n") {
		return nil, "", invalid("label must be a single line")
	}
	switch {
	case in.ExpiresAt.IsZero():
		return nil, "", invalid("expires_at is required")
	case in.ExpiresAt.Before(now.Add(MinShareTTL)) || in.ExpiresAt.After(now.Add(MaxShareTTL)):
		return nil, "", invalid("expires_at must be between 5 minutes and 90 days from now")
	}
	sh.ExpiresAt = in.ExpiresAt.UTC()
	switch {
	case in.Range != "" && (in.From != nil || in.To != nil):
		return nil, "", invalid("give either range or from and to")
	case in.Range != "":
		if _, ok := ParseRelativeRange(in.Range); !ok {
			return nil, "", invalid("range must be a relative range such as 15m, 24h or 7d (at most 31 days)")
		}
		sh.Range = in.Range
	case in.From != nil && in.To != nil:
		if !in.From.Before(*in.To) || in.To.Sub(*in.From) > MaxShareRange {
			return nil, "", invalid("from must be before to, at most 31 days apart")
		}
		sh.From, sh.To = in.From.UTC(), in.To.UTC()
	default:
		return nil, "", invalid("range or from and to is required")
	}
	if sh.Variables, err = lockedVariables(d, in.Variables); err != nil {
		return nil, "", err
	}
	token, err := NewShareToken()
	if err != nil {
		return nil, "", err
	}
	hash, _ := HashShareToken(token)
	if err := ss.CreateShare(ctx, sh, hash, MaxSharesPerDashboard); err != nil {
		return nil, "", err
	}
	return sh, token, nil
}

// lockedVariables validates variable values against the dashboard's variables ("*" and empty selections = All).
func lockedVariables(d *Dashboard, in map[string][]string) (map[string][]string, error) {
	out := map[string][]string{}
	for name, vals := range in {
		var def *Variable
		for i := range d.Variables {
			if d.Variables[i].Name == name {
				def = &d.Variables[i]
			}
		}
		if def == nil {
			return nil, invalid("variables: the dashboard has no variable %q", truncate(name, 64))
		}
		if len(vals) > MaxVariableValues {
			return nil, invalid("variables.%s: at most %d values", name, MaxVariableValues)
		}
		clean := []string{}
		for _, s := range vals {
			if len(s) > 1024 || strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
				return nil, invalid("variables.%s: values must be at most 1024 bytes without control characters", name)
			}
			if s == "*" {
				clean = nil
				break
			}
			clean = append(clean, s)
		}
		if len(clean) == 0 {
			continue
		}
		if !def.Multi && len(clean) > 1 {
			return nil, invalid("variables.%s takes a single value", name)
		}
		out[name] = clean
	}
	return out, nil
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// RevokeShare revokes a share link.
func (m *Manager) RevokeShare(ctx context.Context, orgID, id, shareID string, v Viewer) (*Share, error) {
	ss, err := m.shareStore()
	if err != nil {
		return nil, err
	}
	d, err := m.Get(ctx, orgID, id, v)
	if err != nil {
		return nil, err
	}
	if !v.canManageShares(d) {
		return nil, ErrForbidden
	}
	if _, ok := canonicalID(shareID); !ok {
		return nil, ErrNotFound
	}
	return ss.RevokeShare(ctx, orgID, d.ID, shareID, v.UserID)
}

// OpenShare resolves a share token (ErrNotFound for unknown, expired or revoked links, disabled sharing, a creator
// who left the organization or a deleted dashboard).
func (m *Manager) OpenShare(ctx context.Context, token string) (*SharedDashboard, error) {
	ss, err := m.shareStore()
	if err != nil {
		return nil, err
	}
	hash, ok := HashShareToken(token)
	if !ok {
		return nil, ErrNotFound
	}
	sh, tenant, audit, err := ss.ResolveShare(ctx, hash, m.now().UTC())
	if err != nil {
		return nil, err
	}
	d, err := m.store.Get(ctx, sh.OrgID, sh.DashboardID)
	if err != nil {
		return nil, err
	}
	return &SharedDashboard{Share: sh, Dashboard: d, TenantID: tenant, Audit: audit}, nil
}
