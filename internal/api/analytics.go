package api

import (
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

// Product analytics (analytics.md §6): how many people use an application and whether they come back.
//
// Both reads count **actors**, from analytics_actor_days. A person active on five days is five rows there and has to
// count once, and a sampled-in actor stands for 1/rate people (analytics.md §2).
//
// Neither read asks PostgreSQL whether the application collects visitors. A read path here depends on ClickHouse alone,
// and static auth mode has no keys to ask about, so "collecting" is what the data says: no actors in the range.

const (
	// analyticsDefaultRange is what a chart of people is read over when nobody says otherwise. The default of the
	// rest of the API is an hour, in which DAU means nothing.
	analyticsDefaultRange = 30 * 24 * time.Hour
	// analyticsMaxActivityRange bounds the activity read: each actor-day becomes up to 30 rows there.
	analyticsMaxActivityRange = 92 * 24 * time.Hour
	// analyticsMaxRetentionRange is as far back as actor days are kept (0102_analytics).
	analyticsMaxRetentionRange = 395 * 24 * time.Hour
	// analyticsMAUDays and analyticsWAUDays are the rolling windows, counting the day itself.
	analyticsMAUDays = 30
	analyticsWAUDays = 7
)

func (s *Server) analyticsRoutes(mux *http.ServeMux) {
	route := func(pattern string, h handlerFunc) { mux.Handle(pattern, s.wrap(pattern, h)) }
	route("GET /api/v1/analytics/activity", s.analyticsActivity)
	route("GET /api/v1/analytics/retention", s.analyticsRetention)
	route("GET /api/v1/analytics/overview", s.analyticsOverview)
	route("GET /api/v1/analytics/events", s.analyticsEvents)
	route("GET /api/v1/analytics/acquisition", s.analyticsAcquisition)
	route("GET /api/v1/analytics/funnel", s.analyticsFunnel)
}

// analyticsRange parses from and to with the default above, whole days being what these tables hold.
func (s *Server) analyticsRange(r *http.Request, limit time.Duration) (time.Time, time.Time, error) {
	if r.URL.Query().Get("from") == "" {
		to := s.now().UTC()
		if v := r.URL.Query().Get("to"); v != "" {
			parsed, err := parseTime(v)
			if err != nil {
				return to, to, badRequest("to: %v", err)
			}
			to = parsed
		}
		return to.Add(-analyticsDefaultRange), to, nil
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return from, to, err
	}
	if to.Sub(from) > limit {
		return from, to, badRequest("the range must be at most %d days", int(limit/(24*time.Hour)))
	}
	return from, to, nil
}

func analyticsDate(t time.Time) string { return t.UTC().Format(time.DateOnly) }

// applyActorDays narrows a read of analytics_actor_days to one application. A function of its own because forgetting
// the environment here would merge production into staging with nothing on the chart to show for it.
func applyActorDays(q *query.Select, f rumFilter) *query.Select {
	q.Where("app = {r_app:String}").Param("r_app", f.app)
	if f.env != nil {
		q.Where("deployment_environment = {r_env:String}").Param("r_env", *f.env)
	}
	return q
}

// analyticsActivitySelect returns DAU, WAU and MAU per day.
//
// A day's actors are reduced to one mergeable sketch, and only those are spread: the sketch of day D counts towards the
// MAU of D and of the 29 days after it. Spreading the actors themselves was exact and ran out of memory at 2 million of
// them (measured), while this costs the same at any size. The price is that counts past a few thousand are estimates,
// within about 1%, which is also what Google Analytics reports. Below that the sketch is exact.
//
// It reaches back 29 days before the range, or the first days of a chart would show a MAU built on a single day.
func analyticsActivitySelect(sc *query.Scope, f rumFilter, from, to time.Time) *query.Select {
	days := applyActorDays(sc.From(query.AnalyticsActorDays), f).
		Columns("day AS a_day", "uniqCombined64State(actor_hash) AS a_state", "avg(weight) AS a_w").
		Where("day >= addDays(toDate({d_from:String}), -{d_back:UInt32}) AND day <= toDate({d_to:String})").
		Param("d_back", uint32(analyticsMAUDays-1)).
		GroupBy("day")
	spread := sc.FromSub(days).
		Columns("arrayJoin(range({d_span:UInt32})) AS since", "addDays(a_day, since) AS d", "a_state", "a_w").
		Param("d_span", uint32(analyticsMAUDays)).
		// Spreading runs past the end of the range, and a day that has not happened has no MAU.
		Where("addDays(a_day, arrayJoin(range({d_span:UInt32}))) BETWEEN toDate({d_from:String}) AND toDate({d_to:String})")
	return sc.FromSub(spread).
		Columns("d",
			// A person's weight is 1/rate, so a distinct count is extrapolated by the window's average.
			"uniqCombined64MergeIf(a_state, since = 0) * avgIf(a_w, since = 0) AS v_dau",
			"uniqCombined64MergeIf(a_state, since < {d_wau:UInt32}) * avgIf(a_w, since < {d_wau:UInt32}) AS v_wau",
			"uniqCombined64Merge(a_state) * avg(a_w) AS v_mau").
		Param("d_wau", uint32(analyticsWAUDays)).
		Param("d_from", analyticsDate(from)).Param("d_to", analyticsDate(to)).
		GroupBy("d").OrderBy("d")
}

// analyticsNewActorsSelect returns the actors seen for the first time, per day.
func analyticsNewActorsSelect(sc *query.Scope, f rumFilter, from, to time.Time) *query.Select {
	actors := applyActorDays(sc.From(query.AnalyticsActors), f).
		Columns("actor_hash", "toDate(min(first_seen)) AS d", "max(weight) AS w").
		GroupBy("actor_hash").
		Having("d >= toDate({d_from:String}) AND d <= toDate({d_to:String})")
	return sc.FromSub(actors).
		Columns("d", "sum(w) AS v_new").
		Param("d_from", analyticsDate(from)).Param("d_to", analyticsDate(to)).
		GroupBy("d").OrderBy("d")
}

// analyticsActivity serves GET /api/v1/analytics/activity.
func (s *Server) analyticsActivity(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	f, err := parseRUMFilter(r, true)
	if err != nil {
		return err
	}
	from, to, err := s.analyticsRange(r, analyticsMaxActivityRange)
	if err != nil {
		return err
	}

	fresh := map[string]float64{}
	rows, err := sc.Query(r.Context(), analyticsNewActorsSelect(sc, f, from, to))
	if err != nil {
		return err
	}
	for rows.Next() {
		var d time.Time
		var n float64
		if err := rows.Scan(&d, &n); err != nil {
			rows.Close()
			return err
		}
		fresh[analyticsDate(d)] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = sc.Query(r.Context(), analyticsActivitySelect(sc, f, from, to))
	if err != nil {
		return err
	}
	defer rows.Close()
	days := []map[string]any{}
	for rows.Next() {
		var d time.Time
		var dau, wau, mau float64
		if err := rows.Scan(&d, &dau, &wau, &mau); err != nil {
			return err
		}
		// A day with nobody in the narrower window averages over no rows at all.
		if math.IsNaN(dau) {
			dau = 0
		}
		if math.IsNaN(wau) {
			wau = 0
		}
		date := analyticsDate(d)
		// DAU over MAU, the share of a month's people who show up on a given day.
		var stickiness float64
		if mau > 0 {
			stickiness = dau / mau
		}
		days = append(days, map[string]any{
			"date": date, "dau": dau, "wau": wau, "mau": mau, "new_users": fresh[date], "stickiness": stickiness,
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": days, "collecting": len(days) > 0})
	return nil
}

// analyticsGrains maps a grain to the expression that buckets a date and the unit that counts periods between
// buckets. A closed list, because both end up in the statement: the parameter only ever selects one of these.
var analyticsGrains = map[string]struct{ bucket, unit string }{
	"day":   {"toDate(%s)", "day"},
	"week":  {"toMonday(%s)", "week"},
	"month": {"toStartOfMonth(%s)", "month"},
}

// analyticsRetentionSelect returns, per cohort and period, how many of the cohort's actors were active.
//
// The cohort is the first day an actor appears in analytics_actor_days, read from the table itself over its whole
// lifetime: it holds exactly what is needed, so this needs no join to analytics_actors across shards. The price is
// that an actor gone for more than 13 months counts as new when they return, which is also when their old rows expire.
func analyticsRetentionSelect(sc *query.Scope, f rumFilter, grain string, from, to time.Time) *query.Select {
	g := analyticsGrains[grain]
	bucket := func(e string) string { return strings.Replace(g.bucket, "%s", e, 1) }
	actors := applyActorDays(sc.From(query.AnalyticsActorDays), f).
		Columns("actor_hash", "min(day) AS first_day", "groupUniqArray(day) AS days", "max(weight) AS w").
		GroupBy("actor_hash").
		Having("first_day >= toDate({d_from:String}) AND first_day <= toDate({d_to:String})")
	periods := sc.FromSub(actors).Columns(
		bucket("first_day")+" AS cohort",
		"arrayJoin(arrayDistinct(arrayMap(x -> dateDiff('"+g.unit+"', "+bucket("first_day")+", "+bucket("x")+"), days))) AS period",
		"w",
	)
	return sc.FromSub(periods).
		Columns("cohort", "period", "sum(w) AS v_users", "min(w) AS v_wmin", "max(w) AS v_wmax").
		Param("d_from", analyticsDate(from)).Param("d_to", analyticsDate(to)).
		GroupBy("cohort", "period").OrderBy("cohort", "period")
}

type analyticsPeriodJSON struct {
	Period int64   `json:"period"`
	Users  float64 `json:"users"`
	Rate   float64 `json:"rate"`
}

type analyticsCohortJSON struct {
	Cohort  string                `json:"cohort"`
	Size    float64               `json:"size"`
	Periods []analyticsPeriodJSON `json:"periods"`
}

// analyticsRetention serves GET /api/v1/analytics/retention.
func (s *Server) analyticsRetention(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	f, err := parseRUMFilter(r, true)
	if err != nil {
		return err
	}
	grain := r.URL.Query().Get("grain")
	if grain == "" {
		grain = "week"
	}
	if _, ok := analyticsGrains[grain]; !ok {
		return badRequest("grain must be day, week or month")
	}
	from, to, err := s.analyticsRange(r, analyticsMaxRetentionRange)
	if err != nil {
		return err
	}
	rows, err := sc.Query(r.Context(), analyticsRetentionSelect(sc, f, grain, from, to))
	if err != nil {
		return err
	}
	defer rows.Close()

	cohorts := map[string]*analyticsCohortJSON{}
	var wMin, wMax float64
	for rows.Next() {
		var c time.Time
		var period int64
		var users, lo, hi float64
		if err := rows.Scan(&c, &period, &users, &lo, &hi); err != nil {
			return err
		}
		key := analyticsDate(c)
		co := cohorts[key]
		if co == nil {
			co = &analyticsCohortJSON{Cohort: key, Periods: []analyticsPeriodJSON{}}
			cohorts[key] = co
		}
		if period == 0 {
			co.Size = users
		}
		co.Periods = append(co.Periods, analyticsPeriodJSON{Period: period, Users: users})
		if wMin == 0 || lo < wMin {
			wMin = lo
		}
		wMax = max(wMax, hi)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	out := make([]*analyticsCohortJSON, 0, len(cohorts))
	for _, co := range cohorts {
		for i := range co.Periods {
			if co.Size > 0 {
				co.Periods[i].Rate = co.Periods[i].Users / co.Size
			}
		}
		out = append(out, co)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Cohort < out[j].Cohort })
	writeJSON(w, http.StatusOK, map[string]any{
		"grain": grain, "cohorts": out, "collecting": len(out) > 0,
		// Different weights mean the key's sample rate moved inside the range, so people dropped out of the sample
		// at that moment and not out of the product (analytics.md §2).
		"sampling_changed": wMin != wMax,
	})
	return nil
}
