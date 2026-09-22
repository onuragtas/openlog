package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/apm"
	"github.com/onuragtas/openlog/internal/rum"
)

// Real user monitoring reads (docs/contracts/api.md "Real user monitoring", rum.md §7, D-136).
//
// Every endpoint here reads the rollups of 0094_rum through the tenant-scoped query layer and re-aggregates
// with GROUP BY, like the APM ones (apm.md §8). There is deliberately **no** RUM error endpoint: a browser
// error is an error span of the browser app's service, so it is already in the APM error inbox with the same
// fingerprint, grouping and workflow state, and the UI links straight to `GET /apm/errors?service=<app>`.
// Adding a parallel inbox would mean two places to resolve the same error.

// rumMaxRoutes bounds the pages a list returns; the rollup key is the route, which the ingest bounds too.
const rumMaxRoutes = 200

func (s *Server) rumRoutes(mux *http.ServeMux) {
	route := func(pattern string, h handlerFunc) { mux.Handle(pattern, s.wrap(pattern, h)) }
	route("GET /api/v1/rum/apps", s.rumApps)
	route("GET /api/v1/rum/overview", s.rumOverview)
	route("GET /api/v1/rum/pages", s.rumPages)
	route("GET /api/v1/rum/vitals", s.rumVitals)
	route("GET /api/v1/rum/sessions", s.rumSessions)
	route("GET /api/v1/rum/sessions/{session_id}", s.rumSessionDetail)
	route("GET /api/v1/rum/releases", s.rumReleases)
}

// ---- filters ----

// rumFilter selects one browser application and, optionally, an environment and a route.
type rumFilter struct {
	app   string
	env   *string
	route string
}

func parseRUMFilter(r *http.Request, requireApp bool) (rumFilter, error) {
	q := r.URL.Query()
	f := rumFilter{app: strings.TrimSpace(q.Get("app")), route: strings.TrimSpace(q.Get("route"))}
	if requireApp && f.app == "" {
		return f, badRequest("app is required")
	}
	if len(f.app) > maxServiceNameBytes {
		return f, badRequest("app must be at most %d bytes", maxServiceNameBytes)
	}
	if len(f.route) > rum.MaxRouteBytes {
		return f, badRequest("route must be at most %d bytes", rum.MaxRouteBytes)
	}
	f.env = optionalParam(q, "environment")
	return f, nil
}

// apply adds the app/environment/route conditions. The column is `app` in every RUM rollup.
func (f rumFilter) apply(q *query.Select) *query.Select {
	if f.app != "" {
		q.Where("app = {r_app:String}").Param("r_app", f.app)
	}
	if f.env != nil {
		q.Where("deployment_environment = {r_env:String}").Param("r_env", *f.env)
	}
	if f.route != "" {
		q.Where("route = {r_route:String}").Param("r_route", f.route)
	}
	return q
}

// rumRange bounds the query to whole minutes of the range, like the APM rollup reads.
func rumRange(q *query.Select, from, to time.Time) *query.Select {
	return q.Where("timestamp >= toStartOfMinute(fromUnixTimestamp64Nano({t_from:Int64}))").
		Where("timestamp < toStartOfMinute(fromUnixTimestamp64Nano({t_to:Int64}))").
		Param("t_from", from.UnixNano()).Param("t_to", to.UnixNano())
}

// ---- shapes ----

type rumVitalJSON struct {
	Name  string  `json:"name"`
	Unit  string  `json:"unit"`
	Count float64 `json:"count"`
	// P50/P75/P95 are null without measurements. p75 leads the UI because it is the percentile the Core Web
	// Vitals assessment is defined on.
	P50 *float64 `json:"p50"`
	P75 *float64 `json:"p75"`
	P95 *float64 `json:"p95"`
	Avg *float64 `json:"avg"`
	// Shares of measurements in each band, 0..1.
	Good             float64 `json:"good"`
	NeedsImprovement float64 `json:"needs_improvement"`
	Poor             float64 `json:"poor"`
	// Rating of the p75, the way the programme scores a page ("" without measurements).
	Rating        string  `json:"rating"`
	GoodThreshold float64 `json:"good_threshold"`
	PoorThreshold float64 `json:"poor_threshold"`
}

type rumPageJSON struct {
	Route     string   `json:"route"`
	Views     float64  `json:"views"`
	AvgMs     *float64 `json:"avg_ms"`
	P50Ms     *float64 `json:"p50_ms"`
	P75Ms     *float64 `json:"p75_ms"`
	P95Ms     *float64 `json:"p95_ms"`
	MaxMs     float64  `json:"max_ms"`
	TTFBAvgMs *float64 `json:"ttfb_avg_ms"`
	LCPP75    *float64 `json:"lcp_p75"`
	Errors    float64  `json:"errors"`
}

type rumAppJSON struct {
	App         string  `json:"app"`
	Environment string  `json:"environment"`
	Views       float64 `json:"views"`
	Sessions    uint64  `json:"sessions"`
	Errors      float64 `json:"errors"`
	LastSeen    string  `json:"last_seen"`
}

type rumSessionJSON struct {
	SessionID      string  `json:"session_id"`
	App            string  `json:"app"`
	Environment    string  `json:"environment"`
	StartedAt      string  `json:"started_at"`
	EndedAt        string  `json:"ended_at"`
	DurationMs     float64 `json:"duration_ms"`
	PageViews      float64 `json:"page_views"`
	Errors         float64 `json:"errors"`
	EntryRoute     string  `json:"entry_route"`
	ExitRoute      string  `json:"exit_route"`
	DeviceType     string  `json:"device_type"`
	BrowserName    string  `json:"browser_name"`
	BrowserVersion string  `json:"browser_version"`
	OSName         string  `json:"os_name"`
	// TraceID opens the session's newest trace, so the detail always has somewhere to go even after the
	// page view spans have expired with the 7-day trace retention.
	TraceID string `json:"trace_id"`
	// UserID and Country are **only filled by the session detail**, never by the list. They live on the
	// spans, not on rum_sessions (rum.md §3.7), so the list — which reads the rollup — has no way to know
	// them and leaves both empty rather than running a second query per row.
	UserID  string `json:"user_id"`
	Country string `json:"country"`
}

// rumEventJSON is one stored span of a session (the detail view's timeline).
type rumEventJSON struct {
	Timestamp  string  `json:"timestamp"`
	Event      string  `json:"event"`
	Name       string  `json:"name"`
	Route      string  `json:"route"`
	DurationMs float64 `json:"duration_ms"`
	TraceID    string  `json:"trace_id"`
	SpanID     string  `json:"span_id"`
	// ErrorGroupID links a captured error to its group in the APM error inbox. It is "" when the row is not
	// an error, and when the span is no longer the group's newest sample: a span does not carry its group,
	// so fillRUMErrorGroups resolves it through apm_error_groups, which keeps one sample per group.
	ErrorGroupID string `json:"error_group_id"`
	StatusCode   int    `json:"status_code"`
}

// ---- handlers ----

// rumApps lists the browser applications that reported in the range.
func (s *Server) rumApps(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	// Sessions carry first/last seen and the error count, so one read answers the list.
	q := sc.From(query.RumSessions).Columns(
		"app", "deployment_environment",
		"sum(page_views) AS v_views",
		"toUInt64(count()) AS v_sessions",
		"sum(errors) AS v_errors",
		"max(last_seen) AS v_last",
	).Where("last_seen >= fromUnixTimestamp64Nano({t_from:Int64})").Param("t_from", from.UnixNano()).
		Where("first_seen < fromUnixTimestamp64Nano({t_to:Int64})").Param("t_to", to.UnixNano()).
		GroupBy("app", "deployment_environment").OrderBy("v_views DESC").Limit(s.cfg.MaxRows)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	apps := []rumAppJSON{}
	for rows.Next() {
		var a rumAppJSON
		var last time.Time
		if err := rows.Scan(&a.App, &a.Environment, &a.Views, &a.Sessions, &a.Errors, &last); err != nil {
			return err
		}
		a.LastSeen = formatTime(last)
		apps = append(apps, a)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": apps})
	return nil
}

// rumOverview is the application's headline: the vitals, the page view series and the totals.
func (s *Server) rumOverview(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	f, err := parseRUMFilter(r, true)
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	step, err := apmStep(r, from, to)
	if err != nil {
		return err
	}
	vitals, err := s.rumVitalSummary(r, sc, f, from, to)
	if err != nil {
		return err
	}

	// Page view series.
	series := rumRange(f.apply(sc.From(query.RumPageViews1m).Columns(
		"toStartOfInterval(timestamp, toIntervalSecond({step:UInt32})) AS t",
		"sum(views) AS v_views",
		"sum(duration_sum_ms) AS v_dsum",
	)), from, to).Param("step", uint32(step/time.Second)).GroupBy("t").OrderBy("t").Limit(s.cfg.MaxRows)
	rows, err := sc.Query(r.Context(), series)
	if err != nil {
		return err
	}
	points := []map[string]any{}
	var totalViews, totalDur float64
	for rows.Next() {
		var t time.Time
		var views, dsum float64
		if err := rows.Scan(&t, &views, &dsum); err != nil {
			rows.Close()
			return err
		}
		totalViews += views
		totalDur += dsum
		var avg *float64
		if views > 0 {
			v := dsum / views
			avg = &v
		}
		points = append(points, map[string]any{"t": t.UnixMilli(), "views": views, "avg_ms": avg})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	sessions, errs, err := s.rumSessionTotals(r, sc, f, from, to)
	if err != nil {
		return err
	}
	totals := map[string]any{"views": totalViews, "sessions": sessions, "errors": errs}
	if totalViews > 0 {
		totals["avg_ms"] = totalDur / totalViews
	} else {
		totals["avg_ms"] = nil
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": formatTime(from), "to": formatTime(to), "step": formatStep(step),
		"vitals": vitals, "points": points, "totals": totals,
	})
	return nil
}

// rumSessionTotals counts the sessions and errors of the range.
func (s *Server) rumSessionTotals(r *http.Request, sc *query.Scope, f rumFilter, from, to time.Time) (uint64, float64, error) {
	q := sc.From(query.RumSessions).Columns("toUInt64(count()) AS v_sessions", "sum(errors) AS v_errors").
		Where("last_seen >= fromUnixTimestamp64Nano({t_from:Int64})").Param("t_from", from.UnixNano()).
		Where("first_seen < fromUnixTimestamp64Nano({t_to:Int64})").Param("t_to", to.UnixNano())
	// The session rollup has no route column: a session spans routes.
	sessionFilter := rumFilter{app: f.app, env: f.env}
	sessionFilter.apply(q)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	var sessions uint64
	var errs float64
	if rows.Next() {
		if err := rows.Scan(&sessions, &errs); err != nil {
			return 0, 0, err
		}
	}
	return sessions, errs, rows.Err()
}

// rumVitalSummary reads every vital of the filter in one query and computes the percentiles in Go.
func (s *Server) rumVitalSummary(r *http.Request, sc *query.Scope, f rumFilter, from, to time.Time) ([]rumVitalJSON, error) {
	q := rumRange(f.apply(sc.From(query.RumVitals1m).Columns(
		"vital",
		"sum(count) AS v_count",
		"sum(value_sum) AS v_sum",
		"sum(good) AS v_good",
		"sum(needs_improvement) AS v_ni",
		"sum(poor) AS v_poor",
		"tupleElement(sumMap(value_hist), 1) AS v_hk",
		"tupleElement(sumMap(value_hist), 2) AS v_hv",
	)), from, to).GroupBy("vital").Limit(s.cfg.MaxRows)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byName := map[string]rumVitalJSON{}
	for rows.Next() {
		var name string
		var count, sum, good, ni, poor float64
		var hk []int16
		var hv []float64
		if err := rows.Scan(&name, &count, &sum, &good, &ni, &poor, &hk, &hv); err != nil {
			return nil, err
		}
		h := rum.NewHist(hk, hv)
		v := rumVitalJSON{Name: name, Count: count, Good: share(good, count), NeedsImprovement: share(ni, count), Poor: share(poor, count)}
		if count > 0 {
			v.P50, v.P75, v.P95 = optFloat(h.Quantile(0.50)), optFloat(h.Quantile(0.75)), optFloat(h.Quantile(0.95))
			avg := sum / count
			v.Avg = &avg
			if v.P75 != nil {
				v.Rating = rum.Rate(name, *v.P75)
			}
		}
		byName[name] = v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Always return every vital in a stable order, so the UI shows five cards and says "no data" rather
	// than silently dropping the ones a browser did not report.
	out := make([]rumVitalJSON, 0, len(rum.Vitals))
	for _, name := range rum.Vitals {
		v, ok := byName[name]
		if !ok {
			v = rumVitalJSON{Name: name}
		}
		t := rum.Thresholds[name]
		v.Unit, v.GoodThreshold, v.PoorThreshold = t.Unit, t.Good, t.Poor
		out = append(out, v)
	}
	return out, nil
}

func share(n, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return n / total
}

// rumVitals serves the vitals of one application, optionally for one route.
func (s *Server) rumVitals(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	f, err := parseRUMFilter(r, true)
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	vitals, err := s.rumVitalSummary(r, sc, f, from, to)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": formatTime(from), "to": formatTime(to), "vitals": vitals})
	return nil
}

// rumPages lists the routes of an application: how often they were viewed and how slow they were.
func (s *Server) rumPages(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	f, err := parseRUMFilter(r, true)
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	limit, err := s.apmLimit(r, 50, rumMaxRoutes)
	if err != nil {
		return err
	}
	sort := r.URL.Query().Get("sort")
	order := "p_views DESC"
	switch sort {
	case "", "views":
	case "slowest":
		// By total time, not by p95: the slowest page worth fixing is the one that costs the most seconds
		// across all its views, which is the same reasoning as APM's "most time consuming".
		order = "p_dsum DESC"
	case "avg":
		order = "p_dsum / p_views DESC"
	default:
		return badRequest("sort must be one of views, slowest, avg")
	}

	q := rumRange(f.apply(sc.From(query.RumPageViews1m).Columns(
		"route",
		"sum(views) AS p_views",
		"sum(duration_sum_ms) AS p_dsum",
		"max(duration_max_ms) AS p_max",
		"sum(ttfb_sum_ms) AS p_ttfb",
		"tupleElement(sumMap(duration_hist), 1) AS p_hk",
		"tupleElement(sumMap(duration_hist), 2) AS p_hv",
	)), from, to).GroupBy("route").OrderBy(order).Limit(limit)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	pages := []rumPageJSON{}
	for rows.Next() {
		var p rumPageJSON
		var dsum, ttfb float64
		var hk []int16
		var hv []float64
		if err := rows.Scan(&p.Route, &p.Views, &dsum, &p.MaxMs, &ttfb, &hk, &hv); err != nil {
			return err
		}
		if p.Views > 0 {
			avg := dsum / p.Views
			t := ttfb / p.Views
			p.AvgMs, p.TTFBAvgMs = &avg, &t
			// Page load durations use the APM bucket, so they share apm.HistQuantile's accuracy bound.
			h := rum.NewHist(hk, hv)
			p.P50Ms, p.P75Ms, p.P95Ms = optFloat(h.Quantile(0.50)), optFloat(h.Quantile(0.75)), optFloat(h.Quantile(0.95))
		}
		pages = append(pages, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// The page view rollup carries load time, not vitals (0094_rum.sql), so the LCP of each route comes from
	// the vitals rollup — the same p75 the overview leads with, per route.
	if len(pages) > 0 {
		vq := rumRange(f.apply(sc.From(query.RumVitals1m).Columns(
			"route",
			"tupleElement(sumMap(value_hist), 1) AS v_hk",
			"tupleElement(sumMap(value_hist), 2) AS v_hv",
		)), from, to).Where("vital = {v_name:String}").Param("v_name", rum.VitalLCP).GroupBy("route").Limit(limit)
		vrows, err := sc.Query(r.Context(), vq)
		if err != nil {
			return err
		}
		lcp := map[string]*float64{}
		for vrows.Next() {
			var route string
			var hk []int16
			var hv []float64
			if err := vrows.Scan(&route, &hk, &hv); err != nil {
				vrows.Close()
				return err
			}
			lcp[route] = optFloat(rum.NewHist(hk, hv).Quantile(0.75))
		}
		vrows.Close()
		if err := vrows.Err(); err != nil {
			return err
		}
		for i := range pages {
			pages[i].LCPP75 = lcp[pages[i].Route]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pages": pages})
	return nil
}

// rumSessions lists sessions, newest first.
func (s *Server) rumSessions(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	f, err := parseRUMFilter(r, true)
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	limit, err := s.apmLimit(r, 50, 500)
	if err != nil {
		return err
	}
	q := s.rumSessionSelect(sc).
		Where("last_seen >= fromUnixTimestamp64Nano({t_from:Int64})").Param("t_from", from.UnixNano()).
		Where("first_seen < fromUnixTimestamp64Nano({t_to:Int64})").Param("t_to", to.UnixNano()).
		OrderBy("s_last DESC").Limit(limit)
	rumFilter{app: f.app, env: f.env}.apply(q)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	sessions, err := scanRUMSessions(rows)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
	return nil
}

// rumSessionSelect is the shared session projection (the rollup is aggregating, so everything re-aggregates).
func (s *Server) rumSessionSelect(sc *query.Scope) *query.Select {
	return sc.From(query.RumSessions).Columns(
		"session_id", "app", "deployment_environment",
		"min(first_seen) AS s_first",
		"max(last_seen) AS s_last",
		"sum(page_views) AS s_views",
		"sum(errors) AS s_errors",
		"argMinMerge(entry_route) AS s_entry",
		"argMaxMerge(exit_route) AS s_exit",
		"argMaxMerge(last_trace_id) AS s_trace",
		"anyLast(device_type) AS s_device",
		"anyLast(browser_name) AS s_browser",
		"anyLast(browser_version) AS s_browser_version",
		"anyLast(os_name) AS s_os",
	).GroupBy("session_id", "app", "deployment_environment")
}

func scanRUMSessions(rows query.Rows) ([]rumSessionJSON, error) {
	out := []rumSessionJSON{}
	for rows.Next() {
		var s rumSessionJSON
		var first, last time.Time
		if err := rows.Scan(&s.SessionID, &s.App, &s.Environment, &first, &last, &s.PageViews, &s.Errors,
			&s.EntryRoute, &s.ExitRoute, &s.TraceID, &s.DeviceType, &s.BrowserName, &s.BrowserVersion, &s.OSName); err != nil {
			return nil, err
		}
		s.StartedAt, s.EndedAt = formatTime(first), formatTime(last)
		s.DurationMs = float64(last.Sub(first).Milliseconds())
		out = append(out, s)
	}
	return out, rows.Err()
}

// rumSessionDetail is one session with the events it produced.
//
// The events come from `spans` rather than from a rollup, because a timeline needs the individual rows and
// their trace ids. That read is bounded by the tenant, the session id (a bloom filter skip index, 0094) and
// the range, and by the trace retention: a session older than that still has its summary here, with the
// trace links it recorded, but no timeline.
func (s *Server) rumSessionDetail(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	id := strings.TrimSpace(r.PathValue("session_id"))
	if !isHex(id, 32) {
		return badRequest("session_id must be 32 hex characters")
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	q := s.rumSessionSelect(sc).Where("session_id = {s_id:String}").Param("s_id", id).Limit(1)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	sessions, err := scanRUMSessions(rows)
	rows.Close()
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		return notFound("session not found")
	}
	sess := sessions[0]

	// The timeline: the session's own spans, within the requested range widened to the session itself.
	start, end := from, to
	if t, err := time.Parse(timeLayout, sess.StartedAt); err == nil && t.Before(start) {
		start = t.Add(-time.Minute)
	}
	if t, err := time.Parse(timeLayout, sess.EndedAt); err == nil && t.After(end) {
		end = t.Add(time.Minute)
	}
	erows, err := sc.Query(r.Context(), rumTimelineSelect(sc, id, start, end, min(500, s.cfg.MaxRows)))
	if err != nil {
		return err
	}
	defer erows.Close()
	events := []rumEventJSON{}
	for erows.Next() {
		var e rumEventJSON
		var ts time.Time
		var dur uint64
		var status uint16
		var user, country string
		if err := erows.Scan(&ts, &e.Name, &dur, &e.TraceID, &e.SpanID, &status, &e.Event, &e.Route,
			&user, &country); err != nil {
			return err
		}
		// Last non-empty wins: a visit can sign in part-way through, and the identity the session ended
		// with is the one worth showing. The country is stamped identically on every span, so the same
		// rule costs nothing there.
		if user != "" {
			sess.UserID = user
		}
		if country != "" {
			sess.Country = country
		}
		e.Timestamp = formatTime(ts)
		e.DurationMs = float64(dur) / 1e6
		e.StatusCode = int(status)
		events = append(events, e)
	}
	if err := erows.Err(); err != nil {
		return err
	}
	if err := fillRUMErrorGroups(r.Context(), sc, sess.App, events); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": sess, "events": events})
	return nil
}

// rumMaxErrorGroups bounds the group lookup of one timeline. A browser app's inbox is small next to a
// backend service's, and the query exists to fill a link, not to page through errors.
const rumMaxErrorGroups = 1000

// fillRUMErrorGroups fills ErrorGroupID for the error rows of a timeline. `spans` does not store the group —
// it is a key of apm_error_groups (rum.md §7: a browser error is an APM error group of the same
// application) — and a group keeps only its newest sample (argMax), so an error resolves while it is that
// sample and otherwise keeps the empty id the field documents. One query per session rather than per row,
// and the match is made here because the query builder has no HAVING.
func fillRUMErrorGroups(ctx context.Context, sc *query.Scope, app string, events []rumEventJSON) error {
	wanted := map[string]int{}
	for i, e := range events {
		if e.Event == rum.EventError && e.SpanID != "" {
			wanted[e.SpanID] = i
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	q := sc.From(query.ApmErrorGroups).Columns("error_group_id", "argMaxMerge(last_span_id) AS m_span").
		Where("service_name = {svc:String}").Param("svc", app).
		GroupBy("error_group_id").Limit(rumMaxErrorGroups)
	rows, err := sc.Query(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id uint64
		var span string
		if err := rows.Scan(&id, &span); err != nil {
			return err
		}
		if i, ok := wanted[span]; ok {
			events[i].ErrorGroupID = apm.GroupIDString(id)
		}
	}
	return rows.Err()
}

// rumTimelineSelect builds the span query behind a session timeline. It is a function of its own so a test
// can build it without a session in the database: the handler reaches this query only after the session
// lookup returns a row, which is how a fragment the query layer refuses shipped unnoticed.
func rumTimelineSelect(sc *query.Scope, sessionID string, from, to time.Time, limit int) *query.Select {
	return sc.From(query.Spans).Columns(
		"timestamp", "name", "duration_ns", "trace_id", "span_id",
		// spans stores neither of these as a column: the HTTP status of a fetch span is an attribute
		// (rum.md §2.4), and the error group is a key of apm_error_groups, resolved by fillRUMErrorGroups.
		"toUInt16OrZero(attributes['http.response.status_code']) AS e_status",
		// The keys are bound parameters, not literals: the query layer refuses the token "openlog" anywhere
		// in a fragment (it guards the database name), and every RUM attribute key starts with it.
		"attributes[{a_event:String}] AS e_event",
		"attributes[{a_route:String}] AS e_route",
		// Who and where (rum.md §3.7). They are on the spans rather than on rum_sessions, so the detail is
		// the only read that can answer them — and it already has these rows in hand.
		"attributes[{a_user:String}] AS e_user",
		"attributes[{a_country:String}] AS e_country",
	).
		Param("a_event", rum.AttrEvent).Param("a_route", rum.AttrRoute).
		Param("a_user", rum.AttrUserID).Param("a_country", rum.AttrGeoCountry).
		Where("attributes['session.id'] = {s_id:String}").Param("s_id", sessionID).
		Where("timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp <= fromUnixTimestamp64Nano({t_to:Int64})").
		Param("t_from", from.UnixNano()).Param("t_to", to.UnixNano()).
		OrderBy("timestamp").Limit(limit)
}
