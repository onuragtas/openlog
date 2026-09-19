package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/onuragtas/openlog/internal/sourcemaps"
)

// Source maps (migrations/postgres/0094_source_maps.sql, docs/contracts/rum.md §8). The document itself is
// in object storage; this is the index that makes listing and replacing possible without listing a bucket.
var _ sourcemaps.MetaStore = (*Store)(nil)

const sourceMapCols = `m.id::text, m.org_id::text, m.app, m.script, m.size_bytes, m.sha256,
	coalesce(m.created_by::text, ''), m.created_at, m.updated_at`

func scanSourceMap(r pgx.Row, extra ...any) (sourcemaps.Record, error) {
	var m sourcemaps.Record
	dest := append([]any{&m.ID, &m.OrgID, &m.App, &m.Script, &m.Size, &m.SHA256,
		&m.CreatedBy, &m.CreatedAt, &m.UpdatedAt}, extra...)
	return m, r.Scan(dest...)
}

// PutSourceMap inserts the row or replaces the one already held for the script. The id is kept on a
// replacement, because it is the object key: a new id would leave the previous document orphaned in storage.
func (s *Store) PutSourceMap(ctx context.Context, r *sourcemaps.Record) error {
	r.CreatedAt = ts(r.CreatedAt)
	r.UpdatedAt = ts(r.UpdatedAt)
	return mapErr(s.pool.QueryRow(ctx, `INSERT INTO source_maps
		(org_id, app, script, size_bytes, sha256, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6::uuid, $7, $8)
		ON CONFLICT (org_id, app, script) DO UPDATE SET
			size_bytes = excluded.size_bytes, sha256 = excluded.sha256, updated_at = excluded.updated_at
		RETURNING id::text, created_at`,
		r.OrgID, r.App, r.Script, r.Size, r.SHA256, nullID(r.CreatedBy), r.CreatedAt, r.UpdatedAt).
		Scan(&r.ID, &r.CreatedAt))
}

func (s *Store) ListSourceMaps(ctx context.Context, orgID string) ([]sourcemaps.Record, error) {
	if !validID(orgID) {
		return []sourcemaps.Record{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+sourceMapCols+`, coalesce(u.email, '')
		FROM source_maps m LEFT JOIN users u ON u.id = m.created_by
		WHERE m.org_id = $1 ORDER BY m.created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (sourcemaps.Record, error) {
		var email string
		m, err := scanSourceMap(r, &email)
		m.CreatedByEmail = email
		return m, err
	})
}

func (s *Store) FindSourceMap(ctx context.Context, orgID, app, script string) (sourcemaps.Record, error) {
	if !validID(orgID) {
		return sourcemaps.Record{}, sourcemaps.ErrNotFound
	}
	m, err := scanSourceMap(s.pool.QueryRow(ctx, `SELECT `+sourceMapCols+`
		FROM source_maps m WHERE m.org_id = $1 AND m.app = $2 AND m.script = $3`, orgID, app, script))
	if errors.Is(err, pgx.ErrNoRows) {
		return sourcemaps.Record{}, sourcemaps.ErrNotFound
	}
	return m, mapErr(err)
}

func (s *Store) DeleteSourceMap(ctx context.Context, orgID, id string) (sourcemaps.Record, error) {
	if !validID(orgID) || !validID(id) {
		return sourcemaps.Record{}, sourcemaps.ErrNotFound
	}
	m, err := scanSourceMap(s.pool.QueryRow(ctx, `DELETE FROM source_maps m
		WHERE m.org_id = $1 AND m.id = $2 RETURNING `+sourceMapCols, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return sourcemaps.Record{}, sourcemaps.ErrNotFound
	}
	return m, mapErr(err)
}
