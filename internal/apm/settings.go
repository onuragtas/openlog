package apm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultApdexT is the Apdex threshold of services without a setting (apm.md §4).
const DefaultApdexT = 500 * time.Millisecond

// Apdex T bounds accepted by the settings API and the database.
const (
	MinApdexTMs = 1
	MaxApdexTMs = 600000
)

// ServiceKey identifies a service (apm.md §1).
type ServiceKey struct {
	Name        string
	Namespace   string
	Environment string
}

// Setting is one row of apm_service_settings.
type Setting struct {
	Key            ServiceKey
	ApdexTMs       int
	UpdatedAt      time.Time
	UpdatedByEmail string
}

// Actor is who changed a setting (audit log): a signed-in user, or an API key
// acting with its own role (D-133), in which case UserID and Email are empty.
type Actor struct {
	UserID     string
	Email      string
	IP         string
	APIKeyID   string
	APIKeyName string
}

// SettingsStore persists per-service settings per organization.
type SettingsStore interface {
	// List returns every setting of the organization.
	List(ctx context.Context, orgID string) ([]Setting, error)
	// Put upserts the Apdex T of key and writes an audit event.
	Put(ctx context.Context, orgID string, key ServiceKey, apdexTMs int, actor Actor) (Setting, error)
}

// ResolveApdexT returns the setting that applies to key: exact match, else the
// namespace/environment wildcard row of the service, else nil (default).
func ResolveApdexT(settings []Setting, key ServiceKey) *Setting {
	var wildcard *Setting
	for i := range settings {
		s := &settings[i]
		if s.Key.Name != key.Name {
			continue
		}
		if s.Key == key {
			return s
		}
		if s.Key.Namespace == "" && s.Key.Environment == "" {
			wildcard = s
		}
	}
	return wildcard
}

// PGSettings is the PostgreSQL SettingsStore (migrations/postgres/0005_apm.sql).
type PGSettings struct {
	Pool *pgxpool.Pool
}

// List implements SettingsStore.
func (s PGSettings) List(ctx context.Context, orgID string) ([]Setting, error) {
	rows, err := s.Pool.Query(ctx, `SELECT st.service_name, st.service_namespace, st.deployment_environment, st.apdex_t_ms,
		st.updated_at, COALESCE(u.email, '')
		FROM apm_service_settings st LEFT JOIN users u ON u.id = st.updated_by
		WHERE st.org_id = $1 ORDER BY st.service_name, st.service_namespace, st.deployment_environment`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Setting
	for rows.Next() {
		var st Setting
		if err := rows.Scan(&st.Key.Name, &st.Key.Namespace, &st.Key.Environment, &st.ApdexTMs, &st.UpdatedAt, &st.UpdatedByEmail); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// ErrInvalidSetting is returned for values outside the accepted range.
var ErrInvalidSetting = errors.New("invalid apm setting")

// Put implements SettingsStore. The upsert and the audit event (action
// apm.service_settings.update) are written in one transaction.
func (s PGSettings) Put(ctx context.Context, orgID string, key ServiceKey, apdexTMs int, actor Actor) (Setting, error) {
	if key.Name == "" || apdexTMs < MinApdexTMs || apdexTMs > MaxApdexTMs {
		return Setting{}, fmt.Errorf("%w: apdex_t_ms must be between %d and %d", ErrInvalidSetting, MinApdexTMs, MaxApdexTMs)
	}
	var userID any
	if actor.UserID != "" {
		userID = actor.UserID
	}
	var out Setting
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var previous *int
		if err := tx.QueryRow(ctx, `SELECT apdex_t_ms FROM apm_service_settings
			WHERE org_id = $1 AND service_name = $2 AND service_namespace = $3 AND deployment_environment = $4 FOR UPDATE`,
			orgID, key.Name, key.Namespace, key.Environment).Scan(&previous); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO apm_service_settings
			(org_id, service_name, service_namespace, deployment_environment, apdex_t_ms, updated_at, updated_by)
			VALUES ($1, $2, $3, $4, $5, now(), $6)
			ON CONFLICT (org_id, service_name, service_namespace, deployment_environment)
			DO UPDATE SET apdex_t_ms = EXCLUDED.apdex_t_ms, updated_at = EXCLUDED.updated_at, updated_by = EXCLUDED.updated_by
			RETURNING updated_at`, orgID, key.Name, key.Namespace, key.Environment, apdexTMs, userID).Scan(&out.UpdatedAt); err != nil {
			return err
		}
		details, _ := json.Marshal(map[string]any{"service_namespace": key.Namespace, "environment": key.Environment,
			"apdex_t_ms": apdexTMs, "previous_apdex_t_ms": previous})
		_, err := tx.Exec(ctx, `INSERT INTO audit_log (org_id, actor_user_id, actor_email, actor_api_key_id, actor_api_key_name,
			action, target_type, target_id, details, ip)
			VALUES ($1, $2, $3, $4, $5, 'apm.service_settings.update', 'apm_service', $6, $7, $8)`,
			orgID, userID, actor.Email, uuidOrNil(actor.APIKeyID), actor.APIKeyName, key.Name, details, actor.IP)
		return err
	})
	if err != nil {
		return Setting{}, err
	}
	out.Key, out.ApdexTMs, out.UpdatedByEmail = key, apdexTMs, actor.Email
	return out, nil
}
