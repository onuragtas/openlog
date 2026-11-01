package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/rum"
)

// What people did and where they came from (analytics.md §6), read from analytics_events. Unlike the actor reads these
// work in `sessions` mode as well, so only the counts of people need `visitors`.

const (
	// analyticsMaxEventsRange is as far back as analytics_events is kept (0102_analytics).
	analyticsMaxEventsRange = 90 * 24 * time.Hour
	// analyticsMaxBreakdown bounds a list. Event names are bounded per key and a parameter's values are not.
	analyticsMaxBreakdown = 200
	// analyticsFunnelMaxUserWindow is why a user funnel exists at all: people take days to do what a session cannot.
	analyticsFunnelMaxUserWindow = analyticsMaxEventsRange
	analyticsFunnelUserWindow    = 7 * 24 * time.Hour
)

// analyticsDimensions maps the `by` of /acquisition to its column. A closed list, because the column is in the statement.
var analyticsDimensions = map[string]string{
	"referrer": "referrer_host",
	"source":   "utm_source",
	"medium":   "utm_medium",
	"campaign": "utm_campaign",
}

// analyticsAudienceDimensions maps the `by` of /audience to its column. Closed for the same reason. The model is not among
// them because it has a table of its own.
var analyticsAudienceDimensions = map[string]string{
	"country": "country",
	"device":  "device_type",
	"os":      "os_name",
	"browser": "browser_name",
	"version": "version",
}

const analyticsAudienceModel = "model"

// analyticsEventRange narrows a read of analytics_events to one application and a range.
func analyticsEventRange(q *query.Select, f rumFilter, from, to time.Time) *query.Select {
	return applyActorDays(q, f).
		Where("timestamp >= fromUnixTimestamp64Milli({t_from:Int64}) AND timestamp < fromUnixTimestamp64Milli({t_to:Int64})").
		Param("t_from", from.UnixMilli()).Param("t_to", to.UnixMilli())
}

// analyticsOverviewSelect returns page views, events and sessions per day. A session's weight is counted once per day
// and an event's on every row, which is why the inner query keeps both.
func analyticsOverviewSelect(sc *query.Scope, f rumFilter, from, to time.Time) *query.Select {
	sessions := analyticsEventRange(sc.From(query.AnalyticsEvents), f, from, to).
		Columns("toDate(timestamp) AS d", "session_id", "max(weight) AS w",
			"sumIf(weight, kind = 'page_view') AS c_views", "sumIf(weight, kind = 'custom') AS c_events").
		GroupBy("d", "session_id")
	return sc.FromSub(sessions).
		Columns("d", "sum(c_views) AS v_views", "sum(c_events) AS v_events", "sum(w) AS v_sessions").
		GroupBy("d").OrderBy("d")
}

// analyticsEventsSelect returns the event names, or the values of one parameter of one event.
//
// People are the distinct actors times their average weight. Summing weights would count a person once per session,
// and the weight of an actor differs only when a key's sample rate moved, so the average is the honest extrapolation.
func analyticsEventsSelect(sc *query.Scope, f rumFilter, name, param string, from, to time.Time) *query.Select {
	dim := "name"
	if name != "" {
		dim = "params[{p_key:String}]"
	}
	sessions := analyticsEventRange(sc.From(query.AnalyticsEvents), f, from, to).
		Columns(dim+" AS v_key", "session_id", "actor_hash", "sum(weight) AS c", "max(weight) AS w").
		Where("kind = 'custom'").
		GroupBy("v_key", "session_id", "actor_hash")
	if name != "" {
		sessions.Where("name = {e_name:String}").Param("e_name", name).Param("p_key", param)
	}
	return sc.FromSub(sessions).
		Columns("v_key", "sum(c) AS v_count", "sum(w) AS v_sessions", "uniqExactIf(actor_hash, actor_hash != 0) * avg(w) AS v_users").
		GroupBy("v_key").OrderBy("v_count DESC").Limit(analyticsMaxBreakdown)
}

// analyticsAcquisitionSelect returns sessions by how they started. A session is attributed to its first page view,
// which is the only one carrying attribution, so an empty value here is a direct visit and not a missing one.
func analyticsAcquisitionSelect(sc *query.Scope, f rumFilter, column string, from, to time.Time) *query.Select {
	sessions := analyticsEventRange(sc.From(query.AnalyticsEvents), f, from, to).
		Columns("session_id", "toString(argMin("+column+", timestamp)) AS v_key", "max(weight) AS w", "any(actor_hash) AS a").
		Where("kind = 'page_view'").
		GroupBy("session_id")
	return sc.FromSub(sessions).
		Columns("v_key", "sum(w) AS v_sessions", "uniqExactIf(a, a != 0) * avg(w) AS v_users").
		GroupBy("v_key").OrderBy("v_sessions DESC").Limit(analyticsMaxBreakdown)
}

// analyticsAudienceSelect returns sessions and people by what they ran on. A session counts once, under its first event, so
// someone who crossed a border or updated in the middle of one is not two sessions. A person on two devices is one person
// under each, which is why the people of a list do not add up to the total.
func analyticsAudienceSelect(sc *query.Scope, f rumFilter, column string, from, to time.Time) *query.Select {
	sessions := analyticsEventRange(sc.From(query.AnalyticsEvents), f, from, to).
		Columns("session_id", "toString(argMin("+column+", timestamp)) AS v_key", "max(weight) AS w", "any(actor_hash) AS a").
		GroupBy("session_id")
	return sc.FromSub(sessions).
		Columns("v_key", "sum(w) AS v_sessions", "uniqExactIf(a, a != 0) * avg(w) AS v_users").
		GroupBy("v_key").OrderBy("v_sessions DESC").Limit(analyticsMaxBreakdown)
}

// analyticsAudienceModelSelect is the same answer from the per-day rollup. Past a few thousand these are estimates.
func analyticsAudienceModelSelect(sc *query.Scope, f rumFilter, from, to time.Time) *query.Select {
	return applyActorDays(sc.From(query.AnalyticsDeviceModels), f).
		Columns("toString(model) AS v_key", "uniqCombined64Merge(sessions) * max(weight) AS v_sessions", "uniqCombined64Merge(actors) * max(weight) AS v_users").
		Where("day >= toDate(fromUnixTimestamp64Milli({t_from:Int64})) AND day <= toDate(fromUnixTimestamp64Milli({t_to:Int64}))").
		Param("t_from", from.UnixMilli()).Param("t_to", to.UnixMilli()).
		GroupBy("v_key").OrderBy("v_sessions DESC").Limit(analyticsMaxBreakdown)
}

// analyticsFunnelSelect is rumFunnelSelect over analytics_events, so a step can also be a route and a funnel can reach
// past 7 days. `byUser` follows one person across sessions, which only exists in `visitors` mode.
func analyticsFunnelSelect(sc *query.Scope, f rumFilter, steps []string, byUser bool, window time.Duration, from, to time.Time) *query.Select {
	conds := make([]string, len(steps))
	inner := analyticsEventRange(sc.From(query.AnalyticsEvents), f, from, to)
	for i := range steps {
		conds[i] = fmt.Sprintf("name = {s%d:String}", i)
		inner.Param(fmt.Sprintf("s%d", i), steps[i])
	}
	inner.Columns(fmt.Sprintf("windowFunnel({w_ms:UInt64})(toUInt64(toUnixTimestamp64Milli(timestamp)), %s) AS f_level", strings.Join(conds, ", "))).
		Param("w_ms", uint64(window/time.Millisecond))
	if byUser {
		inner.Where("actor_hash != 0").GroupBy("actor_hash")
	} else {
		inner.GroupBy("session_id")
	}
	return sc.FromSub(inner).Columns("f_level", "count() AS f_sessions").GroupBy("f_level").OrderBy("f_level")
}

// analyticsOverview serves GET /api/v1/analytics/overview.
func (s *Server) analyticsOverview(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	f, err := parseRUMFilter(r, true)
	if err != nil {
		return err
	}
	from, to, err := s.analyticsRange(r, analyticsMaxEventsRange)
	if err != nil {
		return err
	}
	rows, err := sc.Query(r.Context(), analyticsOverviewSelect(sc, f, from, to))
	if err != nil {
		return err
	}
	defer rows.Close()
	days := []map[string]any{}
	var views, events, sessions float64
	for rows.Next() {
		var d time.Time
		var v, e, se float64
		if err := rows.Scan(&d, &v, &e, &se); err != nil {
			return err
		}
		views, events, sessions = views+v, events+e, sessions+se
		days = append(days, map[string]any{"date": analyticsDate(d), "page_views": v, "events": e, "sessions": se})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"days": days, "collecting": len(days) > 0,
		"totals": map[string]float64{"page_views": views, "events": events, "sessions": sessions},
	})
	return nil
}

// analyticsAudience serves GET /api/v1/analytics/audience.
func (s *Server) analyticsAudience(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	f, err := parseRUMFilter(r, true)
	if err != nil {
		return err
	}
	by := r.URL.Query().Get("by")
	if by == "" {
		by = "country"
	}
	column, ok := analyticsAudienceDimensions[by]
	if !ok && by != analyticsAudienceModel {
		return badRequest("by must be country, device, model, os, browser or version")
	}
	from, to, err := s.analyticsRange(r, analyticsMaxEventsRange)
	if err != nil {
		return err
	}
	q := analyticsAudienceSelect
	selected := func() *query.Select { return q(sc, f, column, from, to) }
	if by == analyticsAudienceModel {
		selected = func() *query.Select { return analyticsAudienceModelSelect(sc, f, from, to) }
	}
	rows, err := sc.Query(r.Context(), selected())
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var key string
		var sessions, users float64
		if err := rows.Scan(&key, &sessions, &users); err != nil {
			return err
		}
		out = append(out, map[string]any{"value": key, "sessions": sessions, "users": users})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"by": by, "rows": out})
	return nil
}

// analyticsEvents serves GET /api/v1/analytics/events.
func (s *Server) analyticsEvents(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	f, err := parseRUMFilter(r, true)
	if err != nil {
		return err
	}
	name, param := strings.TrimSpace(r.URL.Query().Get("name")), strings.TrimSpace(r.URL.Query().Get("param"))
	// One without the other has no answer: a parameter belongs to an event, and an event alone is one row of the list.
	if (name == "") != (param == "") {
		return badRequest("name and param must be given together")
	}
	if len(name) > rum.MaxCustomNameBytes || len(param) > rum.MaxCustomParamKeyBytes {
		return badRequest("name or param is too long")
	}
	from, to, err := s.analyticsRange(r, analyticsMaxEventsRange)
	if err != nil {
		return err
	}
	rows, err := sc.Query(r.Context(), analyticsEventsSelect(sc, f, name, param, from, to))
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var key string
		var count, sessions, users float64
		if err := rows.Scan(&key, &count, &sessions, &users); err != nil {
			return err
		}
		out = append(out, map[string]any{"name": key, "count": count, "sessions": sessions, "users": users})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
	return nil
}

// analyticsAcquisition serves GET /api/v1/analytics/acquisition.
func (s *Server) analyticsAcquisition(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	f, err := parseRUMFilter(r, true)
	if err != nil {
		return err
	}
	by := r.URL.Query().Get("by")
	if by == "" {
		by = "referrer"
	}
	column, ok := analyticsDimensions[by]
	if !ok {
		return badRequest("by must be referrer, source, medium or campaign")
	}
	from, to, err := s.analyticsRange(r, analyticsMaxEventsRange)
	if err != nil {
		return err
	}
	rows, err := sc.Query(r.Context(), analyticsAcquisitionSelect(sc, f, column, from, to))
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var key string
		var sessions, users float64
		if err := rows.Scan(&key, &sessions, &users); err != nil {
			return err
		}
		out = append(out, map[string]any{"value": key, "sessions": sessions, "users": users})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"by": by, "sources": out})
	return nil
}

// analyticsFunnel serves GET /api/v1/analytics/funnel.
func (s *Server) analyticsFunnel(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	f, err := parseRUMFilter(r, true)
	if err != nil {
		return err
	}
	steps, err := parseFunnelSteps(r)
	if err != nil {
		return err
	}
	by := r.URL.Query().Get("by")
	if by == "" {
		by = "session"
	}
	if by != "session" && by != "user" {
		return badRequest("by must be session or user")
	}
	from, to, err := s.analyticsRange(r, analyticsMaxEventsRange)
	if err != nil {
		return err
	}
	def, limit := rumFunnelDefaultWindow, 24*time.Hour
	if by == "user" {
		def, limit = analyticsFunnelUserWindow, analyticsFunnelMaxUserWindow
	}
	window, err := parseBoundedDuration(r, "window", def, time.Minute, limit)
	if err != nil {
		return err
	}
	rows, err := sc.Query(r.Context(), analyticsFunnelSelect(sc, f, steps, by == "user", window, from, to))
	if err != nil {
		return err
	}
	out, err := funnelSteps(rows, steps)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"steps": out, "by": by, "window_seconds": int(window / time.Second)})
	return nil
}
