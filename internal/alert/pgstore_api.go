package alert

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/onuragtas/openlog/internal/alert/secrets"
)

// Actor is who performed an API change: a signed-in user, or an API key acting
// with its own role (D-133), in which case UserID and Email are empty.
type Actor struct {
	UserID     string
	Email      string
	IP         string
	APIKeyID   string
	APIKeyName string
}

// RuleStatus is the evaluation status shown with a rule.
type RuleStatus struct {
	State           string
	SeriesPending   int
	SeriesFiring    int
	OpenIncidents   int
	LastEvaluatedAt *time.Time
	LastResult      string
	LastError       string
	LastDurationMs  int
	NextEvalAt      *time.Time
	Owner           string
}

// RuleView is a rule with its status.
type RuleView struct {
	Rule
	Status RuleStatus
}

// RuleFilter filters the rule list.
type RuleFilter struct {
	Type    string
	Enabled *bool
}

// IncidentFilter filters the incident list.
type IncidentFilter struct {
	States   []string
	RuleID   string
	Severity string
	Limit    int
	Cursor   string
}

// IncidentCounts are incident counts by state (resolved: last 7 days).
type IncidentCounts struct {
	Open, Acknowledged, Resolved int
}

// DeliveryFilter filters the delivery log.
type DeliveryFilter struct {
	ChannelID  string
	IncidentID string
	Status     string
	Limit      int
}

// DeliveryView is a notification with its delivery attempts.
type DeliveryView struct {
	Notification
	RuleName    string
	ChannelName string
	AttemptLog  []Attempt
}

func audit(b *pgx.Batch, orgID string, actor Actor, action, targetType, targetID string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	b.Queue(`INSERT INTO audit_log (org_id, actor_user_id, actor_email, actor_api_key_id, actor_api_key_name,
		action, target_type, target_id, details, ip)
		VALUES ($1, $2, $3, $4::uuid, $5, $6, $7, $8, $9, $10)`, orgID, nullID(actor.UserID), actor.Email,
		nullID(actor.APIKeyID), actor.APIKeyName, action, targetType, targetID, details, actor.IP)
}

func sendBatch(ctx context.Context, tx pgx.Tx, b *pgx.Batch) error {
	if b.Len() == 0 {
		return nil
	}
	return mapPGErr(tx.SendBatch(ctx, b).Close())
}

// ---- rules ----

const ruleStatusColumns = `, COALESCE(l.owner, ''), COALESCE(l.lease_until > now(), false), l.next_eval_at, l.last_evaluated_at,
	COALESCE(l.last_result, ''), COALESCE(l.last_error, ''), COALESCE(l.last_duration_ms, 0),
	(SELECT count(*) FROM alert_series_state s WHERE s.rule_id = r.id AND s.state = 'pending'),
	(SELECT count(*) FROM alert_series_state s WHERE s.rule_id = r.id AND s.state = 'firing'),
	(SELECT count(*) FROM alert_incidents i WHERE i.rule_id = r.id AND i.state <> 'resolved')`

func scanRuleView(row pgx.Row) (*RuleView, error) {
	var (
		v                    RuleView
		leaseValid           bool
		next, last           *time.Time
		pending, firing, inc int64
	)
	r, err := scanRule(row, &v.Status.Owner, &leaseValid, &next, &last, &v.Status.LastResult, &v.Status.LastError,
		&v.Status.LastDurationMs, &pending, &firing, &inc)
	if err != nil {
		return nil, err
	}
	v.Rule = *r
	v.Status.SeriesPending, v.Status.SeriesFiring, v.Status.OpenIncidents = int(pending), int(firing), int(inc)
	v.Status.LastEvaluatedAt, v.Status.NextEvalAt = last, next
	if !leaseValid {
		v.Status.Owner = ""
	}
	switch {
	case !r.Enabled:
		v.Status.State = "disabled"
	case v.Status.SeriesFiring > 0:
		v.Status.State = StateFiring
	case v.Status.LastResult == "error":
		v.Status.State = "error"
	case v.Status.SeriesPending > 0:
		v.Status.State = StatePending
	case last == nil:
		v.Status.State = "unknown"
	default:
		v.Status.State = StateOK
	}
	if !r.Enabled {
		v.Status.NextEvalAt = nil
	}
	return &v, nil
}

const ruleViewFrom = ruleFrom + ` LEFT JOIN alert_rule_leases l ON l.rule_id = r.id`

func (s *PGStore) CountRules(ctx context.Context, orgID string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM alert_rules WHERE org_id = $1`, orgID).Scan(&n)
	return n, err
}

func (s *PGStore) ListRules(ctx context.Context, orgID string, f RuleFilter) ([]RuleView, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+ruleColumns+ruleStatusColumns+ruleViewFrom+`
		WHERE r.org_id = $1 AND ($2 = '' OR r.type = $2) AND ($3::boolean IS NULL OR r.enabled = $3)
		ORDER BY r.name, r.id`, orgID, f.Type, f.Enabled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RuleView{}
	for rows.Next() {
		v, err := scanRuleView(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

func (s *PGStore) GetRule(ctx context.Context, orgID, id string) (*RuleView, []SeriesState, error) {
	if !ValidUUID(id) {
		return nil, nil, ErrNotFound
	}
	v, err := scanRuleView(s.pool.QueryRow(ctx, `SELECT `+ruleColumns+ruleStatusColumns+ruleViewFrom+` WHERE r.org_id = $1 AND r.id = $2`, orgID, id))
	if err != nil {
		return nil, nil, err
	}
	states, err := s.LoadSeries(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	out := make([]SeriesState, 0, len(states))
	for _, st := range states {
		if st.State != StateOK {
			out = append(out, st)
		}
	}
	return v, out, nil
}

func checkChannels(ctx context.Context, tx pgx.Tx, orgID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM alert_channels WHERE org_id = $1 AND id = ANY($2::uuid[])`, orgID, ids).Scan(&n); err != nil {
		return err
	}
	if n != len(ids) {
		return invalid("channel_ids", "unknown channel")
	}
	return nil
}

func (s *PGStore) CreateRule(ctx context.Context, orgID string, d *Definition, actor Actor) (*RuleView, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := checkChannels(ctx, tx, orgID, d.ChannelIDs); err != nil {
		return nil, err
	}
	id := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO alert_rules (id, org_id, name, description, type, severity, enabled, interval_seconds, for_seconds,
		recovery_for_seconds, condition, renotify_interval_seconds, flapping, runbook_url, labels, created_by, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $16)`,
		id, orgID, d.Name, d.Description, d.Type, d.Severity, d.Enabled, d.IntervalSeconds, d.ForSeconds, d.RecoveryForSeconds,
		d.ConditionJSON, d.RenotifyIntervalSeconds, d.Flapping, d.RunbookURL, nonNilLabels(d.Labels), nullID(actor.UserID)); err != nil {
		return nil, mapPGErr(err)
	}
	b := &pgx.Batch{}
	for _, cid := range d.ChannelIDs {
		b.Queue(`INSERT INTO alert_rule_channels (rule_id, channel_id) VALUES ($1, $2)`, id, cid)
	}
	b.Queue(`INSERT INTO alert_rule_leases (rule_id, org_id, next_eval_at) VALUES ($1, $2, now())`, id, orgID)
	audit(b, orgID, actor, "alert.rule.create", "alert_rule", id, map[string]any{"name": d.Name, "type": d.Type})
	if err := sendBatch(ctx, tx, b); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	v, _, err := s.GetRule(ctx, orgID, id)
	return v, err
}

// resolveRuleIncidents resolves the open incidents of a rule inside tx (the lease row must be locked) and clears its
// series state.
func resolveRuleIncidents(ctx context.Context, tx pgx.Tx, b *pgx.Batch, r *Rule, reason string, actor Actor, publicURL string) error {
	rows, err := tx.Query(ctx, `SELECT `+incidentColumns+incidentFrom+` WHERE i.rule_id = $1 AND i.state <> 'resolved' FOR UPDATE OF i`, r.ID)
	if err != nil {
		return err
	}
	var open []*Incident
	for rows.Next() {
		inc, err := scanIncident(rows)
		if err != nil {
			rows.Close()
			return err
		}
		open = append(open, inc)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, inc := range open {
		b.Queue(`UPDATE alert_incidents SET state = 'resolved', resolved_at = $2, resolved_by = $3, resolve_reason = $4, updated_at = now()
			WHERE id = $1`, inc.ID, now, nullID(actor.UserID), reason)
		queueEvents(b, []IncidentEvent{{IncidentID: inc.ID, OrgID: r.OrgID, At: now, Kind: EventResolved, ActorUserID: actor.UserID,
			ActorEmail: actor.Email, Message: reason, Details: map[string]any{"reason": reason}}})
		inc.State, inc.ResolvedAt, inc.ResolveReason = IncidentResolved, &now, reason
		queueNotifications(b, resolveNotifications(r, inc, publicURL, uuid.NewString))
	}
	b.Queue(`DELETE FROM alert_series_state WHERE rule_id = $1`, r.ID)
	return nil
}

func lockRule(ctx context.Context, tx pgx.Tx, orgID, id string) (*Rule, error) {
	if !ValidUUID(id) {
		return nil, ErrNotFound
	}
	// Lease row first, then the rule row: the same order as evaluator commits.
	if _, _, _, _, err := lockLease(ctx, tx, id); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return scanRule(tx.QueryRow(ctx, `SELECT `+ruleColumns+ruleFrom+` WHERE r.org_id = $1 AND r.id = $2 FOR UPDATE OF r`, orgID, id))
}

func (s *PGStore) UpdateRule(ctx context.Context, orgID, id string, d *Definition, expectedVersion int, actor Actor, publicURL string) (*RuleView, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	old, err := lockRule(ctx, tx, orgID, id)
	if err != nil {
		return nil, err
	}
	if expectedVersion != 0 && expectedVersion != old.Version {
		return nil, ErrConflict
	}
	if err := checkChannels(ctx, tx, orgID, d.ChannelIDs); err != nil {
		return nil, err
	}
	b := &pgx.Batch{}
	changed := ConditionChanged(old, d)
	if changed {
		if err := resolveRuleIncidents(ctx, tx, b, old, ReasonRuleChanged, actor, publicURL); err != nil {
			return nil, err
		}
	}
	if !d.Enabled && old.Enabled && !changed {
		if err := resolveRuleIncidents(ctx, tx, b, old, ReasonRuleDisabled, actor, publicURL); err != nil {
			return nil, err
		}
	}
	b.Queue(`UPDATE alert_rules SET name = $3, description = $4, type = $5, severity = $6, enabled = $7, interval_seconds = $8,
		for_seconds = $9, recovery_for_seconds = $10, condition = $11, renotify_interval_seconds = $12, flapping = $13, runbook_url = $14,
		labels = $15, updated_by = $16, version = version + 1, updated_at = now() WHERE org_id = $1 AND id = $2`,
		orgID, id, d.Name, d.Description, d.Type, d.Severity, d.Enabled, d.IntervalSeconds, d.ForSeconds, d.RecoveryForSeconds,
		d.ConditionJSON, d.RenotifyIntervalSeconds, d.Flapping, d.RunbookURL, nonNilLabels(d.Labels), nullID(actor.UserID))
	b.Queue(`DELETE FROM alert_rule_channels WHERE rule_id = $1`, id)
	for _, cid := range d.ChannelIDs {
		b.Queue(`INSERT INTO alert_rule_channels (rule_id, channel_id) VALUES ($1, $2)`, id, cid)
	}
	b.Queue(`INSERT INTO alert_rule_leases (rule_id, org_id) VALUES ($1, $2) ON CONFLICT (rule_id) DO NOTHING`, id, orgID)
	if changed || (d.Enabled && !old.Enabled) {
		b.Queue(`UPDATE alert_rule_leases SET next_eval_at = now(), updated_at = now() WHERE rule_id = $1`, id)
	}
	audit(b, orgID, actor, "alert.rule.update", "alert_rule", id, map[string]any{"name": d.Name, "condition_changed": changed})
	if err := sendBatch(ctx, tx, b); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	v, _, err := s.GetRule(ctx, orgID, id)
	return v, err
}

func (s *PGStore) SetRuleEnabled(ctx context.Context, orgID, id string, enabled bool, actor Actor, publicURL string) (*RuleView, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	old, err := lockRule(ctx, tx, orgID, id)
	if err != nil {
		return nil, err
	}
	b := &pgx.Batch{}
	if old.Enabled != enabled {
		if !enabled {
			if err := resolveRuleIncidents(ctx, tx, b, old, ReasonRuleDisabled, actor, publicURL); err != nil {
				return nil, err
			}
		}
		b.Queue(`UPDATE alert_rules SET enabled = $2, version = version + 1, updated_by = $3, updated_at = now() WHERE id = $1`, id, enabled, nullID(actor.UserID))
		if enabled {
			b.Queue(`UPDATE alert_rule_leases SET next_eval_at = now(), updated_at = now() WHERE rule_id = $1`, id)
		}
		action := "alert.rule.disable"
		if enabled {
			action = "alert.rule.enable"
		}
		audit(b, orgID, actor, action, "alert_rule", id, map[string]any{"name": old.Name})
	}
	if err := sendBatch(ctx, tx, b); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	v, _, err := s.GetRule(ctx, orgID, id)
	return v, err
}

func (s *PGStore) DeleteRule(ctx context.Context, orgID, id string, actor Actor, publicURL string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	old, err := lockRule(ctx, tx, orgID, id)
	if err != nil {
		return err
	}
	b := &pgx.Batch{}
	if err := resolveRuleIncidents(ctx, tx, b, old, ReasonRuleDeleted, actor, publicURL); err != nil {
		return err
	}
	b.Queue(`DELETE FROM alert_rules WHERE id = $1`, id)
	audit(b, orgID, actor, "alert.rule.delete", "alert_rule", id, map[string]any{"name": old.Name})
	if err := sendBatch(ctx, tx, b); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---- incidents ----

func encodeCursor(t time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(t.UTC().Format(time.RFC3339Nano) + "|" + id))
}

func decodeCursor(c string) (time.Time, string, bool) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, "", false
	}
	ts, id, ok := strings.Cut(string(b), "|")
	t, err := time.Parse(time.RFC3339Nano, ts)
	if !ok || err != nil || !ValidUUID(id) {
		return time.Time{}, "", false
	}
	return t, id, true
}

func (s *PGStore) ListIncidents(ctx context.Context, orgID string, f IncidentFilter) ([]Incident, string, IncidentCounts, error) {
	var counts IncidentCounts
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE state = 'open'), count(*) FILTER (WHERE state = 'acknowledged'),
		count(*) FILTER (WHERE state = 'resolved' AND resolved_at > now() - interval '7 days') FROM alert_incidents WHERE org_id = $1`, orgID).
		Scan(&counts.Open, &counts.Acknowledged, &counts.Resolved); err != nil {
		return nil, "", counts, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	var (
		curT  *time.Time
		curID *string
	)
	if f.Cursor != "" {
		t, id, ok := decodeCursor(f.Cursor)
		if !ok {
			return nil, "", counts, invalid("cursor", "invalid cursor")
		}
		curT, curID = &t, &id
	}
	states := f.States
	if states == nil {
		states = []string{}
	}
	ruleID := ""
	if f.RuleID != "" {
		if !ValidUUID(f.RuleID) {
			return []Incident{}, "", counts, nil
		}
		ruleID = f.RuleID
	}
	rows, err := s.pool.Query(ctx, `SELECT `+incidentColumns+incidentFrom+`
		WHERE i.org_id = $1 AND (cardinality($2::text[]) = 0 OR i.state = ANY($2)) AND ($3 = '' OR i.rule_id::text = $3)
		  AND ($4 = '' OR i.severity = $4) AND ($5::timestamptz IS NULL OR (i.opened_at, i.id) < ($5, $6::uuid))
		ORDER BY i.opened_at DESC, i.id DESC LIMIT $7`, orgID, states, ruleID, f.Severity, curT, curID, limit+1)
	if err != nil {
		return nil, "", counts, err
	}
	defer rows.Close()
	out := []Incident{}
	for rows.Next() {
		inc, err := scanIncident(rows)
		if err != nil {
			return nil, "", counts, err
		}
		out = append(out, *inc)
	}
	if err := rows.Err(); err != nil {
		return nil, "", counts, err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = encodeCursor(last.OpenedAt, last.ID)
	}
	if err := s.markMuted(ctx, orgID, out); err != nil {
		return nil, "", counts, err
	}
	return out, next, counts, nil
}

func (s *PGStore) markMuted(ctx context.Context, orgID string, incs []Incident) error {
	now := time.Now()
	mutes, err := s.ActiveMutes(ctx, orgID, now)
	if err != nil || len(mutes) == 0 {
		return err
	}
	for i := range incs {
		if incs[i].State != IncidentResolved && MatchingMute(mutes, incs[i].RuleID, incs[i].Labels, now) != nil {
			incs[i].Muted = true
		}
	}
	return nil
}

func (s *PGStore) GetIncident(ctx context.Context, orgID, id string) (*Incident, []IncidentEvent, []DeliveryView, error) {
	if !ValidUUID(id) {
		return nil, nil, nil, ErrNotFound
	}
	inc, err := scanIncident(s.pool.QueryRow(ctx, `SELECT `+incidentColumns+incidentFrom+` WHERE i.org_id = $1 AND i.id = $2`, orgID, id))
	if err != nil {
		return nil, nil, nil, err
	}
	one := []Incident{*inc}
	if err := s.markMuted(ctx, orgID, one); err != nil {
		return nil, nil, nil, err
	}
	*inc = one[0]
	rows, err := s.pool.Query(ctx, `SELECT id, incident_id::text, org_id::text, at, kind, COALESCE(actor_user_id::text, ''), actor_email,
		message, details FROM alert_incident_events WHERE incident_id = $1 ORDER BY at, id`, id)
	if err != nil {
		return nil, nil, nil, err
	}
	events := []IncidentEvent{}
	for rows.Next() {
		var e IncidentEvent
		if err := rows.Scan(&e.ID, &e.IncidentID, &e.OrgID, &e.At, &e.Kind, &e.ActorUserID, &e.ActorEmail, &e.Message, &e.Details); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		events = append(events, e)
	}
	rows.Close()
	deliveries, err := s.ListDeliveries(ctx, orgID, DeliveryFilter{IncidentID: id, Limit: 500})
	if err != nil {
		return nil, nil, nil, err
	}
	return inc, events, deliveries, nil
}

func (s *PGStore) incidentRule(ctx context.Context, tx pgx.Tx, inc *Incident) (*Rule, error) {
	if inc.RuleID != "" {
		r, err := scanRule(tx.QueryRow(ctx, `SELECT `+ruleColumns+ruleFrom+` WHERE r.id = $1`, inc.RuleID))
		if err == nil {
			return r, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	r := &Rule{OrgID: inc.OrgID}
	r.Name, r.Type, r.Severity = inc.RuleName, inc.RuleType, inc.Severity
	_ = tx.QueryRow(ctx, `SELECT name FROM organizations WHERE id = $1`, inc.OrgID).Scan(&r.OrgName)
	return r, nil
}

// channelTypes returns id → type for the channels of an organization among ids.
func channelTypes(ctx context.Context, tx pgx.Tx, orgID string, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text, type FROM alert_channels WHERE org_id = $1 AND id = ANY($2::uuid[])`, orgID, ids)
	if err != nil {
		return nil, mapPGErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, typ string
		if err := rows.Scan(&id, &typ); err != nil {
			return nil, err
		}
		out[id] = typ
	}
	return out, rows.Err()
}

func (s *PGStore) AcknowledgeIncident(ctx context.Context, orgID, id string, actor Actor, publicURL string) (*Incident, error) {
	if !ValidUUID(id) {
		return nil, ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	inc, err := scanIncident(tx.QueryRow(ctx, `SELECT `+incidentColumns+incidentFrom+` WHERE i.org_id = $1 AND i.id = $2 FOR UPDATE OF i`, orgID, id))
	if err != nil {
		return nil, err
	}
	switch inc.State {
	case IncidentResolved:
		return nil, &PreconditionError{Msg: "the incident is already resolved"}
	case IncidentOpen:
		rule, err := s.incidentRule(ctx, tx, inc)
		if err != nil {
			return nil, err
		}
		types, err := channelTypes(ctx, tx, orgID, inc.ChannelIDs)
		if err != nil {
			return nil, err
		}
		b := &pgx.Batch{}
		now := time.Now().UTC()
		b.Queue(`UPDATE alert_incidents SET state = 'acknowledged', acknowledged_at = $2, acknowledged_by = $3, updated_at = now() WHERE id = $1`,
			id, now, nullID(actor.UserID))
		queueEvents(b, []IncidentEvent{{IncidentID: id, OrgID: orgID, At: now, Kind: EventAcknowledged, ActorUserID: actor.UserID, ActorEmail: actor.Email,
			Message: "acknowledged by " + actor.Email}})
		// On-call channels are told that someone took over (§5.1); the other channel types stay silent.
		ack := *inc
		ack.State, ack.AcknowledgedAt, ack.AcknowledgedByEmail = IncidentAcknowledged, &now, actor.Email
		queueNotifications(b, ackNotifications(rule, &ack, types, publicURL, uuid.NewString))
		audit(b, orgID, actor, "alert.incident.acknowledge", "alert_incident", id, map[string]any{"rule_name": inc.RuleName})
		if err := sendBatch(ctx, tx, b); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
	}
	got, _, _, err := s.GetIncident(ctx, orgID, id)
	return got, err
}

func (s *PGStore) ResolveIncident(ctx context.Context, orgID, id, note string, actor Actor, publicURL string) (*Incident, error) {
	if !ValidUUID(id) {
		return nil, ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var ruleID string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(rule_id::text, '') FROM alert_incidents WHERE org_id = $1 AND id = $2`, orgID, id).Scan(&ruleID); err != nil {
		return nil, mapPGErr(err)
	}
	if ruleID != "" {
		if _, _, _, _, err := lockLease(ctx, tx, ruleID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	inc, err := scanIncident(tx.QueryRow(ctx, `SELECT `+incidentColumns+incidentFrom+` WHERE i.id = $1 FOR UPDATE OF i`, id))
	if err != nil {
		return nil, err
	}
	if inc.State == IncidentResolved {
		return nil, &PreconditionError{Msg: "the incident is already resolved"}
	}
	rule, err := s.incidentRule(ctx, tx, inc)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	b := &pgx.Batch{}
	b.Queue(`UPDATE alert_incidents SET state = 'resolved', resolved_at = $2, resolved_by = $3, resolve_reason = 'manual', updated_at = now() WHERE id = $1`,
		id, now, nullID(actor.UserID))
	if ruleID != "" {
		b.Queue(`DELETE FROM alert_series_state WHERE rule_id = $1 AND incident_id = $2`, ruleID, id)
	}
	msg := "resolved by " + actor.Email
	if note = strings.TrimSpace(note); note != "" {
		msg += ": " + note
	}
	queueEvents(b, []IncidentEvent{{IncidentID: id, OrgID: orgID, At: now, Kind: EventResolved, ActorUserID: actor.UserID, ActorEmail: actor.Email,
		Message: msg, Details: map[string]any{"reason": ReasonManual}}})
	inc.State, inc.ResolvedAt, inc.ResolveReason, inc.ResolvedByEmail = IncidentResolved, &now, ReasonManual, actor.Email
	queueNotifications(b, resolveNotifications(rule, inc, publicURL, uuid.NewString))
	audit(b, orgID, actor, "alert.incident.resolve", "alert_incident", id, map[string]any{"rule_name": inc.RuleName})
	if err := sendBatch(ctx, tx, b); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	got, _, _, err := s.GetIncident(ctx, orgID, id)
	return got, err
}

func (s *PGStore) AddIncidentNote(ctx context.Context, orgID, id, text string, actor Actor) (*IncidentEvent, error) {
	if !ValidUUID(id) {
		return nil, ErrNotFound
	}
	e := IncidentEvent{IncidentID: id, OrgID: orgID, Kind: EventNote, ActorUserID: actor.UserID, ActorEmail: actor.Email, Message: text, Details: map[string]any{}}
	err := s.pool.QueryRow(ctx, `INSERT INTO alert_incident_events (incident_id, org_id, kind, actor_user_id, actor_email, message, details)
		SELECT i.id, i.org_id, 'note', $3, $4, $5, '{}'::jsonb FROM alert_incidents i WHERE i.org_id = $1 AND i.id = $2
		RETURNING id, at`, orgID, id, nullID(actor.UserID), actor.Email, text).Scan(&e.ID, &e.At)
	if err != nil {
		return nil, mapPGErr(err)
	}
	return &e, nil
}

// ---- channels ----

const lastDeliveryColumns = `, ld.at, COALESCE(ld.status, ''), COALESCE(ld.error, '')`

const lastDeliveryJoin = ` LEFT JOIN LATERAL (SELECT n.updated_at AS at, n.status, n.last_error AS error FROM alert_notifications n
	WHERE n.channel_id = c.id AND n.status <> 'pending' ORDER BY n.updated_at DESC LIMIT 1) ld ON true`

func scanChannelView(row pgx.Row) (*Channel, error) {
	var (
		at            *time.Time
		status, error string
	)
	c, err := scanChannel(row, &at, &status, &error)
	if err != nil {
		return nil, err
	}
	if at != nil {
		c.LastDelivery = &DeliverySummary{At: *at, Status: status, Error: error}
	}
	return c, nil
}

func (s *PGStore) ListChannels(ctx context.Context, orgID string) ([]Channel, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+channelColumns+lastDeliveryColumns+channelFrom+lastDeliveryJoin+` WHERE c.org_id = $1 ORDER BY c.name, c.id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Channel{}
	for rows.Next() {
		c, err := scanChannelView(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *PGStore) GetChannel(ctx context.Context, orgID, id string) (*Channel, error) {
	if !ValidUUID(id) {
		return nil, ErrNotFound
	}
	return scanChannelView(s.pool.QueryRow(ctx, `SELECT `+channelColumns+lastDeliveryColumns+channelFrom+lastDeliveryJoin+` WHERE c.org_id = $1 AND c.id = $2`, orgID, id))
}

func (s *PGStore) CreateChannel(ctx context.Context, orgID, id string, p *PreparedChannel, stored, keyID string, actor Actor) (*Channel, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	b := &pgx.Batch{}
	b.Queue(`INSERT INTO alert_channels (id, org_id, name, type, enabled, config, secrets, secrets_key_id, secret_hints, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, id, orgID, p.Name, p.Type, p.Enabled, p.Config, stored, keyID, p.Hints, nullID(actor.UserID))
	audit(b, orgID, actor, "alert.channel.create", "alert_channel", id, map[string]any{"name": p.Name, "type": p.Type})
	if err := sendBatch(ctx, tx, b); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetChannel(ctx, orgID, id)
}

func (s *PGStore) UpdateChannel(ctx context.Context, orgID, id string, p *PreparedChannel, stored, keyID string, actor Actor) (*Channel, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	tag, err := tx.Exec(ctx, `UPDATE alert_channels SET name = $3, enabled = $4, config = $5, secrets = $6, secrets_key_id = $7,
		secret_hints = $8, updated_at = now() WHERE org_id = $1 AND id = $2`, orgID, id, p.Name, p.Enabled, p.Config, stored, keyID, p.Hints)
	if err != nil {
		return nil, mapPGErr(err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	b := &pgx.Batch{}
	audit(b, orgID, actor, "alert.channel.update", "alert_channel", id, map[string]any{"name": p.Name})
	if err := sendBatch(ctx, tx, b); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetChannel(ctx, orgID, id)
}

func (s *PGStore) DeleteChannel(ctx context.Context, orgID, id string, actor Actor) error {
	if !ValidUUID(id) {
		return ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var name string
	if err := tx.QueryRow(ctx, `DELETE FROM alert_channels WHERE org_id = $1 AND id = $2 RETURNING name`, orgID, id).Scan(&name); err != nil {
		return mapPGErr(err)
	}
	b := &pgx.Batch{}
	audit(b, orgID, actor, "alert.channel.delete", "alert_channel", id, map[string]any{"name": name})
	if err := sendBatch(ctx, tx, b); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RecordTest stores a synchronous test delivery and its attempt in the delivery log.
func (s *PGStore) RecordTest(ctx context.Context, n Notification, att Attempt, actor Actor) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	b := &pgx.Batch{}
	b.Queue(`INSERT INTO alert_notifications (id, org_id, channel_id, channel_type, kind, idempotency_key, payload, status, attempts,
		last_error, finished_at) VALUES ($1, $2, $3, $4, 'test', $5, $6, $7, 1, $8, now())`,
		n.ID, n.OrgID, n.ChannelID, n.ChannelType, n.IdempotencyKey, n.Payload, n.Status, truncate(n.LastError, 2000))
	b.Queue(`INSERT INTO alert_delivery_attempts (notification_id, org_id, attempt, instance_id, started_at, duration_ms, success, status_code, error)
		VALUES ($1, $2, 1, $3, $4, $5, $6, $7, $8)`, n.ID, n.OrgID, att.Instance, att.StartedAt, int(att.Duration/time.Millisecond),
		att.Success, att.StatusCode, truncate(att.Error, 2000))
	audit(b, n.OrgID, actor, "alert.channel.test", "alert_channel", n.ChannelID, map[string]any{"success": att.Success})
	if err := sendBatch(ctx, tx, b); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---- mutes ----

func (s *PGStore) ListMutes(ctx context.Context, orgID string, includeExpired bool) ([]Mute, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+muteColumns+muteFrom+` WHERE m.org_id = $1 AND ($2 OR m.ends_at > now() - interval '7 days'
			OR (m.schedule IS NOT NULL AND (m.schedule->>'until' IS NULL OR (m.schedule->>'until')::timestamptz > now() - interval '7 days')))
		ORDER BY m.ends_at DESC, m.id`, orgID, includeExpired)
	if err != nil {
		return nil, err
	}
	out := []Mute{}
	for rows.Next() {
		m, err := scanMute(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, *m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.attachMuteHolidays(ctx, out)
}

func (s *PGStore) GetMute(ctx context.Context, orgID, id string) (*Mute, error) {
	if !ValidUUID(id) {
		return nil, ErrNotFound
	}
	m, err := scanMute(s.pool.QueryRow(ctx, `SELECT `+muteColumns+muteFrom+` WHERE m.org_id = $1 AND m.id = $2`, orgID, id))
	if err != nil {
		return nil, err
	}
	one := []Mute{*m}
	if err := s.attachMuteHolidays(ctx, one); err != nil {
		return nil, err
	}
	return &one[0], nil
}

func (s *PGStore) CreateMute(ctx context.Context, orgID string, m *ValidMute, actor Actor) (*Mute, error) {
	id := uuid.NewString()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	b := &pgx.Batch{}
	b.Queue(`INSERT INTO alert_mutes (id, org_id, name, comment, starts_at, ends_at, rule_ids, matchers, created_by, schedule)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, id, orgID, m.Name, m.Comment, m.StartsAt, m.EndsAt, nonNilIDs(m.RuleIDs), m.Matchers,
		nullID(actor.UserID), m.Schedule)
	audit(b, orgID, actor, "alert.mute.create", "alert_mute", id, map[string]any{"name": m.Name, "ends_at": m.EndsAt, "recurring": m.Schedule != nil})
	if err := sendBatch(ctx, tx, b); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetMute(ctx, orgID, id)
}

func (s *PGStore) UpdateMute(ctx context.Context, orgID, id string, m *ValidMute, actor Actor) (*Mute, error) {
	if !ValidUUID(id) {
		return nil, ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	tag, err := tx.Exec(ctx, `UPDATE alert_mutes SET name = $3, comment = $4, starts_at = $5, ends_at = $6, rule_ids = $7, matchers = $8,
		schedule = $9, updated_at = now() WHERE org_id = $1 AND id = $2`, orgID, id, m.Name, m.Comment, m.StartsAt, m.EndsAt, nonNilIDs(m.RuleIDs),
		m.Matchers, m.Schedule)
	if err != nil {
		return nil, mapPGErr(err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	b := &pgx.Batch{}
	audit(b, orgID, actor, "alert.mute.update", "alert_mute", id, map[string]any{"name": m.Name, "ends_at": m.EndsAt})
	if err := sendBatch(ctx, tx, b); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetMute(ctx, orgID, id)
}

func (s *PGStore) DeleteMute(ctx context.Context, orgID, id string, actor Actor) error {
	if !ValidUUID(id) {
		return ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var name string
	if err := tx.QueryRow(ctx, `DELETE FROM alert_mutes WHERE org_id = $1 AND id = $2 RETURNING name`, orgID, id).Scan(&name); err != nil {
		return mapPGErr(err)
	}
	b := &pgx.Batch{}
	audit(b, orgID, actor, "alert.mute.delete", "alert_mute", id, map[string]any{"name": name})
	if err := sendBatch(ctx, tx, b); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---- delivery log ----

func (s *PGStore) ListDeliveries(ctx context.Context, orgID string, f DeliveryFilter) ([]DeliveryView, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	for _, id := range []string{f.ChannelID, f.IncidentID} {
		if id != "" && !ValidUUID(id) {
			return []DeliveryView{}, nil
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT n.id::text, n.org_id::text, COALESCE(n.incident_id::text, ''), COALESCE(n.rule_id::text, ''),
		COALESCE(i.rule_name, r.name, ''), COALESCE(n.channel_id::text, ''), COALESCE(c.name, ''), n.channel_type, n.kind, n.status,
		n.attempts, n.idempotency_key, n.created_at, n.finished_at, n.next_attempt_at, n.last_error
		FROM alert_notifications n LEFT JOIN alert_incidents i ON i.id = n.incident_id LEFT JOIN alert_rules r ON r.id = n.rule_id
		LEFT JOIN alert_channels c ON c.id = n.channel_id
		WHERE n.org_id = $1 AND ($2 = '' OR n.channel_id::text = $2) AND ($3 = '' OR n.incident_id::text = $3) AND ($4 = '' OR n.status = $4)
		ORDER BY n.created_at DESC, n.seq DESC LIMIT $5`, orgID, f.ChannelID, f.IncidentID, f.Status, limit)
	if err != nil {
		return nil, err
	}
	out := []DeliveryView{}
	index := map[string]int{}
	var ids []string
	for rows.Next() {
		var v DeliveryView
		if err := rows.Scan(&v.ID, &v.OrgID, &v.IncidentID, &v.RuleID, &v.RuleName, &v.ChannelID, &v.ChannelName, &v.ChannelType, &v.Kind,
			&v.Status, &v.Attempts, &v.IdempotencyKey, &v.CreatedAt, &v.FinishedAt, &v.NextAttemptAt, &v.LastError); err != nil {
			rows.Close()
			return nil, err
		}
		v.AttemptLog = []Attempt{}
		index[v.ID] = len(out)
		ids = append(ids, v.ID)
		out = append(out, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return out, err
	}
	arows, err := s.pool.Query(ctx, `SELECT notification_id::text, attempt, instance_id, started_at, duration_ms, success, status_code, error
		FROM alert_delivery_attempts WHERE notification_id = ANY($1::uuid[]) ORDER BY notification_id, id`, ids)
	if err != nil {
		return nil, err
	}
	defer arows.Close()
	for arows.Next() {
		var (
			nid string
			a   Attempt
			ms  int
		)
		if err := arows.Scan(&nid, &a.Attempt, &a.Instance, &a.StartedAt, &ms, &a.Success, &a.StatusCode, &a.Error); err != nil {
			return nil, err
		}
		a.Duration = time.Duration(ms) * time.Millisecond
		if i, ok := index[nid]; ok {
			out[i].AttemptLog = append(out[i].AttemptLog, a)
		}
	}
	return out, arows.Err()
}

// ---- secret rotation ----

// RotateSecrets re-encrypts every channel secret not encrypted with the current key. It returns the number of
// rotated channels and of channels that could not be decrypted.
func (s *PGStore) RotateSecrets(ctx context.Context, kr *secrets.Keyring, check bool) (rotated, failed, pending int, err error) {
	if !kr.Configured() {
		return 0, 0, 0, ErrNoSecretsKey
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, org_id::text, secrets FROM alert_channels WHERE secrets <> '' AND secrets_key_id <> $1`, kr.CurrentKeyID())
	if err != nil {
		return 0, 0, 0, err
	}
	type row struct{ id, org, stored string }
	var todo []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.org, &r.stored); err != nil {
			rows.Close()
			return 0, 0, 0, err
		}
		todo = append(todo, r)
	}
	rows.Close()
	if check {
		return 0, 0, len(todo), rows.Err()
	}
	for _, r := range todo {
		plain, err := DecryptSecrets(kr, r.org, r.id, r.stored)
		if err != nil {
			failed++
			continue
		}
		stored, keyID, err := EncryptSecrets(kr, r.org, r.id, plain)
		if err != nil {
			return rotated, failed, 0, err
		}
		tag, err := s.pool.Exec(ctx, `UPDATE alert_channels SET secrets = $2, secrets_key_id = $3, updated_at = now() WHERE id = $1 AND secrets = $4`,
			r.id, stored, keyID, r.stored)
		if err != nil {
			return rotated, failed, 0, err
		}
		rotated += int(tag.RowsAffected())
	}
	return rotated, failed, 0, nil
}
