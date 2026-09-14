package dataexport

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore persists export jobs (data_exports, 0070).
type PGStore struct {
	Pool *pgxpool.Pool
}

const exportCols = `e.id::text, e.kind, coalesce(e.org_id::text, ''), coalesce(o.name, ''), coalesce(o.tenant_id, ''), coalesce(e.user_id::text, ''),
	coalesce(u.email, ''), e.status, e.signals, e.range_from, e.range_to, e.locale, e.storage, e.object_key, e.size_bytes, e.telemetry_rows,
	e.truncated, e.manifest, e.attempts, e.error, e.created_at, e.started_at, e.completed_at, e.expires_at`

const exportFrom = ` FROM data_exports e LEFT JOIN organizations o ON o.id = e.org_id LEFT JOIN users u ON u.id = e.user_id`

func scanExport(row pgx.Row) (Export, error) {
	var e Export
	var manifest []byte
	err := row.Scan(&e.ID, &e.Kind, &e.OrgID, &e.OrgName, &e.TenantID, &e.UserID, &e.UserEmail, &e.Status, &e.Signals, &e.From, &e.To, &e.Locale,
		&e.Storage, &e.ObjectKey, &e.SizeBytes, &e.TelemetryRows, &e.Truncated, &manifest, &e.Attempts, &e.Error, &e.CreatedAt, &e.StartedAt,
		&e.CompletedAt, &e.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	if e.Signals == nil {
		e.Signals = []string{}
	}
	e.Manifest = manifest
	return e, err
}

func collect(rows pgx.Rows, err error) ([]Export, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Export, error) { return scanExport(r) })
}

// Create inserts a pending export. At most one export per organization (organization exports) or user (personal
// exports) may be pending or running, and maxPerDay may be created within 24 hours: ErrActive, ErrRateLimited.
func (s PGStore) Create(ctx context.Context, e *Export, maxPerDay int, now time.Time) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	scope, subject := "org", e.OrgID
	if e.Kind == KindUser {
		scope, subject = "user", e.UserID
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('data_export:' || $1 || ':' || $2))`, scope, subject); err != nil {
		return err
	}
	var active, recent int
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status IN ('pending', 'running')), count(*) FILTER (WHERE created_at > $3::timestamptz - interval '24 hours')
		FROM data_exports WHERE kind = $1 AND (CASE WHEN $1 = 'organization' THEN org_id::text ELSE user_id::text END) = $2`,
		e.Kind, subject, now).Scan(&active, &recent); err != nil {
		return err
	}
	if active > 0 {
		return ErrActive
	}
	if recent >= maxPerDay {
		return ErrRateLimited
	}
	var orgID *string
	if e.OrgID != "" {
		orgID = &e.OrgID
	}
	if e.Signals == nil {
		e.Signals = []string{}
	}
	if err := tx.QueryRow(ctx, `INSERT INTO data_exports (kind, org_id, user_id, signals, range_from, range_to, locale, created_at)
		VALUES ($1, $2::uuid, $3::uuid, $4, $5, $6, $7, $8) RETURNING id::text`,
		e.Kind, orgID, e.UserID, e.Signals, e.From, e.To, e.Locale, now).Scan(&e.ID); err != nil {
		return err
	}
	e.Status, e.CreatedAt = StatusPending, now
	return tx.Commit(ctx)
}

// Get returns an export.
func (s PGStore) Get(ctx context.Context, id string) (Export, error) {
	return scanExport(s.Pool.QueryRow(ctx, `SELECT `+exportCols+exportFrom+` WHERE e.id::text = $1`, id))
}

// GetByTokenHash returns the completed export whose download token hashes to hash.
func (s PGStore) GetByTokenHash(ctx context.Context, hash []byte) (Export, error) {
	return scanExport(s.Pool.QueryRow(ctx, `SELECT `+exportCols+exportFrom+` WHERE e.download_token_hash = $1 AND e.status = 'completed'`, hash))
}

// ListOrg lists the organization's exports, newest first.
func (s PGStore) ListOrg(ctx context.Context, orgID string, limit int) ([]Export, error) {
	return collect(s.Pool.Query(ctx, `SELECT `+exportCols+exportFrom+` WHERE e.kind = 'organization' AND e.org_id::text = $1
		ORDER BY e.created_at DESC LIMIT $2`, orgID, limit))
}

// ListUser lists the user's personal exports, newest first.
func (s PGStore) ListUser(ctx context.Context, userID string, limit int) ([]Export, error) {
	return collect(s.Pool.Query(ctx, `SELECT `+exportCols+exportFrom+` WHERE e.kind = 'user' AND e.user_id::text = $1
		ORDER BY e.created_at DESC LIMIT $2`, userID, limit))
}

// Claim takes the oldest pending export (running exports whose heartbeat is older than stale are retried up to three
// attempts, then failed). nil when there is none.
func (s PGStore) Claim(ctx context.Context, now time.Time, stale time.Duration) (*Export, error) {
	if _, err := s.Pool.Exec(ctx, `UPDATE data_exports SET status = CASE WHEN attempts >= 3 THEN 'failed' ELSE 'pending' END,
			error = CASE WHEN attempts >= 3 THEN 'interrupted too often' ELSE error END
		WHERE status = 'running' AND coalesce(heartbeat_at, started_at) < $1`, now.Add(-stale)); err != nil {
		return nil, err
	}
	var id string
	err := s.Pool.QueryRow(ctx, `UPDATE data_exports SET status = 'running', started_at = $1, heartbeat_at = $1, attempts = attempts + 1
		WHERE id = (SELECT id FROM data_exports WHERE status = 'pending' ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING id::text`, now).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e, err := s.Get(ctx, id)
	return &e, err
}

// Heartbeat marks a running export alive.
func (s PGStore) Heartbeat(ctx context.Context, id string, now time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE data_exports SET heartbeat_at = $2 WHERE id = $1 AND status = 'running'`, id, now)
	return err
}

// Completion is a finished archive.
type Completion struct {
	Storage       string
	ObjectKey     string
	SizeBytes     int64
	TelemetryRows int64
	Truncated     bool
	Manifest      any
	TokenHash     []byte
	ExpiresAt     time.Time
}

// Complete marks the export completed.
func (s PGStore) Complete(ctx context.Context, id string, c Completion, now time.Time) error {
	m, err := json.Marshal(c.Manifest)
	if err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE data_exports SET status = 'completed', storage = $2, object_key = $3, size_bytes = $4, telemetry_rows = $5,
			truncated = $6, manifest = $7::jsonb, download_token_hash = $8, expires_at = $9, completed_at = $10, error = ''
		WHERE id = $1 AND status = 'running'`, id, c.Storage, c.ObjectKey, c.SizeBytes, c.TelemetryRows, c.Truncated, string(m), c.TokenHash, c.ExpiresAt, now)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// Fail marks the export failed with a message safe to show to the requester.
func (s PGStore) Fail(ctx context.Context, id, msg string, now time.Time) error {
	if len(msg) > 1000 {
		msg = msg[:1000]
	}
	_, err := s.Pool.Exec(ctx, `UPDATE data_exports SET status = 'failed', error = $2, completed_at = $3 WHERE id = $1`, id, msg, now)
	return err
}

// Expired returns completed exports past their expiry.
func (s PGStore) Expired(ctx context.Context, now time.Time, limit int) ([]Export, error) {
	return collect(s.Pool.Query(ctx, `SELECT `+exportCols+exportFrom+` WHERE e.status = 'completed' AND e.expires_at <= $1
		ORDER BY e.expires_at LIMIT $2`, now, limit))
}

// MarkExpired forgets the archive and download token of an export whose object was deleted.
func (s PGStore) MarkExpired(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE data_exports SET status = 'expired', object_key = '', download_token_hash = NULL WHERE id = $1`, id)
	return err
}

// PruneOld deletes finished export rows older than age.
func (s PGStore) PruneOld(ctx context.Context, now time.Time, age time.Duration) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM data_exports WHERE status IN ('failed', 'expired') AND created_at < $1`, now.Add(-age))
	return err
}

// OrgObjects returns the stored archives of an organization's exports.
func (s PGStore) OrgObjects(ctx context.Context, orgID string) ([]Export, error) {
	return collect(s.Pool.Query(ctx, `SELECT `+exportCols+exportFrom+` WHERE e.org_id::text = $1 AND e.object_key <> ''`, orgID))
}
