package processor

import (
	"sort"
	"time"
)

// TableUsageIngest holds the uncompressed OTLP protobuf bytes and export requests stored per tenant, hour and signal
// (schema 0050_usage, docs/contracts/usage.md, D-079). Rows are aggregated per chunk and inserted with the chunk's
// deduplication token like every other table, so a re-delivered chunk is not counted twice.
const TableUsageIngest = "usage_ingest_1h"

// UsageIngestRow is one (tenant, hour, signal) aggregate of a chunk.
type UsageIngestRow struct {
	TenantID string
	Hour     time.Time
	Signal   string
	Requests uint64
	Bytes    uint64
}

// Values implements row.
func (r *UsageIngestRow) Values() []any {
	return []any{r.TenantID, r.Hour, r.Signal, r.Requests, r.Bytes}
}

type usageIngestKey struct {
	tenant, signal string
	hour           int64
}

// AddIngestUsage counts one decoded export request of bytes (the Kafka record value: protobuf, as produced by ingest)
// in the hour it was received by ingest. With tail sampling the traces record is the sampled one (D-075).
func (r *Rows) AddIngestUsage(tenant, signal string, receivedAt time.Time, bytes int) {
	hour := receivedAt.UTC().Truncate(time.Hour)
	k := usageIngestKey{tenant: tenant, signal: signal, hour: hour.Unix()}
	if r.usageIngest == nil {
		r.usageIngest = map[usageIngestKey]*UsageIngestRow{}
	}
	u := r.usageIngest[k]
	if u == nil {
		u = &UsageIngestRow{TenantID: tenant, Hour: hour, Signal: signal}
		r.usageIngest[k] = u
	}
	u.Requests++
	u.Bytes += uint64(max(bytes, 0))
}

// UsageIngest returns the usage rows sorted by (tenant, hour, signal).
func (r *Rows) UsageIngest() []UsageIngestRow {
	out := make([]UsageIngestRow, 0, len(r.usageIngest))
	for _, u := range r.usageIngest {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.TenantID != b.TenantID {
			return a.TenantID < b.TenantID
		}
		if !a.Hour.Equal(b.Hour) {
			return a.Hour.Before(b.Hour)
		}
		return a.Signal < b.Signal
	})
	return out
}
