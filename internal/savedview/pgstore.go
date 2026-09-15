package savedview

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore implements Store on PostgreSQL (migrations/postgres/0080_saved_views.sql).
type PGStore struct{ pool *pgxpool.Pool }

var _ Store = (*PGStore)(nil)

// NewPGStore wraps a pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

const viewColumns = `v.id::text, v.org_id::text, v.signal, v.name, v.description, v.visibility, v.state,
	coalesce(v.created_by::text, ''), coalesce(u.email, ''), v.created_at, v.updated_at`

func scanView(row pgx.Row) (*View, error) {
	var x View
	var state []byte
	if err := row.Scan(&x.ID, &x.OrgID, &x.Signal, &x.Name, &x.Description, &x.Visibility, &state, &x.CreatedBy,
		&x.CreatedByEmail, &x.CreatedAt, &x.UpdatedAt); err != nil {
		return nil, err
	}
	x.State = state
	return &x, nil
}

func (s *PGStore) List(ctx context.Context, orgID, viewerID string, admin bool, signal string) ([]View, error) {
	if _, ok := CanonicalID(orgID); !ok {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+viewColumns+`
		FROM saved_views v LEFT JOIN users u ON u.id = v.created_by
		WHERE v.org_id = $1
		  AND ($4 = '' OR v.signal = $4)
		  AND (v.visibility = 'org' OR (v.created_by IS NOT NULL AND v.created_by::text = $2) OR (v.created_by IS NULL AND $3))
		ORDER BY lower(v.name), v.id
		LIMIT 1000`, orgID, viewerID, admin, signal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []View{}
	for rows.Next() {
		x, err := scanView(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

func (s *PGStore) Get(ctx context.Context, orgID, id string) (*View, error) {
	if _, ok := CanonicalID(orgID); !ok {
		return nil, ErrNotFound
	}
	x, err := scanView(s.pool.QueryRow(ctx, `
		SELECT `+viewColumns+`
		FROM saved_views v LEFT JOIN users u ON u.id = v.created_by
		WHERE v.org_id = $1 AND v.id = $2`, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return x, err
}

func (s *PGStore) Create(ctx context.Context, v *View, maxPerOrg int) error {
	var createdBy *string
	if v.CreatedBy != "" {
		createdBy = &v.CreatedBy
	}
	// The count check and the insert are one statement; concurrent creates may exceed the limit by a few views.
	err := s.pool.QueryRow(ctx, `
		INSERT INTO saved_views (id, org_id, signal, name, description, visibility, state, created_by)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8
		WHERE (SELECT count(*) FROM saved_views WHERE org_id = $2) < $9
		RETURNING created_at, updated_at`,
		v.ID, v.OrgID, v.Signal, v.Name, v.Description, v.Visibility, []byte(v.State), createdBy, maxPerOrg).
		Scan(&v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLimit
	}
	return err
}

func (s *PGStore) Update(ctx context.Context, v *View) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE saved_views SET signal = $3, name = $4, description = $5, visibility = $6, state = $7, updated_at = now()
		WHERE org_id = $1 AND id = $2`,
		v.OrgID, v.ID, v.Signal, v.Name, v.Description, v.Visibility, []byte(v.State))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) Delete(ctx context.Context, orgID, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM saved_views WHERE org_id = $1 AND id = $2`, orgID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
