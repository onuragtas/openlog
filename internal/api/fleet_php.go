package api

// PHP agent part of the fleet API (docs/contracts/api.md "Fleet", php-agent.md §7.3).

import (
	"net/http"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/fleet"
)

type fleetPHPOverrideJSON struct {
	Mode      string `json:"mode"`
	UpdatedAt string `json:"updated_at"`
}

type fleetHostPHPJSON struct {
	// Reported is false for agents that do not send the php_agent section (older versions).
	Reported bool `json:"reported"`
	// Mode is the host's effective fleet mode (override or policy); AgentMode what the agent applies.
	Mode      string                `json:"mode"`
	AgentMode string                `json:"agent_mode"`
	Source    string                `json:"source"`
	Capable   bool                  `json:"capable"`
	Reason    string                `json:"reason"`
	ManagedBy string                `json:"managed_by"`
	Version   *string               `json:"version"`
	Runtimes  []fleet.PHPRuntime    `json:"runtimes"`
	Update    *fleet.PHPAgentUpdate `json:"update"`
	Override  *fleetPHPOverrideJSON `json:"override"`
	Status    string                `json:"status"`
	Target    *string               `json:"status_target"`
}

func phpOverrideResponse(o *fleet.PHPOverride) *fleetPHPOverrideJSON {
	if o == nil {
		return nil
	}
	return &fleetPHPOverrideJSON{Mode: o.Mode, UpdatedAt: formatTime(o.UpdatedAt)}
}

func hostPHPResponse(h fleet.HostView) fleetHostPHPJSON {
	out := fleetHostPHPJSON{Mode: h.PHP.Mode, ManagedBy: "none", Runtimes: []fleet.PHPRuntime{},
		Override: phpOverrideResponse(h.PHPOverride), Status: string(h.PHP.Reason), Target: optString(h.PHP.Target)}
	if r := h.PHPAgent; r != nil {
		out.Reported, out.AgentMode, out.Source, out.Capable, out.Reason = true, r.Mode, r.Source, r.Capable, r.Reason
		if r.ManagedBy != "" {
			out.ManagedBy = r.ManagedBy
		}
		out.Version, out.Update = optString(r.Version), r.Update
		if r.Runtimes != nil {
			out.Runtimes = r.Runtimes
		}
	}
	return out
}

func (s *Server) putHostPHPOverride(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		Mode string `json:"mode"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	o, err := s.fleet.PutPHPOverride(r.Context(), p.OrgID, r.PathValue("host_id"), in.Mode, s.actor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, phpOverrideResponse(&o))
	return nil
}

func (s *Server) deleteHostPHPOverride(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := s.fleet.DeletePHPOverride(r.Context(), p.OrgID, r.PathValue("host_id"), s.actor(r, p)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
