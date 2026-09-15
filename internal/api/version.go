package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/updatecheck"
	"github.com/onuragtas/openlog/internal/updatemsg"
	"github.com/onuragtas/openlog/internal/version"
	lib "github.com/onuragtas/openlog/libs/release"
)

// VersionSource reports update information for GET /api/v1/version.
type VersionSource interface {
	Info(ctx context.Context) updatecheck.Info
}

// SetVersionSource enables latest_available / update_check / updater in GET /api/v1/version.
// Without it the endpoint reports the build only (update_check "disabled").
func (s *Server) SetVersionSource(v VersionSource) { s.versions = v }

type versionResponse struct {
	Version         string                 `json:"version"`
	Commit          string                 `json:"commit"`
	Date            string                 `json:"date"`
	LatestAvailable *updatecheck.Available `json:"latest_available"`
	UpdateCheck     string                 `json:"update_check"`
	Updater         json.RawMessage        `json:"updater"`
	// UpdateRequests is the "Check now" / "Update now" channel (updates.go); null in static mode.
	UpdateRequests *updateRequestsJSON `json:"update_requests"`
}

// versionRoutes registers GET /api/v1/version (any authenticated principal, both auth modes).
func (s *Server) versionRoutes(mux *http.ServeMux) {
	const pattern = "GET /api/v1/version"
	mux.Handle(pattern, s.instrument(pattern, func(rec *statusRecorder, r *http.Request) {
		noStore(rec)
		p, r := s.authenticate(rec, r)
		if p == nil {
			return
		}
		writeJSON(rec, http.StatusOK, s.versionInfo(r, p))
	}))
}

func (s *Server) versionInfo(r *http.Request, p *auth.Principal) versionResponse {
	resp := versionResponse{
		Version: version.String(), Commit: version.Commit, Date: version.Date,
		UpdateCheck: updatecheck.StatusDisabled, Updater: json.RawMessage("null"),
	}
	if s.versions != nil {
		info := s.versions.Info(r.Context())
		resp.LatestAvailable, resp.UpdateCheck = info.LatestAvailable, info.UpdateCheck
		if len(info.Updater) > 0 {
			resp.Updater = withLegacyUpdaterNotice(info.Updater, version.String())
		}
	}
	resp.UpdateRequests = s.updateRequestsInfo(r.Context(), p)
	return resp
}

// withLegacyUpdaterNotice adds the notice updater_outdated (Compose) or updater_outdated_kubernetes to a status
// document written by an updater older than updater_version (0.1.26): such an updater is older than this release build
// and cannot report it itself. Documents of newer updaters (which compute the notice themselves), unreadable documents
// and development builds are returned unchanged.
func withLegacyUpdaterNotice(raw json.RawMessage, running string) json.RawMessage {
	if strings.Contains(running, "dev") {
		return raw
	}
	if _, err := lib.ParseVersion(running); err != nil {
		return raw
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil || doc == nil {
		return raw
	}
	if _, ok := doc["updater_version"]; ok {
		return raw
	}
	var engine string
	if json.Unmarshal(doc["engine"], &engine) != nil || engine == "" {
		return raw
	}
	var notices []map[string]any
	if n, ok := doc["notices"]; ok && json.Unmarshal(n, &notices) != nil {
		return raw
	}
	code := updatemsg.UpdaterOutdated
	if engine == "kubernetes" {
		code = updatemsg.UpdaterOutdatedKubernetes
	}
	params := updatemsg.Params{"updater_version": "< " + running, "running_version": running}
	notices = append(notices, map[string]any{"code": code, "message": updatemsg.Format(code, params), "params": params})
	b, err := json.Marshal(notices)
	if err != nil {
		return raw
	}
	doc["notices"] = b
	out, err := json.Marshal(doc)
	if err != nil {
		return raw
	}
	return out
}
