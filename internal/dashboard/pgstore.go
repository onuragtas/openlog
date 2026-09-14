package dashboard

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore implements Store on PostgreSQL (migrations/postgres/0020_dashboards.sql).
type PGStore struct{ pool *pgxpool.Pool }

var _ Store = (*PGStore)(nil)

// NewPGStore wraps a pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

func nullID(id string) *string {
	if c, ok := canonicalID(id); ok {
		return &c
	}
	return nil
}

func (s *PGStore) List(ctx context.Context, orgID, viewerID string, admin bool, q string) ([]Summary, error) {
	if _, ok := canonicalID(orgID); !ok {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `
		SELECT d.id::text, d.name, d.description, d.visibility, coalesce(d.created_by::text, ''), coalesce(u.email, ''), d.updated_at,
		       (SELECT count(*) FROM dashboard_pages p WHERE p.dashboard_id = d.id),
		       (SELECT count(*) FROM dashboard_widgets w WHERE w.dashboard_id = d.id)
		FROM dashboards d LEFT JOIN users u ON u.id = d.created_by
		WHERE d.org_id = $1
		  AND (d.visibility = 'org' OR (d.created_by IS NOT NULL AND d.created_by::text = $2) OR (d.created_by IS NULL AND $3))
		  AND ($4 = '' OR strpos(lower(d.name), lower($4)) > 0)
		ORDER BY lower(d.name), d.id
		LIMIT 5000`, orgID, viewerID, admin, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Summary{}
	for rows.Next() {
		var sm Summary
		if err := rows.Scan(&sm.ID, &sm.Name, &sm.Description, &sm.Visibility, &sm.CreatedBy, &sm.CreatedByEmail, &sm.UpdatedAt,
			&sm.PageCount, &sm.WidgetCount); err != nil {
			return nil, err
		}
		out = append(out, sm)
	}
	return out, rows.Err()
}

func (s *PGStore) Get(ctx context.Context, orgID, id string) (*Dashboard, error) {
	if _, ok := canonicalID(orgID); !ok {
		return nil, ErrNotFound
	}
	cid, ok := canonicalID(id)
	if !ok {
		return nil, ErrNotFound
	}
	var d *Dashboard
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		d = &Dashboard{}
		var vars []byte
		err := tx.QueryRow(ctx, `
			SELECT d.id::text, d.org_id::text, d.name, d.description, d.visibility, d.variables, d.version,
			       coalesce(d.created_by::text, ''), coalesce(u.email, ''), coalesce(d.updated_by::text, ''), d.created_at, d.updated_at
			FROM dashboards d LEFT JOIN users u ON u.id = d.created_by
			WHERE d.org_id = $1 AND d.id = $2`, orgID, cid).
			Scan(&d.ID, &d.OrgID, &d.Name, &d.Description, &d.Visibility, &vars, &d.Version, &d.CreatedBy, &d.CreatedByEmail,
				&d.UpdatedBy, &d.CreatedAt, &d.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		d.Variables = []Variable{}
		if err := json.Unmarshal(vars, &d.Variables); err != nil {
			return err
		}
		prows, err := tx.Query(ctx, `SELECT id::text, name FROM dashboard_pages WHERE dashboard_id = $1 ORDER BY position`, cid)
		if err != nil {
			return err
		}
		index := map[string]int{}
		for prows.Next() {
			p := Page{Widgets: []Widget{}}
			if err := prows.Scan(&p.ID, &p.Name); err != nil {
				prows.Close()
				return err
			}
			index[p.ID] = len(d.Pages)
			d.Pages = append(d.Pages, p)
		}
		prows.Close()
		if err := prows.Err(); err != nil {
			return err
		}
		wrows, err := tx.Query(ctx, `
			SELECT id::text, page_id::text, title, visualization, x, y, w, h, query, markdown, unit, thresholds, options
			FROM dashboard_widgets WHERE dashboard_id = $1 ORDER BY page_id, position`, cid)
		if err != nil {
			return err
		}
		defer wrows.Close()
		for wrows.Next() {
			var (
				w              Widget
				pageID         string
				thresh, optsJS []byte
			)
			if err := wrows.Scan(&w.ID, &pageID, &w.Title, &w.Visualization, &w.Layout.X, &w.Layout.Y, &w.Layout.W, &w.Layout.H,
				&w.Query, &w.Markdown, &w.Unit, &thresh, &optsJS); err != nil {
				return err
			}
			w.Thresholds = []Threshold{}
			if err := json.Unmarshal(thresh, &w.Thresholds); err != nil {
				return err
			}
			if err := json.Unmarshal(optsJS, &w.Options); err != nil {
				return err
			}
			if i, ok := index[pageID]; ok {
				d.Pages[i].Widgets = append(d.Pages[i].Widgets, w)
			}
		}
		return wrows.Err()
	})
	if err != nil {
		return nil, err
	}
	if d.Pages == nil {
		d.Pages = []Page{}
	}
	return d, nil
}

func (s *PGStore) Create(ctx context.Context, d *Dashboard) error {
	vars, err := json.Marshal(d.Variables)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO dashboards (id, org_id, name, description, visibility, variables, version, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb, 1, $7, $8)`,
			d.ID, d.OrgID, d.Name, d.Description, d.Visibility, string(vars), nullID(d.CreatedBy), nullID(d.UpdatedBy)); err != nil {
			return err
		}
		if err := insertPages(ctx, tx, d); err != nil {
			return err
		}
		return insertVersion(ctx, tx, d, 1)
	})
}

func (s *PGStore) Replace(ctx context.Context, d *Dashboard, expectedVersion int) error {
	vars, err := json.Marshal(d.Variables)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var version int
		err := tx.QueryRow(ctx, `SELECT version FROM dashboards WHERE org_id = $1 AND id = $2 FOR UPDATE`, d.OrgID, d.ID).Scan(&version)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if version != expectedVersion {
			return ErrConflict
		}
		if _, err := tx.Exec(ctx, `
			UPDATE dashboards SET name = $3, description = $4, visibility = $5, variables = $6::jsonb, version = version + 1,
			       updated_by = $7, updated_at = now()
			WHERE org_id = $1 AND id = $2`, d.OrgID, d.ID, d.Name, d.Description, d.Visibility, string(vars), nullID(d.UpdatedBy)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM dashboard_pages WHERE dashboard_id = $1`, d.ID); err != nil {
			return err
		}
		if err := insertPages(ctx, tx, d); err != nil {
			return err
		}
		return insertVersion(ctx, tx, d, expectedVersion+1)
	})
}

func insertPages(ctx context.Context, tx pgx.Tx, d *Dashboard) error {
	for pi, p := range d.Pages {
		if _, err := tx.Exec(ctx, `INSERT INTO dashboard_pages (id, dashboard_id, position, name) VALUES ($1, $2, $3, $4)`,
			p.ID, d.ID, pi, p.Name); err != nil {
			return err
		}
		for wi, w := range p.Widgets {
			thresh, err := json.Marshal(w.Thresholds)
			if err != nil {
				return err
			}
			opts, err := json.Marshal(w.Options)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO dashboard_widgets (id, dashboard_id, page_id, position, title, visualization, x, y, w, h, query, markdown, unit, thresholds, options)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14::jsonb, $15::jsonb)`,
				w.ID, d.ID, p.ID, wi, w.Title, w.Visualization, w.Layout.X, w.Layout.Y, w.Layout.W, w.Layout.H, w.Query, w.Markdown, w.Unit,
				string(thresh), string(opts)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *PGStore) Delete(ctx context.Context, orgID, id string) error {
	cid, ok := canonicalID(id)
	if _, okOrg := canonicalID(orgID); !ok || !okOrg {
		return ErrNotFound
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM dashboards WHERE org_id = $1 AND id = $2`, orgID, cid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
