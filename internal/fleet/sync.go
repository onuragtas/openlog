package fleet

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/onuragtas/openlog/internal/alert/secrets"
	"github.com/onuragtas/openlog/internal/fleet/catalog"
	"github.com/onuragtas/openlog/internal/intsettings"
	"github.com/onuragtas/openlog/internal/tenant"
	"github.com/onuragtas/openlog/internal/version"
)

// Ingest paths (contract §3).
const (
	SyncPath        = "/v1/openlog/agent/sync"
	ReleasesPathFmt = "/v1/openlog/releases/"
)

// SyncOptions configure SyncService. Zero values take the defaults in brackets.
type SyncOptions struct {
	PollInterval  time.Duration // poll_interval_seconds [300s]
	ServeMirror   bool          // download_url points to this ingest's mirror endpoint
	MirrorBaseURL string        // external ingest URL for mirror links; empty = derived from the request
	MaxBodyBytes  int64         // [64 KiB]
	// Keys decrypts integration setting passwords (OPENLOG_SECRETS_KEY); without it settings with a password
	// are not delivered.
	Keys       *secrets.Keyring
	Registerer prometheus.Registerer
	Log        *slog.Logger
	Now        func() time.Time
}

// SyncService serves the agent sync and release mirror endpoints on ingest.
type SyncService struct {
	resolver tenant.Resolver
	states   *StateCache      // nil: no policy store (OPENLOG_AUTH_MODE=static), never offers updates
	recorder *Recorder        // nil: reports are not stored
	catalog  *catalog.Catalog // nil: no releases
	o        SyncOptions

	requests     *prometheus.CounterVec
	decisions    *prometheus.CounterVec
	mirror       *prometheus.CounterVec
	integrations *prometheus.CounterVec

	warnMu   sync.Mutex
	lastWarn time.Time
}

// NewSyncService creates the service. states, recorder and cat may be nil.
func NewSyncService(res tenant.Resolver, states *StateCache, rec *Recorder, cat *catalog.Catalog, o SyncOptions) *SyncService {
	if o.PollInterval <= 0 {
		o.PollInterval = 300 * time.Second
	}
	if o.MaxBodyBytes <= 0 {
		o.MaxBodyBytes = 64 << 10
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &SyncService{resolver: res, states: states, recorder: rec, catalog: cat, o: o,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_agent_sync_requests_total", Help: "Agent sync requests by HTTP status code.",
		}, []string{"code"}),
		decisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_agent_sync_decisions_total", Help: "Agent update decisions by reason (offer = an update was returned).",
		}, []string{"reason"}),
		mirror: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_release_mirror_requests_total", Help: "Release mirror downloads by HTTP status code.",
		}, []string{"code"}),
		integrations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openlog_agent_sync_integrations_config_total",
			Help: "Remote integration config in sync answers by result: sent, unchanged, unavailable (settings unknown), error (password not decryptable).",
		}, []string{"result"}),
	}
	if o.Registerer != nil {
		o.Registerer.MustRegister(s.requests, s.decisions, s.mirror, s.integrations)
	}
	return s
}

// Register adds the endpoints to an ingest mux.
func (s *SyncService) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST "+SyncPath, s.handleSync)
	mux.HandleFunc("GET "+ReleasesPathFmt+"{version}/{name}", s.handleRelease)
}

type syncRequest struct {
	HostID   string `json:"host_id"`
	HostName string `json:"host_name"`
	Agent    struct {
		Name          string `json:"name"`
		Version       string `json:"version"`
		Commit        string `json:"commit"`
		OS            string `json:"os"`
		Arch          string `json:"arch"`
		InstallMethod string `json:"install_method"`
		UpdateCapable bool   `json:"update_capable"`
	} `json:"agent"`
	Update *struct {
		State       string `json:"state"`
		FromVersion string `json:"from_version"`
		ToVersion   string `json:"to_version"`
		Error       string `json:"error"`
		ChangedAt   string `json:"changed_at"`
	} `json:"update"`
	ConfigHash                 string `json:"config_hash"`
	IntegrationsConfigRevision string `json:"integrations_config_revision"`
}

// SyncResponse is the sync answer.
type SyncResponse struct {
	PollIntervalSeconds int         `json:"poll_interval_seconds"`
	ServerVersion       string      `json:"server_version"`
	Update              *UpdateJSON `json:"update"`
	// IntegrationsConfig is set only when the host's effective settings differ from the revision it reported;
	// null = keep the current config.
	IntegrationsConfig *IntegrationsConfigJSON `json:"integrations_config"`
}

// IntegrationsConfigJSON is the remote integration config of a host.
type IntegrationsConfigJSON struct {
	Revision string                  `json:"revision"`
	Items    []intsettings.AgentItem `json:"items"`
}

// UpdateJSON is an update order for an agent.
type UpdateJSON struct {
	Action        string `json:"action"`
	TargetVersion string `json:"target_version"`
	Manifest      string `json:"manifest"`
	Signature     string `json:"signature"`
	DownloadURL   string `json:"download_url"`
	RolloutID     string `json:"rollout_id,omitempty"`
	NotBefore     string `json:"not_before,omitempty"`
	Deadline      string `json:"deadline,omitempty"`
}

func clip(s string, n int) string {
	s = strings.ToValidUTF8(s, "")
	if len(s) <= n {
		return s
	}
	for n > 0 && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n]
}

func (s *SyncService) jsonError(w http.ResponseWriter, code int, errCode, msg string) {
	s.requests.WithLabelValues(strconv.Itoa(code)).Inc()
	writeErrorJSON(w, code, errCode, msg)
}

func writeErrorJSON(w http.ResponseWriter, code int, errCode, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": errCode, "message": msg}})
}

// authenticate resolves the license key; it writes the error response and returns "" on failure.
func (s *SyncService) authenticate(w http.ResponseWriter, r *http.Request, fail func(http.ResponseWriter, int, string, string)) string {
	tenantID, err := s.resolver.Resolve(r.Context(), tenant.KeyFromHTTP(r.Header))
	if errors.Is(err, tenant.ErrUnavailable) {
		w.Header().Set("Retry-After", "5")
		fail(w, http.StatusServiceUnavailable, "unavailable", "authentication temporarily unavailable, retry later")
		return ""
	}
	if err != nil {
		fail(w, http.StatusUnauthorized, "unauthenticated", "invalid or missing license key")
		return ""
	}
	return tenantID
}

func (s *SyncService) handleSync(w http.ResponseWriter, r *http.Request) {
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "" && ct != "application/json" {
		s.jsonError(w, http.StatusUnsupportedMediaType, "invalid_argument", "Content-Type must be application/json")
		return
	}
	tenantID := s.authenticate(w, r, s.jsonError)
	if tenantID == "" {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.o.MaxBodyBytes))
	if err != nil {
		s.jsonError(w, http.StatusRequestEntityTooLarge, "resource_exhausted", "request body too large")
		return
	}
	var req syncRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.jsonError(w, http.StatusBadRequest, "invalid_argument", "invalid JSON body: "+err.Error())
		return
	}
	req.HostID = strings.TrimSpace(req.HostID)
	if req.HostID == "" || len(req.HostID) > 256 {
		s.jsonError(w, http.StatusBadRequest, "invalid_argument", "host_id is required (at most 256 bytes)")
		return
	}
	now := s.o.Now()
	rep := HostReport{
		HostID: req.HostID, HostName: clip(req.HostName, 256), AgentName: clip(req.Agent.Name, 128),
		Version: clip(req.Agent.Version, 128), Commit: clip(req.Agent.Commit, 64), OS: clip(req.Agent.OS, 32),
		Arch: clip(req.Agent.Arch, 32), InstallMethod: clip(req.Agent.InstallMethod, 32), UpdateCapable: req.Agent.UpdateCapable,
		UpdateState: StateIdle, ConfigHash: clip(req.ConfigHash, 128),
		IntegrationsConfigRevision: clip(strings.TrimSpace(req.IntegrationsConfigRevision), 128),
	}
	if u := req.Update; u != nil {
		if st := clip(strings.TrimSpace(u.State), 32); st != "" {
			rep.UpdateState = st
		}
		rep.UpdateFrom, rep.UpdateTo, rep.UpdateError = clip(u.FromVersion, 128), clip(u.ToVersion, 128), clip(u.Error, 2048)
		if t, err := time.Parse(time.RFC3339Nano, u.ChangedAt); err == nil {
			rep.UpdateChangedAt = t
		}
	}

	resp := SyncResponse{PollIntervalSeconds: int(s.o.PollInterval / time.Second), ServerVersion: version.Version}
	var rolloutID string
	if s.states != nil {
		reason := ReasonNoCatalog
		if st, found, ok := s.states.Get(r.Context(), tenantID); !ok {
			reason = "policy_unavailable"
			s.integrations.WithLabelValues("unavailable").Inc()
		} else if !found {
			reason = "unknown_organization"
		} else {
			in := Input{Now: now, Host: rep, Policy: st.Policy, Rollout: st.Rollout}
			if s.catalog != nil {
				in.Catalog = s.catalog.Snapshot()
			}
			if o, ok := st.Overrides[rep.HostID]; ok {
				in.Override = &o
			}
			d := Decide(in)
			reason = d.Reason
			if d.Offer() {
				resp.Update = s.updateJSON(r, d, st.Policy, now)
				rolloutID = d.RolloutID
			}
			resp.IntegrationsConfig = s.integrationsConfig(st, rep)
		}
		s.decisions.WithLabelValues(string(reason)).Inc()
		if s.recorder != nil {
			s.recorder.Record(HostRecord{TenantID: tenantID, Report: rep, SyncAt: now, RolloutID: rolloutID})
		}
	}
	s.requests.WithLabelValues("200").Inc()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(resp)
}

// integrationsConfig returns the host's remote integration config when its revision differs from the one the
// agent reported, or nil (unchanged, settings unknown, or a password that cannot be decrypted: the agent keeps
// its last config).
func (s *SyncService) integrationsConfig(st OrgState, rep HostReport) *IntegrationsConfigJSON {
	if !st.IntegrationsLoaded {
		s.integrations.WithLabelValues("unavailable").Inc()
		return nil
	}
	eff := intsettings.Effective(st.Integrations, rep.HostID)
	rev := intsettings.Revision(eff)
	if rev == rep.IntegrationsConfigRevision {
		s.integrations.WithLabelValues("unchanged").Inc()
		return nil
	}
	items, err := intsettings.AgentItems(s.o.Keys, eff)
	if err != nil {
		s.integrations.WithLabelValues("error").Inc()
		s.warnDecrypt(st.OrgID, rep.HostID, err)
		return nil
	}
	s.integrations.WithLabelValues("sent").Inc()
	return &IntegrationsConfigJSON{Revision: rev, Items: items}
}

// warnDecrypt logs at most once per minute per pod (the error names the setting and key id, never secrets).
func (s *SyncService) warnDecrypt(orgID, hostID string, err error) {
	now := s.o.Now()
	s.warnMu.Lock()
	quiet := !s.lastWarn.IsZero() && now.Sub(s.lastWarn) < time.Minute
	if !quiet {
		s.lastWarn = now
	}
	s.warnMu.Unlock()
	if !quiet {
		s.o.Log.Warn("cannot decrypt integration setting password; integrations_config not sent (check OPENLOG_SECRETS_KEY)",
			"org_id", orgID, "host_id", hostID, "err", err)
	}
}

func (s *SyncService) updateJSON(r *http.Request, d Decision, p Policy, now time.Time) *UpdateJSON {
	u := &UpdateJSON{
		Action: d.Action, TargetVersion: d.Release.VersionString(),
		Manifest:  base64.StdEncoding.EncodeToString(d.Release.Raw),
		Signature: string(d.Release.Signature), DownloadURL: d.Artifact.URL, RolloutID: d.RolloutID,
	}
	if s.o.ServeMirror && s.catalog != nil && s.catalog.MirrorEnabled() {
		u.DownloadURL = s.mirrorBase(r) + ReleasesPathFmt + url.PathEscape(d.Release.VersionString()) + "/" + url.PathEscape(d.Artifact.Name)
	}
	if len(p.MaintenanceWindows) > 0 {
		u.NotBefore = now.UTC().Format(time.RFC3339)
		if !d.Deadline.IsZero() {
			u.Deadline = d.Deadline.UTC().Format(time.RFC3339)
		}
	}
	return u
}

func (s *SyncService) mirrorBase(r *http.Request) string {
	if s.o.MirrorBaseURL != "" {
		return strings.TrimRight(s.o.MirrorBaseURL, "/")
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *SyncService) mirrorError(w http.ResponseWriter, code int, errCode, msg string) {
	s.mirror.WithLabelValues(strconv.Itoa(code)).Inc()
	writeErrorJSON(w, code, errCode, msg)
}

// handleRelease serves an artifact of a verified release from the mirror directory (GET and HEAD,
// range requests supported).
func (s *SyncService) handleRelease(w http.ResponseWriter, r *http.Request) {
	if s.authenticate(w, r, s.mirrorError) == "" {
		return
	}
	if s.catalog == nil || !s.catalog.MirrorEnabled() {
		s.mirrorError(w, http.StatusNotFound, "not_found", "release mirror is not enabled")
		return
	}
	mf, err := s.catalog.OpenMirrorFile(r.PathValue("version"), r.PathValue("name"))
	if errors.Is(err, catalog.ErrNotFound) {
		s.mirrorError(w, http.StatusNotFound, "not_found", "no such release file")
		return
	}
	if err != nil {
		s.o.Log.Error("cannot serve release file", "version", r.PathValue("version"), "name", r.PathValue("name"), "err", err)
		s.mirrorError(w, http.StatusInternalServerError, "internal", "release file unavailable")
		return
	}
	defer mf.File.Close()
	ct := "application/octet-stream"
	if strings.HasSuffix(mf.Artifact.Name, ".tar.gz") {
		ct = "application/gzip"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("ETag", `"sha256:`+mf.Artifact.SHA256+`"`)
	s.mirror.WithLabelValues("200").Inc()
	http.ServeContent(w, r, mf.Artifact.Name, mf.ModTime, mf.File)
}
