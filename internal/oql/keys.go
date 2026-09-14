package oql

import (
	"context"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

// Sampling limits of SampleKeys.
const (
	sampleKeys        = 200
	sampleMetricNames = 500
	sampleWindow      = time.Hour
)

// Samples are frequent map keys and metric names of one event type (editors' autocomplete).
type Samples struct {
	AttributeKeys []string
	ResourceKeys  []string
	MetricNames   []string
}

// SampleKeys reads the most frequent attribute/resource keys (and Metric names) of the last hour.
func SampleKeys(ctx context.Context, sc *query.Scope, eventType string, now time.Time) (*Samples, error) {
	et := lookupEventType(eventType)
	out := &Samples{AttributeKeys: []string{}, ResourceKeys: []string{}, MetricNames: []string{}}
	if et == nil {
		return out, nil
	}
	from, to := now.Add(-sampleWindow), now
	if et.name == "Host" || et.name == "Container" {
		from = now.Add(-24 * time.Hour) // entity tables: last_seen advances slowly
	}
	p := &Plan{Event: et, Query: &Query{}}
	read := func(expr string, limit int, dst *[]string) error {
		b := newBuilder(p, 0)
		q := b.source(sc)
		for _, w := range b.filters(from, to) {
			q.Where(w)
		}
		q.Columns(expr+" AS k").GroupBy("k").OrderBy("count() DESC", "k").Limit(limit)
		b.apply(q)
		rows, err := sc.Query(ctx, q)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				return err
			}
			if k != "" {
				*dst = append(*dst, k)
			}
		}
		return rows.Err()
	}
	if et.attrMap != "" {
		if err := read("arrayJoin(mapKeys("+et.attrMap+"))", sampleKeys, &out.AttributeKeys); err != nil {
			return nil, err
		}
	}
	if et.resource != "" {
		if err := read("arrayJoin(mapKeys("+et.resource+"))", sampleKeys, &out.ResourceKeys); err != nil {
			return nil, err
		}
	}
	if et.name == "Metric" {
		if err := read("metric_name", sampleMetricNames, &out.MetricNames); err != nil {
			return nil, err
		}
	}
	return out, nil
}
