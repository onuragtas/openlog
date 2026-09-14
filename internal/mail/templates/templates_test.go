package templates

import (
	"strings"
	"testing"
	"time"
)

func TestFromAcceptLanguage(t *testing.T) {
	for header, want := range map[string]string{
		"":                           "",
		"tr-TR,tr;q=0.9,en-US;q=0.8": "tr",
		"en-US,en;q=0.9,tr;q=0.8":    "en",
		"de-DE,de;q=0.9,tr;q=0.5":    "tr",
		"de, fr;q=0.8":               "",
		"en;q=0.2, tr;q=0.7":         "tr",
		"tr;q=0, en":                 "en",
		"TR":                         "tr",
		"en;q=abc, tr":               "tr",
		"*":                          "",
	} {
		if got := FromAcceptLanguage(header); got != want {
			t.Errorf("FromAcceptLanguage(%q) = %q, want %q", header, got, want)
		}
	}
	for in, want := range map[string]string{"": "en", "tr-TR": "tr", "tr_TR": "tr", "EN": "en", "de": "en"} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every template renders in every language with non-empty subject, text and HTML, and the HTML escapes data.
func TestAllTemplates(t *testing.T) {
	exp := time.Date(2026, 9, 20, 12, 30, 0, 0, time.UTC)
	for _, lang := range Supported {
		msgs := map[string]Message{
			NameInvitation:         Invitation(lang, InvitationData{Inviter: "a@x.test", OrgName: `Acme <script>`, Role: "admin", ExpiresAt: exp, Link: "https://o.test/invite#token=oli_1"}),
			NameVerification:       Verification(lang, VerificationData{ExpiresAt: exp, Link: "https://o.test/verify-email#token=olv_1"}),
			NameDomainVerification: DomainVerification(lang, DomainVerificationData{Requester: "a@x.test", Domain: "acme.test", OrgName: "Acme", Link: "https://o.test/sso/verify-domain#token=d"}),
			NameUsage: Usage(lang, UsageData{OrgName: "Acme", TenantID: "acme", Metric: "ingest_bytes", Used: "85.00 GiB", Limit: "100.00 GiB",
				Percent: 85, Threshold: 80, PlanName: "Free", Period: "2026-09", PeriodEnd: "2026-10-01", Link: "https://o.test/settings/usage"}),
		}
		for name, m := range msgs {
			if m.Subject == "" || strings.ContainsAny(m.Subject, "\r\n") || !strings.HasPrefix(m.Subject, "[openlog] ") {
				t.Errorf("%s.%s subject %q", name, lang, m.Subject)
			}
			if !strings.HasSuffix(m.Text, "\r\n") || strings.Contains(strings.ReplaceAll(m.Text, "\r\n", ""), "\n") || strings.Contains(m.Text, "<no value>") {
				t.Errorf("%s.%s text %q", name, lang, m.Text)
			}
			if !strings.Contains(m.HTML, `<html lang="`+lang+`">`) || strings.Contains(m.HTML, "<script>") || strings.Contains(m.HTML, "<no value>") {
				t.Errorf("%s.%s html %q", name, lang, m.HTML)
			}
		}
		inv := msgs[NameInvitation]
		if !strings.Contains(inv.Text, "https://o.test/invite#token=oli_1") || !strings.Contains(inv.Text, "2026-09-20 12:30 UTC") ||
			!strings.Contains(inv.HTML, `href="https://o.test/invite#token=oli_1"`) || !strings.Contains(inv.HTML, "Acme &lt;script&gt;") {
			t.Errorf("invitation.%s: %q / %q", lang, inv.Text, inv.HTML)
		}
	}

	tr := Invitation("tr-TR", InvitationData{OrgName: "Acme", Role: "viewer", ExpiresAt: exp, Link: "l"})
	if tr.Subject != "[openlog] Acme organizasyonuna davet edildiniz" || !strings.Contains(tr.Text, "Bir yönetici") || !strings.Contains(tr.Text, "izleyici rolüyle") {
		t.Errorf("turkish invitation: %+v", tr)
	}
	en := Invitation("de", InvitationData{OrgName: "Acme", Role: "viewer", ExpiresAt: exp, Link: "l"})
	if en.Subject != "[openlog] You are invited to Acme" || !strings.Contains(en.Text, "An administrator invited you") {
		t.Errorf("fallback invitation: %+v", en)
	}

	hard := Usage("en", UsageData{OrgName: "Acme", Metric: "ingest_bytes", Threshold: 100, Exceeded: true, HardBlock: true, BlockPercent: 110, GracePercent: 10, PeriodEnd: "2026-10-01"})
	if !strings.Contains(hard.Text, "New data is rejected once 110% of the limit is used (grace 10%)") || strings.Contains(hard.Text, "Usage details") {
		t.Errorf("hard usage: %q", hard.Text)
	}
	trUsage := Usage("tr", UsageData{OrgName: "Acme", Metric: "hosts", Threshold: 100, Exceeded: true, PlanName: "Free"})
	if !strings.Contains(trUsage.Subject, "Host sayısı") || !strings.Contains(trUsage.Text, "Limit aşıldı.") {
		t.Errorf("turkish usage: %+v", trUsage)
	}
}
