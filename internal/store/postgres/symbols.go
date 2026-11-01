package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/onuragtas/openlog/internal/symbols"
)

// Crash symbols (migrations/postgres/0106_crash_symbols.sql, docs/contracts/mobile-crash.md §3). The files live in object
// storage, and this is the index.
var _ symbols.MetaStore = (*Store)(nil)

const symbolCols = `id::text, org_id::text, app, kind, key, size_bytes, sha256, coalesce(created_by::text, ''), created_at, updated_at`

func scanSymbol(r pgx.Row) (symbols.Record, error) {
	var s symbols.Record
	err := r.Scan(&s.ID, &s.OrgID, &s.App, &s.Kind, &s.Key, &s.Size, &s.SHA256, &s.CreatedBy, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

func symbolErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return symbols.ErrNotFound
	}
	return err
}

func (s *Store) PutSymbolFile(ctx context.Context, r *symbols.Record) error {
	return s.pool.QueryRow(ctx, `INSERT INTO crash_symbols (org_id, app, kind, key, size_bytes, sha256, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7::uuid)
		ON CONFLICT (org_id, app, kind, key) DO UPDATE SET
			size_bytes = excluded.size_bytes, sha256 = excluded.sha256, updated_at = now()
		RETURNING id::text, created_at, updated_at`,
		r.OrgID, r.App, r.Kind, r.Key, r.Size, r.SHA256, nullID(r.CreatedBy)).Scan(&r.ID, &r.CreatedAt, &r.UpdatedAt)
}

func (s *Store) ListSymbolFiles(ctx context.Context, orgID string) ([]symbols.Record, error) {
	if !validID(orgID) {
		return []symbols.Record{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+symbolCols+` FROM crash_symbols WHERE org_id = $1 ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (symbols.Record, error) { return scanSymbol(r) })
}

func (s *Store) FindSymbolFile(ctx context.Context, orgID, app, kind, key string) (symbols.Record, error) {
	if !validID(orgID) {
		return symbols.Record{}, symbols.ErrNotFound
	}
	rec, err := scanSymbol(s.pool.QueryRow(ctx, `SELECT `+symbolCols+` FROM crash_symbols
		WHERE org_id = $1 AND app = $2 AND kind = $3 AND key = $4`, orgID, app, kind, key))
	return rec, symbolErr(err)
}

func (s *Store) DeleteSymbolFile(ctx context.Context, orgID, id string) (symbols.Record, error) {
	if !validID(orgID, id) {
		return symbols.Record{}, symbols.ErrNotFound
	}
	rec, err := scanSymbol(s.pool.QueryRow(ctx, `DELETE FROM crash_symbols WHERE org_id = $1 AND id = $2 RETURNING `+symbolCols, orgID, id))
	return rec, symbolErr(err)
}
