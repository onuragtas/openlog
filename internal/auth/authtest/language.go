package authtest

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/onuragtas/openlog/internal/auth"
)

// captureMailer records e-mails.
type captureMailer struct {
	mu   sync.Mutex
	sent []auth.Mail
}

func (m *captureMailer) Send(_ context.Context, mail auth.Mail) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, mail)
	return nil
}

func (m *captureMailer) last(to string) (auth.Mail, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.sent) - 1; i >= 0; i-- {
		if m.sent[i].To == to {
			return m.sent[i], true
		}
	}
	return auth.Mail{}, false
}

// Turkish and English invitation e-mails share no subject; the Turkish one uses these words.
func isTurkish(m auth.Mail) bool {
	return strings.Contains(m.Text, "davet") || strings.Contains(m.Subject, "davet")
}

// language: user preference, organization default and the e-mail language precedence (D-095).
func language(t *testing.T, e *Env) {
	mailer := &captureMailer{}
	cfg := e.Svc.Config()
	cfg.Mailer, cfg.PublicURL = mailer, "https://openlog.example.com"
	e.Svc = auth.NewService(e.Store, cfg, nil)
	e.Svc.SetClock(e.Now)

	_, owner := e.Bootstrap("lang")
	if owner.P.Language != "" {
		t.Fatalf("new user has a language preference: %q", owner.P.Language)
	}

	// User preference: stored, visible on the next request, audited once; invalid values rejected.
	ok(t, e.Svc.SetLanguage(ctx, owner.P, "TR", e.Meta), "set tr")
	e.Refresh(owner)
	if owner.P.Language != "tr" {
		t.Fatalf("language after set = %q", owner.P.Language)
	}
	var ae *auth.Error
	if err := e.Svc.SetLanguage(ctx, owner.P, "de", e.Meta); !errors.As(err, &ae) || ae.Code != auth.CodeInvalidArgument {
		t.Fatalf("unsupported language: %v", err)
	}
	ok(t, e.Svc.SetLanguage(ctx, owner.P, "auto", auth.ClientMeta{IP: e.Meta.IP, Locale: "en"}), "set auto")
	e.Refresh(owner)
	if owner.P.Language != "" {
		t.Fatalf("language after auto = %q", owner.P.Language)
	}
	events, err := e.Svc.ListAuditEvents(ctx, owner.P, auth.AuditFilter{Action: "user.language_change"})
	if err == nil && len(events) > 0 {
		t.Fatalf("user-level language changes must not appear in the organization's audit log: %v", events)
	}

	// Organization default: admins and owners only.
	member := e.AddUser(owner, "lang-member", auth.RoleMember)
	if _, err := e.Svc.SetOrgLanguage(ctx, member.P, "tr", e.Meta); !errors.As(err, &ae) || ae.Code != auth.CodePermissionDenied {
		t.Fatalf("member sets organization language: %v", err)
	}
	if _, err := e.Svc.SetOrgLanguage(ctx, owner.P, "xx", e.Meta); !errors.As(err, &ae) || ae.Code != auth.CodeInvalidArgument {
		t.Fatalf("unsupported organization language: %v", err)
	}
	org, err := e.Svc.SetOrgLanguage(ctx, owner.P, "tr", e.Meta)
	ok(t, err, "set organization language")
	if org.Locale != "tr" {
		t.Fatalf("organization language = %q", org.Locale)
	}
	events, err = e.Svc.ListAuditEvents(ctx, owner.P, auth.AuditFilter{Action: "org.language_change"})
	ok(t, err, "audit")
	if len(events) != 1 || events[0].Details["to"] != "tr" {
		t.Fatalf("org.language_change audit = %+v", events)
	}

	// E-mail language: organization default over the inviter's request language …
	english := e.Meta
	english.Locale = "en"
	_, _, err = e.Svc.CreateInvitation(ctx, owner.P, e.Email("new-invitee"), auth.RoleViewer, english)
	ok(t, err, "invite")
	if m, sent := mailer.last(e.Email("new-invitee")); !sent || !isTurkish(m) {
		t.Fatalf("invitation with organization default tr: sent=%v subject=%q", sent, m.Subject)
	}
	// … and the recipient's own preference over the organization default.
	other, otherOwner := e.Bootstrap("lang-other")
	_ = other
	ok(t, e.Svc.SetLanguage(ctx, otherOwner.P, "en", e.Meta), "other owner prefers en")
	_, _, err = e.Svc.CreateInvitation(ctx, owner.P, e.Email("owner-lang-other"), auth.RoleViewer, english)
	ok(t, err, "invite existing user")
	if m, sent := mailer.last(e.Email("owner-lang-other")); !sent || isTurkish(m) {
		t.Fatalf("invitation to a user who prefers en: sent=%v subject=%q", sent, m.Subject)
	}
	// Without an organization default the stored request language applies.
	_, err = e.Svc.SetOrgLanguage(ctx, owner.P, "", e.Meta)
	ok(t, err, "clear organization language")
	turkish := e.Meta
	turkish.Locale = "tr"
	_, _, err = e.Svc.CreateInvitation(ctx, owner.P, e.Email("tr-invitee"), auth.RoleViewer, turkish)
	ok(t, err, "invite with tr request")
	if m, sent := mailer.last(e.Email("tr-invitee")); !sent || !isTurkish(m) {
		t.Fatalf("invitation with request language tr: sent=%v subject=%q", sent, m.Subject)
	}

	// API keys have no user preferences.
	_, key, err := e.Svc.CreateAPIKey(ctx, owner.P, "lang", nil, e.Meta)
	ok(t, err, "api key")
	kp, err := e.Auth(http.MethodGet, nil, WithBearer(key))
	ok(t, err, "api key auth")
	if err := e.Svc.SetLanguage(ctx, kp, "tr", e.Meta); err == nil {
		t.Fatal("API key changed a language preference")
	}
}
