package vuln

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// The findings writer: direct shard inserts into host_vulnerabilities, like every other per-host table
// (D-018). A match run writes its whole result at once — the findings it confirmed and the ones it
// resolved — because the two together are what the run concluded.

// BlockInserter inserts rows into <table>_local on the shard of keyColumns (clickhouse.ShardedWriter).
type BlockInserter interface {
	Insert(ctx context.Context, table string, keyColumns, columns []string, token string, rows [][]any) error
}

// findingColumns is the column list of host_vulnerabilities (schema 0100_host_vulns).
var findingColumns = []string{"tenant_id", "host_id", "host_name", "vuln_id", "cve", "severity", "score",
	"ecosystem", "package", "version", "fixed_in", "summary", "first_seen", "last_seen", "resolved_at"}

// InsertWriter is the FindingWriter backed by ClickHouse.
type InsertWriter struct {
	ins BlockInserter
	// MaxBatch bounds one insert (default 10000 rows).
	MaxBatch int
}

var _ FindingWriter = (*InsertWriter)(nil)

// NewInsertWriter wraps an inserter.
func NewInsertWriter(ins BlockInserter) *InsertWriter {
	return &InsertWriter{ins: ins, MaxBatch: 10000}
}

// WriteFindings implements FindingWriter.
func (w *InsertWriter) WriteFindings(ctx context.Context, rows []Finding) error {
	if len(rows) == 0 {
		return nil
	}
	batch := w.MaxBatch
	if batch <= 0 {
		batch = 10000
	}
	token := "vuln:" + uuid.NewString()
	for i := 0; i < len(rows); i += batch {
		end := min(i+batch, len(rows))
		if err := w.ins.Insert(ctx, "host_vulnerabilities", []string{"tenant_id", "host_id"}, findingColumns,
			tokenFor(token, i), findingRows(rows[i:end])); err != nil {
			return err
		}
	}
	return nil
}

// tokenFor gives every batch of a run its own deduplication token, so a retried batch is dropped by
// ReplicatedMergeTree while the others still go in.
func tokenFor(token string, offset int) string {
	return token + ":" + itoa(offset)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func findingRows(rows []Finding) [][]any {
	out := make([][]any, len(rows))
	for i, f := range rows {
		resolved := f.ResolvedAt
		if resolved.IsZero() {
			resolved = time.Unix(0, 0)
		}
		out[i] = []any{f.TenantID, f.HostID, f.HostName, f.VulnID, f.CVE, f.Severity, float32(f.Score),
			f.Ecosystem, f.Package, f.Version, f.FixedIn, f.Summary, f.FirstSeen.UTC(), f.LastSeen.UTC(),
			resolved.UTC()}
	}
	return out
}
