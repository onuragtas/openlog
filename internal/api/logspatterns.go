package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	qb "github.com/onuragtas/openlog/internal/querybuilder"
)

// Log patterns of the Logs Explorer (docs/contracts/api.md "Logs": POST /api/v1/logs/patterns, D-128). The processor
// derives a template and its id for every record (internal/logpattern) and stores both on the row, so grouping is an
// ordinary GROUP BY on the logs table and every filter of the explorer applies unchanged.
//
// An unfiltered request over a long range reads the hourly rollup (schema 0091_log_patterns) instead, as
// GET /api/v1/metrics does for metric names: the same numbers, but hour-aligned and far cheaper. Every filtered
// request — and every short range, which is what the explorer asks for by default — reads the raw table, so adding a
// filter can never change what the counts mean.

const (
	defaultPatternLimit = 50
	maxPatternLimit     = 500
	// patternRollupMinRange is the shortest range an unfiltered request serves from the hourly rollup.
	patternRollupMinRange = 6 * time.Hour
	// patternSampleBytes bounds the sample body returned per pattern.
	patternSampleBytes = 4096
	// patternServices is how many service names are listed per pattern.
	patternServices = 5
)

type logsPatternsRequest struct {
	logFilter
	Limit int `json:"limit"`
}

// severityMixJSON counts a pattern's records by OpenTelemetry severity range.
type severityMixJSON struct {
	Unspecified uint64 `json:"unspecified"`
	Trace       uint64 `json:"trace"`
	Debug       uint64 `json:"debug"`
	Info        uint64 `json:"info"`
	Warn        uint64 `json:"warn"`
	Error       uint64 `json:"error"`
	Fatal       uint64 `json:"fatal"`
}

// patternSampleJSON is one record of a pattern: the newest one in the range.
type patternSampleJSON struct {
	Timestamp      string `json:"timestamp"`
	Body           string `json:"body"`
	ServiceName    string `json:"service_name"`
	SeverityText   string `json:"severity_text"`
	SeverityNumber uint8  `json:"severity_number"`
	TraceID        string `json:"trace_id"`
}

type logPatternJSON struct {
	// PatternID is a UInt64 as text: JSON numbers lose precision above 2^53. It is the value the pattern_id filter
	// key of POST /api/v1/logs/query expects.
	PatternID         string            `json:"pattern_id"`
	Template          string            `json:"template"`
	Count             uint64            `json:"count"`
	Severity          severityMixJSON   `json:"severity"`
	MaxSeverityNumber uint8             `json:"max_severity_number"`
	Services          []string          `json:"services"`
	FirstSeen         string            `json:"first_seen"`
	LastSeen          string            `json:"last_seen"`
	Sample            patternSampleJSON `json:"sample"`
}

// patternRow is one scanned group before it becomes JSON.
type patternRow struct {
	id                                           string
	template                                     string
	n                                            uint64
	unspec, trace, debug, info, warn, err, fatal uint64
	maxSeverity                                  uint8
	services                                     []string
	first, last                                  time.Time
	sample                                       patternSampleJSON
	total                                        uint64
	unclassified                                 uint64
}

func (p *patternRow) json() logPatternJSON {
	return logPatternJSON{
		PatternID: p.id, Template: p.template, Count: p.n,
		Severity: severityMixJSON{Unspecified: p.unspec, Trace: p.trace, Debug: p.debug, Info: p.info,
			Warn: p.warn, Error: p.err, Fatal: p.fatal},
		MaxSeverityNumber: p.maxSeverity, Services: nonNilStrings(p.services),
		FirstSeen: formatTime(p.first), LastSeen: formatTime(p.last), Sample: p.sample,
	}
}

// patternLimit validates the pattern count of a request.
func patternLimit(n int) (int, error) {
	if n < 0 || n > maxPatternLimit {
		return 0, badRequest("limit must be between 1 and %d", maxPatternLimit)
	} else if n > 0 {
		return n, nil
	}
	return defaultPatternLimit, nil
}

// unfiltered reports whether a request has no condition beyond its time range, so the rollup answers it exactly.
func (f *logFilter) unfiltered() bool {
	for _, g := range f.Groups {
		if len(g) > 0 {
			return false
		}
	}
	return len(f.Filters) == 0 && f.Q == "" && f.Transaction == "" && f.TransactionService == ""
}

// severityCounts are the severity mix expressions on the raw table.
var severityCounts = []string{
	"countIf(severity_number = 0) AS s_unspec",
	"countIf(severity_number BETWEEN 1 AND 4) AS s_trace",
	"countIf(severity_number BETWEEN 5 AND 8) AS s_debug",
	"countIf(severity_number BETWEEN 9 AND 12) AS s_info",
	"countIf(severity_number BETWEEN 13 AND 16) AS s_warn",
	"countIf(severity_number BETWEEN 17 AND 20) AS s_error",
	"countIf(severity_number >= 21) AS s_fatal",
}

func (s *Server) logPatterns(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	var req logsPatternsRequest
	if err := decodeQueryBody(r, &req); err != nil {
		return err
	}
	from, to, err := s.bodyRange(req.From, req.To)
	if err != nil {
		return err
	}
	limit, err := patternLimit(req.Limit)
	if err != nil {
		return err
	}
	rollup := req.unfiltered() && to.Sub(from) >= patternRollupMinRange
	var rows []patternRow
	if rollup {
		rows, err = s.rollupPatterns(r, sc, from, to, limit)
	} else {
		rows, err = s.rawPatterns(r, sc, &req, from, to, limit)
	}
	if err != nil {
		return err
	}
	// Records without a pattern (stored before 0091_log_patterns, or with an empty body) are counted, not listed:
	// they have no template to show. Their count comes from the query, so it is exact even when the group itself
	// falls outside the returned patterns.
	var total, unclassified uint64
	out := make([]logPatternJSON, 0, limit)
	listed := 0
	for i := range rows {
		total, unclassified = rows[i].total, rows[i].unclassified
		if rows[i].id == "0" {
			continue
		}
		listed++
		if len(out) < limit {
			out = append(out, rows[i].json())
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"patterns": out, "total": total, "unclassified": unclassified, "rollup": rollup, "truncated": listed > limit,
	})
	return nil
}

// rawPatterns groups the matching records of the logs table by pattern.
func (s *Server) rawPatterns(r *http.Request, sc *query.Scope, req *logsPatternsRequest, from, to time.Time, limit int) ([]patternRow, error) {
	b := qb.NewBuilder(qb.Logs, "lp")
	cols := []string{"toString(pattern_id) AS pid", "any(pattern_template) AS tmpl", "count() AS n"}
	cols = append(cols, severityCounts...)
	cols = append(cols,
		"max(severity_number) AS s_max",
		"groupUniqArray("+strconv.Itoa(patternServices)+")(toString(service_name)) AS svcs",
		"min(timestamp) AS first_seen",
		"max(timestamp) AS last_seen",
		"substring(argMax(body, timestamp), 1, "+strconv.Itoa(patternSampleBytes)+") AS sample_body",
		"toString(argMax(service_name, timestamp)) AS sample_service",
		"toString(argMax(severity_text, timestamp)) AS sample_severity_text",
		"argMax(severity_number, timestamp) AS sample_severity_number",
		"argMax(trace_id, timestamp) AS sample_trace_id",
		// Window functions run before LIMIT, so these cover every pattern, not only the returned ones.
		"sum(count()) OVER () AS total",
		"sum(countIf(pattern_id = 0)) OVER () AS unclassified")
	q := sc.From(query.Logs).Columns(cols...)
	if err := req.apply(sc, q, b, from, to); err != nil {
		return nil, err
	}
	// One row over the limit detects truncation; one more leaves room for the unclassified group.
	q.GroupBy("pattern_id").OrderBy("n DESC", "pid").Limit(limit + 2)

	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []patternRow
	for rows.Next() {
		var p patternRow
		if err := rows.Scan(&p.id, &p.template, &p.n, &p.unspec, &p.trace, &p.debug, &p.info, &p.warn, &p.err, &p.fatal,
			&p.maxSeverity, &p.services, &p.first, &p.last, &p.sample.Body, &p.sample.ServiceName,
			&p.sample.SeverityText, &p.sample.SeverityNumber, &p.sample.TraceID, &p.total, &p.unclassified); err != nil {
			return nil, err
		}
		p.sample.Timestamp = formatTime(p.last)
		out = append(out, p)
	}
	return out, rows.Err()
}

// rollupPatterns reads the hourly rollup for a request without conditions. Counts cover the whole hours of the range,
// and the rollup stores no record without a pattern, so unclassified stays 0 on this path.
func (s *Server) rollupPatterns(r *http.Request, sc *query.Scope, from, to time.Time, limit int) ([]patternRow, error) {
	q := sc.From(query.LogPatterns1h).Columns(
		"toString(pattern_id) AS pid", "any(template) AS tmpl", "sum(records) AS n",
		"sum(unspecified) AS s_unspec", "sum(traces) AS s_trace", "sum(debugs) AS s_debug", "sum(infos) AS s_info",
		"sum(warns) AS s_warn", "sum(errors) AS s_error", "sum(fatals) AS s_fatal",
		"max(max_severity) AS s_max",
		"groupUniqArray("+strconv.Itoa(patternServices)+")(toString(service_name)) AS svcs",
		"min(first_seen) AS first_seen", "max(last_seen) AS last_seen",
		"substring(argMaxMerge(sample_body), 1, "+strconv.Itoa(patternSampleBytes)+") AS sample_body",
		"sum(sum(records)) OVER () AS total").
		Where("hour >= fromUnixTimestamp({h_from:Int64}) AND hour <= fromUnixTimestamp({h_to:Int64})").
		Param("h_from", from.Truncate(time.Hour).Unix()).Param("h_to", to.Unix()).
		GroupBy("pattern_id").OrderBy("n DESC", "pid").Limit(limit + 2)

	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []patternRow
	for rows.Next() {
		var p patternRow
		if err := rows.Scan(&p.id, &p.template, &p.n, &p.unspec, &p.trace, &p.debug, &p.info, &p.warn, &p.err, &p.fatal,
			&p.maxSeverity, &p.services, &p.first, &p.last, &p.sample.Body, &p.total); err != nil {
			return nil, err
		}
		// The rollup keeps the newest body of a pattern, not the whole record: the sample carries what it has.
		p.sample.Timestamp = formatTime(p.last)
		if len(p.services) == 1 {
			p.sample.ServiceName = p.services[0]
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
