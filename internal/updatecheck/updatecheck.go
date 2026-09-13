// Package updatecheck implements the self-hosted backend's release check
// (docs/contracts/releases-updates.md §5): the leader openlog-api pod fetches the signed release
// index at most once a day, verifies it and stores the newest release of the configured channel
// in PostgreSQL, so every pod can answer GET /api/v1/version without network access.
package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	lib "github.com/onuragtas/openlog/libs/release"

	"github.com/onuragtas/openlog/internal/release"
)

// StateKey is the system_state document written by the checker.
const StateKey = "update_check"

// UpdaterStateKey is the system_state document written by openlog-updater.
const UpdaterStateKey = "updater"

// Values of update_check in GET /api/v1/version.
const (
	StatusEnabled  = "enabled"
	StatusDisabled = "disabled"
	StatusFailed   = "failed"
)

// DefaultIndexURL is the index attached to every GitHub release.
const DefaultIndexURL = "https://github.com/onuragtas/openlog/releases/latest/download/index.json"

// Intervals: by default a successful check is repeated after Interval; a failed one after
// RetryInterval (never later than the configured interval).
const (
	Interval      = 24 * time.Hour
	RetryInterval = 6 * time.Hour
)

// Config is OPENLOG_UPDATE_CHECK, OPENLOG_UPDATE_CHECK_INTERVAL, OPENLOG_RELEASE_INDEX_URL,
// OPENLOG_UPDATE_CHANNEL and OPENLOG_RELEASE_TRUSTED_KEYS_FILE.
type Config struct {
	Enabled         bool
	Interval        time.Duration // between successful checks [Interval]
	IndexURL        string
	Channel         string
	TrustedKeysFile string
}

// Release is a verified release.
type Release struct {
	Version    string    `json:"version"`
	NotesURL   string    `json:"notes_url"`
	ReleasedAt time.Time `json:"released_at"`
}

// State is the stored result of the last check.
type State struct {
	Channel       string     `json:"channel"`
	CheckedAt     time.Time  `json:"checked_at"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	Error         string     `json:"error,omitempty"`
	// Latest is the newest release on the channel at the last successful check (whether or not it
	// is newer than a given pod; every pod compares with its own version).
	Latest *Release `json:"latest,omitempty"`
}

// Store persists State (system_state in PostgreSQL).
type Store interface {
	LoadCheck(ctx context.Context) (State, bool, error)
	SaveCheck(ctx context.Context, st State) error
}

// LatestFunc returns the newest verified release on a channel.
type LatestFunc func(ctx context.Context, indexURL, channel string) (*Release, error)

// FetchLatest returns a LatestFunc that uses f: it verifies the index, picks the newest entry of
// the channel and verifies that entry's manifest (which carries the release notes URL).
func FetchLatest(f *release.Fetcher) LatestFunc {
	return func(ctx context.Context, indexURL, channel string) (*Release, error) {
		idx, err := f.Index(ctx, indexURL)
		if err != nil {
			return nil, err
		}
		e, ok := idx.Latest(channel)
		if !ok {
			return nil, nil
		}
		m, err := f.Manifest(ctx, e.ManifestURL)
		if err != nil {
			return nil, err
		}
		if lib.Compare(m.ParsedVersion(), lib.MustParseVersion(e.Version)) != 0 {
			return nil, fmt.Errorf("manifest %s is version %s, index says %s", e.ManifestURL, m.Version, e.Version)
		}
		return &Release{Version: m.Version, NotesURL: m.NotesURL, ReleasedAt: m.ReleasedAt}, nil
	}
}

// Checker runs the periodic check. Run it only on the leader.
type Checker struct {
	cfg    Config
	store  Store
	latest LatestFunc
	log    *slog.Logger
	now    func() time.Time
}

// NewChecker creates a checker.
func NewChecker(cfg Config, store Store, latest LatestFunc, log *slog.Logger) *Checker {
	return &Checker{cfg: cfg, store: store, latest: latest, log: log, now: time.Now}
}

// Run checks whenever the stored state is due, until ctx is done. State written by a previous
// leader counts, so fail-over does not cause extra requests.
func (c *Checker) Run(ctx context.Context) {
	if !c.cfg.Enabled {
		return
	}
	for {
		wait := time.Minute // PostgreSQL unavailable: look again soon
		st, found, err := c.store.LoadCheck(ctx)
		if err == nil {
			wait = c.nextDue(st, found)
			if wait <= 0 {
				st = c.Check(ctx, st)
				if err := c.store.SaveCheck(ctx, st); err != nil && ctx.Err() == nil {
					c.log.Warn("cannot store update check result", "err", err)
					wait = time.Minute
				} else {
					wait = c.nextDue(st, true)
				}
			}
		} else if ctx.Err() == nil {
			c.log.Debug("cannot load update check state", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (c *Checker) nextDue(st State, found bool) time.Duration {
	if !found || st.Channel != c.cfg.Channel {
		return 0
	}
	iv := c.cfg.Interval
	if iv <= 0 {
		iv = Interval
	}
	if st.Error != "" && iv > RetryInterval {
		iv = RetryInterval
	}
	return time.Until(st.CheckedAt.Add(iv))
}

// CheckNow performs a check immediately and stores it ("Check now" in the UI, rate-limited by the
// caller). Any pod may call it: the leader's Run sees the fresh checked_at and waits a full interval.
func (c *Checker) CheckNow(ctx context.Context) error {
	st, _, err := c.store.LoadCheck(ctx)
	if err != nil {
		return err
	}
	return c.store.SaveCheck(ctx, c.Check(ctx, st))
}

// Check performs one check and returns the new state (prev keeps the last good release on error).
func (c *Checker) Check(ctx context.Context, prev State) State {
	now := c.now().UTC()
	st := State{Channel: c.cfg.Channel, CheckedAt: now}
	if prev.Channel == c.cfg.Channel {
		st.Latest, st.LastSuccessAt = prev.Latest, prev.LastSuccessAt
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	rel, err := c.latest(cctx, c.cfg.IndexURL, c.cfg.Channel)
	if err != nil {
		st.Error = err.Error()
		if errors.Is(err, release.ErrNoTrustedKeys) {
			c.log.Info("update check skipped: this build has no trusted release keys (set OPENLOG_RELEASE_TRUSTED_KEYS_FILE or use a release build)")
		} else {
			c.log.Warn("update check failed", "index", c.cfg.IndexURL, "err", err)
		}
		return st
	}
	st.Latest, st.LastSuccessAt = rel, &now
	if rel != nil {
		c.log.Info("update check succeeded", "channel", c.cfg.Channel, "latest", rel.Version)
	}
	return st
}

// Available is latest_available in GET /api/v1/version.
type Available struct {
	Version   string    `json:"version"`
	NotesURL  string    `json:"notes_url"`
	CheckedAt time.Time `json:"checked_at"`
}

// Info is what GET /api/v1/version reports besides the build.
type Info struct {
	LatestAvailable *Available
	UpdateCheck     string
	// Updater is the raw openlog-updater status document, nil when no updater reported.
	Updater json.RawMessage
}

// UpdaterLoader reads the openlog-updater status document (nil when absent).
type UpdaterLoader func(ctx context.Context) (json.RawMessage, error)

// Reader serves Info from the store with a short cache.
type Reader struct {
	cfg     Config
	current lib.Version
	store   Store
	updater UpdaterLoader
	ttl     time.Duration

	mu     sync.Mutex
	cached *Info
	at     time.Time
}

// NewReader creates a Reader for a pod running currentVersion. store and updater may be nil.
func NewReader(cfg Config, currentVersion string, store Store, updater UpdaterLoader) *Reader {
	v, err := lib.ParseVersion(currentVersion)
	if err != nil {
		v = lib.Version{} // 0.0.0: every release is newer
	}
	// Short cache: the version page polls every few seconds while an update runs.
	return &Reader{cfg: cfg, current: v, store: store, updater: updater, ttl: 5 * time.Second}
}

// Invalidate drops the cached information (after "Check now").
func (r *Reader) Invalidate() {
	r.mu.Lock()
	r.cached = nil
	r.mu.Unlock()
}

// Info returns the cached or freshly loaded information.
func (r *Reader) Info(ctx context.Context) Info {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cached != nil && time.Since(r.at) < r.ttl {
		return *r.cached
	}
	info := Info{UpdateCheck: StatusDisabled}
	ok := true
	if r.cfg.Enabled && r.store != nil {
		info.UpdateCheck = StatusEnabled
		lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		st, found, err := r.store.LoadCheck(lctx)
		cancel()
		switch {
		case err != nil:
			ok = false
			if r.cached != nil {
				info = *r.cached
			}
		case found:
			if st.Error != "" {
				info.UpdateCheck = StatusFailed
			}
			if st.Latest != nil && st.Channel == r.cfg.Channel {
				if lv, err := lib.ParseVersion(st.Latest.Version); err == nil && r.current.Less(lv) {
					checked := st.CheckedAt
					if st.LastSuccessAt != nil {
						checked = *st.LastSuccessAt
					}
					info.LatestAvailable = &Available{Version: st.Latest.Version, NotesURL: st.Latest.NotesURL, CheckedAt: checked}
				}
			}
		}
	}
	if r.updater != nil && ok {
		lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if raw, err := r.updater(lctx); err == nil {
			info.Updater = raw
		}
		cancel()
	}
	if ok {
		r.cached, r.at = &info, time.Now()
	}
	return info
}
