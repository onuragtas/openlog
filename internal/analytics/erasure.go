// Package analytics deletes one person's product analytics data (docs/contracts/analytics.md §7).
//
// A request is queued in PostgreSQL and carried out by a leader task, because the API reads ClickHouse as a read-only
// user. The job submits mutations, which ClickHouse applies in the background, so "completed" here means submitted: the
// API says that rather than implying the rows are already gone.
package analytics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Tables holds the local analytics tables of 0102_analytics, every one of which has an actor_hash.
var Tables = []string{"analytics_events_local", "analytics_actors_local", "analytics_actor_days_local"}

// ErrNotFound is an erasure request that does not exist in this organization.
var ErrNotFound = errors.New("analytics erasure not found")

// Erasure is one request. Actor is empty once it completed: see 0105_analytics_erasures.
type Erasure struct {
	ID          string
	OrgID       string
	TenantID    string
	App         string
	Actor       string
	RequestedAt time.Time
	CompletedAt *time.Time
}

// Store is the queue.
type Store interface {
	EnqueueErasure(ctx context.Context, e Erasure) (Erasure, error)
	GetErasure(ctx context.Context, orgID, id string) (Erasure, error)
	PendingErasures(ctx context.Context, limit int) ([]Erasure, error)
	CompleteErasure(ctx context.Context, id string, at time.Time) error
}

// PGStore is the PostgreSQL Store.
type PGStore struct{ Pool *pgxpool.Pool }

// EnqueueErasure implements Store.
func (s PGStore) EnqueueErasure(ctx context.Context, e Erasure) (Erasure, error) {
	err := s.Pool.QueryRow(ctx, `INSERT INTO analytics_erasures (org_id, tenant_id, app, actor)
		VALUES ($1, $2, $3, $4) RETURNING id::text, requested_at`, e.OrgID, e.TenantID, e.App, e.Actor).Scan(&e.ID, &e.RequestedAt)
	return e, err
}

// GetErasure implements Store. The actor is not read back: nothing that asks about a request needs it.
func (s PGStore) GetErasure(ctx context.Context, orgID, id string) (Erasure, error) {
	e := Erasure{ID: id, OrgID: orgID}
	err := s.Pool.QueryRow(ctx, `SELECT tenant_id, app, requested_at, completed_at FROM analytics_erasures
		WHERE org_id = $1::uuid AND id::text = $2`, orgID, id).Scan(&e.TenantID, &e.App, &e.RequestedAt, &e.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Erasure{}, ErrNotFound
	}
	return e, err
}

// PendingErasures implements Store, oldest first.
func (s PGStore) PendingErasures(ctx context.Context, limit int) ([]Erasure, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id::text, org_id::text, tenant_id, app, actor, requested_at FROM analytics_erasures
		WHERE completed_at IS NULL ORDER BY requested_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Erasure, error) {
		var e Erasure
		return e, r.Scan(&e.ID, &e.OrgID, &e.TenantID, &e.App, &e.Actor, &e.RequestedAt)
	})
}

// CompleteErasure implements Store, and forgets who it was for.
func (s PGStore) CompleteErasure(ctx context.Context, id string, at time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE analytics_erasures SET completed_at = $2, actor = NULL WHERE id = $1::uuid`, id, at)
	return err
}

// Job runs the queue on the leader.
type Job struct {
	Conn     driver.Conn
	Database string
	Cluster  string
	Store    Store
	// Interval is how often the queue is read [1m].
	Interval time.Duration
	// Batch bounds one pass, since every request is three mutations [50].
	Batch int
	Log   *slog.Logger
}

// quote returns v as a ClickHouse string literal.
func quote(v string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) + "'"
}

// Statements returns the mutations for one actor of one application.
//
// The hash is a literal, computed before this by the database so it is the same function the views ran. That is on
// purpose: a mutation's text stays in system.mutations and in the query log, and the identifier being erased must not.
func Statements(database, cluster, tenant, app string, hash uint64) []string {
	out := make([]string, 0, len(Tables))
	for _, t := range Tables {
		out = append(out, fmt.Sprintf("ALTER TABLE `%s`.%s ON CLUSTER %s DELETE WHERE tenant_id = %s AND app = %s AND actor_hash = %d",
			database, t, quote(cluster), quote(tenant), quote(app), hash))
	}
	return out
}

// RunOnce carries out what is queued and returns how many requests completed. A request that fails stays queued, and
// mutations are idempotent, so the next pass simply repeats it.
func (j *Job) RunOnce(ctx context.Context) (int, error) {
	batch := j.Batch
	if batch <= 0 {
		batch = 50
	}
	pending, err := j.Store.PendingErasures(ctx, batch)
	if err != nil {
		return 0, err
	}
	done := 0
	for _, e := range pending {
		var hash uint64
		qctx := ch.Context(ctx, ch.WithParameters(ch.Parameters{"t": e.TenantID, "a": e.Actor}))
		if err := j.Conn.QueryRow(qctx, "SELECT sipHash64({t:String}, {a:String})").Scan(&hash); err != nil {
			return done, fmt.Errorf("hash an actor: %w", err)
		}
		for _, st := range Statements(j.Database, j.Cluster, e.TenantID, e.App, hash) {
			if err := j.Conn.Exec(ctx, st); err != nil {
				return done, fmt.Errorf("submit an erasure: %w", err)
			}
		}
		if err := j.Store.CompleteErasure(ctx, e.ID, time.Now()); err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}

// Run processes the queue until ctx is done.
func (j *Job) Run(ctx context.Context) {
	interval := j.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	log := j.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	for {
		rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		n, err := j.RunOnce(rctx)
		cancel()
		if err != nil && ctx.Err() == nil {
			log.Warn("analytics erasure failed; it stays queued", "completed", n, "err", err)
		}
		if n > 0 {
			log.Info("analytics erasures submitted", "completed", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
