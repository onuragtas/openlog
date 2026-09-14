package usage

import (
	"context"
	"fmt"
	"time"
)

// SaaS operations queries: host ids for hard host limits and hourly ingest for the abuse detector
// (docs/operations/saas.md, D-105, D-106).

// HostSeen is a host of a tenant and the last day it reported.
type HostSeen struct {
	HostID   string
	LastSeen time.Time
}

// ActiveHostIDs returns, for each of tenants, the hosts that reported on the UTC day of at or the day before (the
// window of the hosts limit, usage.md §4). Tenants without hosts are absent.
func (r *Reader) ActiveHostIDs(ctx context.Context, tenants []string, at time.Time) (map[string][]HostSeen, error) {
	out := map[string][]HostSeen{}
	if len(tenants) == 0 {
		return out, nil
	}
	day := time.Date(at.UTC().Year(), at.UTC().Month(), at.UTC().Day(), 0, 0, 0, 0, time.UTC)
	p := params("", day.AddDate(0, 0, -1), day.AddDate(0, 0, 1))
	p["tenants"] = arrayParam(tenants)
	err := r.query(ctx, p, "SELECT tenant_id, entity, max(day) FROM %s WHERE kind = 'host' AND day >= {d1:Date} AND day <= {d2:Date} "+
		"AND tenant_id IN {tenants:Array(String)} AND entity != '' GROUP BY tenant_id, entity", "usage_entities_1d", func(rows scanner) error {
		var t, h string
		var d time.Time
		if err := rows.Scan(&t, &h, &d); err != nil {
			return err
		}
		out[t] = append(out[t], HostSeen{HostID: h, LastSeen: d})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("active host ids: %w", err)
	}
	return out, nil
}

// IngestSince returns every tenant's ingested bytes in [from, to) (hour granularity).
func (r *Reader) IngestSince(ctx context.Context, from, to time.Time) (map[string]uint64, error) {
	p := params("", from, to)
	out := map[string]uint64{}
	err := r.query(ctx, p, "SELECT tenant_id, sum(bytes) FROM %s WHERE hour >= {from:DateTime('UTC')} AND hour < {to:DateTime('UTC')} GROUP BY tenant_id",
		"usage_ingest_1h", func(rows scanner) error {
			var t string
			var b uint64
			if err := rows.Scan(&t, &b); err != nil {
				return err
			}
			out[t] = b
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("ingest since: %w", err)
	}
	return out, nil
}

// arrayParam renders a ClickHouse Array(String) query parameter literal.
func arrayParam(vs []string) string {
	b := []byte{'['}
	for i, v := range vs {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '\'')
		for _, c := range []byte(v) {
			if c == '\'' || c == '\\' {
				b = append(b, '\\')
			}
			b = append(b, c)
		}
		b = append(b, '\'')
	}
	return string(append(b, ']'))
}
