package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Sharing settings and share links (migrations/postgres/0054_dashboard_shares.sql).

var _ ShareStore = (*PGStore)(nil)

func (s *PGStore) GetSettings(ctx context.Context, orgID string) (Settings, error) {
	st := Settings{ReportDomains: []string{}}
	if _, ok := canonicalID(orgID); !ok {
		return st, ErrNotFound
	}
	err := s.pool.QueryRow(ctx, `
		SELECT shares_enabled, report_domains, coalesce(updated_by::text, ''), updated_at
		FROM dashboard_org_settings WHERE org_id = $1`, orgID).Scan(&st.SharesEnabled, &st.ReportDomains, &st.UpdatedBy, &st.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{ReportDomains: []string{}}, nil
	}
	if st.ReportDomains == nil {
		st.ReportDomains = []string{}
	}
	return st, err
}

func (s *PGStore) PutSettings(ctx context.Context, orgID string, st Settings) error {
	if _, ok := canonicalID(orgID); !ok {
		return ErrNotFound
	}
	domains := st.ReportDomains
	if domains == nil {
		domains = []string{}
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO dashboard_org_settings (org_id, shares_enabled, report_domains, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (org_id) DO UPDATE SET shares_enabled = EXCLUDED.shares_enabled, report_domains = EXCLUDED.report_domains,
		    updated_by = EXCLUDED.updated_by, updated_at = now()`, orgID, st.SharesEnabled, domains, nullID(st.UpdatedBy))
	return err
}

const shareColumns = `s.id::text, s.org_id::text, s.dashboard_id::text, s.label, s.time_range, s.range_from, s.range_to, s.variables,
	coalesce(s.created_by::text, ''), coalesce(u.email, ''), s.created_at, s.expires_at, s.revoked_at, s.last_used_at, s.use_count`

// scanShare scans shareColumns followed by extra destinations.
func scanShare(row pgx.Row, extra ...any) (*Share, error) {
	var (
		sh       Share
		from, to *time.Time
		vars     []byte
	)
	dest := append([]any{&sh.ID, &sh.OrgID, &sh.DashboardID, &sh.Label, &sh.Range, &from, &to, &vars, &sh.CreatedBy, &sh.CreatedByEmail,
		&sh.CreatedAt, &sh.ExpiresAt, &sh.RevokedAt, &sh.LastUsedAt, &sh.UseCount}, extra...)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	if from != nil && to != nil {
		sh.From, sh.To = from.UTC(), to.UTC()
	}
	sh.Variables = map[string][]string{}
	if err := json.Unmarshal(vars, &sh.Variables); err != nil {
		return nil, err
	}
	return &sh, nil
}

func (s *PGStore) ListShares(ctx context.Context, orgID, dashboardID string) ([]Share, error) {
	cid, ok := canonicalID(dashboardID)
	if _, okOrg := canonicalID(orgID); !ok || !okOrg {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `SELECT `+shareColumns+`
		FROM dashboard_shares s LEFT JOIN users u ON u.id = s.created_by
		WHERE s.org_id = $1 AND s.dashboard_id = $2
		ORDER BY s.created_at DESC, s.id LIMIT 200`, orgID, cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Share{}
	for rows.Next() {
		sh, err := scanShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sh)
	}
	return out, rows.Err()
}

func (s *PGStore) CreateShare(ctx context.Context, sh *Share, tokenHash []byte, maxActive int) error {
	vars, err := json.Marshal(sh.Variables)
	if err != nil {
		return err
	}
	var from, to *time.Time
	if sh.Range == "" {
		from, to = &sh.From, &sh.To
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var one int
		err := tx.QueryRow(ctx, `SELECT 1 FROM dashboards WHERE org_id = $1 AND id = $2 FOR UPDATE`, sh.OrgID, sh.DashboardID).Scan(&one)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var active int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM dashboard_shares WHERE dashboard_id = $1 AND revoked_at IS NULL AND expires_at > now()`,
			sh.DashboardID).Scan(&active); err != nil {
			return err
		}
		if active >= maxActive {
			return invalid("a dashboard has at most %d active share links; revoke one first", maxActive)
		}
		return tx.QueryRow(ctx, `
			INSERT INTO dashboard_shares (id, org_id, dashboard_id, token_hash, label, time_range, range_from, range_to, variables, created_by, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11) RETURNING created_at`,
			sh.ID, sh.OrgID, sh.DashboardID, tokenHash, sh.Label, sh.Range, from, to, string(vars), nullID(sh.CreatedBy), sh.ExpiresAt).Scan(&sh.CreatedAt)
	})
}

func (s *PGStore) RevokeShare(ctx context.Context, orgID, dashboardID, shareID, userID string) (*Share, error) {
	cid, ok := canonicalID(dashboardID)
	sid, okShare := canonicalID(shareID)
	if _, okOrg := canonicalID(orgID); !ok || !okOrg || !okShare {
		return nil, ErrNotFound
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE dashboard_shares SET revoked_by = CASE WHEN revoked_at IS NULL THEN $4::uuid ELSE revoked_by END,
		    revoked_at = coalesce(revoked_at, now())
		WHERE org_id = $1 AND dashboard_id = $2 AND id = $3`, orgID, cid, sid, nullID(userID))
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	sh, err := scanShare(s.pool.QueryRow(ctx, `SELECT `+shareColumns+` FROM dashboard_shares s LEFT JOIN users u ON u.id = s.created_by WHERE s.id = $1`, sid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return sh, err
}

func (s *PGStore) ResolveShare(ctx context.Context, tokenHash []byte, now time.Time) (*Share, string, bool, error) {
	var tenant string
	sh, err := scanShare(s.pool.QueryRow(ctx, `SELECT `+shareColumns+`, o.tenant_id
		FROM dashboard_shares s
		JOIN organizations o ON o.id = s.org_id
		JOIN dashboard_org_settings st ON st.org_id = s.org_id AND st.shares_enabled
		LEFT JOIN users u ON u.id = s.created_by
		WHERE s.token_hash = $1 AND s.revoked_at IS NULL AND s.expires_at > $2
		  AND s.created_by IS NOT NULL
		  AND EXISTS (SELECT 1 FROM memberships m WHERE m.org_id = s.org_id AND m.user_id = s.created_by)`, tokenHash, now), &tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", false, ErrNotFound
	}
	if err != nil {
		return nil, "", false, err
	}
	// The use is recorded at most once a minute per link; the access is audited when the previous use is older than
	// an hour (only the request that recorded the use audits it).
	audit := false
	if sh.LastUsedAt == nil || now.Sub(*sh.LastUsedAt) >= time.Minute {
		tag, err := s.pool.Exec(ctx, `
			UPDATE dashboard_shares SET last_used_at = $2, use_count = use_count + 1
			WHERE id = $1 AND (last_used_at IS NULL OR last_used_at <= $2::timestamptz - interval '1 minute')`, sh.ID, now)
		if err != nil {
			return nil, "", false, err
		}
		audit = tag.RowsAffected() == 1 && (sh.LastUsedAt == nil || now.Sub(*sh.LastUsedAt) >= time.Hour)
	}
	return sh, tenant, audit, nil
}
