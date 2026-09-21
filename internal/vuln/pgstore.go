package vuln

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore is the PostgreSQL Catalog (migrations/postgres/0097_vulnerabilities.sql). The catalog is
// installation-wide rather than per organization: OSV advisories are public data and every tenant's hosts
// are matched against the same rows.
type PGStore struct{ pool *pgxpool.Pool }

var _ Catalog = (*PGStore)(nil)

// NewPGStore wraps a pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

// Upsert implements Catalog. An advisory is replaced whole — its ranges are deleted and rewritten — because
// a feed that drops a range means the range no longer applies, and merging would keep it forever.
func (s *PGStore) Upsert(ctx context.Context, source string, vulns []Vulnerability) error {
	if len(vulns) == 0 {
		return nil
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		now := time.Now().UTC()
		batch := &pgx.Batch{}
		for _, v := range vulns {
			batch.Queue(`INSERT INTO vulnerabilities (id, source, cve, aliases, summary, details, severity,
				score, vector, published, modified, references_, withdrawn, synced_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
				ON CONFLICT (id) DO UPDATE SET source = EXCLUDED.source, cve = EXCLUDED.cve,
					aliases = EXCLUDED.aliases, summary = EXCLUDED.summary, details = EXCLUDED.details,
					severity = EXCLUDED.severity, score = EXCLUDED.score, vector = EXCLUDED.vector,
					published = EXCLUDED.published, modified = EXCLUDED.modified,
					references_ = EXCLUDED.references_, withdrawn = EXCLUDED.withdrawn,
					synced_at = EXCLUDED.synced_at`,
				v.ID, source, v.CVE(), nonNil(v.Aliases), v.Summary, v.Details, v.Severity, clampScore(v.Score),
				v.Vector, nullTime(v.Published), nullTime(v.Modified), nonNil(v.References), v.Withdrawn, now)
			batch.Queue(`DELETE FROM vulnerability_affected WHERE vuln_id = $1`, v.ID)
			for _, a := range v.Affected {
				batch.Queue(`INSERT INTO vulnerability_affected (vuln_id, ecosystem, package, introduced, fixed, last_affected)
					VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING`,
					v.ID, a.Ecosystem, a.Package, a.Introduced, a.Fixed, a.LastAffected)
			}
		}
		results := tx.SendBatch(ctx, batch)
		defer results.Close()
		for range batch.Len() {
			if _, err := results.Exec(); err != nil {
				return err
			}
		}
		return nil
	})
}

// Affected implements Catalog: every range of the named ecosystems, joined with the advisory's own fields.
// A withdrawn advisory is left out, which is what withdrawing one means.
func (s *PGStore) Affected(ctx context.Context, ecosystems []string) ([]AffectedRow, error) {
	if len(ecosystems) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT a.vuln_id, a.ecosystem, a.package, a.introduced, a.fixed,
		a.last_affected, v.cve, v.severity, v.score, v.summary
		FROM vulnerability_affected a
		JOIN vulnerabilities v ON v.id = a.vuln_id
		WHERE a.ecosystem = ANY($1) AND NOT v.withdrawn`, ecosystems)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AffectedRow{}
	for rows.Next() {
		var r AffectedRow
		if err := rows.Scan(&r.VulnID, &r.Ecosystem, &r.Package, &r.Introduced, &r.Fixed, &r.LastAffected,
			&r.CVE, &r.Severity, &r.Score, &r.Summary); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Stats implements Catalog.
func (s *PGStore) Stats(ctx context.Context) (CatalogStats, error) {
	var st CatalogStats
	var last *time.Time
	err := s.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM vulnerabilities),
		(SELECT count(*) FROM vulnerability_affected),
		(SELECT count(DISTINCT ecosystem) FROM vulnerability_affected),
		(SELECT max(synced_at) FROM vulnerability_sync)`).Scan(&st.Vulnerabilities, &st.AffectedRanges, &st.Ecosystems, &last)
	st.LastSyncedAt = last
	return st, err
}

// MarkSync implements Catalog.
func (s *PGStore) MarkSync(ctx context.Context, source, ecosystem string, at time.Time, count int, syncErr error) error {
	msg := ""
	if syncErr != nil {
		msg = truncate(syncErr.Error(), 1024)
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO vulnerability_sync (source, ecosystem, synced_at, count, error)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (source, ecosystem) DO UPDATE SET synced_at = EXCLUDED.synced_at,
			count = EXCLUDED.count, error = EXCLUDED.error`, source, ecosystem, at.UTC(), count, msg)
	return err
}

// SyncStatus implements Catalog.
func (s *PGStore) SyncStatus(ctx context.Context) ([]SyncState, error) {
	rows, err := s.pool.Query(ctx, `SELECT source, ecosystem, synced_at, count, error
		FROM vulnerability_sync ORDER BY ecosystem`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SyncState{}
	for rows.Next() {
		var st SyncState
		if err := rows.Scan(&st.Source, &st.Ecosystem, &st.SyncedAt, &st.Count, &st.Error); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

func clampScore(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 10:
		return 10
	}
	return v
}
