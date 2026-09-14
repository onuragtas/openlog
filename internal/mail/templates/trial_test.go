package templates

import (
	"strings"
	"testing"
	"time"
)

func TestTrialTemplates(t *testing.T) {
	ends := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	for _, lang := range Supported {
		for _, d := range []TrialData{
			{OrgName: "Acme <b>", TenantID: "acme", PlanName: "Pro", FallbackPlanName: "Free", DaysLeft: 3, EndsAt: ends, Link: "https://o.test/settings/usage"},
			{OrgName: "Acme", TenantID: "acme", PlanName: "Pro", FallbackPlanName: "Free", EndsAt: ends, Ended: true},
		} {
			m := Trial(lang, d)
			if !strings.HasPrefix(m.Subject, "[openlog] ") || strings.Contains(m.Text, "<no value>") || !strings.Contains(m.Text, "Free") ||
				!strings.Contains(m.Text, "2026-09-28 09:00 UTC") || strings.Contains(m.HTML, "<b>") || !strings.Contains(m.HTML, `<html lang="`+lang+`">`) {
				t.Errorf("trial.%s %+v: %+v", lang, d, m)
			}
		}
	}
	if m := Trial("en", TrialData{OrgName: "Acme", PlanName: "Pro", DaysLeft: 1, EndsAt: ends}); m.Subject != "[openlog] Acme: your Pro trial ends in 1 day" {
		t.Errorf("subject %q", m.Subject)
	}
	if m := Trial("tr", TrialData{OrgName: "Acme", PlanName: "Pro", Ended: true, EndsAt: ends}); !strings.Contains(m.Subject, "sona erdi") {
		t.Errorf("tr subject %q", m.Subject)
	}
}
