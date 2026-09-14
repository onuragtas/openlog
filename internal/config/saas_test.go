package config

import (
	"strings"
	"testing"
	"time"
)

func TestSaaSDefaultsAndValidation(t *testing.T) {
	c, err := Load(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	s := c.SaaS
	if s.HostSyncInterval != time.Minute || s.AutoSuspend || s.AbuseIngestMultiplier != 10 || s.AbuseNewOrgHosts != 50 ||
		s.AbuseSourceIPs != 200 || len(s.TrialNotifyDays) != 3 || s.TrialNotifyDays[0] != 7 || s.SupportSessionTTL != 2*time.Hour {
		t.Errorf("defaults %+v", s)
	}
	env := map[string]string{"OPENLOG_SAAS_TRIAL_NOTIFY_DAYS": "1,14,3,3", "OPENLOG_SAAS_AUTO_SUSPEND": "true", "OPENLOG_SAAS_ABUSE_INGEST_MULTIPLIER": "2.5"}
	c, err = Load(func(n string) string { return env[n] })
	if err != nil {
		t.Fatal(err)
	}
	if got := c.SaaS.TrialNotifyDays; len(got) != 3 || got[0] != 14 || got[2] != 1 || !c.SaaS.AutoSuspend || c.SaaS.AbuseIngestMultiplier != 2.5 {
		t.Errorf("parsed %+v", c.SaaS)
	}
	for name, val := range map[string]string{
		"OPENLOG_SAAS_TRIAL_NOTIFY_DAYS":       "0",
		"OPENLOG_SAAS_HOST_SYNC_INTERVAL":      "1s",
		"OPENLOG_SAAS_SUPPORT_SESSION_TTL":     "48h",
		"OPENLOG_SAAS_ABUSE_INGEST_MULTIPLIER": "x",
		"OPENLOG_SAAS_ABUSE_NEW_ORG_DAYS":      "0",
	} {
		if _, err := Load(func(n string) string {
			if n == name {
				return val
			}
			return ""
		}); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s=%s: %v", name, val, err)
		}
	}
}
