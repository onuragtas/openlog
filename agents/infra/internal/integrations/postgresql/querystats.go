package postgresql

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
)

// pg_stat_statements guards (integrations.postgresql.query_stats).
const (
	DefaultQueryStatsTopN = 20
	MaxQueryStatsTopN     = 100
	// MaxQueryTextBytes bounds db.query.text (after whitespace collapsing and literal redaction).
	MaxQueryTextBytes = 1024
	// fetchQueryTextBytes bounds the text transferred per statement.
	fetchQueryTextBytes = 4096
)

// QueryStatsExtensionQuery returns the installed pg_stat_statements version of the connection database.
const QueryStatsExtensionQuery = `SELECT extversion FROM pg_extension WHERE extname = 'pg_stat_statements'`

const insufficientPrivilege = "<insufficient privilege>"

// QueryStatsQuery returns the top-N statements by total execution time.
// Extension versions before 1.8 (PostgreSQL < 13) name the column total_time.
// dbs limits the rows to the collected databases (empty: all).
func QueryStatsQuery(extVersion string, topN, minCalls int, dbs []string) string {
	total := "total_exec_time"
	if !extAtLeast(extVersion, 1, 8) {
		total = "total_time"
	}
	where := "s.calls >= " + strconv.Itoa(max(1, minCalls))
	if len(dbs) > 0 {
		quoted := make([]string, len(dbs))
		for i, d := range dbs {
			quoted[i] = "'" + strings.ReplaceAll(d, "'", "''") + "'"
		}
		where += " AND d.datname IN (" + strings.Join(quoted, ", ") + ")"
	}
	return `SELECT s.queryid, d.datname, r.rolname, s.calls, s.rows, s.` + total + `, s.shared_blks_hit, s.shared_blks_read, left(s.query, ` + strconv.Itoa(fetchQueryTextBytes) + `)
FROM pg_stat_statements s LEFT JOIN pg_database d ON d.oid = s.dbid LEFT JOIN pg_roles r ON r.oid = s.userid
WHERE ` + where + ` ORDER BY s.` + total + ` DESC, s.queryid LIMIT ` + strconv.Itoa(topN)
}

func extAtLeast(v string, major, minor int) bool {
	a, b, _ := strings.Cut(v, ".")
	ma, err1 := strconv.Atoi(a)
	mi, err2 := strconv.Atoi(b)
	if err1 != nil || err2 != nil {
		return true // unknown: assume a current version
	}
	return ma > major || (ma == major && mi >= minor)
}

// collectQueryStats emits the pg_stat_statements metrics; a returned error
// makes the collection partial.
func (c *collector) collectQueryStats(ctx context.Context, b *integrations.Batch, main Conn, dbs []string) error {
	qs := c.inst.Settings.QueryStats
	topN := qs.TopN
	if topN <= 0 {
		topN = DefaultQueryStatsTopN
	}
	topN = min(topN, MaxQueryStatsTopN)
	rows, err := main.Query(ctx, QueryStatsExtensionQuery)
	if err != nil {
		return c.classify(err, "")
	}
	if len(rows) == 0 {
		return fmt.Errorf("extension not installed in database %q: run CREATE EXTENSION pg_stat_statements there "+
			"(the server needs shared_preload_libraries = 'pg_stat_statements' and a restart)", c.mainDB())
	}
	rows, err = main.Query(ctx, QueryStatsQuery(str(rows[0][0]), topN, qs.MinCalls, dbs))
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "55000" { // object_not_in_prerequisite_state
			return fmt.Errorf("%s: add pg_stat_statements to shared_preload_libraries and restart PostgreSQL", pe.Message)
		}
		if errors.As(err, &pe) && pe.Code == "42501" {
			return fmt.Errorf("permission denied: %s (grant pg_monitor or pg_read_all_stats)", pe.Message)
		}
		return c.classify(err, "")
	}
	if hidden := RecordQueryStats(b, rows); hidden > 0 {
		return fmt.Errorf("%d statements of other roles are hidden (%s): grant pg_read_all_stats or pg_monitor", hidden, insufficientPrivilege)
	}
	return nil
}

// RecordQueryStats emits one resource per statement (queryid, database, role)
// and returns the number of rows skipped for insufficient privileges.
func RecordQueryStats(b *integrations.Batch, rows [][]any) (hidden int) {
	seen := map[string]bool{}
	for _, r := range rows {
		if len(r) < 9 {
			continue
		}
		text := str(r[8])
		if r[0] == nil || text == insufficientPrivilege {
			hidden++
			continue
		}
		id, db, role := str(r[0]), str(r[1]), str(r[2])
		// With pg_stat_statements.track = all a statement can appear as top-level
		// and nested; the first (larger total time) row wins.
		key := id + "\x00" + db + "\x00" + role
		if seen[key] {
			continue
		}
		seen[key] = true
		s := b.Resource(otlputil.Str("postgresql.database.name", db), otlputil.Str("postgresql.queryid", id),
			otlputil.Str("postgresql.rolname", role), otlputil.Str("db.query.text", NormalizeQueryText(text)))
		s.SumInt("postgresql.query.calls", "{call}", true, i64(r[3]))
		s.SumInt("postgresql.query.rows", "{row}", true, i64(r[4]))
		s.SumDouble("postgresql.query.total_exec_time", "ms", true, f64(r[5]))
		s.SumInt("postgresql.query.shared_blocks", "{block}", true, i64(r[6]), otlputil.Str("source", "hit"))
		s.SumInt("postgresql.query.shared_blocks", "{block}", true, i64(r[7]), otlputil.Str("source", "read"))
	}
	return hidden
}

// NormalizeQueryText collapses whitespace, replaces string literals ('…',
// E'…', $$…$$) with '?' — pg_stat_statements does not normalize utility
// statements such as CREATE ROLE … PASSWORD '…' — and truncates the text to
// MaxQueryTextBytes on a UTF-8 boundary.
func NormalizeQueryText(q string) string {
	var sb strings.Builder
	space := false
	for i := 0; i < len(q); {
		ch := q[i]
		switch {
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '\f' || ch == '\v':
			space = true
			i++
			continue
		case ch == '\'':
			j := i + 1
			for j < len(q) {
				if q[j] == '\'' {
					if j+1 < len(q) && q[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			writeToken(&sb, &space, "'?'")
			i = j + 1
			continue
		case ch == '$' && i+1 < len(q) && q[i+1] == '$':
			end := strings.Index(q[i+2:], "$$")
			writeToken(&sb, &space, "$$?$$")
			if end < 0 {
				i = len(q)
			} else {
				i += 2 + end + 2
			}
			continue
		}
		writeToken(&sb, &space, string(ch))
		i++
	}
	out := sb.String()
	if len(out) > MaxQueryTextBytes {
		cut := MaxQueryTextBytes
		for cut > 0 && !utf8.RuneStart(out[cut]) {
			cut--
		}
		out = out[:cut]
	}
	return out
}

func writeToken(sb *strings.Builder, space *bool, s string) {
	if *space && sb.Len() > 0 {
		sb.WriteByte(' ')
	}
	*space = false
	sb.WriteString(s)
}
