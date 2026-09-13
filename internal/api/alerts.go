package api

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/alert"
	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
)

// SetAlerts enables the alerting endpoints (docs/contracts/api.md "Alerting", alerting.md). They also need
// SetAccounts (postgres auth mode). Must be called before Run.
func (s *Server) SetAlerts(m *alert.Manager) {
	s.alerts = m
	s.srv.Handler = s.Handler()
}

// alertAccess is the permission an alert route needs.
type alertAccess int

const (
	alertRead   alertAccess = iota // any role, API keys too
	alertWrite                     // signed-in member or higher
	alertManage                    // signed-in admin or owner
)

type alertFunc func(w http.ResponseWriter, r *http.Request, p *auth.Principal) error

func (s *Server) alertRoutes(mux *http.ServeMux) {
	if s.alerts == nil || s.accounts == nil {
		return
	}
	route := func(pattern string, access alertAccess, h alertFunc) {
		mux.Handle(pattern, s.instrument(pattern, func(rec *statusRecorder, r *http.Request) {
			noStore(rec)
			p, r := s.authenticate(rec, r)
			if p == nil {
				return
			}
			if ae := alertAllowed(p, access); ae != nil {
				writeError(rec, ae)
				return
			}
			if err := h(rec, r, p); err != nil {
				s.writeAlertError(rec, pattern, err)
			}
		}))
	}
	route("GET /api/v1/alerts/rule-types", alertRead, s.alertRuleTypes)
	route("GET /api/v1/alerts/rules", alertRead, s.listAlertRules)
	route("POST /api/v1/alerts/rules", alertWrite, s.createAlertRule)
	route("GET /api/v1/alerts/rules/{id}", alertRead, s.getAlertRule)
	route("PUT /api/v1/alerts/rules/{id}", alertWrite, s.updateAlertRule)
	route("DELETE /api/v1/alerts/rules/{id}", alertWrite, s.deleteAlertRule)
	route("POST /api/v1/alerts/rules/{id}/enable", alertWrite, s.enableAlertRule)
	route("POST /api/v1/alerts/rules/{id}/disable", alertWrite, s.disableAlertRule)
	// Preview reads telemetry: it runs through wrap, which binds the query scope to the principal's tenant.
	mux.Handle("POST /api/v1/alerts/rules/preview", s.wrap("POST /api/v1/alerts/rules/preview", s.previewAlertRule))
	route("GET /api/v1/alerts/incidents", alertRead, s.listAlertIncidents)
	route("GET /api/v1/alerts/incidents/{id}", alertRead, s.getAlertIncident)
	route("POST /api/v1/alerts/incidents/{id}/acknowledge", alertWrite, s.ackAlertIncident)
	route("POST /api/v1/alerts/incidents/{id}/resolve", alertWrite, s.resolveAlertIncident)
	route("POST /api/v1/alerts/incidents/{id}/notes", alertWrite, s.noteAlertIncident)
	route("GET /api/v1/alerts/channels", alertRead, s.listAlertChannels)
	route("POST /api/v1/alerts/channels", alertManage, s.createAlertChannel)
	route("GET /api/v1/alerts/channels/{id}", alertRead, s.getAlertChannel)
	route("PUT /api/v1/alerts/channels/{id}", alertManage, s.updateAlertChannel)
	route("DELETE /api/v1/alerts/channels/{id}", alertManage, s.deleteAlertChannel)
	route("POST /api/v1/alerts/channels/{id}/test", alertManage, s.testAlertChannel)
	route("GET /api/v1/alerts/mutes", alertRead, s.listAlertMutes)
	route("POST /api/v1/alerts/mutes", alertWrite, s.createAlertMute)
	route("PUT /api/v1/alerts/mutes/{id}", alertWrite, s.updateAlertMute)
	route("DELETE /api/v1/alerts/mutes/{id}", alertWrite, s.deleteAlertMute)
	route("GET /api/v1/alerts/deliveries", alertRead, s.listAlertDeliveries)
}

func alertAllowed(p *auth.Principal, access alertAccess) *apiError {
	denied := func() *apiError {
		return &apiError{http.StatusForbidden, "permission_denied", "your role (" + string(p.Role) + ") does not allow this operation"}
	}
	if !p.HasOrg() {
		return &apiError{http.StatusForbidden, "permission_denied", "you are not a member of any organization"}
	}
	if access == alertRead {
		if !p.Role.Can(auth.ActReadAlerts) {
			return denied()
		}
		return nil
	}
	if p.Kind != auth.KindSession {
		return &apiError{http.StatusForbidden, "permission_denied", "this operation requires a signed-in user; API keys are read-only"}
	}
	action := auth.ActWriteAlerts
	if access == alertManage {
		action = auth.ActManageAlerts
	}
	if !p.Role.Can(action) {
		return denied()
	}
	return nil
}

func (s *Server) writeAlertError(w http.ResponseWriter, route string, err error) {
	var (
		ve *alert.ValidationError
		pe *alert.PreconditionError
		le *alert.LimitError
	)
	switch {
	case errors.As(err, &ve):
		writeError(w, &apiError{http.StatusBadRequest, "invalid_argument", ve.Error()})
	case errors.As(err, &le):
		writeError(w, &apiError{http.StatusBadRequest, "invalid_argument", le.Msg})
	case errors.As(err, &pe):
		writeError(w, &apiError{http.StatusConflict, "failed_precondition", pe.Msg})
	case errors.Is(err, alert.ErrNoSecretsKey):
		writeError(w, &apiError{http.StatusConflict, "failed_precondition", "OPENLOG_SECRETS_KEY is not configured on the server; channels cannot be saved or tested"})
	case errors.Is(err, alert.ErrNotFound):
		writeError(w, &apiError{http.StatusNotFound, "not_found", "not found"})
	case errors.Is(err, alert.ErrConflict):
		writeError(w, &apiError{http.StatusConflict, "failed_precondition", "conflicting change; reload and try again"})
	case errors.Is(err, alert.ErrForbidden):
		writeError(w, &apiError{http.StatusForbidden, "permission_denied", "members can only change rules and mutes they created"})
	default:
		s.writeAccountError(w, route, err)
	}
}

func (s *Server) alertActor(r *http.Request, p *auth.Principal) alert.Actor {
	return alert.Actor{UserID: p.UserID, Email: p.Email, IP: s.accounts.Meta(r).IP}
}

func manageAny(p *auth.Principal) bool { return p.Role.Can(auth.ActManageAlerts) }

// ---- response shapes ----

type alertRuleStatusJSON struct {
	State           string  `json:"state"`
	SeriesPending   int     `json:"series_pending"`
	SeriesFiring    int     `json:"series_firing"`
	OpenIncidents   int     `json:"open_incidents"`
	LastEvaluatedAt *string `json:"last_evaluated_at"`
	LastResult      string  `json:"last_result"`
	LastError       string  `json:"last_error"`
	LastDurationMs  int     `json:"last_duration_ms"`
	NextEvaluation  *string `json:"next_evaluation_at"`
	Owner           *string `json:"owner"`
}

type alertRuleJSON struct {
	ID                      string              `json:"id"`
	Name                    string              `json:"name"`
	Description             string              `json:"description"`
	Type                    string              `json:"type"`
	Severity                string              `json:"severity"`
	Enabled                 bool                `json:"enabled"`
	IntervalSeconds         int                 `json:"interval_seconds"`
	ForSeconds              int                 `json:"for_seconds"`
	RecoveryForSeconds      int                 `json:"recovery_for_seconds"`
	Condition               json.RawMessage     `json:"condition"`
	ChannelIDs              []string            `json:"channel_ids"`
	RenotifyIntervalSeconds int                 `json:"renotify_interval_seconds"`
	Flapping                alert.Flapping      `json:"flapping"`
	RunbookURL              string              `json:"runbook_url"`
	Labels                  map[string]string   `json:"labels"`
	Version                 int                 `json:"version"`
	CreatedByUserID         *string             `json:"created_by_user_id"`
	CreatedByEmail          string              `json:"created_by_email"`
	CreatedAt               string              `json:"created_at"`
	UpdatedAt               string              `json:"updated_at"`
	Status                  alertRuleStatusJSON `json:"status"`
}

func alertRuleResponse(v *alert.RuleView) alertRuleJSON {
	out := alertRuleJSON{
		ID: v.ID, Name: v.Name, Description: v.Description, Type: v.Type, Severity: v.Severity, Enabled: v.Enabled,
		IntervalSeconds: v.IntervalSeconds, ForSeconds: v.ForSeconds, RecoveryForSeconds: v.RecoveryForSeconds,
		Condition: v.ConditionJSON, ChannelIDs: v.ChannelIDs, RenotifyIntervalSeconds: v.RenotifyIntervalSeconds,
		Flapping: v.Flapping, RunbookURL: v.RunbookURL, Labels: nonNilMap(v.Labels), Version: v.Version,
		CreatedByUserID: optString(v.CreatedBy), CreatedByEmail: v.CreatedByEmail, CreatedAt: formatTime(v.CreatedAt), UpdatedAt: formatTime(v.UpdatedAt),
		Status: alertRuleStatusJSON{State: v.Status.State, SeriesPending: v.Status.SeriesPending, SeriesFiring: v.Status.SeriesFiring,
			OpenIncidents: v.Status.OpenIncidents, LastEvaluatedAt: optTime(v.Status.LastEvaluatedAt), LastResult: v.Status.LastResult,
			LastError: v.Status.LastError, LastDurationMs: v.Status.LastDurationMs, NextEvaluation: optTime(v.Status.NextEvalAt), Owner: optString(v.Status.Owner)},
	}
	if out.ChannelIDs == nil {
		out.ChannelIDs = []string{}
	}
	if len(out.Condition) == 0 {
		out.Condition = json.RawMessage("{}")
	}
	return out
}

func optFloat(f float64) *float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return &f
}

func optZeroTime(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	v := formatTime(t)
	return &v
}

type alertSeriesJSON struct {
	SeriesKey       string            `json:"series_key"`
	Labels          map[string]string `json:"labels"`
	State           string            `json:"state"`
	Value           *float64          `json:"value"`
	PendingSince    *string           `json:"pending_since"`
	FiringSince     *string           `json:"firing_since"`
	RecoveringSince *string           `json:"recovering_since"`
	IncidentID      *string           `json:"incident_id"`
	Flapping        bool              `json:"flapping"`
	UpdatedAt       string            `json:"updated_at"`
}

type alertIncidentJSON struct {
	ID                  string            `json:"id"`
	RuleID              *string           `json:"rule_id"`
	RuleName            string            `json:"rule_name"`
	RuleType            string            `json:"rule_type"`
	Severity            string            `json:"severity"`
	State               string            `json:"state"`
	SeriesKey           string            `json:"series_key"`
	Labels              map[string]string `json:"labels"`
	Summary             string            `json:"summary"`
	Value               *float64          `json:"value"`
	LastValue           *float64          `json:"last_value"`
	Threshold           *float64          `json:"threshold"`
	Flapping            bool              `json:"flapping"`
	Muted               bool              `json:"muted"`
	OpenedAt            string            `json:"opened_at"`
	AcknowledgedAt      *string           `json:"acknowledged_at"`
	AcknowledgedByEmail *string           `json:"acknowledged_by_email"`
	ResolvedAt          *string           `json:"resolved_at"`
	ResolvedByEmail     *string           `json:"resolved_by_email"`
	ResolveReason       *string           `json:"resolve_reason"`
	ChannelIDs          []string          `json:"channel_ids"`
}

func alertIncidentResponse(i *alert.Incident) alertIncidentJSON {
	out := alertIncidentJSON{ID: i.ID, RuleID: optString(i.RuleID), RuleName: i.RuleName, RuleType: i.RuleType, Severity: i.Severity,
		State: i.State, SeriesKey: i.SeriesKey, Labels: nonNilMap(i.Labels), Summary: i.Summary, Value: i.Value, LastValue: i.LastValue,
		Threshold: i.Threshold, Flapping: i.Flapping, Muted: i.Muted, OpenedAt: formatTime(i.OpenedAt), AcknowledgedAt: optTime(i.AcknowledgedAt),
		AcknowledgedByEmail: optString(i.AcknowledgedByEmail), ResolvedAt: optTime(i.ResolvedAt), ResolvedByEmail: optString(i.ResolvedByEmail),
		ResolveReason: optString(i.ResolveReason), ChannelIDs: i.ChannelIDs}
	if out.ChannelIDs == nil {
		out.ChannelIDs = []string{}
	}
	return out
}

type alertEventJSON struct {
	ID         int64          `json:"id"`
	At         string         `json:"at"`
	Kind       string         `json:"kind"`
	ActorEmail *string        `json:"actor_email"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details"`
}

func alertEventResponse(e alert.IncidentEvent) alertEventJSON {
	d := e.Details
	if d == nil {
		d = map[string]any{}
	}
	return alertEventJSON{ID: e.ID, At: formatTime(e.At), Kind: e.Kind, ActorEmail: optString(e.ActorEmail), Message: e.Message, Details: d}
}

type alertAttemptJSON struct {
	Attempt    int    `json:"attempt"`
	At         string `json:"at"`
	DurationMs int    `json:"duration_ms"`
	Success    bool   `json:"success"`
	StatusCode int    `json:"status_code"`
	Error      string `json:"error"`
}

type alertDeliveryJSON struct {
	ID             string             `json:"id"`
	IncidentID     *string            `json:"incident_id"`
	RuleID         *string            `json:"rule_id"`
	RuleName       string             `json:"rule_name"`
	ChannelID      *string            `json:"channel_id"`
	ChannelName    string             `json:"channel_name"`
	ChannelType    string             `json:"channel_type"`
	Kind           string             `json:"kind"`
	Status         string             `json:"status"`
	Attempts       int                `json:"attempts"`
	IdempotencyKey string             `json:"idempotency_key"`
	CreatedAt      string             `json:"created_at"`
	FinishedAt     *string            `json:"finished_at"`
	NextAttemptAt  *string            `json:"next_attempt_at"`
	LastError      string             `json:"last_error"`
	AttemptLog     []alertAttemptJSON `json:"attempt_log"`
}

func alertDeliveriesResponse(ds []alert.DeliveryView) []alertDeliveryJSON {
	out := make([]alertDeliveryJSON, 0, len(ds))
	for _, d := range ds {
		j := alertDeliveryJSON{ID: d.ID, IncidentID: optString(d.IncidentID), RuleID: optString(d.RuleID), RuleName: d.RuleName,
			ChannelID: optString(d.ChannelID), ChannelName: d.ChannelName, ChannelType: d.ChannelType, Kind: d.Kind, Status: d.Status,
			Attempts: d.Attempts, IdempotencyKey: d.IdempotencyKey, CreatedAt: formatTime(d.CreatedAt), FinishedAt: optTime(d.FinishedAt),
			LastError: d.LastError, AttemptLog: []alertAttemptJSON{}}
		if d.Status == alert.StatusPending {
			j.NextAttemptAt = optZeroTime(d.NextAttemptAt)
		}
		for _, a := range d.AttemptLog {
			j.AttemptLog = append(j.AttemptLog, alertAttemptJSON{Attempt: a.Attempt, At: formatTime(a.StartedAt),
				DurationMs: int(a.Duration / time.Millisecond), Success: a.Success, StatusCode: a.StatusCode, Error: a.Error})
		}
		out = append(out, j)
	}
	return out
}

type alertChannelJSON struct {
	ID               string              `json:"id"`
	Name             string              `json:"name"`
	Type             string              `json:"type"`
	Enabled          bool                `json:"enabled"`
	Config           alert.ChannelConfig `json:"config"`
	SecretHints      map[string]string   `json:"secret_hints"`
	GeneratedSecrets map[string]string   `json:"generated_secrets,omitempty"`
	CreatedByEmail   string              `json:"created_by_email"`
	CreatedAt        string              `json:"created_at"`
	UpdatedAt        string              `json:"updated_at"`
	LastDelivery     *struct {
		At     string `json:"at"`
		Status string `json:"status"`
		Error  string `json:"error"`
	} `json:"last_delivery"`
}

func alertChannelResponse(c *alert.Channel) alertChannelJSON {
	out := alertChannelJSON{ID: c.ID, Name: c.Name, Type: c.Type, Enabled: c.Enabled, Config: c.Config, SecretHints: nonNilMap(c.SecretHints),
		CreatedByEmail: c.CreatedByEmail, CreatedAt: formatTime(c.CreatedAt), UpdatedAt: formatTime(c.UpdatedAt)}
	if c.LastDelivery != nil {
		out.LastDelivery = &struct {
			At     string `json:"at"`
			Status string `json:"status"`
			Error  string `json:"error"`
		}{formatTime(c.LastDelivery.At), c.LastDelivery.Status, c.LastDelivery.Error}
	}
	return out
}

type alertMuteJSON struct {
	ID              string              `json:"id"`
	Name            string              `json:"name"`
	Comment         string              `json:"comment"`
	StartsAt        string              `json:"starts_at"`
	EndsAt          string              `json:"ends_at"`
	RuleIDs         []string            `json:"rule_ids"`
	Matchers        []alert.MuteMatcher `json:"matchers"`
	Active          bool                `json:"active"`
	CreatedByUserID *string             `json:"created_by_user_id"`
	CreatedByEmail  string              `json:"created_by_email"`
	CreatedAt       string              `json:"created_at"`
	UpdatedAt       string              `json:"updated_at"`
}

func (s *Server) alertMuteResponse(m *alert.Mute) alertMuteJSON {
	out := alertMuteJSON{ID: m.ID, Name: m.Name, Comment: m.Comment, StartsAt: formatTime(m.StartsAt), EndsAt: formatTime(m.EndsAt),
		RuleIDs: m.RuleIDs, Matchers: m.Matchers, Active: m.Active(s.now()), CreatedByUserID: optString(m.CreatedBy),
		CreatedByEmail: m.CreatedByEmail, CreatedAt: formatTime(m.CreatedAt), UpdatedAt: formatTime(m.UpdatedAt)}
	if out.RuleIDs == nil {
		out.RuleIDs = []string{}
	}
	if out.Matchers == nil {
		out.Matchers = []alert.MuteMatcher{}
	}
	return out
}

// ---- rules ----

func (s *Server) alertRuleTypes(w http.ResponseWriter, _ *http.Request, _ *auth.Principal) error {
	writeJSON(w, http.StatusOK, map[string]any{"types": alert.RuleTypes()})
	return nil
}

func (s *Server) listAlertRules(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	q := r.URL.Query()
	f := alert.RuleFilter{Type: q.Get("type")}
	if v := q.Get("enabled"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return badRequest("enabled must be true or false")
		}
		f.Enabled = &b
	}
	rules, err := s.alerts.ListRules(r.Context(), p.OrgID, f)
	if err != nil {
		return err
	}
	out := make([]alertRuleJSON, 0, len(rules))
	for i := range rules {
		out = append(out, alertRuleResponse(&rules[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": out})
	return nil
}

func (s *Server) getAlertRule(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	v, series, err := s.alerts.GetRule(r.Context(), p.OrgID, r.PathValue("id"))
	if err != nil {
		return err
	}
	out := struct {
		alertRuleJSON
		Series []alertSeriesJSON `json:"series"`
	}{alertRuleResponse(v), []alertSeriesJSON{}}
	for _, st := range series {
		out.Series = append(out.Series, alertSeriesJSON{SeriesKey: st.Key, Labels: nonNilMap(st.Labels), State: st.State, Value: optFloat(st.LastValue),
			PendingSince: optZeroTime(st.PendingSince), FiringSince: optZeroTime(st.FiringSince), RecoveringSince: optZeroTime(st.RecoveringSince),
			IncidentID: optString(st.IncidentID), Flapping: st.Incident != nil && st.Incident.Flapping, UpdatedAt: formatTime(maxTime(st.LastSeenAt, st.FiringSince, st.PendingSince))})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func maxTime(ts ...time.Time) time.Time {
	var m time.Time
	for _, t := range ts {
		if t.After(m) {
			m = t
		}
	}
	return m
}

func decodeRuleInput(r *http.Request) (alert.RuleInput, error) {
	var in alert.RuleInput
	err := decodeJSON(r, &in)
	return in, err
}

func (s *Server) createAlertRule(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	in, err := decodeRuleInput(r)
	if err != nil {
		return err
	}
	v, err := s.alerts.CreateRule(r.Context(), p.OrgID, in, s.alertActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, alertRuleResponse(v))
	return nil
}

func (s *Server) updateAlertRule(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	in, err := decodeRuleInput(r)
	if err != nil {
		return err
	}
	v, err := s.alerts.UpdateRule(r.Context(), p.OrgID, r.PathValue("id"), in, s.alertActor(r, p), manageAny(p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, alertRuleResponse(v))
	return nil
}

func (s *Server) setAlertRuleEnabled(w http.ResponseWriter, r *http.Request, p *auth.Principal, enabled bool) error {
	v, err := s.alerts.SetRuleEnabled(r.Context(), p.OrgID, r.PathValue("id"), enabled, s.alertActor(r, p), manageAny(p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, alertRuleResponse(v))
	return nil
}

func (s *Server) enableAlertRule(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	return s.setAlertRuleEnabled(w, r, p, true)
}

func (s *Server) disableAlertRule(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	return s.setAlertRuleEnabled(w, r, p, false)
}

func (s *Server) deleteAlertRule(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := s.alerts.DeleteRule(r.Context(), p.OrgID, r.PathValue("id"), s.alertActor(r, p), manageAny(p)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type previewPointJSON [2]any

func (s *Server) previewAlertRule(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	p, _ := auth.PrincipalFrom(r.Context())
	var in struct {
		Rule  alert.RuleInput `json:"rule"`
		Hours int             `json:"hours"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	res, err := s.alerts.Preview(r.Context(), sc, p.OrgID, in.Rule, in.Hours)
	if err != nil {
		var ve *alert.ValidationError
		var le *alert.LimitError
		switch {
		case errors.As(err, &ve):
			return &apiError{http.StatusBadRequest, "invalid_argument", ve.Error()}
		case errors.As(err, &le):
			return &apiError{http.StatusBadRequest, "invalid_argument", le.Msg}
		}
		return err
	}
	type transitionJSON struct {
		At    string   `json:"at"`
		State string   `json:"state"`
		Value *float64 `json:"value"`
	}
	type incidentJSON struct {
		OpenedAt   string   `json:"opened_at"`
		ResolvedAt *string  `json:"resolved_at"`
		Peak       *float64 `json:"peak"`
	}
	type seriesJSON struct {
		Key         string             `json:"key"`
		Labels      map[string]string  `json:"labels"`
		Points      []previewPointJSON `json:"points"`
		Transitions []transitionJSON   `json:"transitions"`
		Incidents   []incidentJSON     `json:"incidents"`
	}
	out := struct {
		From        string       `json:"from"`
		To          string       `json:"to"`
		StepSeconds int          `json:"step_seconds"`
		Operator    *string      `json:"operator"`
		Threshold   *float64     `json:"threshold"`
		Recovery    *float64     `json:"recovery_threshold"`
		Unit        string       `json:"unit"`
		Series      []seriesJSON `json:"series"`
		Truncated   bool         `json:"truncated"`
		Approximate bool         `json:"approximate"`
	}{From: formatTime(res.From), To: formatTime(res.To), StepSeconds: int(res.Step / time.Second), Unit: res.Unit,
		Threshold: optFloat(res.Judge.Threshold), Recovery: optFloat(res.Judge.Recovery), Series: []seriesJSON{},
		Truncated: res.Truncated, Approximate: res.Approximate}
	if res.HasOperator {
		out.Operator = &res.Judge.Operator
	}
	for _, ps := range res.Series {
		sj := seriesJSON{Key: ps.Key, Labels: nonNilMap(ps.Labels), Points: make([]previewPointJSON, 0, len(ps.Ends)),
			Transitions: []transitionJSON{}, Incidents: []incidentJSON{}}
		for i, e := range ps.Ends {
			sj.Points = append(sj.Points, previewPointJSON{e.UnixMilli(), optFloat(ps.Values[i])})
		}
		for _, t := range ps.Transitions {
			sj.Transitions = append(sj.Transitions, transitionJSON{At: formatTime(t.At), State: t.State, Value: optFloat(t.Value)})
		}
		for _, inc := range ps.Incidents {
			sj.Incidents = append(sj.Incidents, incidentJSON{OpenedAt: formatTime(inc.OpenedAt), ResolvedAt: optTime(inc.ResolvedAt), Peak: optFloat(inc.Peak)})
		}
		out.Series = append(out.Series, sj)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// ---- incidents ----

var incidentStates = map[string]bool{alert.IncidentOpen: true, alert.IncidentAcknowledged: true, alert.IncidentResolved: true}
var severities = map[string]bool{alert.SeverityCritical: true, alert.SeverityWarning: true, alert.SeverityInfo: true}

func boundedLimit(r *http.Request, def, max int) (int, error) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, badRequest("limit must be a positive integer")
	}
	return min(n, max), nil
}

func (s *Server) listAlertIncidents(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	q := r.URL.Query()
	f := alert.IncidentFilter{RuleID: q.Get("rule_id"), Severity: q.Get("severity"), Cursor: q.Get("cursor")}
	for _, st := range strings.Split(q.Get("state"), ",") {
		if st = strings.TrimSpace(st); st == "" {
			continue
		}
		if !incidentStates[st] {
			return badRequest("state must be a comma-separated list of open, acknowledged, resolved")
		}
		f.States = append(f.States, st)
	}
	if f.Severity != "" && !severities[f.Severity] {
		return badRequest("severity must be critical, warning or info")
	}
	var err error
	if f.Limit, err = boundedLimit(r, 50, 500); err != nil {
		return err
	}
	incs, next, counts, err := s.alerts.ListIncidents(r.Context(), p.OrgID, f)
	if err != nil {
		return err
	}
	out := make([]alertIncidentJSON, 0, len(incs))
	for i := range incs {
		out = append(out, alertIncidentResponse(&incs[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"incidents": out, "next_cursor": optString(next),
		"counts": map[string]int{"open": counts.Open, "acknowledged": counts.Acknowledged, "resolved": counts.Resolved}})
	return nil
}

func (s *Server) getAlertIncident(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	inc, events, deliveries, err := s.alerts.GetIncident(r.Context(), p.OrgID, r.PathValue("id"))
	if err != nil {
		return err
	}
	out := struct {
		alertIncidentJSON
		Events     []alertEventJSON    `json:"events"`
		Deliveries []alertDeliveryJSON `json:"deliveries"`
	}{alertIncidentResponse(inc), []alertEventJSON{}, alertDeliveriesResponse(deliveries)}
	for _, e := range events {
		out.Events = append(out.Events, alertEventResponse(e))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) ackAlertIncident(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	inc, err := s.alerts.AcknowledgeIncident(r.Context(), p.OrgID, r.PathValue("id"), s.alertActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, alertIncidentResponse(inc))
	return nil
}

func (s *Server) resolveAlertIncident(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		Note string `json:"note"`
	}
	if r.ContentLength != 0 && r.Header.Get("Content-Type") != "" {
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
	}
	inc, err := s.alerts.ResolveIncident(r.Context(), p.OrgID, r.PathValue("id"), in.Note, s.alertActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, alertIncidentResponse(inc))
	return nil
}

func (s *Server) noteAlertIncident(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		Text string `json:"text"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	e, err := s.alerts.AddIncidentNote(r.Context(), p.OrgID, r.PathValue("id"), in.Text, s.alertActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, alertEventResponse(*e))
	return nil
}

// ---- channels ----

func (s *Server) listAlertChannels(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	chs, err := s.alerts.ListChannels(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	out := make([]alertChannelJSON, 0, len(chs))
	for i := range chs {
		out = append(out, alertChannelResponse(&chs[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": out, "secrets_configured": s.alerts.SecretsConfigured()})
	return nil
}

func (s *Server) getAlertChannel(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	ch, err := s.alerts.GetChannel(r.Context(), p.OrgID, r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, alertChannelResponse(ch))
	return nil
}

func (s *Server) createAlertChannel(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in alert.ChannelInput
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	ch, generated, err := s.alerts.CreateChannel(r.Context(), p.OrgID, in, s.alertActor(r, p))
	if err != nil {
		return err
	}
	out := alertChannelResponse(ch)
	if len(generated) > 0 {
		out.GeneratedSecrets = generated
	}
	writeJSON(w, http.StatusCreated, out)
	return nil
}

func (s *Server) updateAlertChannel(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in alert.ChannelInput
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	ch, err := s.alerts.UpdateChannel(r.Context(), p.OrgID, r.PathValue("id"), in, s.alertActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, alertChannelResponse(ch))
	return nil
}

func (s *Server) deleteAlertChannel(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := s.alerts.DeleteChannel(r.Context(), p.OrgID, r.PathValue("id"), s.alertActor(r, p)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) testAlertChannel(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	res, err := s.alerts.TestChannel(r.Context(), p.OrgID, r.PathValue("id"), s.alertActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": res.Success, "status_code": res.StatusCode, "error": res.Error,
		"duration_ms": int(res.Duration / time.Millisecond), "notification_id": res.NotificationID})
	return nil
}

// ---- mutes ----

func (s *Server) listAlertMutes(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	include := false
	if v := r.URL.Query().Get("include_expired"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return badRequest("include_expired must be true or false")
		}
		include = b
	}
	ms, err := s.alerts.ListMutes(r.Context(), p.OrgID, include)
	if err != nil {
		return err
	}
	out := make([]alertMuteJSON, 0, len(ms))
	for i := range ms {
		out = append(out, s.alertMuteResponse(&ms[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"mutes": out})
	return nil
}

func (s *Server) createAlertMute(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in alert.MuteInput
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	m, err := s.alerts.CreateMute(r.Context(), p.OrgID, in, parseTime, s.alertActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, s.alertMuteResponse(m))
	return nil
}

func (s *Server) updateAlertMute(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in alert.MuteInput
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	m, err := s.alerts.UpdateMute(r.Context(), p.OrgID, r.PathValue("id"), in, parseTime, s.alertActor(r, p), manageAny(p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.alertMuteResponse(m))
	return nil
}

func (s *Server) deleteAlertMute(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := s.alerts.DeleteMute(r.Context(), p.OrgID, r.PathValue("id"), s.alertActor(r, p), manageAny(p)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// ---- deliveries ----

var notificationStatuses = map[string]bool{alert.StatusPending: true, alert.StatusSending: true, alert.StatusDelivered: true,
	alert.StatusFailed: true, alert.StatusSuppressed: true}

func (s *Server) listAlertDeliveries(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	q := r.URL.Query()
	f := alert.DeliveryFilter{ChannelID: q.Get("channel_id"), IncidentID: q.Get("incident_id"), Status: q.Get("status")}
	if f.Status != "" && !notificationStatuses[f.Status] {
		return badRequest("status must be pending, sending, delivered, failed or suppressed")
	}
	var err error
	if f.Limit, err = boundedLimit(r, 100, 500); err != nil {
		return err
	}
	ds, err := s.alerts.ListDeliveries(r.Context(), p.OrgID, f)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": alertDeliveriesResponse(ds)})
	return nil
}
