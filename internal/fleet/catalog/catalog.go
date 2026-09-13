// Package catalog keeps the verified release catalog of a backend pod: the signed release index and
// the signed manifests it lists (docs/contracts/releases-updates.md §2). Releases come from
// OPENLOG_RELEASE_INDEX_URL or a local mirror directory; nothing is used unless its signature
// verifies with a trusted key.
package catalog

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	lib "github.com/onuragtas/openlog/libs/release"
)

// DefaultIndexURL is the contract's default index location.
const DefaultIndexURL = "https://github.com/onuragtas/openlog/releases/latest/download/index.json"

// Size limits for fetched files.
const (
	maxIndexBytes    = 4 << 20
	maxManifestBytes = 1 << 20
	maxSigBytes      = 64 << 10
)

// Catalog states reported by Status.
const (
	StateOK       = "ok"
	StatePending  = "pending"
	StateError    = "error"
	StateDisabled = "disabled"
)

// Release is one verified release.
type Release struct {
	Version   lib.Version
	Manifest  *lib.Manifest
	Raw       []byte // exact manifest.json bytes
	Signature []byte // manifest.json.sig contents
	KeyID     string
}

// VersionString is the canonical version (no "v").
func (r *Release) VersionString() string { return r.Manifest.Version }

// VisibleOn reports whether the release belongs to a channel: beta sees every release, stable only
// stable manifests.
func (r *Release) VisibleOn(channel string) bool {
	switch channel {
	case lib.ChannelBeta:
		return true
	case lib.ChannelStable:
		return r.Manifest.Channel == lib.ChannelStable && !r.Version.IsPrerelease()
	}
	return false
}

// Snapshot is an immutable set of verified releases.
type Snapshot struct {
	GeneratedAt time.Time
	FetchedAt   time.Time
	releases    []*Release // newest first
	byVersion   map[string]*Release
}

// NewSnapshot builds a snapshot (used by the catalog and by tests).
func NewSnapshot(releases []*Release, generatedAt, fetchedAt time.Time) *Snapshot {
	s := &Snapshot{GeneratedAt: generatedAt, FetchedAt: fetchedAt, byVersion: map[string]*Release{}}
	for _, r := range releases {
		key := r.Version.String()
		if _, dup := s.byVersion[key]; dup {
			continue
		}
		s.byVersion[key] = r
		s.releases = append(s.releases, r)
	}
	sort.SliceStable(s.releases, func(i, j int) bool { return lib.Compare(s.releases[i].Version, s.releases[j].Version) > 0 })
	return s
}

// Len is the number of releases.
func (s *Snapshot) Len() int {
	if s == nil {
		return 0
	}
	return len(s.releases)
}

// Releases returns all releases, newest first. Callers must not modify the slice.
func (s *Snapshot) Releases() []*Release {
	if s == nil {
		return nil
	}
	return s.releases
}

// Release finds a release by version (with or without "v"; build metadata ignored).
func (s *Snapshot) Release(version string) (*Release, bool) {
	if s == nil {
		return nil, false
	}
	v, err := lib.ParseVersion(version)
	if err != nil {
		return nil, false
	}
	r, ok := s.byVersion[v.String()]
	return r, ok
}

// Latest returns the newest release visible on channel.
func (s *Snapshot) Latest(channel string) (*Release, bool) {
	for _, r := range s.Releases() {
		if r.VisibleOn(channel) {
			return r, true
		}
	}
	return nil, false
}

// LatestPatch returns the newest release on channel with the given major and minor.
func (s *Snapshot) LatestPatch(channel string, major, minor uint64) (*Release, bool) {
	for _, r := range s.Releases() {
		if r.Version.Major == major && r.Version.Minor == minor && r.VisibleOn(channel) {
			return r, true
		}
	}
	return nil, false
}

// Status describes the catalog for operators and the UI.
type Status struct {
	State         string     `json:"status"`
	Source        string     `json:"source"`
	CheckedAt     *time.Time `json:"checked_at"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	Error         string     `json:"error"`
	Releases      int        `json:"releases"`
	Warnings      []string   `json:"warnings"`
}

// Options configure a Catalog.
type Options struct {
	IndexURL    string // used when MirrorDir is empty [DefaultIndexURL]
	MirrorDir   string // local mirror: <dir>/index.json(.sig), <dir>/v<version>/manifest.json(.sig) and artifacts
	TrustedKeys []ed25519.PublicKey
	KeysError   error         // why no keys could be loaded (reported in Status)
	Refresh     time.Duration // [15m]
	HTTPClient  *http.Client
	MaxReleases int // newest releases kept from the index [200]
	Registerer  prometheus.Registerer
	Log         *slog.Logger
	Now         func() time.Time
}

// Catalog periodically loads and verifies releases.
type Catalog struct {
	o    Options
	snap atomic.Pointer[Snapshot]

	mu        sync.Mutex
	status    Status
	etag      string
	indexRaw  []byte
	indexSig  []byte
	manifests map[string]*Release // URL source: verified manifests are immutable, keyed by version

	hashMu sync.Mutex
	hashes map[string]fileHash // mirror artifact verification cache

	refreshes *prometheus.CounterVec
	releases  prometheus.Gauge
}

type fileHash struct {
	size    int64
	modTime time.Time
	ok      bool
}

// New creates a catalog. Call Run (or Refresh) to load it.
func New(o Options) *Catalog {
	if o.IndexURL == "" {
		o.IndexURL = DefaultIndexURL
	}
	if o.Refresh <= 0 {
		o.Refresh = 15 * time.Minute
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if o.MaxReleases <= 0 {
		o.MaxReleases = 200
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	c := &Catalog{
		o: o, manifests: map[string]*Release{}, hashes: map[string]fileHash{},
		refreshes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_release_catalog_refreshes_total", Help: "Release catalog refreshes by result (ok, not_modified, error, disabled).",
		}, []string{"result"}),
		releases: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "openlog_release_catalog_releases", Help: "Verified releases in the release catalog.",
		}),
	}
	if o.Registerer != nil {
		o.Registerer.MustRegister(c.refreshes, c.releases)
	}
	c.status = Status{State: StatePending, Source: c.source(), Warnings: []string{}}
	if len(o.TrustedKeys) == 0 {
		msg := "no trusted release keys"
		if o.KeysError != nil {
			msg += ": " + o.KeysError.Error()
		}
		c.status.State, c.status.Error = StateDisabled, msg
	}
	return c
}

func (c *Catalog) source() string {
	if c.o.MirrorDir != "" {
		return "mirror:" + c.o.MirrorDir
	}
	return c.o.IndexURL
}

// Enabled reports whether trusted keys are configured.
func (c *Catalog) Enabled() bool { return len(c.o.TrustedKeys) > 0 }

// MirrorEnabled reports whether a mirror directory is configured.
func (c *Catalog) MirrorEnabled() bool { return c.o.MirrorDir != "" }

// Snapshot returns the last verified snapshot, or nil.
func (c *Catalog) Snapshot() *Snapshot { return c.snap.Load() }

// Status returns the current status.
func (c *Catalog) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.status
	s.Warnings = append([]string{}, s.Warnings...)
	s.Releases = c.snap.Load().Len()
	return s
}

// Run refreshes immediately and then every Refresh until ctx is done. After a failure it retries
// sooner (every minute, at most Refresh).
func (c *Catalog) Run(ctx context.Context) {
	if !c.Enabled() {
		c.o.Log.Warn("release catalog disabled: no trusted release keys (agent updates are not offered)", "err", c.o.KeysError)
		c.refreshes.WithLabelValues("disabled").Inc()
		return
	}
	for {
		rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := c.Refresh(rctx)
		cancel()
		wait := c.o.Refresh
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.o.Log.Warn("release catalog refresh failed", "source", c.source(), "err", err)
			wait = min(wait, time.Minute)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// Refresh loads and verifies the index and manifests. On failure the previous snapshot stays.
func (c *Catalog) Refresh(ctx context.Context) error {
	if !c.Enabled() {
		return errors.New(c.Status().Error)
	}
	now := c.o.Now()
	snap, warnings, notModified, err := c.load(ctx, now)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.CheckedAt = &now
	if err != nil {
		c.status.State, c.status.Error = StateError, err.Error()
		c.refreshes.WithLabelValues("error").Inc()
		return err
	}
	c.status.State, c.status.Error = StateOK, ""
	c.status.LastSuccessAt = &now
	if notModified {
		c.refreshes.WithLabelValues("not_modified").Inc()
		return nil
	}
	c.status.Warnings = warnings
	c.snap.Store(snap)
	c.releases.Set(float64(snap.Len()))
	c.refreshes.WithLabelValues("ok").Inc()
	return nil
}

// load returns a new snapshot, or notModified when the index did not change (URL source).
func (c *Catalog) load(ctx context.Context, now time.Time) (*Snapshot, []string, bool, error) {
	var (
		indexRaw, indexSig []byte
		err                error
	)
	if c.o.MirrorDir != "" {
		if indexRaw, err = readFileLimit(c.o.MirrorDir, "index.json", maxIndexBytes); err != nil {
			return nil, nil, false, err
		}
		if indexSig, err = readFileLimit(c.o.MirrorDir, "index.json.sig", maxSigBytes); err != nil {
			return nil, nil, false, err
		}
	} else {
		c.mu.Lock()
		etag := c.etag
		c.mu.Unlock()
		raw, newTag, status, ferr := c.fetch(ctx, c.o.IndexURL, maxIndexBytes, etag)
		if ferr != nil {
			return nil, nil, false, ferr
		}
		if status == http.StatusNotModified && c.snap.Load() != nil {
			return nil, nil, true, nil
		}
		indexRaw = raw
		if indexSig, _, _, err = c.fetch(ctx, c.o.IndexURL+".sig", maxSigBytes, ""); err != nil {
			return nil, nil, false, err
		}
		defer func() {
			if err == nil {
				c.mu.Lock()
				c.etag = newTag
				c.mu.Unlock()
			}
		}()
	}
	idx, _, err := lib.VerifyIndex(indexRaw, indexSig, c.o.TrustedKeys)
	if err != nil {
		return nil, nil, false, fmt.Errorf("index: %w", err)
	}
	// A replayed older (validly signed) index could hide newer releases.
	if prev := c.snap.Load(); prev != nil && idx.GeneratedAt.Before(prev.GeneratedAt) {
		err = fmt.Errorf("index generated_at %s is older than the current index (%s); ignoring it",
			idx.GeneratedAt.UTC().Format(time.RFC3339), prev.GeneratedAt.UTC().Format(time.RFC3339))
		return nil, nil, false, err
	}

	type entry struct {
		lib.IndexEntry
		channel string
		version lib.Version
	}
	var entries []entry
	seen := map[string]bool{}
	for _, ch := range []string{lib.ChannelStable, lib.ChannelBeta} {
		for _, e := range idx.Channels[ch] {
			v, perr := lib.ParseVersion(e.Version)
			if perr != nil || seen[v.String()] {
				continue
			}
			seen[v.String()] = true
			entries = append(entries, entry{IndexEntry: e, channel: ch, version: v})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return lib.Compare(entries[i].version, entries[j].version) > 0 })
	if len(entries) > c.o.MaxReleases {
		entries = entries[:c.o.MaxReleases]
	}

	warnings := []string{}
	var releases []*Release
	keep := map[string]*Release{}
	for _, e := range entries {
		key := e.version.String()
		rel, lerr := c.loadManifest(ctx, e.IndexEntry, key)
		if lerr == nil && rel.Version.String() != key {
			lerr = fmt.Errorf("manifest version %s does not match index entry %s", rel.Manifest.Version, e.Version)
		}
		if lerr == nil && e.channel == lib.ChannelStable && rel.Manifest.Channel != lib.ChannelStable {
			lerr = fmt.Errorf("listed on stable but manifest channel is %s", rel.Manifest.Channel)
		}
		if lerr != nil {
			warnings = append(warnings, fmt.Sprintf("release %s: %v", key, lerr))
			c.o.Log.Warn("release rejected", "version", key, "err", lerr)
			continue
		}
		keep[key] = rel
		releases = append(releases, rel)
	}
	if c.o.MirrorDir == "" {
		c.mu.Lock()
		c.manifests = keep
		c.indexRaw, c.indexSig = indexRaw, indexSig
		c.mu.Unlock()
	}
	return NewSnapshot(releases, idx.GeneratedAt, now), warnings, false, nil
}

func (c *Catalog) loadManifest(ctx context.Context, e lib.IndexEntry, key string) (*Release, error) {
	var raw, sig []byte
	var err error
	if c.o.MirrorDir != "" {
		dir := "v" + key
		if raw, err = readFileLimit(c.o.MirrorDir, dir+"/manifest.json", maxManifestBytes); err != nil {
			return nil, err
		}
		if sig, err = readFileLimit(c.o.MirrorDir, dir+"/manifest.json.sig", maxSigBytes); err != nil {
			return nil, err
		}
	} else {
		c.mu.Lock()
		cached := c.manifests[key]
		c.mu.Unlock()
		if cached != nil {
			return cached, nil
		}
		if raw, _, _, err = c.fetch(ctx, e.ManifestURL, maxManifestBytes, ""); err != nil {
			return nil, err
		}
		if sig, _, _, err = c.fetch(ctx, e.ManifestURL+".sig", maxSigBytes, ""); err != nil {
			return nil, err
		}
	}
	m, keyID, err := lib.VerifyManifest(raw, sig, c.o.TrustedKeys)
	if err != nil {
		return nil, err
	}
	return &Release{Version: m.ParsedVersion(), Manifest: m, Raw: raw, Signature: sig, KeyID: keyID}, nil
}

// fetch GETs url with a size limit. A 304 answer returns status 304 and no body.
func (c *Catalog) fetch(ctx context.Context, url string, limit int64, etag string) ([]byte, string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", 0, err
	}
	req.Header.Set("User-Agent", "openlog-backend")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.o.HTTPClient.Do(req)
	if err != nil {
		return nil, "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, resp.StatusCode, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", resp.StatusCode, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", resp.StatusCode, fmt.Errorf("GET %s: %w", url, err)
	}
	if int64(len(b)) > limit {
		return nil, "", resp.StatusCode, fmt.Errorf("GET %s: larger than %d bytes", url, limit)
	}
	return b, resp.Header.Get("ETag"), resp.StatusCode, nil
}

// readFileLimit reads a file below root without following paths out of it.
func readFileLimit(root, name string, limit int64) ([]byte, error) {
	f, err := os.OpenInRoot(root, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: larger than %d bytes", name, limit)
	}
	return b, nil
}

// ErrNotFound means a mirror file is not part of a verified release.
var ErrNotFound = errors.New("release file not found")

// MirrorFile is an artifact that can be served from the mirror directory.
type MirrorFile struct {
	File     *os.File
	Artifact lib.Artifact
	ModTime  time.Time
}

// OpenMirrorFile opens an artifact of a verified release from the mirror directory. Only file names
// listed in the manifest's artifacts are served; the file must match the manifest's size and
// sha256 (checked once per file size and modification time). The caller closes File.
func (c *Catalog) OpenMirrorFile(version, name string) (*MirrorFile, error) {
	if c.o.MirrorDir == "" {
		return nil, ErrNotFound
	}
	rel, ok := c.Snapshot().Release(version)
	if !ok || name == "" || path.Base(name) != name || strings.HasPrefix(name, ".") || strings.ContainsAny(name, `/\`) {
		return nil, ErrNotFound
	}
	var art lib.Artifact
	found := false
	for _, a := range rel.Manifest.Artifacts {
		if a.Name == name {
			art, found = a, true
			break
		}
	}
	if !found {
		return nil, ErrNotFound
	}
	rel0 := "v" + rel.Manifest.Version + "/" + name
	f, err := os.OpenInRoot(c.o.MirrorDir, rel0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, ErrNotFound
	}
	if st.Size() != art.Size {
		f.Close()
		return nil, fmt.Errorf("mirror file %s: size %d does not match manifest (%d)", rel0, st.Size(), art.Size)
	}
	if err := c.checkHash(f, rel0, st, art.SHA256); err != nil {
		f.Close()
		return nil, err
	}
	return &MirrorFile{File: f, Artifact: art, ModTime: st.ModTime()}, nil
}

func (c *Catalog) checkHash(f *os.File, key string, st os.FileInfo, want string) error {
	c.hashMu.Lock()
	h, ok := c.hashes[key]
	c.hashMu.Unlock()
	if ok && h.size == st.Size() && h.modTime.Equal(st.ModTime()) {
		if h.ok {
			return nil
		}
		return fmt.Errorf("mirror file %s: sha256 does not match manifest", key)
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	good := hex.EncodeToString(sum.Sum(nil)) == want
	c.hashMu.Lock()
	if len(c.hashes) > 10000 {
		c.hashes = map[string]fileHash{}
	}
	c.hashes[key] = fileHash{size: st.Size(), modTime: st.ModTime(), ok: good}
	c.hashMu.Unlock()
	if !good {
		return fmt.Errorf("mirror file %s: sha256 does not match manifest", key)
	}
	return nil
}
