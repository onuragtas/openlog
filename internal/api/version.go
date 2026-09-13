package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/onuragtas/openlog/internal/updatecheck"
	"github.com/onuragtas/openlog/internal/version"
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
		resp := versionResponse{
			Version: version.String(), Commit: version.Commit, Date: version.Date,
			UpdateCheck: updatecheck.StatusDisabled, Updater: json.RawMessage("null"),
		}
		if s.versions != nil {
			info := s.versions.Info(r.Context())
			resp.LatestAvailable, resp.UpdateCheck = info.LatestAvailable, info.UpdateCheck
			if len(info.Updater) > 0 {
				resp.Updater = info.Updater
			}
		}
		writeJSON(rec, http.StatusOK, resp)
	}))
}
