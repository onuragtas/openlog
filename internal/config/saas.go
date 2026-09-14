package config

import (
	"fmt"
	"sort"
	"strconv"
	"time"
)

// SaaS configures the operator console, organization lifecycle (suspension, trials), hard host/user limits and abuse
// detection (docs/operations/saas.md, D-105, D-106). Enforcement and the lifecycle jobs only run with
// OPENLOG_SAAS_MODE=true (Usage.SaaSMode).
type SaaS struct {
	// HostSyncInterval is how often the api leader writes tenant host limits and active hosts
	// (OPENLOG_SAAS_HOST_SYNC_INTERVAL).
	HostSyncInterval time.Duration
	// SignupTrialPlan starts a trial of this plan for organizations created by sign-up (OPENLOG_SAAS_SIGNUP_TRIAL_PLAN;
	// empty = no automatic trial). The plan must define trial_days.
	SignupTrialPlan string
	// TrialNotifyDays are the days before the trial end that e-mail owners (OPENLOG_SAAS_TRIAL_NOTIFY_DAYS).
	TrialNotifyDays []int
	// LifecycleInterval is the trial job interval (OPENLOG_SAAS_LIFECYCLE_INTERVAL).
	LifecycleInterval time.Duration
	// SupportSessionTTL bounds one operator support view (OPENLOG_SAAS_SUPPORT_SESSION_TTL).
	SupportSessionTTL time.Duration
	// AutoSuspend suspends flagged organizations automatically (OPENLOG_SAAS_AUTO_SUSPEND).
	AutoSuspend bool
	// AbuseInterval is the abuse detector interval (OPENLOG_SAAS_ABUSE_INTERVAL).
	AbuseInterval time.Duration
	// AbuseIngestMultiplier flags ingest of the last hour above this multiple of the plan's hourly share
	// (ingest_gb_month / 730) (OPENLOG_SAAS_ABUSE_INGEST_MULTIPLIER; 0 disables).
	AbuseIngestMultiplier float64
	// AbuseNewOrgDays is the age below which an organization counts as new (OPENLOG_SAAS_ABUSE_NEW_ORG_DAYS).
	AbuseNewOrgDays int
	// AbuseNewOrgHosts flags new organizations with more active hosts (OPENLOG_SAAS_ABUSE_NEW_ORG_HOSTS; 0 disables).
	AbuseNewOrgHosts int
	// AbuseSourceIPs flags tenants whose license keys were used from more distinct client addresses within an hour
	// (OPENLOG_SAAS_ABUSE_SOURCE_IPS; 0 disables).
	AbuseSourceIPs int
}

func loadSaaS(p *parser) SaaS {
	s := SaaS{
		HostSyncInterval:  p.duration("OPENLOG_SAAS_HOST_SYNC_INTERVAL", time.Minute),
		SignupTrialPlan:   p.str("OPENLOG_SAAS_SIGNUP_TRIAL_PLAN", ""),
		LifecycleInterval: p.duration("OPENLOG_SAAS_LIFECYCLE_INTERVAL", 5*time.Minute),
		SupportSessionTTL: p.duration("OPENLOG_SAAS_SUPPORT_SESSION_TTL", 2*time.Hour),
		AutoSuspend:       p.bool("OPENLOG_SAAS_AUTO_SUSPEND", false),
		AbuseInterval:     p.duration("OPENLOG_SAAS_ABUSE_INTERVAL", 10*time.Minute),
		AbuseNewOrgDays:   int(p.int64("OPENLOG_SAAS_ABUSE_NEW_ORG_DAYS", 7)),
		AbuseNewOrgHosts:  int(p.int64("OPENLOG_SAAS_ABUSE_NEW_ORG_HOSTS", 50)),
		AbuseSourceIPs:    int(p.int64("OPENLOG_SAAS_ABUSE_SOURCE_IPS", 200)),
	}
	if v, ok := p.raw("OPENLOG_SAAS_ABUSE_INGEST_MULTIPLIER"); ok {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			p.errs = append(p.errs, fmt.Errorf("OPENLOG_SAAS_ABUSE_INGEST_MULTIPLIER: invalid number %q", v))
		}
		s.AbuseIngestMultiplier = f
	} else {
		s.AbuseIngestMultiplier = 10
	}
	seen := map[int]bool{}
	for _, v := range p.list("OPENLOG_SAAS_TRIAL_NOTIFY_DAYS", "7,3,1") {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 90 {
			p.errs = append(p.errs, fmt.Errorf("OPENLOG_SAAS_TRIAL_NOTIFY_DAYS: %q must be a number of days between 1 and 90", v))
			continue
		}
		if !seen[n] {
			seen[n] = true
			s.TrialNotifyDays = append(s.TrialNotifyDays, n)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(s.TrialNotifyDays)))
	return s
}

func (c Config) validateSaaS() []error {
	var errs []error
	s := c.SaaS
	if s.HostSyncInterval < 10*time.Second || s.HostSyncInterval > time.Hour {
		errs = append(errs, fmt.Errorf("OPENLOG_SAAS_HOST_SYNC_INTERVAL: must be between 10s and 1h, got %s", s.HostSyncInterval))
	}
	if s.LifecycleInterval < 10*time.Second || s.LifecycleInterval > time.Hour {
		errs = append(errs, fmt.Errorf("OPENLOG_SAAS_LIFECYCLE_INTERVAL: must be between 10s and 1h, got %s", s.LifecycleInterval))
	}
	if s.SupportSessionTTL < 5*time.Minute || s.SupportSessionTTL > 24*time.Hour {
		errs = append(errs, fmt.Errorf("OPENLOG_SAAS_SUPPORT_SESSION_TTL: must be between 5m and 24h, got %s", s.SupportSessionTTL))
	}
	if s.AbuseInterval < time.Minute || s.AbuseInterval > 24*time.Hour {
		errs = append(errs, fmt.Errorf("OPENLOG_SAAS_ABUSE_INTERVAL: must be between 1m and 24h, got %s", s.AbuseInterval))
	}
	if s.AbuseIngestMultiplier < 0 || s.AbuseIngestMultiplier > 100000 {
		errs = append(errs, fmt.Errorf("OPENLOG_SAAS_ABUSE_INGEST_MULTIPLIER: must be between 0 and 100000, got %g", s.AbuseIngestMultiplier))
	}
	if s.AbuseNewOrgDays < 1 || s.AbuseNewOrgDays > 365 {
		errs = append(errs, fmt.Errorf("OPENLOG_SAAS_ABUSE_NEW_ORG_DAYS: must be between 1 and 365, got %d", s.AbuseNewOrgDays))
	}
	if s.AbuseNewOrgHosts < 0 || s.AbuseSourceIPs < 0 {
		errs = append(errs, fmt.Errorf("OPENLOG_SAAS_ABUSE_NEW_ORG_HOSTS and OPENLOG_SAAS_ABUSE_SOURCE_IPS must be >= 0"))
	}
	return errs
}
