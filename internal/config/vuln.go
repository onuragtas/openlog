package config

import (
	"fmt"
	"strings"
	"time"
)

// Vuln configures vulnerability matching (openlog-api, D-142).
type Vuln struct {
	// Enabled offers /api/v1/vulnerabilities/* and runs the matcher on the api leader
	// (OPENLOG_VULN_ENABLED; postgres auth mode only).
	Enabled bool
	// FeedURL is where the OSV ecosystem exports are downloaded from (OPENLOG_VULN_FEED_URL). An empty
	// value disables the download: the installation fills the catalog itself, which is what an air-gapped
	// one does, and matching still runs against what is stored.
	FeedURL string
	// FeedTimeout bounds one ecosystem download (OPENLOG_VULN_FEED_TIMEOUT).
	FeedTimeout time.Duration
	// SyncInterval is how often the feed is downloaded (OPENLOG_VULN_SYNC_INTERVAL).
	SyncInterval time.Duration
	// MatchInterval is how often hosts are matched again (OPENLOG_VULN_MATCH_INTERVAL).
	MatchInterval time.Duration
}

func loadVuln(p *parser) Vuln {
	return Vuln{
		Enabled:       p.bool("OPENLOG_VULN_ENABLED", true),
		FeedURL:       strings.TrimSpace(p.str("OPENLOG_VULN_FEED_URL", "https://osv-vulnerabilities.storage.googleapis.com")),
		FeedTimeout:   p.duration("OPENLOG_VULN_FEED_TIMEOUT", 10*time.Minute),
		SyncInterval:  p.duration("OPENLOG_VULN_SYNC_INTERVAL", 12*time.Hour),
		MatchInterval: p.duration("OPENLOG_VULN_MATCH_INTERVAL", time.Hour),
	}
}

func (c Config) validateVuln() []error {
	var errs []error
	v := c.Vuln
	if v.FeedURL != "" && !strings.HasPrefix(v.FeedURL, "http://") && !strings.HasPrefix(v.FeedURL, "https://") {
		errs = append(errs, fmt.Errorf("OPENLOG_VULN_FEED_URL: must be an http(s) URL or empty, got %q", v.FeedURL))
	}
	if v.FeedTimeout < time.Minute || v.FeedTimeout > time.Hour {
		errs = append(errs, fmt.Errorf("OPENLOG_VULN_FEED_TIMEOUT: must be between 1m and 1h, got %s", v.FeedTimeout))
	}
	if v.SyncInterval < time.Hour || v.SyncInterval > 168*time.Hour {
		errs = append(errs, fmt.Errorf("OPENLOG_VULN_SYNC_INTERVAL: must be between 1h and 168h, got %s", v.SyncInterval))
	}
	if v.MatchInterval < 5*time.Minute || v.MatchInterval > 24*time.Hour {
		errs = append(errs, fmt.Errorf("OPENLOG_VULN_MATCH_INTERVAL: must be between 5m and 24h, got %s", v.MatchInterval))
	}
	return errs
}
