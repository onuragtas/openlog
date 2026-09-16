package api

import (
	"net/http"

	"github.com/onuragtas/openlog/internal/alert"
	"github.com/onuragtas/openlog/internal/auth"
)

// Notification routing rules (api.md "Alerting", alerting.md §5.6).

type alertRouteWindowJSON struct {
	Timezone  string   `json:"timezone"`
	Days      []string `json:"days"`
	StartTime string   `json:"start_time"`
	EndTime   string   `json:"end_time"`
}

type alertRouteMatchJSON struct {
	Severities []string              `json:"severities"`
	Services   []string              `json:"services"`
	RuleTypes  []string              `json:"rule_types"`
	Labels     []alert.RouteMatcher  `json:"labels"`
	TimeWindow *alertRouteWindowJSON `json:"time_window"`
}

type alertRoutingRuleJSON struct {
	ID             string              `json:"id"`
	Name           string              `json:"name"`
	Position       int                 `json:"position"`
	Enabled        bool                `json:"enabled"`
	IsDefault      bool                `json:"is_default"`
	Match          alertRouteMatchJSON `json:"match"`
	ChannelIDs     []string            `json:"channel_ids"`
	CreatedByEmail string              `json:"created_by_email"`
	CreatedAt      string              `json:"created_at"`
	UpdatedAt      string              `json:"updated_at"`
}

func orEmptyStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func alertRoutingRuleResponse(r *alert.RoutingRule) alertRoutingRuleJSON {
	out := alertRoutingRuleJSON{ID: r.ID, Name: r.Name, Position: r.Position, Enabled: r.Enabled, IsDefault: r.IsDefault,
		ChannelIDs: orEmptyStrings(r.ChannelIDs), CreatedByEmail: r.CreatedByEmail, CreatedAt: formatTime(r.CreatedAt),
		UpdatedAt: formatTime(r.UpdatedAt)}
	out.Match = alertRouteMatchJSON{Severities: orEmptyStrings(r.Match.Severities), Services: orEmptyStrings(r.Match.Services),
		RuleTypes: orEmptyStrings(r.Match.RuleTypes), Labels: r.Match.Labels}
	if out.Match.Labels == nil {
		out.Match.Labels = []alert.RouteMatcher{}
	}
	if w := r.Match.TimeWindow; w != nil {
		out.Match.TimeWindow = &alertRouteWindowJSON{Timezone: w.Timezone, Days: orEmptyStrings(w.Days), StartTime: w.StartTime, EndTime: w.EndTime}
	}
	return out
}

func alertRoutingRulesResponse(rs []alert.RoutingRule) []alertRoutingRuleJSON {
	out := make([]alertRoutingRuleJSON, 0, len(rs))
	for i := range rs {
		out = append(out, alertRoutingRuleResponse(&rs[i]))
	}
	return out
}

func (s *Server) alertRoutingRoutes(route func(pattern string, access alertAccess, h alertFunc)) {
	route("GET /api/v1/alerts/routing-rules", alertRead, s.listAlertRoutingRules)
	route("POST /api/v1/alerts/routing-rules", alertManage, s.createAlertRoutingRule)
	route("POST /api/v1/alerts/routing-rules/reorder", alertManage, s.reorderAlertRoutingRules)
	route("GET /api/v1/alerts/routing-rules/{id}", alertRead, s.getAlertRoutingRule)
	route("PUT /api/v1/alerts/routing-rules/{id}", alertManage, s.updateAlertRoutingRule)
	route("DELETE /api/v1/alerts/routing-rules/{id}", alertManage, s.deleteAlertRoutingRule)
}

func (s *Server) listAlertRoutingRules(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	rs, err := s.alerts.ListRoutingRules(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"routing_rules": alertRoutingRulesResponse(rs)})
	return nil
}

func (s *Server) getAlertRoutingRule(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	rr, err := s.alerts.GetRoutingRule(r.Context(), p.OrgID, r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, alertRoutingRuleResponse(rr))
	return nil
}

func (s *Server) createAlertRoutingRule(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in alert.RoutingRuleInput
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	rr, err := s.alerts.CreateRoutingRule(r.Context(), p.OrgID, in, s.alertActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, alertRoutingRuleResponse(rr))
	return nil
}

func (s *Server) updateAlertRoutingRule(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in alert.RoutingRuleInput
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	rr, err := s.alerts.UpdateRoutingRule(r.Context(), p.OrgID, r.PathValue("id"), in, s.alertActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, alertRoutingRuleResponse(rr))
	return nil
}

func (s *Server) deleteAlertRoutingRule(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := s.alerts.DeleteRoutingRule(r.Context(), p.OrgID, r.PathValue("id"), s.alertActor(r, p)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// reorderAlertRoutingRules sets the evaluation order; ids must list every routing rule of the organization once.
func (s *Server) reorderAlertRoutingRules(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		IDs []string `json:"ids"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	rs, err := s.alerts.ReorderRoutingRules(r.Context(), p.OrgID, in.IDs, s.alertActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"routing_rules": alertRoutingRulesResponse(rs)})
	return nil
}
