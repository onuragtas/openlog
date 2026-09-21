package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/rum"
)

// Browser keys (migrations/postgres/0093_browser_keys.sql, docs/contracts/rum.md §3).
//
// The same Store serves two interfaces, exactly as it does for license keys: auth.Store for the management
// endpoints and rum.Store for the ingest lookup. Keeping the ingest lookup out of auth.Store is what lets
// internal/rum stay free of a dependency on internal/auth (internal/quota already imports auth, so the
// reverse edge would close a cycle).
var _ rum.Store = (*Store)(nil)

// browserKeyCols and scanBrowserKey are positional: every column here has its destination at the same index
// below, and a column inserted in one but not the other corrupts silently rather than failing.
const browserKeyCols = `k.id::text, k.org_id::text, k.name, k.key_prefix, k.key_hash, k.key_value, k.service_name, k.environment,
	k.kind, k.origins, k.app_ids, k.rate_limit_per_minute, k.sample_rate, coalesce(k.created_by::text, ''),
	k.created_at, k.updated_at, k.last_used_at, k.revoked_at`

func scanBrowserKey(r pgx.Row, extra ...any) (auth.BrowserKey, error) {
	var k auth.BrowserKey
	dest := append([]any{&k.ID, &k.OrgID, &k.Name, &k.Prefix, &k.Hash, &k.Value, &k.ServiceName, &k.Environment,
		&k.Kind, &k.Origins, &k.AppIDs, &k.RateLimitPerMinute, &k.SampleRate, &k.CreatedBy,
		&k.CreatedAt, &k.UpdatedAt, &k.LastUsedAt, &k.RevokedAt}, extra...)
	return k, r.Scan(dest...)
}

func (s *Store) CreateBrowserKey(ctx context.Context, k *auth.BrowserKey) error {
	k.CreatedAt = ts(k.CreatedAt)
	k.UpdatedAt = ts(k.UpdatedAt)
	return mapErr(s.pool.QueryRow(ctx, `INSERT INTO browser_keys
		(org_id, name, key_prefix, key_hash, key_value, service_name, environment, kind, origins, app_ids,
		 rate_limit_per_minute, sample_rate, created_by, created_at, updated_at, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::uuid, $14, $15, $13::uuid) RETURNING id::text`,
		k.OrgID, k.Name, k.Prefix, k.Hash, k.Value, k.ServiceName, k.Environment, k.Kind, k.Origins, k.AppIDs,
		k.RateLimitPerMinute, k.SampleRate, nullID(k.CreatedBy), k.CreatedAt, k.UpdatedAt).Scan(&k.ID))
}

func (s *Store) ListBrowserKeys(ctx context.Context, orgID string) ([]auth.BrowserKey, error) {
	if !validID(orgID) {
		return []auth.BrowserKey{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+browserKeyCols+`, coalesce(u.email, '')
		FROM browser_keys k LEFT JOIN users u ON u.id = k.created_by
		WHERE k.org_id = $1 ORDER BY k.created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (auth.BrowserKey, error) {
		var email string
		k, err := scanBrowserKey(r, &email)
		k.CreatedByEmail = email
		return k, err
	})
}

func (s *Store) GetBrowserKey(ctx context.Context, orgID, id string) (auth.BrowserKey, error) {
	if !validID(orgID, id) {
		return auth.BrowserKey{}, auth.ErrNotFound
	}
	var email string
	k, err := scanBrowserKey(s.pool.QueryRow(ctx, `SELECT `+browserKeyCols+`, coalesce(u.email, '')
		FROM browser_keys k LEFT JOIN users u ON u.id = k.created_by
		WHERE k.org_id = $1 AND k.id = $2`, orgID, id), &email)
	k.CreatedByEmail = email
	return k, mapErr(err)
}

// UpdateBrowserKey replaces the editable fields of a key that has not been revoked. A revoked key is never
// edited back into service: its value may already be cached in browsers, so reviving it would silently
// re-authorize traffic an operator believed they had stopped.
func (s *Store) UpdateBrowserKey(ctx context.Context, orgID, id string, in auth.BrowserKeyInput, by string, at time.Time) (auth.BrowserKey, error) {
	if !validID(orgID, id) {
		return auth.BrowserKey{}, auth.ErrNotFound
	}
	k, err := scanBrowserKey(s.pool.QueryRow(ctx, `UPDATE browser_keys k
		SET name = $3, service_name = $4, environment = $5, kind = $6, origins = $7, app_ids = $8,
		    rate_limit_per_minute = $9, sample_rate = $10, updated_at = $11, updated_by = $12::uuid
		WHERE k.org_id = $1 AND k.id = $2 AND k.revoked_at IS NULL
		RETURNING `+browserKeyCols,
		orgID, id, in.Name, in.ServiceName, in.Environment, in.Kind, in.Origins, in.AppIDs,
		in.RateLimitPerMinute, in.SampleRate, ts(at), nullID(by)))
	return k, mapErr(err)
}

func (s *Store) RevokeBrowserKey(ctx context.Context, orgID, id, by string, at time.Time) (auth.BrowserKey, error) {
	if !validID(orgID, id) {
		return auth.BrowserKey{}, auth.ErrNotFound
	}
	k, err := scanBrowserKey(s.pool.QueryRow(ctx, `UPDATE browser_keys k
		SET revoked_at = coalesce(k.revoked_at, $3), revoked_by = coalesce(k.revoked_by, $4::uuid)
		WHERE k.org_id = $1 AND k.id = $2 RETURNING `+browserKeyCols, orgID, id, ts(at), nullID(by)))
	return k, mapErr(err)
}

// LookupBrowserKey implements rum.Store: the ingest-side resolution of a browser key. It returns everything
// the ingest needs to both authorize and rewrite the payload in one round trip — the tenant, the application
// name the payload is forced into, the origin allowlist, the rate limit and the sample rate.
//
// Unlike LookupLicenseKey there is no rehash-on-read (D-044): browser keys are always generated with 192
// bits of entropy and were introduced after OPENLOG_KEY_HASH_SECRET existed, so no row can carry an older
// hash format. Candidate hashes are still tried in order, so a secret rotation keeps working.
func (s *Store) LookupBrowserKey(ctx context.Context, hashes [][]byte) (rum.Key, error) {
	if len(hashes) == 0 {
		return rum.Key{}, rum.ErrUnknownKey
	}
	var k rum.Key
	err := s.pool.QueryRow(ctx, `SELECT k.id::text, o.tenant_id, k.service_name, k.environment, k.kind,
			k.origins, k.app_ids, k.rate_limit_per_minute, k.sample_rate
		FROM browser_keys k JOIN organizations o ON o.id = k.org_id
		WHERE k.key_hash = ANY($1::bytea[]) AND k.revoked_at IS NULL AND o.deleted_at IS NULL
		ORDER BY array_position($1::bytea[], k.key_hash) LIMIT 1`, hashes).
		Scan(&k.KeyID, &k.TenantID, &k.ServiceName, &k.Environment, &k.Kind, &k.Origins, &k.AppIDs,
			&k.RateLimitPerMinute, &k.SampleRate)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return rum.Key{}, rum.ErrUnknownKey
		}
		return rum.Key{}, err
	}
	return k, nil
}

// TouchBrowserKeys implements rum.Store. Rows used within the last minute are not rewritten.
func (s *Store) TouchBrowserKeys(ctx context.Context, ids []string, at time.Time) error {
	valid := ids[:0:0]
	for _, id := range ids {
		if validID(id) {
			valid = append(valid, id)
		}
	}
	if len(valid) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE browser_keys SET last_used_at = $2::timestamptz
		WHERE id = ANY($1::uuid[]) AND (last_used_at IS NULL OR last_used_at < $2::timestamptz - interval '1 minute')`, valid, at)
	return err
}
