package api

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

// Metric correlation (docs/contracts/api.md "Metric correlation", D-146): given a window in which something
// went wrong, which other series behaved differently in it than they did before.
//
// This is the question every incident starts with and no chart answers: a dashboard shows what its author
// expected to matter, and the metric that explains this outage is the one nobody put on it. The comparison
// is deliberately simple and explainable — a mean over the window against a mean over the baseline, scored
// by how many of the baseline's own standard deviations the difference is — because a correlation a person
// cannot check is a correlation they cannot act on.

// Correlation limits.
const (
	// correlateMaxSeries bounds the series scored in one request.
	correlateMaxSeries = 20000
	// correlateDefaultLimit is how many correlated series are returned by default.
	correlateDefaultLimit = 20
	// correlateMaxLimit bounds it.
	correlateMaxLimit = 200
	// correlateMinPoints is the fewest one-minute buckets a series needs on each side to be scored: two
	// points can differ for any reason at all.
	correlateMinPoints = 3
	// correlateMaxRange bounds the highlight window; beyond it the comparison stops meaning "during the
	// incident".
	correlateMaxRange = 6 * time.Hour
	// correlateBaselineFactor is how much longer the default baseline is than the window it explains.
	correlateBaselineFactor = 4
)

type correlationJSON struct {
	MetricName string            `json:"metric_name"`
	SeriesID   string            `json:"series_id"`
	HostID     string            `json:"host_id"`
	Attributes map[string]string `json:"attributes"`
	Unit       string            `json:"unit"`
	// BaselineMean and WindowMean are the two numbers the score comes from, so a person can check it.
	BaselineMean float64 `json:"baseline_mean"`
	WindowMean   float64 `json:"window_mean"`
	// ChangeRatio is the relative change ((window − baseline) / |baseline|); null when the baseline is 0,
	// where a ratio would be infinite rather than large.
	ChangeRatio *float64 `json:"change_ratio"`
	// Score is how many of the baseline's own standard deviations the difference is.
	Score float64 `json:"score"`
	// Direction is "up" or "down": which way it moved, which is half of what makes a finding readable.
	Direction string `json:"direction"`
	// Points is how many one-minute buckets the window had, so a thin series can be discounted by eye.
	Points uint64 `json:"points"`
}

// correlate answers "what else changed in this window".
func (s *Server) correlate(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	q := r.URL.Query()
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	if !to.After(from) {
		return &apiError{http.StatusBadRequest, "invalid_argument", "to must be after from"}
	}
	if to.Sub(from) > correlateMaxRange {
		return &apiError{http.StatusBadRequest, "invalid_argument",
			fmt.Sprintf("the window is longer than %s; correlate the part of it that matters", correlateMaxRange)}
	}
	// The baseline defaults to the four hours before the window: long enough to know what "normal" was,
	// recent enough that it is the same system.
	baselineTo := from
	baselineFrom := from.Add(-correlateBaselineFactor * to.Sub(from))
	if v := q.Get("baseline_from"); v != "" {
		if baselineFrom, err = parseTime(v); err != nil {
			return &apiError{http.StatusBadRequest, "invalid_argument", "baseline_from: " + err.Error()}
		}
	}
	if v := q.Get("baseline_to"); v != "" {
		if baselineTo, err = parseTime(v); err != nil {
			return &apiError{http.StatusBadRequest, "invalid_argument", "baseline_to: " + err.Error()}
		}
	}
	if !baselineTo.After(baselineFrom) {
		return &apiError{http.StatusBadRequest, "invalid_argument", "baseline_to must be after baseline_from"}
	}
	if baselineTo.After(to) {
		return &apiError{http.StatusBadRequest, "invalid_argument", "the baseline must not overlap the end of the window"}
	}
	limit := correlateDefaultLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > correlateMaxLimit {
			return &apiError{http.StatusBadRequest, "invalid_argument",
				fmt.Sprintf("limit must be between 1 and %d", correlateMaxLimit)}
		}
		limit = n
	}

	// One query over the rollup: both windows in the same pass, so the two means come from the same scan.
	sel := sc.From(query.Metrics1m).Columns(
		"metric_name",
		"toString(series_id) AS c_series",
		"any(host_id) AS c_host",
		"any(attributes) AS c_attrs",
		"any(unit) AS c_unit",
		"avgIf(value_sum / value_count, timestamp >= fromUnixTimestamp64Milli({c_wfrom:Int64}) AND timestamp < fromUnixTimestamp64Milli({c_wto:Int64})) AS c_window",
		"countIf(timestamp >= fromUnixTimestamp64Milli({c_wfrom:Int64}) AND timestamp < fromUnixTimestamp64Milli({c_wto:Int64})) AS c_wcount",
		"avgIf(value_sum / value_count, timestamp >= fromUnixTimestamp64Milli({c_bfrom:Int64}) AND timestamp < fromUnixTimestamp64Milli({c_bto:Int64})) AS c_base",
		"countIf(timestamp >= fromUnixTimestamp64Milli({c_bfrom:Int64}) AND timestamp < fromUnixTimestamp64Milli({c_bto:Int64})) AS c_bcount",
		"stddevPopIf(value_sum / value_count, timestamp >= fromUnixTimestamp64Milli({c_bfrom:Int64}) AND timestamp < fromUnixTimestamp64Milli({c_bto:Int64})) AS c_bstddev",
	).
		Where("value_count > 0").
		Where("timestamp >= fromUnixTimestamp64Milli({c_bfrom:Int64}) AND timestamp < fromUnixTimestamp64Milli({c_wto:Int64})").
		Param("c_wfrom", from.UnixMilli()).Param("c_wto", to.UnixMilli()).
		Param("c_bfrom", baselineFrom.UnixMilli()).Param("c_bto", baselineTo.UnixMilli()).
		GroupBy("metric_name", "series_id").
		// Both sides must have enough buckets to compare at all; a series that appeared during the window
		// has no baseline and is reported by the "new series" half of the answer instead.
		Having(fmt.Sprintf("c_wcount >= %d AND c_bcount >= %d", correlateMinPoints, correlateMinPoints)).
		Limit(correlateMaxSeries)
	if v := q.Get("host_id"); v != "" {
		sel.Where("host_id = {c_host_id:String}").Param("c_host_id", v)
	}
	if v := q.Get("metric"); v != "" {
		sel.Where("metric_name = {c_metric:String}").Param("c_metric", v)
	}

	rows, err := sc.Query(r.Context(), sel)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []correlationJSON{}
	scanned := 0
	for rows.Next() {
		var (
			c                            correlationJSON
			windowMean, baseMean, stddev float64
			wcount, bcount               uint64
		)
		if err := rows.Scan(&c.MetricName, &c.SeriesID, &c.HostID, &c.Attributes, &c.Unit,
			&windowMean, &wcount, &baseMean, &bcount, &stddev); err != nil {
			return err
		}
		scanned++
		c.BaselineMean, c.WindowMean, c.Points = baseMean, windowMean, wcount
		c.Attributes = nonNilMap(c.Attributes)
		c.Score = correlationScore(windowMean, baseMean, stddev)
		if c.Score <= 0 {
			continue
		}
		if baseMean != 0 {
			ratio := (windowMean - baseMean) / math.Abs(baseMean)
			if finite(ratio) {
				c.ChangeRatio = &ratio
			}
		}
		c.Direction = "up"
		if windowMean < baseMean {
			c.Direction = "down"
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].MetricName < out[j].MetricName
	})
	if len(out) > limit {
		out = out[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": formatTime(from), "to": formatTime(to),
		"baseline_from": formatTime(baselineFrom), "baseline_to": formatTime(baselineTo),
		"series_compared": scanned,
		"correlations":    out,
	})
	return nil
}

// correlationScore is how many of the baseline's own standard deviations the window's mean moved.
//
// The denominator has a floor of one percent of the baseline's magnitude, which is what keeps a series that
// was perfectly flat and then moved by a rounding error from scoring infinitely higher than a series that
// doubled. A series whose baseline is flat *and* whose window is identical scores 0 and is dropped, because
// "nothing happened here" is not a correlation.
func correlationScore(windowMean, baseMean, stddev float64) float64 {
	diff := math.Abs(windowMean - baseMean)
	if !finite(diff) || diff == 0 {
		return 0
	}
	floor := math.Abs(baseMean) * 0.01
	denom := math.Max(stddev, floor)
	if denom <= 0 {
		// A baseline that is exactly zero and exactly flat: any movement is worth reporting, but it cannot
		// be expressed in its own standard deviations, so it is scored by its magnitude alone.
		denom = math.Abs(diff)
	}
	score := diff / denom
	if !finite(score) {
		return 0
	}
	return score
}
