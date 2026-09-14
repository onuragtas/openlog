package templates

import "time"

// NameTrial is the trial ending / ended e-mail (SaaS mode, D-106).
const NameTrial = "trial"

// TrialData is the owner notification of a trial that ends in DaysLeft days or has Ended.
type TrialData struct {
	OrgName          string
	TenantID         string
	PlanName         string
	FallbackPlanName string
	DaysLeft         int
	EndsAt           time.Time
	Ended            bool
	Link             string // usage page; empty without OPENLOG_PUBLIC_URL
}

// Trial renders the trial e-mail.
func Trial(locale string, d TrialData) Message {
	return must(Render(NameTrial, locale, d))
}
