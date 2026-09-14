package dashboard

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Version history (migrations/postgres/0053_dashboard_versions.sql).

// insertVersion stores the snapshot of d as version and prunes all but the newest MaxVersions versions.
func insertVersion(ctx context.Context, tx pgx.Tx, d *Dashboard, version int) error {
	doc, err := json.Marshal(d.Snapshot())
	if err != nil {
		return err
	}
	var restored *int
	if d.RestoredFrom > 0 {
		restored = &d.RestoredFrom
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO dashboard_versions (dashboard_id, version, document, author_id, restored_from)
		VALUES ($1, $2, $3::jsonb, $4, $5)
		ON CONFLICT (dashboard_id, version) DO UPDATE SET document = EXCLUDED.document, author_id = EXCLUDED.author_id,
		    restored_from = EXCLUDED.restored_from, created_at = now()`,
		d.ID, version, string(doc), nullID(d.UpdatedBy), restored); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		DELETE FROM dashboard_versions WHERE dashboard_id = $1 AND version < (
		    SELECT min(version) FROM (SELECT version FROM dashboard_versions WHERE dashboard_id = $1 ORDER BY version DESC LIMIT $2) newest)`,
		d.ID, MaxVersions)
	return err
}

func (s *PGStore) ListVersions(ctx context.Context, orgID, id string) ([]VersionInfo, error) {
	cid, ok := canonicalID(id)
	if _, okOrg := canonicalID(orgID); !ok || !okOrg {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `
		SELECT v.version, coalesce(v.author_id::text, ''), coalesce(u.email, ''), v.created_at, coalesce(v.restored_from, 0),
		       jsonb_array_length(v.document->'pages'),
		       (SELECT coalesce(sum(jsonb_array_length(p->'widgets')), 0) FROM jsonb_array_elements(v.document->'pages') p)
		FROM dashboard_versions v
		JOIN dashboards d ON d.id = v.dashboard_id AND d.org_id = $1
		LEFT JOIN users u ON u.id = v.author_id
		WHERE v.dashboard_id = $2
		ORDER BY v.version DESC
		LIMIT $3`, orgID, cid, MaxVersions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VersionInfo{}
	for rows.Next() {
		var vi VersionInfo
		if err := rows.Scan(&vi.Version, &vi.AuthorID, &vi.AuthorEmail, &vi.CreatedAt, &vi.RestoredFrom, &vi.PageCount, &vi.WidgetCount); err != nil {
			return nil, err
		}
		out = append(out, vi)
	}
	return out, rows.Err()
}

func (s *PGStore) GetVersion(ctx context.Context, orgID, id string, version int) (*Version, error) {
	cid, ok := canonicalID(id)
	if _, okOrg := canonicalID(orgID); !ok || !okOrg {
		return nil, ErrNotFound
	}
	var (
		v   Version
		doc []byte
	)
	err := s.pool.QueryRow(ctx, `
		SELECT v.version, coalesce(v.author_id::text, ''), coalesce(u.email, ''), v.created_at, coalesce(v.restored_from, 0), v.document
		FROM dashboard_versions v
		JOIN dashboards d ON d.id = v.dashboard_id AND d.org_id = $1
		LEFT JOIN users u ON u.id = v.author_id
		WHERE v.dashboard_id = $2 AND v.version = $3`, orgID, cid, version).
		Scan(&v.Version, &v.AuthorID, &v.AuthorEmail, &v.CreatedAt, &v.RestoredFrom, &doc)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(doc, &v.Document); err != nil {
		return nil, err
	}
	fixSnapshot(&v)
	return &v, nil
}

// fixSnapshot replaces nil slices and fills the counts.
func fixSnapshot(v *Version) {
	d := &v.Document
	if d.Variables == nil {
		d.Variables = []Variable{}
	}
	if d.Pages == nil {
		d.Pages = []Page{}
	}
	v.PageCount, v.WidgetCount = len(d.Pages), 0
	for i := range d.Pages {
		if d.Pages[i].Widgets == nil {
			d.Pages[i].Widgets = []Widget{}
		}
		v.WidgetCount += len(d.Pages[i].Widgets)
		for j := range d.Pages[i].Widgets {
			if d.Pages[i].Widgets[j].Thresholds == nil {
				d.Pages[i].Widgets[j].Thresholds = []Threshold{}
			}
		}
	}
}
