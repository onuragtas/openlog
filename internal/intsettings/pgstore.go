package intsettings

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore implements Store on PostgreSQL (migrations/postgres/0008_integration_settings.sql).
type PGStore struct{ pool *pgxpool.Pool }

var _ Store = (*PGStore)(nil)

// NewPGStore wraps a pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

func validUUID(ids ...string) bool {
	for _, id := range ids {
		if _, err := uuid.Parse(id); err != nil {
			return false
		}
	}
	return true
}

func nullUUID(id string) *string {
	if id == "" || !validUUID(id) {
		return nil
	}
	return &id
}

func nullText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// IsUndefinedTable reports a query against a table that does not exist yet (migration 0008 not applied).
func IsUndefinedTable(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "42P01"
}

func mapPGErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return ErrConflict
	}
	return err
}

const settingCols = `s.id::text, s.org_id::text, coalesce(s.host_id, ''), s.integration, s.match_port, s.match_container,
	s.match_endpoint, s.match_instance, s.enabled, s.endpoint, s.username, s.password_enc, s.database, s.databases,
	s.created_at, s.updated_at, coalesce(s.updated_by::text, ''), coalesce(u.email, '')`

const settingFrom = ` FROM integration_settings s LEFT JOIN users u ON u.id = s.updated_by `

func scanSetting(row pgx.Row) (Setting, error) {
	var (
		s    Setting
		port *int32
	)
	err := row.Scan(&s.ID, &s.OrgID, &s.HostID, &s.Integration, &port, &s.Match.Container, &s.Match.Endpoint, &s.Match.Instance,
		&s.Enabled, &s.Endpoint, &s.Username, &s.PasswordEnc, &s.Database, &s.Databases, &s.CreatedAt, &s.UpdatedAt,
		&s.UpdatedBy, &s.UpdatedByEmail)
	if port != nil {
		p := int(*port)
		s.Match.Port = &p
	}
	if s.Databases == nil {
		s.Databases = []string{}
	}
	return s, err
}

func (s *PGStore) ListSettings(ctx context.Context, orgID string) ([]Setting, error) {
	if !validUUID(orgID) {
		return []Setting{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+settingCols+settingFrom+`WHERE s.org_id = $1 ORDER BY s.created_at, s.id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Setting{}
	for rows.Next() {
		st, err := scanSetting(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *PGStore) GetSetting(ctx context.Context, orgID, id string) (Setting, error) {
	if !validUUID(orgID, id) {
		return Setting{}, ErrNotFound
	}
	st, err := scanSetting(s.pool.QueryRow(ctx, `SELECT `+settingCols+settingFrom+`WHERE s.org_id = $1 AND s.id = $2`, orgID, id))
	return st, mapPGErr(err)
}

func matchPort(m Match) *int32 {
	if m.Port == nil {
		return nil
	}
	p := int32(*m.Port)
	return &p
}

func databases(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func (s *PGStore) CreateSetting(ctx context.Context, st Setting) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO integration_settings (id, org_id, host_id, integration, match_port, match_container,
			match_endpoint, match_instance, enabled, endpoint, username, password_enc, database, databases, created_at, updated_at, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $15, $16::uuid)`,
		st.ID, st.OrgID, nullText(st.HostID), st.Integration, matchPort(st.Match), st.Match.Container, st.Match.Endpoint,
		st.Match.Instance, st.Enabled, st.Endpoint, st.Username, st.PasswordEnc, st.Database, databases(st.Databases),
		st.CreatedAt, nullUUID(st.UpdatedBy))
	return mapPGErr(err)
}

func (s *PGStore) UpdateSetting(ctx context.Context, st Setting) error {
	if !validUUID(st.OrgID, st.ID) {
		return ErrNotFound
	}
	tag, err := s.pool.Exec(ctx, `UPDATE integration_settings SET host_id = $3, integration = $4, match_port = $5, match_container = $6,
			match_endpoint = $7, match_instance = $8, enabled = $9, endpoint = $10, username = $11, password_enc = $12, database = $13,
			databases = $14, updated_at = $15, updated_by = $16::uuid
		WHERE org_id = $1 AND id = $2`,
		st.OrgID, st.ID, nullText(st.HostID), st.Integration, matchPort(st.Match), st.Match.Container, st.Match.Endpoint,
		st.Match.Instance, st.Enabled, st.Endpoint, st.Username, st.PasswordEnc, st.Database, databases(st.Databases),
		st.UpdatedAt, nullUUID(st.UpdatedBy))
	if err != nil {
		return mapPGErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) DeleteSetting(ctx context.Context, orgID, id string) (Setting, error) {
	if !validUUID(orgID, id) {
		return Setting{}, ErrNotFound
	}
	st, err := scanSetting(s.pool.QueryRow(ctx, `WITH s AS (DELETE FROM integration_settings WHERE org_id = $1 AND id = $2 RETURNING *)
		SELECT `+settingCols+` FROM s LEFT JOIN users u ON u.id = s.updated_by`, orgID, id))
	return st, mapPGErr(err)
}

func (s *PGStore) AddAudit(ctx context.Context, e AuditEntry) error {
	details := []byte("{}")
	if len(e.Details) > 0 {
		b, err := json.Marshal(e.Details)
		if err != nil {
			return err
		}
		details = b
	}
	at := e.At
	if at.IsZero() {
		at = time.Now()
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO audit_log (org_id, actor_user_id, actor_email, action, target_type, target_id, details, ip, created_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7::jsonb, $8, $9)`,
		nullUUID(e.OrgID), nullUUID(e.ActorUserID), e.ActorEmail, e.Action, e.TargetType, e.TargetID, string(details), e.IP, at)
	return err
}
