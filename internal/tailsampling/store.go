package tailsampling

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/apm"
)

// StoredPolicy is a tenant's saved policy.
type StoredPolicy struct {
	Policy         Policy
	Version        int
	UpdatedAt      time.Time
	UpdatedByEmail string
}

// ErrVersionConflict is returned by Put when the stored version differs from the expected one.
var ErrVersionConflict = errors.New("tail sampling policy was changed by someone else")

// Store persists tail sampling policies per organization.
type Store interface {
	// Get returns the organization's policy, or nil when none is stored.
	Get(ctx context.Context, orgID string) (*StoredPolicy, error)
	// Put saves the policy. expectedVersion is the version the caller edited (0: no stored policy).
	Put(ctx context.Context, orgID string, p Policy, expectedVersion int, actor apm.Actor) (StoredPolicy, error)
}

// PGStore is the PostgreSQL store (migrations/postgres/0040_tail_sampling.sql).
type PGStore struct {
	Pool *pgxpool.Pool
}

// Get implements Store.
func (s PGStore) Get(ctx context.Context, orgID string) (*StoredPolicy, error) {
	var (
		raw []byte
		out StoredPolicy
	)
	err := s.Pool.QueryRow(ctx, `SELECT p.policy, p.version, p.updated_at, COALESCE(u.email, '')
		FROM tail_sampling_policies p LEFT JOIN users u ON u.id = p.updated_by WHERE p.org_id = $1`, orgID).
		Scan(&raw, &out.Version, &out.UpdatedAt, &out.UpdatedByEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if out.Policy, err = ParsePolicy(raw); err != nil {
		return nil, err
	}
	return &out, nil
}

// Put implements Store. The upsert and the audit event (apm.tail_sampling.update) share a transaction.
func (s PGStore) Put(ctx context.Context, orgID string, p Policy, expectedVersion int, actor apm.Actor) (StoredPolicy, error) {
	if err := p.Validate(); err != nil {
		return StoredPolicy{}, err
	}
	doc, err := json.Marshal(p)
	if err != nil {
		return StoredPolicy{}, err
	}
	var userID any
	if actor.UserID != "" {
		userID = actor.UserID
	}
	// A change made with an API key has no user: the key is named instead (D-133).
	var keyID any
	if actor.APIKeyID != "" {
		keyID = actor.APIKeyID
	}
	out := StoredPolicy{Policy: p, UpdatedByEmail: actor.Email}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var current int
		if err := tx.QueryRow(ctx, `SELECT version FROM tail_sampling_policies WHERE org_id = $1 FOR UPDATE`, orgID).Scan(&current); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if current != expectedVersion {
			return ErrVersionConflict
		}
		if err := tx.QueryRow(ctx, `INSERT INTO tail_sampling_policies (org_id, policy, version, updated_at, updated_by)
			VALUES ($1, $2, 1, now(), $3)
			ON CONFLICT (org_id) DO UPDATE SET policy = EXCLUDED.policy, version = tail_sampling_policies.version + 1,
				updated_at = EXCLUDED.updated_at, updated_by = EXCLUDED.updated_by
			RETURNING version, updated_at`, orgID, doc, userID).Scan(&out.Version, &out.UpdatedAt); err != nil {
			return err
		}
		details, _ := json.Marshal(map[string]any{"version": out.Version, "enabled": p.Enabled, "baseline_ratio": p.BaselineRatio,
			"max_spans_per_second": p.MaxSpansPerSecond, "rules": len(p.Rules)})
		_, err := tx.Exec(ctx, `INSERT INTO audit_log (org_id, actor_user_id, actor_email, actor_api_key_id, actor_api_key_name,
			action, target_type, target_id, details, ip)
			VALUES ($1, $2, $3, $4, $5, 'apm.tail_sampling.update', 'tail_sampling_policy', $1::text, $6, $7)`,
			orgID, userID, actor.Email, keyID, actor.APIKeyName, details, actor.IP)
		return err
	})
	if err != nil {
		return StoredPolicy{}, err
	}
	return out, nil
}

// LoadAll returns the stored policy of every tenant (by tenant id). Invalid documents are skipped.
func (s PGStore) LoadAll(ctx context.Context) (map[string]Policy, error) {
	rows, err := s.Pool.Query(ctx, `SELECT o.tenant_id, p.policy FROM tail_sampling_policies p JOIN organizations o ON o.id = p.org_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Policy{}
	for rows.Next() {
		var (
			tenant string
			raw    []byte
		)
		if err := rows.Scan(&tenant, &raw); err != nil {
			return nil, err
		}
		if p, err := ParsePolicy(raw); err == nil {
			out[tenant] = p
		}
	}
	return out, rows.Err()
}

// PolicyCache serves policies from memory and reloads them periodically. Until the first load
// succeeds, and for tenants without a stored policy, Default applies.
type PolicyCache struct {
	Default  Policy
	Load     func(ctx context.Context) (map[string]Policy, error)
	Interval time.Duration
	Log      *slog.Logger
	Metrics  *Metrics

	mu sync.RWMutex
	m  map[string]Policy
}

// Policy implements Policies.
func (c *PolicyCache) Policy(tenantID string) Policy {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if p, ok := c.m[tenantID]; ok {
		return p
	}
	return c.Default
}

// Refresh loads the policies once.
func (c *PolicyCache) Refresh(ctx context.Context) error {
	if c.Load == nil {
		return nil
	}
	lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	m, err := c.Load(lctx)
	if err != nil {
		if c.Metrics != nil {
			c.Metrics.PolicyErrors.Inc()
		}
		return err
	}
	c.mu.Lock()
	c.m = m
	c.mu.Unlock()
	return nil
}

// Run refreshes every Interval until ctx is done.
func (c *PolicyCache) Run(ctx context.Context) {
	interval := c.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	for {
		if err := c.Refresh(ctx); err != nil && ctx.Err() == nil && c.Log != nil {
			c.Log.Warn("tail sampling policies reload failed, keeping the previous ones", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
