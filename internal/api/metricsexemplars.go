package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	qb "github.com/onuragtas/openlog/internal/querybuilder"
)

// Metric exemplars of the Metrics Explorer (docs/contracts/api.md "Metrics": POST /api/v1/metrics/exemplars, D-130):
// the traces behind a metric's data points, so a spike in a chart can be opened as an actual trace.
//
// The processor stores every exemplar with the identifying columns of its data point (schema 0092_metric_exemplars),
// so this endpoint applies exactly the filter conditions of POST /api/v1/metrics/query — the dots always belong to
// the series the chart drew.
//
// Exemplars are returned spread over the range rather than as the first n: the range is divided into `limit` buckets
// and the largest-valued exemplar of each bucket is returned. A chart of a slow hour is otherwise answered with the
// first 50 exemplars of its first minute, which is useless for finding the spike; the largest value per bucket is the
// one a reader is looking for (the slowest request, the biggest payload).

const (
	defaultExemplarLimit = 50
	maxExemplarLimit     = 500
	// minExemplarBucket is the shortest bucket the spread uses, matching the explorer's minimum step.
	minExemplarBucket = 10 * time.Second
)

type metricExemplarsRequest struct {
	Metric  string          `json:"metric"`
	From    json.RawMessage `json:"from"`
	To      json.RawMessage `json:"to"`
	Filters []qb.Filter     `json:"filters"`
	Groups  [][]qb.Filter   `json:"groups"`
	Limit   int             `json:"limit"`
}

type metricExemplarJSON struct {
	Timestamp string  `json:"timestamp"`
	Value     float64 `json:"value"`
	TraceID   string  `json:"trace_id"`
	// SpanID is "" when the SDK recorded the exemplar outside a span.
	SpanID      string            `json:"span_id"`
	ServiceName string            `json:"service_name"`
	Attributes  map[string]string `json:"attributes"`
	// FilteredAttributes are the measurement attributes the SDK dropped from the metric's own attributes.
	FilteredAttributes map[string]string `json:"filtered_attributes"`
}

// exemplarBucket is the width the range is divided into so limit exemplars spread over it.
func exemplarBucket(from, to time.Time, limit int) time.Duration {
	b := to.Sub(from) / time.Duration(limit)
	if b < minExemplarBucket {
		return minExemplarBucket
	}
	return b.Round(time.Second)
}

func (s *Server) metricExemplars(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	var req metricExemplarsRequest
	if err := decodeQueryBody(r, &req); err != nil {
		return err
	}
	if err := validMetricName(req.Metric); err != nil {
		return err
	}
	from, to, err := s.bodyRange(req.From, req.To)
	if err != nil {
		return err
	}
	limit := defaultExemplarLimit
	if req.Limit < 0 || req.Limit > maxExemplarLimit {
		return badRequest("limit must be between 1 and %d", maxExemplarLimit)
	} else if req.Limit > 0 {
		limit = req.Limit
	}
	// Conditions are validated before the statement runs, as in POST /api/v1/metrics/query.
	b := qb.NewBuilder(qb.Metrics, "mx")
	cond, err := b.Where(req.Filters, req.Groups, "")
	if err != nil {
		return queryBuilderError(err)
	}

	bucket := uint32(exemplarBucket(from, to, limit) / time.Second)
	q := sc.From(query.MetricExemplars).Columns(
		"toStartOfInterval(timestamp, toIntervalSecond({bucket:UInt32})) AS b",
		"timestamp", "value", "trace_id", "span_id", "toString(service_name) AS svc",
		"CAST(attributes, 'Map(String, String)') AS attrs",
		"CAST(filtered_attributes, 'Map(String, String)') AS fattrs",
		// The window runs before LIMIT BY, so this counts every matching exemplar, not only the returned ones.
		"count() OVER () AS total").
		Where("metric_name = {name:String}").Param("name", req.Metric).
		Param("bucket", bucket)
	timeWhere(q, from, to)
	if cond != "" {
		q.Where(cond)
	}
	b.Bind(q)
	// One exemplar per bucket, the largest-valued one; the buckets themselves stay in time order.
	q.OrderBy("b", "value DESC", "trace_id").LimitBy(1, "b").Limit(limit)

	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []metricExemplarJSON{}
	var total uint64
	for rows.Next() {
		var e metricExemplarJSON
		var ts, bucketTS time.Time
		if err := rows.Scan(&bucketTS, &ts, &e.Value, &e.TraceID, &e.SpanID, &e.ServiceName, &e.Attributes, &e.FilteredAttributes, &total); err != nil {
			return err
		}
		if !finite(e.Value) {
			continue
		}
		e.Timestamp = formatTime(ts)
		e.Attributes, e.FilteredAttributes = nonNilMap(e.Attributes), nonNilMap(e.FilteredAttributes)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"exemplars": out, "total": total, "truncated": total > uint64(len(out)),
	})
	return nil
}
