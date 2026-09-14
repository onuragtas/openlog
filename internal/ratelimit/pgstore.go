package ratelimit

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore keeps the counters in rate_limit_counters (migrations/postgres/0061_rate_limit_counters.sql).
type PGStore struct {
	Pool *pgxpool.Pool
}

var _ Store = PGStore{}

// Add upserts all deltas in one statement (keys in sorted order, so concurrent batches do not deadlock) and reads the
// counts of the current and previous bucket.
func (s PGStore) Add(ctx context.Context, bucket time.Time, window time.Duration, deltas map[Key]int) (map[Key]Counts, error) {
	keys := make([]Key, 0, len(deltas))
	for k := range deltas {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return string(keys[i][:]) < string(keys[j][:]) })
	kb := make([][]byte, len(keys))
	ns := make([]int32, len(keys))
	for i, k := range keys {
		kb[i] = append([]byte(nil), k[:]...)
		ns[i] = int32(min(max(deltas[k], 0), 1<<30))
	}
	rows, err := s.Pool.Query(ctx, `
		WITH d AS (SELECT * FROM unnest($1::bytea[], $2::int[]) AS d (key, n)),
		up AS (
			INSERT INTO rate_limit_counters AS c (key, bucket, n)
			SELECT key, $3, n FROM d WHERE n > 0 ORDER BY key
			ON CONFLICT (key, bucket) DO UPDATE SET n = c.n + excluded.n
			RETURNING key, n)
		SELECT d.key, coalesce(up.n, cur.n, 0), coalesce(prev.n, 0)
		FROM d
		LEFT JOIN up ON up.key = d.key
		LEFT JOIN rate_limit_counters cur ON cur.key = d.key AND cur.bucket = $3
		LEFT JOIN rate_limit_counters prev ON prev.key = d.key AND prev.bucket = $4`,
		kb, ns, bucket, bucket.Add(-window))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[Key]Counts, len(keys))
	for rows.Next() {
		var (
			kbytes    []byte
			cur, prev int
		)
		if err := rows.Scan(&kbytes, &cur, &prev); err != nil {
			return nil, err
		}
		var k Key
		copy(k[:], kbytes)
		out[k] = Counts{Current: cur, Previous: prev}
	}
	return out, rows.Err()
}

// Prune deletes buckets that start before before.
func (s PGStore) Prune(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM rate_limit_counters WHERE bucket < $1`, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
