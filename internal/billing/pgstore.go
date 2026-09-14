package billing

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore implements PushStore on billing_usage_pushes.
type PGStore struct{ Pool *pgxpool.Pool }

// ClaimPush implements PushStore. A pending claim older than 15 minutes (a crashed pusher) can be taken over.
func (s PGStore) ClaimPush(ctx context.Context, r PushRecord, maxAttempts int) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `INSERT INTO billing_usage_pushes (idempotency_key, org_id, provider, metric, day, quantity, status, attempts)
		VALUES ($1, $2::uuid, $3, $4, $5, $6, 'pending', 1)
		ON CONFLICT (idempotency_key) DO UPDATE SET status = 'pending', attempts = billing_usage_pushes.attempts + 1,
			quantity = EXCLUDED.quantity, updated_at = now()
		WHERE billing_usage_pushes.attempts < $7 AND (billing_usage_pushes.status = 'failed'
			OR (billing_usage_pushes.status = 'pending' AND billing_usage_pushes.updated_at < now() - interval '15 minutes'))`,
		r.IdempotencyKey, r.OrgID, r.Provider, r.Metric, r.Day.UTC().Format(time.DateOnly), r.Quantity, maxAttempts)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// FinishPush implements PushStore.
func (s PGStore) FinishPush(ctx context.Context, key string, pushErr error) error {
	status, msg := "pushed", ""
	if pushErr != nil {
		status, msg = "failed", pushErr.Error()
		if len(msg) > 1000 {
			msg = msg[:1000]
		}
	}
	_, err := s.Pool.Exec(ctx, `UPDATE billing_usage_pushes SET status = $2, error = $3, updated_at = now() WHERE idempotency_key = $1`, key, status, msg)
	return err
}
