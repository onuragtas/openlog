package updatereq

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/store/postgres"
)

// enqueueLock serializes Enqueue across api pods (transaction-level advisory lock, "ol-updrq").
const enqueueLock int64 = 0x6f6c2d7570647271

// PGQueue implements Queue and Poller on migrations/postgres/0009_update_requests.sql.
type PGQueue struct{ Pool *pgxpool.Pool }

// IsUndefinedTable reports a query against update_requests before migration 0009 was applied.
func IsUndefinedTable(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "42P01"
}

const requestCols = `id::text, action, target_version, ignore_maintenance_window, state, message,
	COALESCE(org_id::text, ''), COALESCE(requested_by::text, ''), requested_by_email, requested_at, picked_at, finished_at`

func scanRequest(row pgx.Row) (*Request, error) {
	var r Request
	err := row.Scan(&r.ID, &r.Action, &r.TargetVersion, &r.IgnoreMaintenanceWindow, &r.State, &r.Message,
		&r.OrgID, &r.RequestedBy, &r.RequestedByEmail, &r.RequestedAt, &r.PickedAt, &r.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func nullUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Enqueue implements Queue.
func (q PGQueue) Enqueue(ctx context.Context, r *Request, minGap time.Duration) error {
	switch r.Action {
	case ActionCheck, ActionApply:
	default:
		return fmt.Errorf("unknown update request action %q", r.Action)
	}
	return pgx.BeginFunc(ctx, q.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", enqueueLock); err != nil {
			return err
		}
		var since *float64
		if err := tx.QueryRow(ctx, `SELECT EXTRACT(EPOCH FROM now() - max(requested_at))::float8
			FROM update_requests WHERE action = $1`, r.Action).Scan(&since); err != nil {
			return err
		}
		if since != nil {
			if d := time.Duration(*since * float64(time.Second)); d < minGap {
				return &TooSoonError{RetryAfter: minGap - max(d, 0)}
			}
		}
		if r.Action == ActionApply {
			var busy bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM update_requests WHERE action = 'apply' AND (
				(state = 'pending' AND requested_at > now() - make_interval(secs => $1)) OR
				(state = 'running' AND COALESCE(picked_at, requested_at) > now() - make_interval(secs => $2))))`,
				PendingTTL.Seconds(), RunningTTL.Seconds()).Scan(&busy); err != nil {
				return err
			}
			if busy {
				return ErrBusy
			}
		}
		got, err := scanRequest(tx.QueryRow(ctx, `INSERT INTO update_requests
			(action, target_version, ignore_maintenance_window, org_id, requested_by, requested_by_email)
			VALUES ($1, $2, $3, $4::uuid, $5::uuid, $6) RETURNING `+requestCols,
			r.Action, r.TargetVersion, r.IgnoreMaintenanceWindow, nullUUID(r.OrgID), nullUUID(r.RequestedBy), r.RequestedByEmail))
		if err != nil {
			return err
		}
		*r = *got
		return nil
	})
}

// Latest implements Queue.
func (q PGQueue) Latest(ctx context.Context) (*Request, error) {
	return scanRequest(q.Pool.QueryRow(ctx, `SELECT `+requestCols+` FROM update_requests ORDER BY requested_at DESC LIMIT 1`))
}

// Heartbeat implements Queue.
func (q PGQueue) Heartbeat(ctx context.Context) (*Heartbeat, error) {
	var hb Heartbeat
	_, found, err := postgres.GetSystemState(ctx, q.Pool, HeartbeatKey, &hb)
	if err != nil || !found {
		return nil, err
	}
	return &hb, nil
}

// Claim implements Poller. Two cheap statements on the partial index; no row → no write.
func (q PGQueue) Claim(ctx context.Context) (*Request, error) {
	if _, err := q.Pool.Exec(ctx, `UPDATE update_requests SET state = 'expired', finished_at = now(), message = $1
		WHERE state = 'pending' AND requested_at <= now() - make_interval(secs => $2)`, expiredMessage, PendingTTL.Seconds()); err != nil {
		return nil, err
	}
	return scanRequest(q.Pool.QueryRow(ctx, `UPDATE update_requests SET state = 'running', picked_at = now()
		WHERE id = (SELECT id FROM update_requests WHERE state = 'pending' ORDER BY requested_at LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING `+requestCols))
}

// Finish implements Poller.
func (q PGQueue) Finish(ctx context.Context, id, state, message string) error {
	_, err := q.Pool.Exec(ctx, `UPDATE update_requests SET state = $2, message = $3, finished_at = now() WHERE id = $1::uuid`,
		id, state, clip(message))
	return err
}

// Abandon implements Poller.
func (q PGQueue) Abandon(ctx context.Context, message string) error {
	_, err := q.Pool.Exec(ctx, `UPDATE update_requests SET state = 'failed', message = $1, finished_at = now() WHERE state = 'running'`,
		clip(message))
	return err
}

// PutHeartbeat implements Poller.
func (q PGQueue) PutHeartbeat(ctx context.Context, hb Heartbeat) error {
	return postgres.PutSystemState(ctx, q.Pool, HeartbeatKey, hb)
}
