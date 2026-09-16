package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/fleet"
	"github.com/onuragtas/openlog/internal/intsettings"
)

// SetIntegrationSettings enables /api/v1/integrations/settings (docs/contracts/api.md "Integration settings"). The
// endpoints also need SetAccounts (postgres auth mode); applied revisions come from SetFleet. Must be called before Run.
func (s *Server) SetIntegrationSettings(m *intsettings.Manager) {
	s.intSettings = m
	s.srv.Handler = s.Handler()
}

func (s *Server) integrationSettingsRoutes(mux *http.ServeMux) {
	if s.intSettings == nil || s.accounts == nil {
		return
	}
	// Reads for every role (API keys too); changes for admins and owners, including an API key
	// with the admin role (integration settings are configuration, D-133).
	route := func(pattern string, write bool, h fleetFunc) {
		mux.Handle(pattern, s.instrument(pattern, func(rec *statusRecorder, r *http.Request) {
			noStore(rec)
			p, r := s.authenticate(rec, r)
			if p == nil {
				return
			}
			act := auth.ActReadIntegrationSettings
			if write {
				act = auth.ActManageIntegrationSettings
			}
			if ae := authorize(p, act); ae != nil {
				writeError(rec, ae)
				return
			}
			if err := h(rec, r, p); err != nil {
				s.writeIntegrationSettingsError(rec, pattern, err)
			}
		}))
	}
	route("GET /api/v1/integrations/settings", false, s.listIntegrationSettings)
	route("POST /api/v1/integrations/settings", true, s.createIntegrationSetting)
	route("PUT /api/v1/integrations/settings/{id}", true, s.updateIntegrationSetting)
	route("DELETE /api/v1/integrations/settings/{id}", true, s.deleteIntegrationSetting)
}

func (s *Server) writeIntegrationSettingsError(w http.ResponseWriter, route string, err error) {
	var ve *intsettings.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, &apiError{http.StatusBadRequest, "invalid_argument", ve.Msg})
	case errors.Is(err, intsettings.ErrNoSecretsKey):
		writeError(w, &apiError{http.StatusConflict, "failed_precondition", "OPENLOG_SECRETS_KEY is not configured on the server; integration passwords cannot be saved"})
	case errors.Is(err, intsettings.ErrConflict):
		writeError(w, &apiError{http.StatusConflict, "already_exists", "a setting for this integration, host and match already exists"})
	case errors.Is(err, intsettings.ErrNotFound):
		writeError(w, &apiError{http.StatusNotFound, "not_found", "not found"})
	default:
		s.writeAccountError(w, route, err)
	}
}

func (s *Server) intActor(r *http.Request, p *auth.Principal) intsettings.Actor {
	return intsettings.Actor{UserID: p.UserID, Email: p.Email, IP: s.accounts.Meta(r).IP,
		APIKeyID: p.APIKeyID, APIKeyName: p.APIKeyName}
}

// ---- response shapes ----

type intMatchJSON struct {
	Port      *int   `json:"port"`
	Container string `json:"container"`
	Endpoint  string `json:"endpoint"`
	Instance  string `json:"instance"`
}

type intSettingJSON struct {
	ID             string       `json:"id"`
	HostID         *string      `json:"host_id"`
	Integration    string       `json:"integration"`
	Match          intMatchJSON `json:"match"`
	Enabled        bool         `json:"enabled"`
	Endpoint       string       `json:"endpoint"`
	Username       string       `json:"username"`
	PasswordSet    bool         `json:"password_set"`
	Database       string       `json:"database"`
	Databases      []string     `json:"databases"`
	CreatedAt      string       `json:"created_at"`
	UpdatedAt      string       `json:"updated_at"`
	UpdatedByEmail string       `json:"updated_by_email"`
}

// intSettingResponse renders a setting; the password (plaintext or ciphertext) is never part of it.
func intSettingResponse(st intsettings.Setting) intSettingJSON {
	out := intSettingJSON{ID: st.ID, HostID: optString(st.HostID), Integration: st.Integration,
		Match:   intMatchJSON{Port: st.Match.Port, Container: st.Match.Container, Endpoint: st.Match.Endpoint, Instance: st.Match.Instance},
		Enabled: st.Enabled, Endpoint: st.Endpoint, Username: st.Username, PasswordSet: st.PasswordEnc != "", Database: st.Database,
		Databases: st.Databases, CreatedAt: formatTime(st.CreatedAt), UpdatedAt: formatTime(st.UpdatedAt), UpdatedByEmail: st.UpdatedByEmail}
	if out.Databases == nil {
		out.Databases = []string{}
	}
	return out
}

type intHostJSON struct {
	HostID               string  `json:"host_id"`
	Revision             string  `json:"revision"`
	AppliedRevision      string  `json:"applied_revision"`
	AppliedAt            *string `json:"applied_at"`
	RemoteConfigDisabled bool    `json:"remote_config_disabled"`
}

// ---- handlers ----

func (s *Server) listIntegrationSettings(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	hostID := strings.TrimSpace(r.URL.Query().Get("host_id"))
	if len(hostID) > intsettings.MaxHostIDBytes {
		return badRequest("host_id must be at most %d bytes", intsettings.MaxHostIDBytes)
	}
	var (
		items []intsettings.Setting
		host  *intHostJSON
		err   error
	)
	if hostID == "" {
		items, err = s.intSettings.List(r.Context(), p.OrgID)
	} else {
		host = &intHostJSON{HostID: hostID}
		items, host.Revision, err = s.intSettings.ForHost(r.Context(), p.OrgID, hostID)
		if err == nil && s.fleet != nil {
			var h fleet.Host
			h, err = s.fleet.Host(r.Context(), p.OrgID, hostID)
			switch {
			case errors.Is(err, fleet.ErrNotFound):
				err = nil
			case err == nil && h.IntegrationsConfigRevision != "":
				host.AppliedRevision = h.IntegrationsConfigRevision
				host.AppliedAt = optTime(&h.LastSyncAt)
				host.RemoteConfigDisabled = h.IntegrationsConfigRevision == intsettings.RevisionDisabled
			}
		}
	}
	if err != nil {
		return err
	}
	out := make([]intSettingJSON, 0, len(items))
	for _, st := range items {
		out = append(out, intSettingResponse(st))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "host": host})
	return nil
}

func (s *Server) createIntegrationSetting(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in intsettings.Input
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	st, err := s.intSettings.Create(r.Context(), p.OrgID, in, s.intActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, intSettingResponse(st))
	return nil
}

func (s *Server) updateIntegrationSetting(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in intsettings.Input
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	st, err := s.intSettings.Update(r.Context(), p.OrgID, r.PathValue("id"), in, s.intActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, intSettingResponse(st))
	return nil
}

func (s *Server) deleteIntegrationSetting(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := s.intSettings.Delete(r.Context(), p.OrgID, r.PathValue("id"), s.intActor(r, p)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
