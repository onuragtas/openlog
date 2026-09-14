package memstore_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/auth/authtest"
)

// E-mail language (internal/mail/templates): Accept-Language of the request that creates the verification or the
// invitation; an invitation keeps its language when it is resent.
func TestEmailLanguage(t *testing.T) {
	e, st, m := saasEnv(t, nil)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Accept-Language", "tr-TR,tr;q=0.9,en;q=0.5")
	if got := e.Svc.Meta(r).Locale; got != "tr" {
		t.Fatalf("Meta locale = %q", got)
	}
	tr, en := e.Meta, e.Meta
	tr.Locale, en.Locale = "tr", "en"

	res, _, err := e.Svc.Signup(bg, auth.SignupInput{Email: e.Email("dil"), Password: authtest.Password, OrgName: "Dil"}, tr)
	if err != nil {
		t.Fatal(err)
	}
	if mail := m.last(t); mail.Subject != "[openlog] E-posta adresinizi doğrulayın" || !strings.Contains(mail.HTML, `lang="tr"`) ||
		!strings.Contains(mail.Text, "#token=olv_") {
		t.Errorf("verification e-mail: %+v", mail)
	}
	if u, err := st.GetUser(bg, res.Session.UserID); err != nil || u.Locale != "tr" {
		t.Errorf("user locale %q (%v)", u.Locale, err)
	}

	_, owner := e.Bootstrap("dilinv")
	inv, _, err := e.Svc.CreateInvitation(bg, owner.P, e.Email("yeni"), auth.RoleMember, tr)
	if err != nil {
		t.Fatal(err)
	}
	if mail := m.last(t); !strings.HasSuffix(mail.Subject, "organizasyonuna davet edildiniz") || !strings.Contains(mail.Text, "üye rolüyle") {
		t.Errorf("invitation e-mail: %+v", mail)
	}
	if _, _, _, err := e.Svc.ResendInvitation(bg, owner.P, inv.ID, en); err != nil {
		t.Fatal(err)
	}
	if mail := m.last(t); !strings.HasSuffix(mail.Subject, "organizasyonuna davet edildiniz") {
		t.Errorf("resent invitation lost its language: %+v", mail)
	}

	// No supported language: English.
	if _, _, err := e.Svc.CreateInvitation(bg, owner.P, e.Email("eng"), auth.RoleViewer, e.Meta); err != nil {
		t.Fatal(err)
	}
	if mail := m.last(t); !strings.HasPrefix(mail.Subject, "[openlog] You are invited to ") || !strings.Contains(mail.HTML, `lang="en"`) {
		t.Errorf("default e-mail: %+v", mail)
	}
}
