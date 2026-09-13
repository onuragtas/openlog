package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/onuragtas/openlog/internal/migrate/phase"
)

// UpsertHeartbeat records that an instance is alive (component_heartbeats). last_seen uses the
// database clock so instances with skewed clocks are compared fairly.
func UpsertHeartbeat(ctx context.Context, q Querier, in phase.Instance) error {
	_, err := q.Exec(ctx, `INSERT INTO component_heartbeats (component, instance_id, version, started_at, last_seen)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (component, instance_id) DO UPDATE SET version = EXCLUDED.version, last_seen = now()`,
		in.Component, in.InstanceID, in.Version, ts(in.StartedAt))
	return err
}

// DeleteHeartbeat removes an instance's row when it stops gracefully.
func DeleteHeartbeat(ctx context.Context, q Querier, component, instanceID string) error {
	_, err := q.Exec(ctx, "DELETE FROM component_heartbeats WHERE component = $1 AND instance_id = $2", component, instanceID)
	return err
}

// LiveInstances returns instances seen within phase.LiveWindow, ordered by component.
func LiveInstances(ctx context.Context, q Querier) ([]phase.Instance, error) {
	rows, err := q.Query(ctx, `SELECT component, instance_id, version, started_at, last_seen FROM component_heartbeats
		WHERE last_seen > now() - make_interval(secs => $1) ORDER BY component, instance_id`, phase.LiveWindow.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []phase.Instance{}
	for rows.Next() {
		var in phase.Instance
		if err := rows.Scan(&in.Component, &in.InstanceID, &in.Version, &in.StartedAt, &in.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// PruneHeartbeats deletes rows not seen for olderThan.
func PruneHeartbeats(ctx context.Context, q Querier, olderThan time.Duration) (int64, error) {
	tag, err := q.Exec(ctx, "DELETE FROM component_heartbeats WHERE last_seen < now() - make_interval(secs => $1)", olderThan.Seconds())
	return tag.RowsAffected(), err
}

// GetSystemState decodes the system_state document key into dst. found is false when absent.
func GetSystemState(ctx context.Context, q Querier, key string, dst any) (updatedAt time.Time, found bool, err error) {
	var raw []byte
	err = q.QueryRow(ctx, "SELECT value, updated_at FROM system_state WHERE key = $1", key).Scan(&raw, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return updatedAt, true, json.Unmarshal(raw, dst)
}

// PutSystemState stores v as the system_state document key.
func PutSystemState(ctx context.Context, q Querier, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO system_state (key, value, updated_at) VALUES ($1, $2::jsonb, now())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, string(b))
	return err
}
