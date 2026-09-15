package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/fleet"
)

// SetFleet enables the fleet management endpoints (docs/contracts/api.md "Fleet"). They also need
// SetAccounts (postgres auth mode). Must be called before Run.
func (s *Server) SetFleet(m *fleet.Manager) {
	s.fleet = m
	s.srv.Handler = s.Handler()
}

type fleetFunc func(w http.ResponseWriter, r *http.Request, p *auth.Principal) error

func (s *Server) fleetRoutes(mux *http.ServeMux) {
	if s.fleet == nil || s.accounts == nil {
		return
	}
	route := func(pattern string, write bool, h fleetFunc) {
		mux.Handle(pattern, s.instrument(pattern, func(rec *statusRecorder, r *http.Request) {
			noStore(rec)
			p, r := s.authenticate(rec, r)
			if p == nil {
				return
			}
			if ae := fleetAllowed(p, write); ae != nil {
				writeError(rec, ae)
				return
			}
			if err := h(rec, r, p); err != nil {
				s.writeFleetError(rec, pattern, err)
			}
		}))
	}
	route("GET /api/v1/fleet/policy", false, s.getFleetPolicy)
	route("PUT /api/v1/fleet/policy", true, s.putFleetPolicy)
	route("GET /api/v1/fleet/summary", false, s.fleetSummary)
	route("GET /api/v1/fleet/hosts", false, s.fleetHosts)
	route("PUT /api/v1/fleet/hosts/{host_id}/override", true, s.putHostOverride)
	route("DELETE /api/v1/fleet/hosts/{host_id}/override", true, s.deleteHostOverride)
	route("PUT /api/v1/fleet/hosts/{host_id}/php-agent", true, s.putHostPHPOverride)         // fleet_php.go
	route("DELETE /api/v1/fleet/hosts/{host_id}/php-agent", true, s.deleteHostPHPOverride)   // fleet_php.go
	route("PUT /api/v1/fleet/hosts/{host_id}/java-agent", true, s.putHostJavaOverride)       // fleet_java.go
	route("DELETE /api/v1/fleet/hosts/{host_id}/java-agent", true, s.deleteHostJavaOverride) // fleet_java.go
	route("GET /api/v1/fleet/rollouts", false, s.fleetRollouts)
	route("POST /api/v1/fleet/rollouts/{id}/pause", true, s.pauseRollout)
	route("POST /api/v1/fleet/rollouts/{id}/resume", true, s.resumeRollout)
	route("POST /api/v1/fleet/rollouts/{id}/deploy-now", true, s.deployRolloutNow)
	route("POST /api/v1/fleet/rollback", true, s.fleetRollback)
}

func fleetAllowed(p *auth.Principal, write bool) *apiError {
	if !p.HasOrg() {
		return &apiError{http.StatusForbidden, "permission_denied", "you are not a member of any organization"}
	}
	if !write {
		if !p.Role.Can(auth.ActReadFleet) {
			return &apiError{http.StatusForbidden, "permission_denied", "your role (" + string(p.Role) + ") does not allow this operation"}
		}
		return nil
	}
	if p.Kind != auth.KindSession {
		return &apiError{http.StatusForbidden, "permission_denied", "this operation requires a signed-in user; API keys are read-only"}
	}
	if !p.Role.Can(auth.ActManageFleet) {
		return &apiError{http.StatusForbidden, "permission_denied", "your role (" + string(p.Role) + ") does not allow this operation"}
	}
	return nil
}

func (s *Server) writeFleetError(w http.ResponseWriter, route string, err error) {
	var (
		ve *fleet.ValidationError
		pe *fleet.PreconditionError
	)
	switch {
	case errors.As(err, &ve):
		writeError(w, &apiError{http.StatusBadRequest, "invalid_argument", ve.Msg})
	case errors.As(err, &pe):
		writeError(w, &apiError{http.StatusConflict, "failed_precondition", pe.Msg})
	case errors.Is(err, fleet.ErrNotFound):
		writeError(w, &apiError{http.StatusNotFound, "not_found", "not found"})
	case errors.Is(err, fleet.ErrConflict):
		writeError(w, &apiError{http.StatusConflict, "failed_precondition", "conflicting change; reload and try again"})
	default:
		s.writeAccountError(w, route, err)
	}
}

func (s *Server) actor(r *http.Request, p *auth.Principal) fleet.Actor {
	return fleet.Actor{UserID: p.UserID, Email: p.Email, IP: s.accounts.Meta(r).IP}
}

// ---- response shapes ----

type fleetPolicyJSON struct {
	fleet.Policy
	IsDefault      bool    `json:"is_default"`
	UpdatedAt      *string `json:"updated_at"`
	UpdatedByEmail string  `json:"updated_by_email"`
}

func policyResponse(sp fleet.StoredPolicy) fleetPolicyJSON {
	if sp.Waves == nil {
		sp.Waves = []int{}
	}
	if sp.MaintenanceWindows == nil {
		sp.MaintenanceWindows = []fleet.Window{}
	}
	for i := range sp.MaintenanceWindows {
		if sp.MaintenanceWindows[i].Days == nil {
			sp.MaintenanceWindows[i].Days = []string{}
		}
	}
	return fleetPolicyJSON{Policy: sp.Policy, IsDefault: sp.IsDefault, UpdatedAt: optTime(sp.UpdatedAt), UpdatedByEmail: sp.UpdatedByEmail}
}

type rolloutJSON struct {
	ID              string            `json:"id"`
	Action          string            `json:"action"`
	FromVersion     *string           `json:"from_version"`
	ToVersion       *string           `json:"to_version"`
	Targets         map[string]string `json:"targets"`
	Waves           []int             `json:"waves"`
	CurrentWave     int               `json:"current_wave"`
	WavePercent     int               `json:"wave_percent"`
	WaveStartedAt   string            `json:"wave_started_at"`
	NextWaveAt      *string           `json:"next_wave_at"`
	WaveSoakMinutes int               `json:"wave_soak_minutes"`
	HaltFailureRate float64           `json:"halt_failure_rate"`
	State           string            `json:"state"`
	StateReason     string            `json:"state_reason"`
	Counters        fleet.Counters    `json:"counters"`
	CreatedByEmail  *string           `json:"created_by_email"`
	CreatedAt       string            `json:"created_at"`
	UpdatedAt       string            `json:"updated_at"`
	EndedAt         *string           `json:"ended_at"`
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func rolloutResponse(r *fleet.Rollout) *rolloutJSON {
	if r == nil {
		return nil
	}
	out := &rolloutJSON{
		ID: r.ID, Action: r.Action, FromVersion: optString(r.FromVersion), ToVersion: optString(r.ToVersion),
		Targets: r.Targets, Waves: r.Waves, CurrentWave: r.CurrentWave, WavePercent: r.WavePercent(),
		WaveStartedAt: formatTime(r.WaveStartedAt), WaveSoakMinutes: r.WaveSoakMinutes, HaltFailureRate: r.HaltFailureRate,
		State: r.State, StateReason: r.StateReason, Counters: r.Counters, CreatedByEmail: optString(r.CreatedByEmail),
		CreatedAt: formatTime(r.CreatedAt), UpdatedAt: formatTime(r.UpdatedAt), EndedAt: optTime(r.EndedAt),
	}
	if out.Targets == nil {
		out.Targets = map[string]string{}
	}
	if out.Waves == nil {
		out.Waves = []int{}
	}
	if r.State == fleet.RolloutActive && r.CurrentWave < len(r.Waves)-1 {
		next := formatTime(r.WaveStartedAt.Add(time.Duration(r.WaveSoakMinutes) * time.Minute))
		out.NextWaveAt = &next
	}
	return out
}

type fleetOverrideJSON struct {
	Action    string  `json:"action"`
	Version   *string `json:"version"`
	UpdatedAt string  `json:"updated_at"`
}

func overrideResponse(o *fleet.Override) *fleetOverrideJSON {
	if o == nil {
		return nil
	}
	return &fleetOverrideJSON{Action: o.Action, Version: optString(o.Version), UpdatedAt: formatTime(o.UpdatedAt)}
}

type fleetHostJSON struct {
	HostID string `json:"host_id"`
	Name   string `json:"host_name"`
	Agent  struct {
		Name          string `json:"name"`
		Version       string `json:"version"`
		Commit        string `json:"commit"`
		OS            string `json:"os"`
		Arch          string `json:"arch"`
		InstallMethod string `json:"install_method"`
		UpdateCapable bool   `json:"update_capable"`
	} `json:"agent"`
	Update struct {
		State       string  `json:"state"`
		FromVersion string  `json:"from_version"`
		ToVersion   string  `json:"to_version"`
		Error       string  `json:"error"`
		ChangedAt   *string `json:"changed_at"`
	} `json:"update"`
	FirstSeenAt  string             `json:"first_seen_at"`
	LastSyncAt   string             `json:"last_sync_at"`
	RolloutID    *string            `json:"rollout_id"`
	Override     *fleetOverrideJSON `json:"override"`
	Outdated     bool               `json:"outdated"`
	Supported    bool               `json:"supported"`
	Status       string             `json:"status"`
	StatusTarget *string            `json:"status_target"`
	PHPAgent     fleetHostPHPJSON   `json:"php_agent"` // fleet_php.go
	// PHPAccess: PHP-FPM pools and their access to php.sock (null when not reported).
	PHPAccess *fleet.PHPAccessReport `json:"php_access"`
	// JavaAgent: JVMs with the openlog Java agent and the managed jar (fleet_java.go).
	JavaAgent fleetHostJavaJSON `json:"java_agent"`
}

func hostResponse(h fleet.HostView) fleetHostJSON {
	var out fleetHostJSON
	out.HostID, out.Name = h.HostID, h.HostName
	out.Agent.Name, out.Agent.Version, out.Agent.Commit = h.AgentName, h.Version, h.Commit
	out.Agent.OS, out.Agent.Arch, out.Agent.InstallMethod, out.Agent.UpdateCapable = h.OS, h.Arch, h.InstallMethod, h.UpdateCapable
	out.Update.State, out.Update.FromVersion, out.Update.ToVersion, out.Update.Error = h.UpdateState, h.UpdateFrom, h.UpdateTo, h.UpdateError
	if !h.UpdateChangedAt.IsZero() {
		t := formatTime(h.UpdateChangedAt)
		out.Update.ChangedAt = &t
	}
	out.FirstSeenAt, out.LastSyncAt = formatTime(h.FirstSeenAt), formatTime(h.LastSyncAt)
	out.RolloutID, out.Override = optString(h.RolloutID), overrideResponse(h.Override)
	out.Outdated, out.Supported, out.Status, out.StatusTarget = h.Outdated, h.Supported, string(h.Status), optString(h.Target)
	out.PHPAgent = hostPHPResponse(h)
	out.PHPAccess = h.PHPAccess
	out.JavaAgent = hostJavaResponse(h)
	return out
}

// ---- handlers ----

func (s *Server) getFleetPolicy(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	sp, err := s.fleet.Policy(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, policyResponse(sp))
	return nil
}

func (s *Server) putFleetPolicy(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in fleet.Policy
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	sp, err := s.fleet.PutPolicy(r.Context(), p.OrgID, in, s.actor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, policyResponse(sp))
	return nil
}

func (s *Server) fleetSummary(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	sum, err := s.fleet.Summary(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, struct {
		fleet.Summary
		CurrentRollout *rolloutJSON `json:"current_rollout"`
	}{sum, rolloutResponse(sum.CurrentRollout)})
	return nil
}

func (s *Server) fleetHosts(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	q := r.URL.Query()
	f := fleet.HostFilter{Version: q.Get("version"), State: q.Get("state"), Query: q.Get("q"), Cursor: q.Get("cursor"), Limit: 100}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return badRequest("limit must be a positive integer")
		}
		f.Limit = min(n, 1000)
	}
	hosts, next, err := s.fleet.Hosts(r.Context(), p.OrgID, f)
	if err != nil {
		return err
	}
	out := make([]fleetHostJSON, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, hostResponse(h))
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosts": out, "next_cursor": optString(next)})
	return nil
}

func (s *Server) putHostOverride(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		Action  string `json:"action"`
		Version string `json:"version"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	o, err := s.fleet.PutOverride(r.Context(), p.OrgID, r.PathValue("host_id"), in.Action, in.Version, s.actor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, overrideResponse(&o))
	return nil
}

func (s *Server) deleteHostOverride(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := s.fleet.DeleteOverride(r.Context(), p.OrgID, r.PathValue("host_id"), s.actor(r, p)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) fleetRollouts(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return badRequest("limit must be a positive integer")
		}
		limit = min(n, 100)
	}
	rs, err := s.fleet.Rollouts(r.Context(), p.OrgID, limit)
	if err != nil {
		return err
	}
	out := make([]*rolloutJSON, 0, len(rs))
	for i := range rs {
		out = append(out, rolloutResponse(&rs[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"rollouts": out})
	return nil
}

func (s *Server) pauseRollout(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	ro, err := s.fleet.Pause(r.Context(), p.OrgID, r.PathValue("id"), s.actor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, rolloutResponse(&ro))
	return nil
}

func (s *Server) resumeRollout(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	ro, err := s.fleet.Resume(r.Context(), p.OrgID, r.PathValue("id"), s.actor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, rolloutResponse(&ro))
	return nil
}

func (s *Server) deployRolloutNow(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	ro, err := s.fleet.DeployNow(r.Context(), p.OrgID, r.PathValue("id"), s.actor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, rolloutResponse(&ro))
	return nil
}

func (s *Server) fleetRollback(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		ToVersion string `json:"to_version"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	ro, err := s.fleet.Rollback(r.Context(), p.OrgID, in.ToVersion, s.actor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, rolloutResponse(&ro))
	return nil
}
