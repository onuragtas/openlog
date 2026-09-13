package config

import (
	"strings"
	"testing"
	"time"
)

func loadWith(env map[string]string) (Config, error) {
	return Load(func(k string) string { return env[k] })
}

func TestFleetDefaults(t *testing.T) {
	c, err := loadWith(nil)
	if err != nil {
		t.Fatal(err)
	}
	f := c.Fleet
	if f.SyncInterval != 5*time.Minute || f.PolicyCacheTTL != 15*time.Second || f.CatalogRefresh != 15*time.Minute ||
		f.ControllerInterval != 30*time.Second || f.HostStaleAfter != 24*time.Hour || f.ReportQueueSize != 10000 ||
		f.ReleaseMirrorDir != "" || f.ReleaseServeMirror {
		t.Errorf("defaults = %+v", f)
	}
}

func TestFleetValidation(t *testing.T) {
	cases := map[string]struct {
		env  map[string]string
		want string
	}{
		"serve mirror without dir": {map[string]string{"OPENLOG_RELEASE_SERVE_MIRROR": "true"}, "requires OPENLOG_RELEASE_MIRROR_DIR"},
		"cache ttl too long":       {map[string]string{"OPENLOG_FLEET_POLICY_CACHE_TTL": "31s"}, "OPENLOG_FLEET_POLICY_CACHE_TTL"},
		"sync interval too short":  {map[string]string{"OPENLOG_FLEET_SYNC_INTERVAL": "30s"}, "OPENLOG_FLEET_SYNC_INTERVAL"},
		"refresh too short":        {map[string]string{"OPENLOG_RELEASE_CATALOG_REFRESH": "1s"}, "OPENLOG_RELEASE_CATALOG_REFRESH"},
		"bad mirror base url":      {map[string]string{"OPENLOG_RELEASE_MIRROR_BASE_URL": "ingest:4318"}, "OPENLOG_RELEASE_MIRROR_BASE_URL"},
		"bad queue size":           {map[string]string{"OPENLOG_FLEET_REPORT_QUEUE_SIZE": "0"}, "OPENLOG_FLEET_REPORT_QUEUE_SIZE"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadWith(tc.env)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	c, err := loadWith(map[string]string{"OPENLOG_RELEASE_MIRROR_DIR": "/srv/releases", "OPENLOG_RELEASE_SERVE_MIRROR": "true",
		"OPENLOG_FLEET_SYNC_INTERVAL": "60s", "OPENLOG_FLEET_POLICY_CACHE_TTL": "30s"})
	if err != nil || !c.Fleet.ReleaseServeMirror || c.Fleet.SyncInterval != time.Minute {
		t.Fatalf("valid config: %+v %v", c.Fleet, err)
	}
}
