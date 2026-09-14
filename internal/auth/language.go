package auth

import (
	"context"
	"strings"

	mailtemplates "github.com/onuragtas/openlog/internal/mail/templates"
)

// Language preferences (D-095, docs/contracts/api.md "E-mail language"): a user may choose the language of the web UI
// and of e-mails sent to them (users.locale with locale_explicit); owners and admins set the organization's default
// e-mail language (organizations.locale). E-mails use: user preference → organization default → stored request
// language → English.

// LanguageAuto is the API value of "no preference" (the browser's language).
const LanguageAuto = "auto"

// validLanguage reports whether l is a supported language code ("en", "tr").
func validLanguage(l string) bool {
	for _, s := range mailtemplates.Supported {
		if l == s {
			return true
		}
	}
	return false
}

func languageError(allowed string) error {
	return invalid("language must be one of %s, %s", allowed, strings.Join(mailtemplates.Supported, ", "))
}

// SetLanguage sets the signed-in user's language: "auto" (clears the preference; the request's Accept-Language is kept
// as the stored request language) or a supported language.
func (s *Service) SetLanguage(ctx context.Context, p *Principal, language string, meta ClientMeta) error {
	if err := requireSession(p); err != nil {
		return err
	}
	language = strings.ToLower(strings.TrimSpace(language))
	explicit := language != LanguageAuto
	locale := language
	if !explicit {
		locale = meta.Locale
	} else if !validLanguage(language) {
		return languageError(`"auto"`)
	}
	from := p.Language
	if from == "" {
		from = LanguageAuto
	}
	if err := s.store.SetUserLocale(ctx, p.UserID, locale, explicit); err != nil {
		return s.fail(err)
	}
	if explicit {
		p.Language = language
	} else {
		p.Language = ""
	}
	if from != language {
		s.audit(ctx, "", p.UserID, p.Email, meta, "user.language_change", "user", p.UserID, map[string]any{"from": from, "to": language})
	}
	return nil
}

// SetOrgLanguage sets the organization's default e-mail language (admin+): "" (none) or a supported language.
func (s *Service) SetOrgLanguage(ctx context.Context, p *Principal, language string, meta ClientMeta) (Organization, error) {
	if err := s.gate(p, ActUpdateOrg); err != nil {
		return Organization{}, err
	}
	language = strings.ToLower(strings.TrimSpace(language))
	if language != "" && !validLanguage(language) {
		return Organization{}, languageError(`""`)
	}
	cur, err := s.store.GetOrganization(ctx, p.OrgID)
	if err != nil {
		return Organization{}, s.fail(err)
	}
	if cur.Locale != language {
		if err := s.store.SetOrganizationLocale(ctx, p.OrgID, language); err != nil {
			return Organization{}, s.fail(err)
		}
		s.audit(ctx, p.OrgID, p.UserID, p.Email, meta, "org.language_change", "organization", p.OrgID,
			map[string]any{"from": cur.Locale, "to": language})
	}
	return s.CurrentOrg(ctx, p)
}

// mailLanguage returns the language of an e-mail to address sent in organization orgID ("" = none): the recipient's
// preference when the address belongs to a user, then the organization's default, then the stored request languages
// in order, English otherwise. Lookup failures fall through to the next source.
func (s *Service) mailLanguage(ctx context.Context, orgID, address string, requestLocales ...string) string {
	candidates := []string{}
	if address != "" {
		if u, err := s.store.GetUserByEmail(ctx, NormalizeEmail(address)); err == nil {
			candidates = append(candidates, u.Preference())
		}
	}
	if orgID != "" {
		if org, err := s.store.GetOrganization(ctx, orgID); err == nil {
			candidates = append(candidates, org.Locale)
		}
	}
	return mailtemplates.Resolve(append(candidates, requestLocales...)...)
}

// ---- member removal cleanup ----

// MemberCleanup is what was cleaned up with a removed membership (D-096).
type MemberCleanup struct {
	// ReportsUpdated are the ids of dashboard reports the member's address was removed from; ReportsDisabled those of
	// them that had no recipient left and were disabled.
	ReportsUpdated  []string
	ReportsDisabled []string
}

// MemberRemover is implemented by stores that remove organization data tied to a member (dashboard report recipients)
// in the same transaction as the membership (internal/store/postgres).
type MemberRemover interface {
	RemoveMemberWithCleanup(ctx context.Context, orgID, userID string) (MemberCleanup, error) // ErrLastOwner
}

// RemoveMembership removes the membership and, when st supports it, the member's report subscriptions (API member
// removal and SCIM deactivation). Errors are those of Store.RemoveMember.
func RemoveMembership(ctx context.Context, st Store, orgID, userID string) (MemberCleanup, error) {
	if mr, ok := st.(MemberRemover); ok {
		return mr.RemoveMemberWithCleanup(ctx, orgID, userID)
	}
	return MemberCleanup{}, st.RemoveMember(ctx, orgID, userID)
}

// Details adds the cleanup to audit details and returns whether anything was cleaned up.
func (c MemberCleanup) Details(details map[string]any) bool {
	if len(c.ReportsUpdated) == 0 {
		return false
	}
	details["reports"] = c.ReportsUpdated
	if len(c.ReportsDisabled) > 0 {
		details["disabled_reports"] = c.ReportsDisabled
	}
	return true
}
