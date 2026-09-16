package slo

import (
	"context"
	"fmt"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/apm"
)

// MaxBuckets bounds one Load (a 30-day window at 1-minute buckets would be 43 200 rows; callers pick the step).
const MaxBuckets = 5000

// Load reads the SLO's service from apm_transactions_1m through the tenant-scoped query layer and returns one
// bucket per step in [from, to) — empty buckets included, so the series has no holes. from and to are
// truncated to whole minutes (the rollup is per minute, apm.md §8); step must be a whole number of minutes.
//
// Everything is re-aggregated with GROUP BY (sharding correctness, apm.md §8): sums and the merged duration
// histogram of a bucket are exact whatever the step is, so the budget over a range does not depend on it.
func Load(ctx context.Context, sc *query.Scope, s *SLO, from, to time.Time, step time.Duration) ([]Bucket, error) {
	from, to = from.Truncate(time.Minute), to.Truncate(time.Minute)
	if step < time.Minute {
		step = time.Minute
	}
	step = step.Truncate(time.Minute)
	n := 0
	if to.After(from) {
		n = int((to.Sub(from) + step - 1) / step)
	}
	if n <= 0 {
		return nil, nil
	}
	if n > MaxBuckets {
		return nil, fmt.Errorf("slo: %d buckets exceed the limit of %d; use a larger step", n, MaxBuckets)
	}
	cols := []string{
		"intDiv(toInt64(toUnixTimestamp(timestamp)) - {b_origin_s:Int64}, {b_step_s:Int64}) AS bk",
		"sum(requests) AS req", "sum(errors) AS errs",
	}
	latency := s.SLIType == SLILatency
	if latency {
		cols = append(cols, "tupleElement(sumMap(duration_hist), 1) AS hk", "tupleElement(sumMap(duration_hist), 2) AS hv")
	}
	q := sc.From(query.ApmTransactions1m).Columns(cols...).
		Param("b_origin_s", from.Unix()).Param("b_step_s", int64(step/time.Second)).GroupBy("bk").
		Where("service_name = {svc:String}").Param("svc", s.ServiceName).
		Where("timestamp >= toDateTime({ts_from:Int64}, 'UTC') AND timestamp < toDateTime({ts_to:Int64}, 'UTC')").
		Param("ts_from", from.Unix()).Param("ts_to", from.Add(time.Duration(n)*step).Unix()).
		Limit(n + 1)
	if s.ServiceNamespace != nil {
		q.Where("service_namespace = {svc_ns:String}").Param("svc_ns", *s.ServiceNamespace)
	}
	if s.Environment != nil {
		q.Where("deployment_environment = {svc_env:String}").Param("svc_env", *s.Environment)
	}
	rows, err := sc.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	buckets := make([]Bucket, n)
	for i := range buckets {
		buckets[i].Start = from.Add(time.Duration(i) * step)
	}
	for rows.Next() {
		var (
			bk        int64
			req, errs float64
			hk        []int16
			hv        []float64
			dest      = []any{&bk, &req, &errs}
		)
		if latency {
			dest = append(dest, &hk, &hv)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		if bk < 0 || bk >= int64(n) {
			continue
		}
		b := &buckets[bk]
		b.Requests += req
		b.Errors += errs
		b.Good += GoodOf(s, req, errs, apm.NewHist(hk, hv))
	}
	return buckets, rows.Err()
}

// BurnStep is the bucket width used for burn-rate windows: burn windows are whole minutes, so 1-minute
// buckets make every window exact (6 h of buckets = 360 rows).
const BurnStep = time.Minute

// LoadBurn reads the buckets needed to evaluate windows ending at end (the longest Long window back).
func LoadBurn(ctx context.Context, sc *query.Scope, s *SLO, end time.Time, windows []BurnWindow) ([]Bucket, error) {
	var longest time.Duration
	for _, w := range windows {
		longest = max(longest, w.Long, w.Short)
	}
	if longest <= 0 {
		return nil, nil
	}
	return Load(ctx, sc, s, end.Add(-longest), end, BurnStep)
}
