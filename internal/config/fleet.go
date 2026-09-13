package config

import (
	"errors"
	"fmt"
	"net/url"
	"time"
)

// Fleet holds the agent fleet update variables (ingest and api). The release index URL and the
// trusted keys file are shared with the update check (UpdateCheck.IndexURL, UpdateCheck.TrustedKeysFile).
type Fleet struct {
	// ReleaseMirrorDir is a local mirror: <dir>/index.json(.sig), <dir>/v<version>/<files>.
	ReleaseMirrorDir string
	// ReleaseServeMirror makes sync answers point download_url at ingest's mirror endpoint.
	ReleaseServeMirror bool
	// ReleaseMirrorBaseURL is the external ingest URL used in mirror links (empty = request host).
	ReleaseMirrorBaseURL string
	// CatalogRefresh is how often the release index is re-checked.
	CatalogRefresh time.Duration
	// SyncInterval is poll_interval_seconds sent to agents.
	SyncInterval time.Duration
	// RolloutSyncInterval is poll_interval_seconds for agents waiting for a later wave of an active rollout.
	RolloutSyncInterval time.Duration
	// PolicyCacheTTL bounds how long ingest serves a cached policy/rollout (≤ 30s).
	PolicyCacheTTL time.Duration
	// ControllerInterval is the rollout controller period (api leader).
	ControllerInterval time.Duration
	// HostStaleAfter excludes agents that did not sync for this long from rollouts and the summary.
	HostStaleAfter time.Duration
	// ReportQueueSize bounds sync reports queued in ingest for PostgreSQL.
	ReportQueueSize int
}

func loadFleet(p *parser) Fleet {
	return Fleet{
		ReleaseMirrorDir:     p.str("OPENLOG_RELEASE_MIRROR_DIR", ""),
		ReleaseServeMirror:   p.bool("OPENLOG_RELEASE_SERVE_MIRROR", false),
		ReleaseMirrorBaseURL: p.str("OPENLOG_RELEASE_MIRROR_BASE_URL", ""),
		CatalogRefresh:       p.duration("OPENLOG_RELEASE_CATALOG_REFRESH", 15*time.Minute),
		SyncInterval:         p.duration("OPENLOG_FLEET_SYNC_INTERVAL", 300*time.Second),
		RolloutSyncInterval:  p.duration("OPENLOG_FLEET_ROLLOUT_SYNC_INTERVAL", 60*time.Second),
		PolicyCacheTTL:       p.duration("OPENLOG_FLEET_POLICY_CACHE_TTL", 15*time.Second),
		ControllerInterval:   p.duration("OPENLOG_FLEET_CONTROLLER_INTERVAL", 30*time.Second),
		HostStaleAfter:       p.duration("OPENLOG_FLEET_HOST_STALE_AFTER", 24*time.Hour),
		ReportQueueSize:      int(p.int64("OPENLOG_FLEET_REPORT_QUEUE_SIZE", 10000)),
	}
}

func (f Fleet) validate() []error {
	var errs []error
	if f.ReleaseServeMirror && f.ReleaseMirrorDir == "" {
		errs = append(errs, errors.New("OPENLOG_RELEASE_SERVE_MIRROR=true requires OPENLOG_RELEASE_MIRROR_DIR"))
	}
	if f.ReleaseMirrorBaseURL != "" {
		if u, err := url.Parse(f.ReleaseMirrorBaseURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			errs = append(errs, fmt.Errorf("OPENLOG_RELEASE_MIRROR_BASE_URL: must be an http(s) URL, got %q", f.ReleaseMirrorBaseURL))
		}
	}
	if f.CatalogRefresh < 10*time.Second {
		errs = append(errs, errors.New("OPENLOG_RELEASE_CATALOG_REFRESH must be >= 10s"))
	}
	if f.SyncInterval < time.Minute || f.SyncInterval > time.Hour {
		errs = append(errs, errors.New("OPENLOG_FLEET_SYNC_INTERVAL must be between 1m and 1h (agents clamp to that range)"))
	}
	if f.RolloutSyncInterval < time.Minute || f.RolloutSyncInterval > time.Hour {
		errs = append(errs, errors.New("OPENLOG_FLEET_ROLLOUT_SYNC_INTERVAL must be between 1m and 1h (agents clamp to that range)"))
	}
	if f.PolicyCacheTTL <= 0 || f.PolicyCacheTTL > 30*time.Second {
		errs = append(errs, errors.New("OPENLOG_FLEET_POLICY_CACHE_TTL must be > 0 and <= 30s"))
	}
	if f.ControllerInterval < time.Second {
		errs = append(errs, errors.New("OPENLOG_FLEET_CONTROLLER_INTERVAL must be >= 1s"))
	}
	if f.HostStaleAfter <= 0 {
		errs = append(errs, errors.New("OPENLOG_FLEET_HOST_STALE_AFTER must be > 0"))
	}
	if f.ReportQueueSize <= 0 {
		errs = append(errs, errors.New("OPENLOG_FLEET_REPORT_QUEUE_SIZE must be > 0"))
	}
	return errs
}
