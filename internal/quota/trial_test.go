package quota

import (
	"strings"
	"testing"
)

func TestCatalogTrials(t *testing.T) {
	c, err := ParseCatalog(`{"default":"free","plans":[{"id":"free"},{"id":"pro","trial_days":14},{"id":"team","trial_days":7,"trial_fallback_plan":"pro"}]}`, "")
	if err != nil {
		t.Fatal(err)
	}
	pro, _ := c.Plan("pro")
	team, _ := c.Plan("team")
	if pro.TrialDays != 14 || c.TrialFallback(pro) != "free" || c.TrialFallback(team) != "pro" {
		t.Errorf("trials: %+v %+v", pro, team)
	}
	for doc, want := range map[string]string{
		`{"plans":[{"id":"free","trial_days":14}]}`:                                       "trial fallback must be another plan",
		`{"plans":[{"id":"free"},{"id":"pro","trial_days":400}]}`:                         "trial_days",
		`{"plans":[{"id":"free"},{"id":"pro","trial_days":3,"trial_fallback_plan":"x"}]}`: "not defined",
	} {
		if _, err := ParseCatalog(doc, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v (want %q)", doc, err, want)
		}
	}
}
