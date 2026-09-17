package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/onuragtas/openlog/internal/alert/secrets"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/cloudconnect"
)

// Cloud connections (docs/contracts/api.md "Cloud connections", D-135): the managed cloud services an
// organization lets openlog read metrics from. Definitions live in PostgreSQL with the last poll per scope;
// the metrics themselves are ordinary rows of `metrics` and are read through the Metrics Explorer like any
// other source, so there is no telemetry endpoint here.
//
// Reads need any role (API keys too); changes need an admin or owner, an API key with that role included —
// a connection stores cloud credentials and spends money at the provider. Credentials are write-only: no
// response, log or audit detail ever contains one.

// SetCloudConnect enables /api/v1/cloud/* (postgres auth mode). Must be called before Run.
func (s *Server) SetCloudConnect(m *cloudconnect.Manager) {
	s.cloud = m
	s.srv.Handler = s.Handler()
}

func (s *Server) cloudRoutes(mux *http.ServeMux) {
	if s.cloud == nil || s.accounts == nil {
		return
	}
	route := func(pattern string, write bool, h fleetFunc) {
		mux.Handle(pattern, s.instrument(pattern, func(rec *statusRecorder, r *http.Request) {
			noStore(rec)
			p, r := s.authenticate(rec, r)
			if p == nil {
				return
			}
			act := auth.ActReadCloudConnections
			if write {
				act = auth.ActManageCloudConnections
			}
			if ae := authorize(p, act); ae != nil {
				writeError(rec, ae)
				return
			}
			if err := h(rec, r, p); err != nil {
				s.writeCloudError(rec, pattern, err)
			}
		}))
	}
	route("GET /api/v1/cloud/providers", false, s.listCloudProviders)
	route("GET /api/v1/cloud/connections", false, s.listCloudConnections)
	route("GET /api/v1/cloud/connections/{id}", false, s.getCloudConnection)
	route("GET /api/v1/cloud/connections/{id}/runs", false, s.cloudConnectionRuns)
	route("POST /api/v1/cloud/connections", true, s.createCloudConnection)
	route("PUT /api/v1/cloud/connections/{id}", true, s.updateCloudConnection)
	route("DELETE /api/v1/cloud/connections/{id}", true, s.deleteCloudConnection)
	route("POST /api/v1/cloud/connections/test", true, s.testCloudConnection)
}

func (s *Server) writeCloudError(w http.ResponseWriter, route string, err error) {
	var ve *cloudconnect.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, &apiError{http.StatusBadRequest, "invalid_argument", ve.Error()})
	case errors.Is(err, cloudconnect.ErrNotFound):
		writeError(w, &apiError{http.StatusNotFound, "not_found", "cloud connection not found"})
	case errors.Is(err, cloudconnect.ErrLimit):
		writeError(w, &apiError{http.StatusConflict, "failed_precondition",
			fmt.Sprintf("the organization already has the maximum number of cloud connections (%d); delete some first",
				cloudconnect.MaxPerOrg)})
	case errors.Is(err, cloudconnect.ErrNoCredentials):
		writeError(w, &apiError{http.StatusConflict, "failed_precondition",
			"the connection has no stored credentials; save them before testing or polling"})
	case errors.Is(err, secrets.ErrNoKey):
		writeError(w, &apiError{http.StatusConflict, "failed_precondition",
			"OPENLOG_SECRETS_KEY is not configured on the server; cloud credentials cannot be saved"})
	default:
		s.writeAccountError(w, route, err)
	}
}

func (s *Server) cloudActor(r *http.Request, p *auth.Principal) cloudconnect.Actor {
	return cloudconnect.Actor{UserID: p.UserID, Email: p.Email, IP: s.accounts.Meta(r).IP,
		APIKeyID: p.APIKeyID, APIKeyName: p.APIKeyName}
}

// ---- response shapes ----

type cloudScopeStatusJSON struct {
	Scope             string  `json:"scope"`
	NextRunAt         string  `json:"next_run_at"`
	LastRunAt         *string `json:"last_run_at"`
	LastStatus        string  `json:"last_status"`
	LastError         string  `json:"last_error"`
	LastMetrics       int     `json:"last_metrics"`
	LastAPICalls      int     `json:"last_api_calls"`
	LastDurationMs    float64 `json:"last_duration_ms"`
	ConsecutiveErrors int     `json:"consecutive_errors"`
}

type cloudConnectionJSON struct {
	ID                  string                 `json:"id"`
	Name                string                 `json:"name"`
	Provider            string                 `json:"provider"`
	IngestMode          string                 `json:"ingest_mode"`
	Enabled             bool                   `json:"enabled"`
	Scopes              []string               `json:"scopes"`
	Services            []string               `json:"services"`
	PollIntervalSeconds int                    `json:"poll_interval_seconds"`
	MaxMetricsPerPoll   int                    `json:"max_metrics_per_poll"`
	MaxAPICallsPerPoll  int                    `json:"max_api_calls_per_poll"`
	CredentialsSet      bool                   `json:"credentials_set"`
	CredentialsKeyID    string                 `json:"credentials_key_id"`
	CreatedByEmail      string                 `json:"created_by_email"`
	UpdatedByEmail      string                 `json:"updated_by_email"`
	CreatedAt           string                 `json:"created_at"`
	UpdatedAt           string                 `json:"updated_at"`
	Status              []cloudScopeStatusJSON `json:"status"`
}

// cloudConnectionResponse renders a connection; no credential value is ever part of it, only whether one is
// stored and which key encrypted it.
func cloudConnectionResponse(c *cloudconnect.Connection) cloudConnectionJSON {
	out := cloudConnectionJSON{ID: c.ID, Name: c.Name, Provider: c.Provider, IngestMode: c.IngestMode,
		Enabled: c.Enabled, Scopes: c.Scopes, Services: c.Services, PollIntervalSeconds: c.PollIntervalSeconds,
		MaxMetricsPerPoll: c.MaxMetricsPerPoll, MaxAPICallsPerPoll: c.MaxAPICallsPerPoll,
		CredentialsSet: c.CredentialsSet, CredentialsKeyID: c.CredentialsKeyID,
		CreatedByEmail: c.CreatedByEmail, UpdatedByEmail: c.UpdatedByEmail,
		CreatedAt: formatTime(c.CreatedAt), UpdatedAt: formatTime(c.UpdatedAt),
		Status: make([]cloudScopeStatusJSON, 0, len(c.Status))}
	if out.Scopes == nil {
		out.Scopes = []string{}
	}
	if out.Services == nil {
		out.Services = []string{}
	}
	for _, st := range c.Status {
		out.Status = append(out.Status, cloudScopeStatusJSON{Scope: st.Scope,
			NextRunAt: formatTime(st.NextRunAt), LastRunAt: optTime(st.LastRunAt), LastStatus: st.LastStatus,
			LastError: st.LastError, LastMetrics: st.LastMetrics, LastAPICalls: st.LastAPICalls,
			LastDurationMs: st.LastDurationMs, ConsecutiveErrors: st.ConsecutiveErrors})
	}
	return out
}

type cloudServiceRunJSON struct {
	Service string `json:"service"`
	Metrics int    `json:"metrics"`
	Error   string `json:"error"`
}

type cloudRunJSON struct {
	ID         int64                 `json:"id"`
	Scope      string                `json:"scope"`
	StartedAt  string                `json:"started_at"`
	DurationMs float64               `json:"duration_ms"`
	Status     string                `json:"status"`
	Metrics    int                   `json:"metrics"`
	APICalls   int                   `json:"api_calls"`
	Throttled  int                   `json:"throttled"`
	Error      string                `json:"error"`
	Services   []cloudServiceRunJSON `json:"services"`
}

func cloudRunResponse(r cloudconnect.Run) cloudRunJSON {
	out := cloudRunJSON{ID: r.ID, Scope: r.Scope, StartedAt: formatTime(r.StartedAt), DurationMs: r.DurationMs,
		Status: r.Status, Metrics: r.Metrics, APICalls: r.APICalls, Throttled: r.Throttled, Error: r.Error,
		Services: make([]cloudServiceRunJSON, 0, len(r.Services))}
	for _, s := range r.Services {
		out.Services = append(out.Services, cloudServiceRunJSON{Service: s.Service, Metrics: s.Metrics, Error: s.Error})
	}
	return out
}

// ---- handlers ----

// listCloudProviders serves the catalog the create form is built from: the providers, what one scope is
// called, the credential fields to ask for and the services that can be collected.
func (s *Server) listCloudProviders(w http.ResponseWriter, _ *http.Request, _ *auth.Principal) error {
	type fieldJSON struct {
		Key      string `json:"key"`
		Required bool   `json:"required"`
		Secret   bool   `json:"secret"`
	}
	type serviceJSON struct {
		ID      string `json:"id"`
		Metrics int    `json:"metrics"`
	}
	type providerJSON struct {
		ID          string        `json:"id"`
		ScopeLabel  string        `json:"scope_label"`
		Credentials []fieldJSON   `json:"credentials"`
		Services    []serviceJSON `json:"services"`
	}
	out := make([]providerJSON, 0, len(cloudconnect.Providers()))
	for _, id := range cloudconnect.Providers() {
		p := providerJSON{ID: id, ScopeLabel: cloudconnect.ScopeLabel(id),
			Credentials: []fieldJSON{}, Services: []serviceJSON{}}
		for _, f := range cloudconnect.CredentialFields(id) {
			p.Credentials = append(p.Credentials, fieldJSON{Key: f.Key, Required: f.Required, Secret: f.Secret})
		}
		for _, svc := range cloudconnect.Services(id) {
			p.Services = append(p.Services, serviceJSON{ID: svc.ID, Metrics: len(svc.Metrics)})
		}
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out,
		"secrets_configured": s.cloud.SecretsConfigured(), "test_supported": s.cloud.TestSupported()})
	return nil
}

func (s *Server) listCloudConnections(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	list, err := s.cloud.List(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	out := make([]cloudConnectionJSON, 0, len(list))
	for i := range list {
		out = append(out, cloudConnectionResponse(&list[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": out,
		"secrets_configured": s.cloud.SecretsConfigured(), "test_supported": s.cloud.TestSupported()})
	return nil
}

func (s *Server) getCloudConnection(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	c, err := s.cloud.Get(r.Context(), p.OrgID, r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, cloudConnectionResponse(c))
	return nil
}

// cloudInputJSON decodes an input; enabled is a pointer so that omitting it means "enabled" instead of
// silently creating a connection that never polls.
type cloudInputJSON struct {
	cloudconnect.Input
	Enabled *bool `json:"enabled"`
}

func decodeCloudInput(r *http.Request) (cloudconnect.Input, error) {
	var in cloudInputJSON
	if err := decodeJSON(r, &in); err != nil {
		return cloudconnect.Input{}, err
	}
	out := in.Input
	out.Enabled = in.Enabled == nil || *in.Enabled
	return out, nil
}

func (s *Server) createCloudConnection(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	in, err := decodeCloudInput(r)
	if err != nil {
		return err
	}
	c, err := s.cloud.Create(r.Context(), p.OrgID, in, s.cloudActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, cloudConnectionResponse(c))
	return nil
}

func (s *Server) updateCloudConnection(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	in, err := decodeCloudInput(r)
	if err != nil {
		return err
	}
	c, err := s.cloud.Update(r.Context(), p.OrgID, r.PathValue("id"), in, s.cloudActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, cloudConnectionResponse(c))
	return nil
}

func (s *Server) deleteCloudConnection(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := s.cloud.Delete(r.Context(), p.OrgID, r.PathValue("id"), s.cloudActor(r, p)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// cloudConnectionRuns serves the poll history of a connection: what each scope collected, what it cost in
// provider requests and what failed.
func (s *Server) cloudConnectionRuns(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	id := r.PathValue("id")
	// Reading the connection first keeps "unknown id" a 404 rather than an empty list.
	c, err := s.cloud.Get(r.Context(), p.OrgID, id)
	if err != nil {
		return err
	}
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	if len(scope) > cloudconnect.MaxScopeLen {
		return badRequest("scope must be at most %d characters", cloudconnect.MaxScopeLen)
	}
	limit := cloudconnect.MaxRunsPerResponse
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return badRequest("limit must be a positive integer")
		}
		limit = min(n, cloudconnect.MaxRunsPerResponse)
	}
	runs, err := s.cloud.Runs(r.Context(), p.OrgID, id, scope, limit)
	if err != nil {
		return err
	}
	out := make([]cloudRunJSON, 0, len(runs))
	for _, run := range runs {
		out = append(out, cloudRunResponse(run))
	}
	writeJSON(w, http.StatusOK, map[string]any{"connection": cloudConnectionResponse(c), "runs": out})
	return nil
}

// testCloudConnection makes one provider call with the submitted (or stored) credentials. A rejected
// credential is a normal outcome, not an API failure: the response is 200 with ok=false and the provider's
// message, so the form can show it inline.
func (s *Server) testCloudConnection(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var req cloudconnect.TestRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	err := s.cloud.Test(r.Context(), p.OrgID, req)
	// Errors that are about the request itself (unknown connection, invalid input, no key) stay API errors;
	// everything else is the provider's answer.
	var ve *cloudconnect.ValidationError
	switch {
	case errors.As(err, &ve), errors.Is(err, cloudconnect.ErrNotFound),
		errors.Is(err, cloudconnect.ErrNoCredentials), errors.Is(err, secrets.ErrNoKey):
		return err
	}
	body := map[string]any{"ok": err == nil, "error": ""}
	if err != nil {
		body["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, body)
	return nil
}
