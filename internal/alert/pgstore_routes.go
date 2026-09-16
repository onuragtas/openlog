package alert

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Routing rules (migrations/postgres/0088_alert_routing.sql, alerting.md §5.6).

const routeColumns = `r.id::text, r.org_id::text, r.name, r."position", r.enabled, r.is_default, r.match,
	COALESCE(r.created_by::text, ''), COALESCE(u.email, ''), r.created_at, r.updated_at,
	COALESCE((SELECT array_agg(rc.channel_id::text ORDER BY rc.channel_id) FROM alert_routing_rule_channels rc WHERE rc.route_id = r.id), '{}')`

const routeFrom = ` FROM alert_routing_rules r LEFT JOIN users u ON u.id = r.created_by`

func scanRoutingRule(row pgx.Row) (*RoutingRule, error) {
	r := &RoutingRule{}
	var match []byte
	if err := row.Scan(&r.ID, &r.OrgID, &r.Name, &r.Position, &r.Enabled, &r.IsDefault, &match, &r.CreatedBy,
		&r.CreatedByEmail, &r.CreatedAt, &r.UpdatedAt, &r.ChannelIDs); err != nil {
		return nil, mapPGErr(err)
	}
	if len(match) > 0 {
		_ = json.Unmarshal(match, &r.Match)
	}
	if r.ChannelIDs == nil {
		r.ChannelIDs = []string{}
	}
	return r, nil
}

func (s *PGStore) ListRoutingRules(ctx context.Context, orgID string) ([]RoutingRule, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+routeColumns+routeFrom+` WHERE r.org_id = $1 ORDER BY r."position", r.created_at, r.id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RoutingRule{}
	for rows.Next() {
		r, err := scanRoutingRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *PGStore) GetRoutingRule(ctx context.Context, orgID, id string) (*RoutingRule, error) {
	if !ValidUUID(id) {
		return nil, ErrNotFound
	}
	return scanRoutingRule(s.pool.QueryRow(ctx, `SELECT `+routeColumns+routeFrom+` WHERE r.org_id = $1 AND r.id = $2`, orgID, id))
}

// checkDefaultRoute refuses a second default route (the partial unique index is the second guard).
func checkDefaultRoute(ctx context.Context, tx pgx.Tx, orgID, exceptID string) error {
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM alert_routing_rules WHERE org_id = $1 AND is_default AND ($2 = '' OR id::text <> $2)`,
		orgID, exceptID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return &PreconditionError{Msg: "the organization already has a default route"}
	}
	return nil
}

func queueRouteChannels(b *pgx.Batch, id string, channelIDs []string) {
	for _, cid := range channelIDs {
		b.Queue(`INSERT INTO alert_routing_rule_channels (route_id, channel_id) VALUES ($1, $2)`, id, cid)
	}
}

func (s *PGStore) CreateRoutingRule(ctx context.Context, orgID string, v *ValidRoutingRule, actor Actor) (*RoutingRule, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM alert_routing_rules WHERE org_id = $1`, orgID).Scan(&n); err != nil {
		return nil, err
	}
	if n >= MaxRoutingRulesPerOrg {
		return nil, &PreconditionError{Msg: "the organization has reached the maximum number of routing rules"}
	}
	if err := checkChannels(ctx, tx, orgID, v.ChannelIDs); err != nil {
		return nil, err
	}
	if v.IsDefault {
		if err := checkDefaultRoute(ctx, tx, orgID, ""); err != nil {
			return nil, err
		}
	}
	id := uuid.NewString()
	b := &pgx.Batch{}
	b.Queue(`INSERT INTO alert_routing_rules (id, org_id, name, "position", enabled, is_default, match, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, id, orgID, v.Name, v.Position, v.Enabled, v.IsDefault, v.Match, nullID(actor.UserID))
	queueRouteChannels(b, id, v.ChannelIDs)
	audit(b, orgID, actor, "alert.routing_rule.create", "alert_routing_rule", id,
		map[string]any{"name": v.Name, "is_default": v.IsDefault, "channels": len(v.ChannelIDs)})
	if err := sendBatch(ctx, tx, b); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetRoutingRule(ctx, orgID, id)
}

func (s *PGStore) UpdateRoutingRule(ctx context.Context, orgID, id string, v *ValidRoutingRule, actor Actor) (*RoutingRule, error) {
	if !ValidUUID(id) {
		return nil, ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := checkChannels(ctx, tx, orgID, v.ChannelIDs); err != nil {
		return nil, err
	}
	if v.IsDefault {
		if err := checkDefaultRoute(ctx, tx, orgID, id); err != nil {
			return nil, err
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE alert_routing_rules SET name = $3, "position" = $4, enabled = $5, is_default = $6, match = $7,
		updated_at = now() WHERE org_id = $1 AND id = $2`, orgID, id, v.Name, v.Position, v.Enabled, v.IsDefault, v.Match)
	if err != nil {
		return nil, mapPGErr(err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	b := &pgx.Batch{}
	b.Queue(`DELETE FROM alert_routing_rule_channels WHERE route_id = $1`, id)
	queueRouteChannels(b, id, v.ChannelIDs)
	audit(b, orgID, actor, "alert.routing_rule.update", "alert_routing_rule", id,
		map[string]any{"name": v.Name, "is_default": v.IsDefault, "channels": len(v.ChannelIDs)})
	if err := sendBatch(ctx, tx, b); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetRoutingRule(ctx, orgID, id)
}

func (s *PGStore) DeleteRoutingRule(ctx context.Context, orgID, id string, actor Actor) error {
	if !ValidUUID(id) {
		return ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var name string
	if err := tx.QueryRow(ctx, `DELETE FROM alert_routing_rules WHERE org_id = $1 AND id = $2 RETURNING name`, orgID, id).Scan(&name); err != nil {
		return mapPGErr(err)
	}
	b := &pgx.Batch{}
	audit(b, orgID, actor, "alert.routing_rule.delete", "alert_routing_rule", id, map[string]any{"name": name})
	if err := sendBatch(ctx, tx, b); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ReorderRoutingRules sets the order of every routing rule of the organization; ids must list each rule exactly once.
func (s *PGStore) ReorderRoutingRules(ctx context.Context, orgID string, ids []string, actor Actor) ([]RoutingRule, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	rows, err := tx.Query(ctx, `SELECT id::text FROM alert_routing_rules WHERE org_id = $1 FOR UPDATE`, orgID)
	if err != nil {
		return nil, err
	}
	stored := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		stored[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !stored[id] || seen[id] {
			return nil, invalid("ids", "must list every routing rule of the organization exactly once")
		}
		seen[id] = true
	}
	if len(seen) != len(stored) {
		return nil, invalid("ids", "must list every routing rule of the organization exactly once")
	}
	b := &pgx.Batch{}
	for i, id := range ids {
		b.Queue(`UPDATE alert_routing_rules SET "position" = $3, updated_at = now() WHERE org_id = $1 AND id = $2`, orgID, id, i)
	}
	audit(b, orgID, actor, "alert.routing_rule.reorder", "alert_routing_rule", "", map[string]any{"rules": len(ids)})
	if err := sendBatch(ctx, tx, b); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.ListRoutingRules(ctx, orgID)
}

// LoadRouting implements EvalStore: the routing rules of an organization in evaluation order and its channels.
func (s *PGStore) LoadRouting(ctx context.Context, orgID string) (*Routing, error) {
	rules, err := s.ListRoutingRules(ctx, orgID)
	if err != nil {
		return nil, err
	}
	rt := &Routing{Rules: rules}
	if len(rules) == 0 {
		return rt, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, type, enabled FROM alert_channels WHERE org_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c ChannelRef
		if err := rows.Scan(&c.ID, &c.Type, &c.Enabled); err != nil {
			return nil, err
		}
		rt.Channels = append(rt.Channels, c)
	}
	return rt, rows.Err()
}
