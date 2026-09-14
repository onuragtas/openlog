package config

import (
	"strings"
	"testing"
	"time"
)

func TestAPMRelinkDefaultsAndValidation(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	a := c.APM
	if !a.RelinkEnabled || a.RelinkAfter != 10*time.Minute || a.RelinkMaxAge != 7*24*time.Hour || a.RelinkInterval != 5*time.Minute || a.RelinkMaxMinutes != 120 {
		t.Errorf("relink defaults = %+v", a)
	}
	// RELINK_AFTER defaults to the link lookback.
	if c, err := Load(env(map[string]string{"OPENLOG_APM_LINK_LOOKBACK": "30m"})); err != nil || c.APM.RelinkAfter != 30*time.Minute {
		t.Errorf("after with lookback 30m: %v %v", c.APM.RelinkAfter, err)
	}
	for k, v := range map[string]string{
		"OPENLOG_APM_RELINK_AFTER":       "11m",  // > lookback 10m
		"OPENLOG_APM_RELINK_MAX_AGE":     "721h", // > 30 days
		"OPENLOG_APM_RELINK_INTERVAL":    "5s",
		"OPENLOG_APM_RELINK_MAX_MINUTES": "0",
	} {
		if _, err := Load(env(map[string]string{k: v})); err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("%s=%s: %v", k, v, err)
		}
	}
	// The max age is bounded by the APM retention.
	if _, err := Load(env(map[string]string{"OPENLOG_APM_RETENTION_DAYS": "3"})); err == nil || !strings.Contains(err.Error(), "OPENLOG_APM_RELINK_MAX_AGE") {
		t.Errorf("retention 3 days with max age 7 days: %v", err)
	}
	if _, err := Load(env(map[string]string{"OPENLOG_APM_RETENTION_DAYS": "3", "OPENLOG_APM_RELINK_MAX_AGE": "72h"})); err != nil {
		t.Errorf("retention 3 days with max age 72h: %v", err)
	}
}
