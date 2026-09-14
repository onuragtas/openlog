package alert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore implements LeaseStore, EvalStore, OutboxStore and ManagerStore on PostgreSQL (migrations/postgres/0004_alerting.sql).
type PGStore struct {
	pool *pgxpool.Pool
}

var (
	_ LeaseStore  = (*PGStore)(nil)
	_ EvalStore   = (*PGStore)(nil)
	_ OutboxStore = (*PGStore)(nil)
)

// NewPGStore wraps a pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

func secs(d time.Duration) float64 { return d.Seconds() }

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func timeOr(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func nullFloat(f float64) *float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return &f
}

func floatOr(f *float64) float64 {
	if f == nil {
		return math.NaN()
	}
	return *f
}

func mapPGErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "22P02", "23503": // invalid uuid text, foreign key
			return ErrNotFound
		case "23505":
			return ErrConflict
		}
	}
	return err
}

// ---- LeaseStore ----

func (s *PGStore) Heartbeat(ctx context.Context, instance, version string, ttl time.Duration) (int, error) {
	if _, err := s.pool.Exec(ctx, `INSERT INTO alert_evaluators (instance_id, version) VALUES ($1, $2)
		ON CONFLICT (instance_id) DO UPDATE SET last_seen = now(), version = EXCLUDED.version`, instance, version); err != nil {
		return 0, err
	}
	var live int
	err := s.pool.QueryRow(ctx, `WITH pruned AS (DELETE FROM alert_evaluators WHERE last_seen < now() - interval '1 hour')
		SELECT count(*) FROM alert_evaluators WHERE last_seen > now() - make_interval(secs => $1)`, secs(ttl)).Scan(&live)
	return live, err
}

func (s *PGStore) Leave(ctx context.Context, instance string) error {
	if _, err := s.pool.Exec(ctx, `UPDATE alert_rule_leases SET owner = '', lease_until = '-infinity', updated_at = now() WHERE owner = $1`, instance); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM alert_evaluators WHERE instance_id = $1`, instance)
	return err
}

const leaseReturning = `l.rule_id::text, l.org_id::text, l.next_eval_at, l.last_eval_end, l.lease_until`

func scanLeases(rows pgx.Rows) ([]Lease, error) {
	defer rows.Close()
	var out []Lease
	for rows.Next() {
		var l Lease
		var last *time.Time
		if err := rows.Scan(&l.RuleID, &l.OrgID, &l.NextEvalAt, &last, &l.LeaseUntil); err != nil {
			return nil, err
		}
		l.LastEvalEnd = timeOr(last)
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *PGStore) Renew(ctx context.Context, instance string, ttl time.Duration) ([]Lease, error) {
	if _, err := s.pool.Exec(ctx, `UPDATE alert_rule_leases l SET owner = '', lease_until = '-infinity', updated_at = now()
		FROM alert_rules r WHERE r.id = l.rule_id AND l.owner = $1 AND NOT r.enabled`, instance); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `UPDATE alert_rule_leases l SET lease_until = now() + make_interval(secs => $2), updated_at = now()
		FROM alert_rules r WHERE r.id = l.rule_id AND l.owner = $1 AND r.enabled
		RETURNING `+leaseReturning, instance, secs(ttl))
	if err != nil {
		return nil, err
	}
	return scanLeases(rows)
}

func (s *PGStore) CountEnabled(ctx context.Context) (int, error) {
	if _, err := s.pool.Exec(ctx, `INSERT INTO alert_rule_leases (rule_id, org_id)
		SELECT r.id, r.org_id FROM alert_rules r WHERE NOT EXISTS (SELECT 1 FROM alert_rule_leases l WHERE l.rule_id = r.id)
		ON CONFLICT DO NOTHING`); err != nil {
		return 0, err
	}
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM alert_rules WHERE enabled`).Scan(&n)
	return n, err
}

func (s *PGStore) Claim(ctx context.Context, instance string, n int, ttl time.Duration) ([]Lease, error) {
	rows, err := s.pool.Query(ctx, `WITH c AS (
			SELECT l.rule_id FROM alert_rule_leases l JOIN alert_rules r ON r.id = l.rule_id
			JOIN organizations o ON o.id = r.org_id AND o.deleted_at IS NULL -- scheduled for deletion: not evaluated (D-107)
			WHERE r.enabled AND l.lease_until < now()
			ORDER BY l.next_eval_at LIMIT $2
			FOR UPDATE OF l SKIP LOCKED)
		UPDATE alert_rule_leases l SET owner = $1, lease_until = now() + make_interval(secs => $3), claimed_at = now(), updated_at = now()
		FROM c WHERE l.rule_id = c.rule_id
		RETURNING `+leaseReturning, instance, n, secs(ttl))
	if err != nil {
		return nil, err
	}
	return scanLeases(rows)
}

func (s *PGStore) Release(ctx context.Context, instance string, n int) ([]string, error) {
	rows, err := s.pool.Query(ctx, `WITH c AS (
			SELECT rule_id FROM alert_rule_leases WHERE owner = $1 ORDER BY next_eval_at DESC LIMIT $2 FOR UPDATE SKIP LOCKED)
		UPDATE alert_rule_leases l SET owner = '', lease_until = '-infinity', updated_at = now()
		FROM c WHERE l.rule_id = c.rule_id RETURNING l.rule_id::text`, instance, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ---- EvalStore ----

const ruleColumns = `r.id::text, r.org_id::text, o.tenant_id, o.name, r.name, r.description, r.type, r.severity, r.enabled,
	r.interval_seconds, r.for_seconds, r.recovery_for_seconds, r.condition, r.renotify_interval_seconds, r.flapping,
	r.runbook_url, r.labels, r.version, COALESCE(r.created_by::text, ''), COALESCE(u.email, ''), r.created_at, r.updated_at,
	COALESCE((SELECT array_agg(rc.channel_id::text ORDER BY rc.channel_id) FROM alert_rule_channels rc WHERE rc.rule_id = r.id), '{}')`

const ruleFrom = ` FROM alert_rules r JOIN organizations o ON o.id = r.org_id LEFT JOIN users u ON u.id = r.created_by`

func scanRule(row pgx.Row, extra ...any) (*Rule, error) {
	r := &Rule{}
	var flapping []byte
	dest := []any{&r.ID, &r.OrgID, &r.TenantID, &r.OrgName, &r.Name, &r.Description, &r.Type, &r.Severity, &r.Enabled,
		&r.IntervalSeconds, &r.ForSeconds, &r.RecoveryForSeconds, &r.ConditionJSON, &r.RenotifyIntervalSeconds, &flapping,
		&r.RunbookURL, &r.Labels, &r.Version, &r.CreatedBy, &r.CreatedByEmail, &r.CreatedAt, &r.UpdatedAt, &r.ChannelIDs}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return nil, mapPGErr(err)
	}
	r.Flapping = DefaultFlapping
	if len(flapping) > 2 {
		_ = json.Unmarshal(flapping, &r.Flapping)
	}
	if r.Labels == nil {
		r.Labels = map[string]string{}
	}
	if r.ChannelIDs == nil {
		r.ChannelIDs = []string{}
	}
	// A condition that no longer parses (e.g. a type removed by a downgrade) leaves Condition nil; the evaluator
	// records an error instead of failing the lease loop.
	_ = ParseStoredRule(r)
	return r, nil
}

func (s *PGStore) LoadRule(ctx context.Context, ruleID string) (*Rule, []ChannelRef, error) {
	if !ValidUUID(ruleID) {
		return nil, nil, ErrNotFound
	}
	r, err := scanRule(s.pool.QueryRow(ctx, `SELECT `+ruleColumns+ruleFrom+` WHERE r.id = $1`, ruleID))
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT c.id::text, c.type, c.enabled FROM alert_rule_channels rc
		JOIN alert_channels c ON c.id = rc.channel_id WHERE rc.rule_id = $1 ORDER BY c.name, c.id`, ruleID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var chans []ChannelRef
	for rows.Next() {
		var c ChannelRef
		if err := rows.Scan(&c.ID, &c.Type, &c.Enabled); err != nil {
			return nil, nil, err
		}
		chans = append(chans, c)
	}
	return r, chans, rows.Err()
}

const incidentColumns = `i.id::text, i.org_id::text, COALESCE(i.rule_id::text, ''), i.rule_name, i.rule_type, i.severity, i.series_key,
	i.labels, i.summary, i.state, i.value, i.last_value, i.threshold, COALESCE(i.channel_ids::text[], '{}'), i.flapping, i.opened_at,
	i.acknowledged_at, COALESCE(ua.email, ''), i.resolved_at, COALESCE(ur.email, ''), COALESCE(i.resolve_reason, '')`

const incidentFrom = ` FROM alert_incidents i LEFT JOIN users ua ON ua.id = i.acknowledged_by LEFT JOIN users ur ON ur.id = i.resolved_by`

func scanIncident(row pgx.Row) (*Incident, error) {
	inc := &Incident{}
	if err := row.Scan(&inc.ID, &inc.OrgID, &inc.RuleID, &inc.RuleName, &inc.RuleType, &inc.Severity, &inc.SeriesKey,
		&inc.Labels, &inc.Summary, &inc.State, &inc.Value, &inc.LastValue, &inc.Threshold, &inc.ChannelIDs, &inc.Flapping, &inc.OpenedAt,
		&inc.AcknowledgedAt, &inc.AcknowledgedByEmail, &inc.ResolvedAt, &inc.ResolvedByEmail, &inc.ResolveReason); err != nil {
		return nil, mapPGErr(err)
	}
	if inc.Labels == nil {
		inc.Labels = map[string]string{}
	}
	return inc, nil
}

func (s *PGStore) LoadSeries(ctx context.Context, ruleID string) (map[string]SeriesState, error) {
	rows, err := s.pool.Query(ctx, `SELECT series_key, labels, state, pending_since, firing_since, recovering_since, last_value,
		last_seen_at, COALESCE(incident_id::text, ''), transitions, last_notified_at, renotify_count
		FROM alert_series_state WHERE rule_id = $1`, ruleID)
	if err != nil {
		return nil, err
	}
	out := map[string]SeriesState{}
	var incIDs []string
	for rows.Next() {
		var (
			st                          SeriesState
			pending, firing, recovering *time.Time
			lastSeen, lastNotified      *time.Time
			lastValue                   *float64
		)
		if err := rows.Scan(&st.Key, &st.Labels, &st.State, &pending, &firing, &recovering, &lastValue, &lastSeen, &st.IncidentID,
			&st.Transitions, &lastNotified, &st.RenotifyCount); err != nil {
			rows.Close()
			return nil, err
		}
		st.PendingSince, st.FiringSince, st.RecoveringSince = timeOr(pending), timeOr(firing), timeOr(recovering)
		st.LastSeenAt, st.LastNotifiedAt, st.LastValue = timeOr(lastSeen), timeOr(lastNotified), floatOr(lastValue)
		if st.IncidentID != "" {
			incIDs = append(incIDs, st.IncidentID)
		}
		out[st.Key] = st
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(incIDs) == 0 {
		return out, nil
	}
	irows, err := s.pool.Query(ctx, `SELECT `+incidentColumns+incidentFrom+` WHERE i.id = ANY($1::uuid[])`, incIDs)
	if err != nil {
		return nil, err
	}
	defer irows.Close()
	incs := map[string]*Incident{}
	for irows.Next() {
		inc, err := scanIncident(irows)
		if err != nil {
			return nil, err
		}
		incs[inc.ID] = inc
	}
	if err := irows.Err(); err != nil {
		return nil, err
	}
	for k, st := range out {
		if st.IncidentID != "" {
			st.Incident = incs[st.IncidentID]
			out[k] = st
		}
	}
	return out, nil
}

// lockLease locks the lease row of a rule (serializing evaluator commits and API changes of the rule).
func lockLease(ctx context.Context, tx pgx.Tx, ruleID string) (owner string, until time.Time, lastEnd *time.Time, now time.Time, err error) {
	err = tx.QueryRow(ctx, `SELECT owner, lease_until, last_eval_end, now() FROM alert_rule_leases WHERE rule_id = $1 FOR UPDATE`, ruleID).
		Scan(&owner, &until, &lastEnd, &now)
	return
}

func (s *PGStore) Commit(ctx context.Context, instance string, p *Plan) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	owner, until, lastEnd, now, err := lockLease(ctx, tx, p.RuleID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	if owner != instance || !until.After(now) {
		return ErrLeaseLost
	}
	if lastEnd != nil && !p.EvalEnd.After(*lastEnd) {
		return ErrStale
	}
	var (
		version int
		enabled bool
	)
	if err := tx.QueryRow(ctx, `SELECT version, enabled FROM alert_rules WHERE id = $1 FOR SHARE`, p.RuleID).Scan(&version, &enabled); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRuleChanged
		}
		return err
	}
	if version != p.RuleVersion || !enabled {
		return ErrRuleChanged
	}

	b := &pgx.Batch{}
	for _, inc := range p.Opens {
		b.Queue(`INSERT INTO alert_incidents (id, org_id, rule_id, rule_name, rule_type, severity, series_key, labels, summary, state,
			value, last_value, threshold, channel_ids, flapping, opened_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'open', $10, $11, $12, $13, false, $14)`,
			inc.ID, inc.OrgID, inc.RuleID, inc.RuleName, inc.RuleType, inc.Severity, inc.SeriesKey, nonNilLabels(inc.Labels), inc.Summary,
			inc.Value, inc.LastValue, inc.Threshold, nonNilIDs(inc.ChannelIDs), inc.OpenedAt)
	}
	for _, r := range p.Resolves {
		b.Queue(`UPDATE alert_incidents SET state = 'resolved', resolved_at = $2, resolve_reason = $3, last_value = COALESCE($4, last_value),
			updated_at = now() WHERE id = $1 AND state <> 'resolved'`, r.ID, r.At, r.Reason, r.LastValue)
	}
	for _, u := range p.Updates {
		b.Queue(`UPDATE alert_incidents SET last_value = COALESCE($2, last_value), flapping = flapping OR $3, updated_at = now() WHERE id = $1`,
			u.ID, u.LastValue, u.Flapping)
	}
	if len(p.SeriesDeletes) > 0 {
		b.Queue(`DELETE FROM alert_series_state WHERE rule_id = $1 AND series_key = ANY($2)`, p.RuleID, p.SeriesDeletes)
	}
	for _, st := range p.SeriesUpserts {
		b.Queue(`INSERT INTO alert_series_state (rule_id, series_key, labels, state, pending_since, firing_since, recovering_since,
			last_value, last_seen_at, incident_id, transitions, last_notified_at, renotify_count, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, now())
			ON CONFLICT (rule_id, series_key) DO UPDATE SET labels = EXCLUDED.labels, state = EXCLUDED.state,
			pending_since = EXCLUDED.pending_since, firing_since = EXCLUDED.firing_since, recovering_since = EXCLUDED.recovering_since,
			last_value = EXCLUDED.last_value, last_seen_at = EXCLUDED.last_seen_at, incident_id = EXCLUDED.incident_id,
			transitions = EXCLUDED.transitions, last_notified_at = EXCLUDED.last_notified_at, renotify_count = EXCLUDED.renotify_count,
			updated_at = now()`,
			p.RuleID, st.Key, nonNilLabels(st.Labels), st.State, nullTime(st.PendingSince), nullTime(st.FiringSince), nullTime(st.RecoveringSince),
			nullFloat(st.LastValue), nullTime(st.LastSeenAt), nullID(st.IncidentID), nonNilTimes(st.Transitions), nullTime(st.LastNotifiedAt), st.RenotifyCount)
	}
	queueEvents(b, p.Events)
	queueNotifications(b, p.Notifications)
	b.Queue(`UPDATE alert_rule_leases SET last_eval_end = $2, next_eval_at = $3, last_evaluated_at = now(), last_result = $4,
		last_error = $5, last_duration_ms = $6, updated_at = now() WHERE rule_id = $1`,
		p.RuleID, p.EvalEnd, p.NextEvalAt, p.Result, p.Error, int(p.Duration/time.Millisecond))
	if err := tx.SendBatch(ctx, b).Close(); err != nil {
		return fmt.Errorf("commit evaluation: %w", err)
	}
	return tx.Commit(ctx)
}

func queueEvents(b *pgx.Batch, events []IncidentEvent) {
	for _, e := range events {
		details := e.Details
		if details == nil {
			details = map[string]any{}
		}
		b.Queue(`INSERT INTO alert_incident_events (incident_id, org_id, at, kind, actor_user_id, actor_email, message, details)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, e.IncidentID, e.OrgID, e.At, e.Kind, nullID(e.ActorUserID), e.ActorEmail, truncate(e.Message, 4000), details)
	}
}

// queueNotifications inserts outbox rows; the channel type comes from alert_channels (a deleted channel gets no row).
func queueNotifications(b *pgx.Batch, ns []Notification) {
	for _, n := range ns {
		b.Queue(`INSERT INTO alert_notifications (id, org_id, incident_id, rule_id, channel_id, channel_type, kind, idempotency_key, payload)
			SELECT $1, $2, $3, $4, c.id, c.type, $6, $7, $8 FROM alert_channels c WHERE c.id = $5 AND c.org_id = $2
			ON CONFLICT (idempotency_key) DO NOTHING`,
			n.ID, n.OrgID, nullID(n.IncidentID), nullID(n.RuleID), n.ChannelID, n.Kind, n.IdempotencyKey, n.Payload)
	}
}

func nullID(id string) *string {
	if id == "" || !ValidUUID(id) {
		return nil
	}
	return &id
}

func nonNilLabels(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func nonNilIDs(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

func nonNilTimes(ts []time.Time) []time.Time {
	if ts == nil {
		return []time.Time{}
	}
	return ts
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ---- OutboxStore ----

func (s *PGStore) RequeueExpired(ctx context.Context) (int, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE alert_notifications SET status = 'pending', claimed_by = '', claimed_until = NULL, updated_at = now()
		WHERE status = 'sending' AND claimed_until < now()`)
	return int(tag.RowsAffected()), err
}

const channelColumns = `c.id::text, c.org_id::text, c.name, c.type, c.enabled, c.config, c.secrets, c.secrets_key_id, c.secret_hints,
	COALESCE(c.created_by::text, ''), COALESCE(u.email, ''), c.created_at, c.updated_at`

const channelFrom = ` FROM alert_channels c LEFT JOIN users u ON u.id = c.created_by`

func scanChannel(row pgx.Row, extra ...any) (*Channel, error) {
	c := &Channel{}
	var cfg []byte
	if err := row.Scan(append([]any{&c.ID, &c.OrgID, &c.Name, &c.Type, &c.Enabled, &cfg, &c.Secrets, &c.SecretsKeyID, &c.SecretHints,
		&c.CreatedBy, &c.CreatedByEmail, &c.CreatedAt, &c.UpdatedAt}, extra...)...); err != nil {
		return nil, mapPGErr(err)
	}
	_ = json.Unmarshal(cfg, &c.Config)
	if c.SecretHints == nil {
		c.SecretHints = map[string]string{}
	}
	return c, nil
}

func (s *PGStore) ClaimDeliveries(ctx context.Context, instance string, n int, claimTTL time.Duration) ([]*Delivery, error) {
	rows, err := s.pool.Query(ctx, `WITH c AS (
			SELECT n.id FROM alert_notifications n
			WHERE n.status = 'pending' AND n.next_attempt_at <= now()
			  AND NOT EXISTS (SELECT 1 FROM alert_notifications p WHERE p.incident_id = n.incident_id
			      AND p.channel_id IS NOT DISTINCT FROM n.channel_id AND p.seq < n.seq AND p.status IN ('pending', 'sending'))
			ORDER BY n.next_attempt_at, n.seq LIMIT $2
			FOR UPDATE SKIP LOCKED)
		UPDATE alert_notifications n SET status = 'sending', claimed_by = $1, claimed_until = now() + make_interval(secs => $3),
			attempts = n.attempts + 1, updated_at = now()
		FROM c WHERE n.id = c.id
		RETURNING n.id::text, n.org_id::text, COALESCE(n.incident_id::text, ''), COALESCE(n.rule_id::text, ''), COALESCE(n.channel_id::text, ''),
			n.channel_type, n.kind, n.idempotency_key, n.payload, n.status, n.attempts, n.next_attempt_at, n.last_error, n.created_at, n.muted_logged`,
		instance, n, secs(claimTTL))
	if err != nil {
		return nil, err
	}
	var out []*Delivery
	chanIDs, incIDs := map[string]bool{}, map[string]bool{}
	for rows.Next() {
		d := &Delivery{}
		if err := rows.Scan(&d.ID, &d.OrgID, &d.IncidentID, &d.RuleID, &d.ChannelID, &d.ChannelType, &d.Kind, &d.IdempotencyKey,
			&d.Payload, &d.Status, &d.Attempts, &d.NextAttemptAt, &d.LastError, &d.CreatedAt, &d.MutedLogged); err != nil {
			rows.Close()
			return nil, err
		}
		if d.ChannelID != "" {
			chanIDs[d.ChannelID] = true
		}
		if d.IncidentID != "" {
			incIDs[d.IncidentID] = true
		}
		out = append(out, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out) == 0 {
		return out, err
	}
	channels := map[string]*Channel{}
	if len(chanIDs) > 0 {
		crows, err := s.pool.Query(ctx, `SELECT `+channelColumns+channelFrom+` WHERE c.id = ANY($1::uuid[])`, keys(chanIDs))
		if err != nil {
			return nil, err
		}
		for crows.Next() {
			c, err := scanChannel(crows)
			if err != nil {
				crows.Close()
				return nil, err
			}
			channels[c.ID] = c
		}
		crows.Close()
	}
	incidents := map[string]*Incident{}
	opened := map[string]string{}
	if len(incIDs) > 0 {
		irows, err := s.pool.Query(ctx, `SELECT `+incidentColumns+incidentFrom+` WHERE i.id = ANY($1::uuid[])`, keys(incIDs))
		if err != nil {
			return nil, err
		}
		for irows.Next() {
			inc, err := scanIncident(irows)
			if err != nil {
				irows.Close()
				return nil, err
			}
			incidents[inc.ID] = inc
		}
		irows.Close()
		orows, err := s.pool.Query(ctx, `SELECT incident_id::text, COALESCE(channel_id::text, ''), status FROM alert_notifications
			WHERE kind = 'opened' AND incident_id = ANY($1::uuid[])`, keys(incIDs))
		if err != nil {
			return nil, err
		}
		for orows.Next() {
			var inc, ch, st string
			if err := orows.Scan(&inc, &ch, &st); err != nil {
				orows.Close()
				return nil, err
			}
			opened[inc+"/"+ch] = st
		}
		orows.Close()
	}
	for _, d := range out {
		d.Channel = channels[d.ChannelID]
		d.Incident = incidents[d.IncidentID]
		d.OpenedStatus = opened[d.IncidentID+"/"+d.ChannelID]
	}
	return out, nil
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ErrClaimLost is returned by FinishDelivery when another dispatcher took the row over.
var ErrClaimLost = errors.New("notification claim lost")

func (s *PGStore) FinishDelivery(ctx context.Context, instance string, d *Delivery, out DeliveryOutcome) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if a := out.Attempt; a != nil {
		if _, err := tx.Exec(ctx, `INSERT INTO alert_delivery_attempts (notification_id, org_id, attempt, instance_id, started_at, duration_ms,
			success, status_code, error) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			d.ID, d.OrgID, a.Attempt, a.Instance, a.StartedAt, int(a.Duration/time.Millisecond), a.Success, a.StatusCode, truncate(a.Error, 2000)); err != nil {
			return err
		}
	}
	next := out.NextAttemptAt
	if next.IsZero() {
		next = time.Now()
	}
	tag, err := tx.Exec(ctx, `UPDATE alert_notifications SET status = $2,
			next_attempt_at = CASE WHEN $2 = 'pending' THEN $3 ELSE next_attempt_at END,
			last_error = $4, muted_logged = $5, claimed_by = '', claimed_until = NULL,
			finished_at = CASE WHEN $2 IN ('delivered', 'failed', 'suppressed') THEN now() ELSE NULL END, updated_at = now()
		WHERE id = $1 AND status = 'sending' AND claimed_by = $6`,
		d.ID, out.Status, next, truncate(out.Error, 2000), out.MutedLogged, instance)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// Keep the attempt in the delivery log, but do not overwrite the new owner's state.
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return ErrClaimLost
	}
	if out.Event != nil {
		b := &pgx.Batch{}
		queueEvents(b, []IncidentEvent{*out.Event})
		if err := tx.SendBatch(ctx, b).Close(); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

const muteColumns = `m.id::text, m.org_id::text, m.name, m.comment, m.starts_at, m.ends_at, COALESCE(m.rule_ids::text[], '{}'), m.matchers,
	COALESCE(m.created_by::text, ''), COALESCE(u.email, ''), m.created_at, m.updated_at, m.schedule`

const muteFrom = ` FROM alert_mutes m LEFT JOIN users u ON u.id = m.created_by`

func scanMute(row pgx.Row) (*Mute, error) {
	m := &Mute{}
	var matchers, schedule []byte
	if err := row.Scan(&m.ID, &m.OrgID, &m.Name, &m.Comment, &m.StartsAt, &m.EndsAt, &m.RuleIDs, &matchers,
		&m.CreatedBy, &m.CreatedByEmail, &m.CreatedAt, &m.UpdatedAt, &schedule); err != nil {
		return nil, mapPGErr(err)
	}
	_ = json.Unmarshal(matchers, &m.Matchers)
	if m.Matchers == nil {
		m.Matchers = []MuteMatcher{}
	}
	if m.RuleIDs == nil {
		m.RuleIDs = []string{}
	}
	if len(schedule) > 0 && string(schedule) != "null" {
		var sc MuteSchedule
		if err := json.Unmarshal(schedule, &sc); err != nil {
			return nil, fmt.Errorf("mute %s: invalid schedule: %w", m.ID, err)
		}
		m.Schedule = &sc
	}
	return m, nil
}

// ActiveMutes loads one-off mutes covering at and every recurring mute, then keeps those in an occurrence.
func (s *PGStore) ActiveMutes(ctx context.Context, orgID string, at time.Time) ([]Mute, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+muteColumns+muteFrom+` WHERE m.org_id = $1
		AND ((m.schedule IS NULL AND m.starts_at <= $2 AND m.ends_at > $2) OR m.schedule IS NOT NULL)`, orgID, at)
	if err != nil {
		return nil, err
	}
	var all []Mute
	for rows.Next() {
		m, err := scanMute(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, *m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.attachMuteHolidays(ctx, all); err != nil {
		return nil, err
	}
	var out []Mute
	for i := range all {
		if all[i].Active(at) {
			out = append(out, all[i])
		}
	}
	return out, nil
}

// RollRecurringMutes implements OutboxStore. The update is conditional on the old window, so concurrent
// dispatchers write the same result at most once.
func (s *PGStore) RollRecurringMutes(ctx context.Context, now time.Time) (int, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text, org_id::text, schedule, ends_at FROM alert_mutes
		WHERE schedule IS NOT NULL AND ends_at <= $1
		  AND (schedule->>'until' IS NULL OR (schedule->>'until')::timestamptz > $1)
		ORDER BY ends_at LIMIT 1000`, now)
	if err != nil {
		return 0, err
	}
	type due struct {
		mute Mute
		ends time.Time
	}
	var list []due
	for rows.Next() {
		var (
			d   due
			raw []byte
			sc  MuteSchedule
		)
		if err := rows.Scan(&d.mute.ID, &d.mute.OrgID, &raw, &d.ends); err != nil {
			rows.Close()
			return 0, err
		}
		if json.Unmarshal(raw, &sc) == nil {
			d.mute.Schedule = &sc
			list = append(list, d)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	mutes := make([]Mute, len(list))
	for i := range list {
		mutes[i] = list[i].mute
	}
	if err := s.attachMuteHolidays(ctx, mutes); err != nil {
		return 0, err
	}
	moved := 0
	for i, d := range list {
		start, end, ok := mutes[i].Schedule.Window(now)
		if !ok {
			continue
		}
		tag, err := s.pool.Exec(ctx, `UPDATE alert_mutes SET starts_at = $2, ends_at = $3 WHERE id = $1 AND ends_at = $4`, d.mute.ID, start, end, d.ends)
		if err != nil {
			return moved, err
		}
		moved += int(tag.RowsAffected())
	}
	return moved, nil
}

func (s *PGStore) PendingCount(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM alert_notifications WHERE status IN ('pending', 'sending')`).Scan(&n)
	return n, err
}

func (s *PGStore) Prune(ctx context.Context, before time.Time) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM alert_notifications WHERE finished_at < $1`, before)
	if err != nil {
		return 0, err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM alert_incidents WHERE state = 'resolved' AND resolved_at < now() - interval '400 days'`); err != nil {
		return int(tag.RowsAffected()), err
	}
	return int(tag.RowsAffected()), nil
}
