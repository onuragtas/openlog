package config

import (
	"strings"
	"testing"
)

func TestStorageConfig(t *testing.T) {
	c, err := Load(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	s := c.Storage
	if s.TieringEnabled || s.Policy != DefaultStoragePolicy || s.Tiers["metrics"].ColdAfterDays != 7 || s.Tiers["logs"].ColdAfterDays != 3 {
		t.Fatalf("defaults %+v", s)
	}
	if s.Tier("metrics") != (StorageTier{}) {
		t.Error("Tier must be zero while tiering is disabled")
	}

	env := map[string]string{
		"OPENLOG_STORAGE_TIERING_ENABLED":         "true",
		"OPENLOG_STORAGE_POLICY":                  "my_policy",
		"OPENLOG_STORAGE_COLD_AFTER_DAYS_METRICS": "10",
		"OPENLOG_STORAGE_WARM_AFTER_DAYS_METRICS": "2",
		"OPENLOG_STORAGE_COLD_AFTER_DAYS_TRACES":  "0",
	}
	c, err = Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Storage.Tier("metrics"); got != (StorageTier{WarmAfterDays: 2, ColdAfterDays: 10}) {
		t.Errorf("metrics tier %+v", got)
	}
	if got := c.Storage.Tier("traces"); got != (StorageTier{}) {
		t.Errorf("traces tier %+v", got)
	}
	if c.Storage.Policy != "my_policy" {
		t.Errorf("policy %q", c.Storage.Policy)
	}

	for _, bad := range []struct{ env map[string]string }{
		{map[string]string{"OPENLOG_STORAGE_POLICY": "x'y"}},
		{map[string]string{"OPENLOG_STORAGE_COLD_AFTER_DAYS_LOGS": "-1"}},
		{map[string]string{"OPENLOG_STORAGE_WARM_AFTER_DAYS_APM": "3651"}},
		{map[string]string{"OPENLOG_STORAGE_WARM_AFTER_DAYS_ALERTS": "7", "OPENLOG_STORAGE_COLD_AFTER_DAYS_ALERTS": "7"}},
		{map[string]string{"OPENLOG_STORAGE_COLD_AFTER_DAYS_METRICS_1M": "many"}},
	} {
		_, err := Load(func(k string) string { return bad.env[k] })
		if err == nil || !strings.Contains(err.Error(), "OPENLOG_STORAGE_") {
			t.Errorf("%v: err %v", bad.env, err)
		}
	}
}
