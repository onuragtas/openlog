package api

// Java agent part of the fleet API (docs/contracts/api.md "Fleet", java-agent.md §2).

import (
	"net/http"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/fleet"
)

type fleetJavaOverrideJSON struct {
	Mode      string `json:"mode"`
	UpdatedAt string `json:"updated_at"`
}

type fleetHostJavaJSON struct {
	// Reported is false for agents that do not send the java_agent section (older versions).
	Reported bool `json:"reported"`
	// Mode is the host's effective fleet mode (override or policy); AgentMode what the agent applies.
	Mode      string `json:"mode"`
	AgentMode string `json:"agent_mode"`
	Source    string `json:"source"`
	Capable   bool   `json:"capable"`
	Reason    string `json:"reason"`
	Managed   bool   `json:"managed"`
	// Version is the managed jar's current version (null when none).
	Version *string `json:"version"`
	// State is the agent's status (installed, staged, restart_pending, unmanaged, error, not_found; "" when not reported).
	State     string                 `json:"state"`
	Detail    string                 `json:"detail"`
	LinkPath  string                 `json:"link_path"`
	LinkState string                 `json:"link_state"`
	JVMs      []fleet.JavaJVM        `json:"jvms"`
	Update    *fleet.JavaAgentUpdate `json:"update"`
	Override  *fleetJavaOverrideJSON `json:"override"`
	// Status is the fleet decision for the host now; Target its version.
	Status string  `json:"status"`
	Target *string `json:"status_target"`
}

func javaOverrideResponse(o *fleet.JavaOverride) *fleetJavaOverrideJSON {
	if o == nil {
		return nil
	}
	return &fleetJavaOverrideJSON{Mode: o.Mode, UpdatedAt: formatTime(o.UpdatedAt)}
}

func hostJavaResponse(h fleet.HostView) fleetHostJavaJSON {
	out := fleetHostJavaJSON{Mode: h.Java.Mode, JVMs: []fleet.JavaJVM{}, Override: javaOverrideResponse(h.JavaOverride),
		Status: string(h.Java.Reason), Target: optString(h.Java.Target)}
	if r := h.JavaAgent; r != nil {
		out.Reported, out.AgentMode, out.Source, out.Capable, out.Reason = true, r.Mode, r.Source, r.Capable, r.Reason
		out.Managed, out.Version, out.State, out.Detail = r.Managed, optString(r.CurrentVersion), r.Status, r.Detail
		out.LinkPath, out.LinkState, out.Update = r.LinkPath, r.LinkState, r.Update
		if r.JVMs != nil {
			out.JVMs = r.JVMs
		}
	}
	return out
}

func (s *Server) putHostJavaOverride(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		Mode string `json:"mode"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	o, err := s.fleet.PutJavaOverride(r.Context(), p.OrgID, r.PathValue("host_id"), in.Mode, s.actor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, javaOverrideResponse(&o))
	return nil
}

func (s *Server) deleteHostJavaOverride(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := s.fleet.DeleteJavaOverride(r.Context(), p.OrgID, r.PathValue("host_id"), s.actor(r, p)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
