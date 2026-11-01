package api

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
)

// A period against the one before it (analytics.md §6.1). Both are whole days ending with yesterday, in UTC: today has only just
// started in the morning, so counting it would show every number falling, every morning.

const (
	analyticsDefaultCompareDays = 28
	analyticsMaxCompareDays     = 90
)

// analyticsPeriod is one window of complete days. to is exclusive and last the final day in it.
type analyticsPeriod struct {
	from, to, last time.Time
}

func analyticsPeriods(now time.Time, days int) (current, previous analyticsPeriod) {
	today := now.UTC().Truncate(24 * time.Hour)
	span := time.Duration(days) * 24 * time.Hour
	current = analyticsPeriod{today.Add(-span), today, today.Add(-24 * time.Hour)}
	previous = analyticsPeriod{current.from.Add(-span), current.from, current.from.Add(-24 * time.Hour)}
	return current, previous
}

func parseCompareDays(r *http.Request) (int, error) {
	v := r.URL.Query().Get("days")
	if v == "" {
		return analyticsDefaultCompareDays, nil
	}
	days, err := strconv.Atoi(v)
	if err != nil || days < 1 || days > analyticsMaxCompareDays {
		return 0, badRequest("days must be between 1 and %d", analyticsMaxCompareDays)
	}
	return days, nil
}

// analyticsMetric is one number and how far it moved. previous is null where that period can't be known, and change_rate also where it
// was zero, because a rise from nothing has no percentage: a screen should say "new", not "+∞%".
type analyticsMetric struct {
	Current    float64  `json:"current"`
	Previous   *float64 `json:"previous"`
	Change     *float64 `json:"change"`
	ChangeRate *float64 `json:"change_rate"`
}

func compareMetric(current float64, previous *float64) analyticsMetric {
	m := analyticsMetric{Current: current, Previous: previous}
	if previous == nil {
		return m
	}
	change := current - *previous
	m.Change = &change
	if *previous != 0 {
		rate := change / *previous
		m.ChangeRate = &rate
	}
	return m
}

func analyticsTotalsSelect(sc *query.Scope, f rumFilter, p analyticsPeriod) *query.Select {
	sessions := analyticsEventRange(sc.From(query.AnalyticsEvents), f, p.from, p.to).
		Columns("session_id", "max(weight) AS w", "sumIf(weight, kind = 'page_view') AS c_views", "sumIf(weight, kind = 'custom') AS c_events").
		GroupBy("session_id")
	return sc.FromSub(sessions).Columns("sum(w) AS v_sessions", "sum(c_views) AS v_views", "sum(c_events) AS v_events")
}

// analyticsPeopleSelect counts the distinct people of a period, and how many a day on average. The table is aggregating and sharded, so
// an actor can be on several rows, which is why these are distinct counts and not sums.
func analyticsPeopleSelect(sc *query.Scope, f rumFilter, p analyticsPeriod) *query.Select {
	return applyActorDays(sc.From(query.AnalyticsActorDays), f).
		Columns("uniqCombined64(actor_hash) * if(count() = 0, 0, avg(weight)) AS v_users",
			"uniqCombined64(tuple(day, actor_hash)) * if(count() = 0, 0, avg(weight)) AS v_actor_days").
		Where("day >= toDate({d_from:String}) AND day <= toDate({d_to:String})").
		Param("d_from", analyticsDate(p.from)).Param("d_to", analyticsDate(p.last))
}

type analyticsNumbers struct {
	users, avgDailyUsers, newUsers, dau, wau, mau, stickiness float64
	sessions, pageViews, events                               float64
	known                                                     bool
}

func (s *Server) analyticsPeriodNumbers(r *http.Request, sc *query.Scope, f rumFilter, p analyticsPeriod, days int, withEvents bool) (analyticsNumbers, error) {
	n := analyticsNumbers{known: withEvents}
	ctx := r.Context()
	if withEvents {
		rows, err := sc.Query(ctx, analyticsTotalsSelect(sc, f, p))
		if err != nil {
			return n, err
		}
		for rows.Next() {
			if err := rows.Scan(&n.sessions, &n.pageViews, &n.events); err != nil {
				rows.Close()
				return n, err
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return n, err
		}
	}
	rows, err := sc.Query(ctx, analyticsPeopleSelect(sc, f, p))
	if err != nil {
		return n, err
	}
	for rows.Next() {
		var actorDays float64
		if err := rows.Scan(&n.users, &actorDays); err != nil {
			rows.Close()
			return n, err
		}
		n.avgDailyUsers = actorDays / float64(days)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return n, err
	}
	rows, err = sc.Query(ctx, analyticsNewActorsSelect(sc, f, p.from, p.last))
	if err != nil {
		return n, err
	}
	for rows.Next() {
		var d time.Time
		var fresh float64
		if err := rows.Scan(&d, &fresh); err != nil {
			rows.Close()
			return n, err
		}
		n.newUsers += fresh
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return n, err
	}
	// Each as it stood on the period's last day, which is what the same number on the chart says.
	rows, err = sc.Query(ctx, analyticsActivitySelect(sc, f, p.last, p.last))
	if err != nil {
		return n, err
	}
	defer rows.Close()
	for rows.Next() {
		var d time.Time
		if err := rows.Scan(&d, &n.dau, &n.wau, &n.mau); err != nil {
			return n, err
		}
		if math.IsNaN(n.dau) {
			n.dau = 0
		}
		if math.IsNaN(n.wau) {
			n.wau = 0
		}
		if n.mau > 0 {
			n.stickiness = n.dau / n.mau
		}
	}
	return n, rows.Err()
}

// analyticsFirstDay is when this application first has people on record, zero if never: a previous period from before then would compare
// a real period against a half-empty one, and the rise would be nothing but the SDK having been installed.
func analyticsFirstDay(r *http.Request, sc *query.Scope, f rumFilter) (time.Time, error) {
	rows, err := sc.Query(r.Context(), applyActorDays(sc.From(query.AnalyticsActorDays), f).Columns("min(day) AS v_first, count() AS v_n"))
	if err != nil {
		return time.Time{}, err
	}
	defer rows.Close()
	var first time.Time
	var n uint64
	for rows.Next() {
		if err := rows.Scan(&first, &n); err != nil {
			return time.Time{}, err
		}
	}
	if n == 0 {
		return time.Time{}, rows.Err()
	}
	return first, rows.Err()
}

// analyticsCompare serves GET /api/v1/analytics/compare.
func (s *Server) analyticsCompare(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	f, err := parseRUMFilter(r, true)
	if err != nil {
		return err
	}
	days, err := parseCompareDays(r)
	if err != nil {
		return err
	}
	current, previous := analyticsPeriods(s.now(), days)
	now, err := s.analyticsPeriodNumbers(r, sc, f, current, days, true)
	if err != nil {
		return err
	}
	// analytics_events is kept for 90 days, so a previous period reaching past that would read as a fall that never happened.
	eventsKnown := s.now().Sub(previous.from) <= analyticsMaxEventsRange
	before, err := s.analyticsPeriodNumbers(r, sc, f, previous, days, eventsKnown)
	if err != nil {
		return err
	}
	first, err := analyticsFirstDay(r, sc, f)
	if err != nil {
		return err
	}
	ptr := func(v float64) *float64 { return &v }
	event := func(v float64) *float64 {
		if !eventsKnown {
			return nil
		}
		return &v
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"days":             days,
		"current":          map[string]string{"from": analyticsDate(current.from), "to": analyticsDate(current.last)},
		"previous":         map[string]string{"from": analyticsDate(previous.from), "to": analyticsDate(previous.last)},
		"previous_partial": first.IsZero() || first.After(previous.from),
		"collecting":       !first.IsZero(),
		"metrics": map[string]analyticsMetric{
			"users":           compareMetric(now.users, ptr(before.users)),
			"new_users":       compareMetric(now.newUsers, ptr(before.newUsers)),
			"avg_daily_users": compareMetric(now.avgDailyUsers, ptr(before.avgDailyUsers)),
			"dau":             compareMetric(now.dau, ptr(before.dau)),
			"wau":             compareMetric(now.wau, ptr(before.wau)),
			"mau":             compareMetric(now.mau, ptr(before.mau)),
			"stickiness":      compareMetric(now.stickiness, ptr(before.stickiness)),
			"sessions":        compareMetric(now.sessions, event(before.sessions)),
			"page_views":      compareMetric(now.pageViews, event(before.pageViews)),
			"events":          compareMetric(now.events, event(before.events)),
		},
	})
	return nil
}

// analyticsWindow reads the range of a list. With days it is that period, and second says it is to be compared with the one before it.
func (s *Server) analyticsWindow(r *http.Request, limit time.Duration) (from, to time.Time, previous *analyticsPeriod, err error) {
	if r.URL.Query().Get("days") == "" {
		from, to, err = s.analyticsRange(r, limit)
		return from, to, nil, err
	}
	days, err := parseCompareDays(r)
	if err != nil {
		return from, to, nil, err
	}
	current, before := analyticsPeriods(s.now(), days)
	if s.now().Sub(before.from) <= limit {
		previous = &before
	}
	return current.from, current.to, previous, nil
}

// compareRows puts a list's previous numbers on its rows. A row that only the previous period has is added with zeroes, or whatever
// stopped altogether, the largest fall there is, would simply not be on the screen.
func compareRows(rows []map[string]any, key string, numbers []string, before []map[string]any) []map[string]any {
	byKey := make(map[string]map[string]any, len(rows))
	for _, row := range rows {
		byKey[fmt.Sprint(row[key])] = row
		for _, n := range numbers {
			row["previous_"+n] = float64(0)
		}
	}
	for _, old := range before {
		row, ok := byKey[fmt.Sprint(old[key])]
		if !ok {
			row = map[string]any{key: old[key]}
			for _, n := range numbers {
				row[n] = float64(0)
			}
			rows = append(rows, row)
		}
		for _, n := range numbers {
			row["previous_"+n] = old[n]
		}
	}
	return rows
}
