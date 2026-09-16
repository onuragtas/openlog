package slo

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/google/uuid"
)

// PGStore is the PostgreSQL Store (migrations/postgres/0087_slo.sql). Every write and its audit event are
// written in one transaction, like the APM settings store.
type PGStore struct{ pool *pgxpool.Pool }

var _ Store = (*PGStore)(nil)

// NewPGStore wraps a pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

const sloColumns = `s.id::text, s.org_id::text, s.name, s.description, s.service_name, s.service_namespace,
	s.deployment_environment, s.sli_type, s.latency_threshold_ms, s.objective, s.window_days,
	coalesce(s.created_by::text, ''), coalesce(c.email, ''), coalesce(u.email, ''), s.created_at, s.updated_at`

const sloFrom = ` FROM slos s LEFT JOIN users c ON c.id = s.created_by LEFT JOIN users u ON u.id = s.updated_by`

func scanSLO(row pgx.Row) (*SLO, error) {
	var (
		x       SLO
		latency *int
	)
	if err := row.Scan(&x.ID, &x.OrgID, &x.Name, &x.Description, &x.ServiceName, &x.ServiceNamespace,
		&x.Environment, &x.SLIType, &latency, &x.Objective, &x.WindowDays, &x.CreatedBy, &x.CreatedByEmail,
		&x.UpdatedByEmail, &x.CreatedAt, &x.UpdatedAt); err != nil {
		return nil, err
	}
	if latency != nil {
		x.LatencyThresholdMs = float64(*latency)
	}
	return &x, nil
}

// List implements Store.
func (s *PGStore) List(ctx context.Context, orgID string) ([]SLO, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+sloColumns+sloFrom+`
		WHERE s.org_id = $1 ORDER BY lower(s.name), s.id LIMIT $2`, orgID, MaxPerOrg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SLO{}
	for rows.Next() {
		x, err := scanSLO(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

// Get implements Store.
func (s *PGStore) Get(ctx context.Context, orgID, id string) (*SLO, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrNotFound
	}
	x, err := scanSLO(s.pool.QueryRow(ctx, `SELECT `+sloColumns+sloFrom+` WHERE s.org_id = $1 AND s.id = $2`, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return x, err
}

func latencyParam(in Input) any {
	if in.SLIType != SLILatency {
		return nil
	}
	return int(in.LatencyThresholdMs)
}

func actorID(a Actor) any {
	if a.UserID == "" {
		return nil
	}
	return a.UserID
}

func auditDetails(in Input) []byte {
	b, _ := json.Marshal(map[string]any{"name": in.Name, "service_name": in.ServiceName,
		"service_namespace": in.ServiceNamespace, "environment": in.Environment, "sli_type": in.SLIType,
		"latency_threshold_ms": latencyParam(in), "objective": in.Objective, "window_days": in.WindowDays})
	return b
}

// keyID is the API key that made the change, or NULL for a signed-in user.
func keyID(a Actor) any {
	if a.APIKeyID == "" {
		return nil
	}
	return a.APIKeyID
}

func audit(ctx context.Context, tx pgx.Tx, orgID, action, id string, details []byte, actor Actor) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_log (org_id, actor_user_id, actor_email, actor_api_key_id, actor_api_key_name,
		action, target_type, target_id, details, ip)
		VALUES ($1, $2, $3, $4, $5, $6, 'slo', $7, $8, $9)`, orgID, actorID(actor), actor.Email,
		keyID(actor), actor.APIKeyName, action, id, details, actor.IP)
	return err
}

// Create implements Store. The count check and the insert are one statement; concurrent creates may exceed
// the limit by a few rows (like saved views).
func (s *PGStore) Create(ctx context.Context, orgID string, in Input, actor Actor) (*SLO, error) {
	id := uuid.NewString()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var stored string
		err := tx.QueryRow(ctx, `INSERT INTO slos (id, org_id, name, description, service_name, service_namespace,
			deployment_environment, sli_type, latency_threshold_ms, objective, window_days, created_by, updated_by)
			SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12
			WHERE (SELECT count(*) FROM slos WHERE org_id = $2) < $13
			RETURNING id::text`,
			id, orgID, in.Name, in.Description, in.ServiceName, in.ServiceNamespace, in.Environment,
			in.SLIType, latencyParam(in), in.Objective, in.WindowDays, actorID(actor), MaxPerOrg).Scan(&stored)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLimit
		}
		if err != nil {
			return err
		}
		return audit(ctx, tx, orgID, "slo.create", id, auditDetails(in), actor)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, orgID, id)
}

// Update implements Store.
func (s *PGStore) Update(ctx context.Context, orgID, id string, in Input, actor Actor) (*SLO, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrNotFound
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE slos SET name = $3, description = $4, service_name = $5, service_namespace = $6,
			deployment_environment = $7, sli_type = $8, latency_threshold_ms = $9, objective = $10, window_days = $11,
			updated_by = $12, updated_at = now() WHERE org_id = $1 AND id = $2`,
			orgID, id, in.Name, in.Description, in.ServiceName, in.ServiceNamespace, in.Environment,
			in.SLIType, latencyParam(in), in.Objective, in.WindowDays, actorID(actor))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return audit(ctx, tx, orgID, "slo.update", id, auditDetails(in), actor)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, orgID, id)
}

// Delete implements Store.
func (s *PGStore) Delete(ctx context.Context, orgID, id string, actor Actor) error {
	if _, err := uuid.Parse(id); err != nil {
		return ErrNotFound
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var name string
		err := tx.QueryRow(ctx, `DELETE FROM slos WHERE org_id = $1 AND id = $2 RETURNING name`, orgID, id).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		details, _ := json.Marshal(map[string]any{"name": name})
		return audit(ctx, tx, orgID, "slo.delete", id, details, actor)
	})
}
