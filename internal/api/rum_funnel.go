package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/rum"
)

// Funnels (rum.md §2.8): of the sessions that did the first step, how many reached each of the next ones.
//
// The steps are custom event names — what an application already names itself through recordEvent — so a
// funnel needs no new instrumentation, only the events a product is already sending.
//
// **Order matters, and that is why this is not five separate counts.** ClickHouse's windowFunnel returns,
// per session, the furthest step reached *in order and within a window*; counting each step independently
// would report a session that checked out before it ever saw the cart as having completed the funnel. The
// two levels are a sub-select (sessions → level) inside an outer aggregate (level → sessions), which the
// query builder already supports and which keeps the tenant predicate where it belongs: on the innermost
// read of the table.
//
// Like everything else that reads the spans rather than a rollup, this reaches back only as far as the
// **7-day trace retention** (§3.7).

const (
	// rumMinFunnelSteps is two because one step is not a funnel, it is a count.
	rumMinFunnelSteps = 2
	// rumMaxFunnelSteps bounds the query and the chart. Beyond this the answer is a report, not a funnel.
	rumMaxFunnelSteps = 6
	// rumFunnelDefaultWindow is how long a session has to get from the first step to the last.
	rumFunnelDefaultWindow = 30 * time.Minute
)

type rumFunnelStepJSON struct {
	Step string `json:"step"`
	// Sessions reached this step **and every step before it**, in order.
	Sessions uint64 `json:"sessions"`
	// Rate is relative to the first step: 1 for the first, and the share that got this far afterwards.
	Rate float64 `json:"rate"`
}

// rumFunnelSelect counts sessions by the furthest funnel step they reached.
//
// A function of its own so a test can build it without data: the handler reaches this query only after its
// filter parses, and a fragment the query layer refuses would otherwise be found in production.
func rumFunnelSelect(sc *query.Scope, f rumFilter, steps []string, window time.Duration, from, to time.Time) *query.Select {
	conds := make([]string, len(steps))
	inner := sc.From(query.Spans)
	for i := range steps {
		// Each step is its own parameter; the attribute key is one too, because the query layer refuses
		// the token "openlog" anywhere in a fragment and every RUM attribute key starts with it.
		conds[i] = fmt.Sprintf("attributes[{a_custom:String}] = {s%d:String}", i)
		inner.Param(fmt.Sprintf("s%d", i), steps[i])
	}
	inner.Columns(fmt.Sprintf("windowFunnel({w_sec:UInt64})(timestamp, %s) AS f_level", strings.Join(conds, ", "))).
		Param("w_sec", uint64(window/time.Second)).
		Param("a_custom", rum.AttrCustomName).Param("a_session", rum.AttrSessionID).Param("a_event", rum.AttrEvent).
		Where("service_name = {r_app:String}").Param("r_app", f.app).
		// Only RUM spans, and only sessions: a span with no session id cannot belong to a funnel.
		Where("attributes[{a_event:String}] != ''").
		Where("attributes[{a_session:String}] != ''").
		Where("timestamp >= fromUnixTimestamp64Nano({t_from:Int64}) AND timestamp < fromUnixTimestamp64Nano({t_to:Int64})").
		Param("t_from", from.UnixNano()).Param("t_to", to.UnixNano()).
		GroupBy("attributes[{a_session:String}]")
	if f.env != nil {
		inner.Where("deployment_environment = {r_env:String}").Param("r_env", *f.env)
	}
	// Deliberately no limit on the inner query: truncating sessions would change every percentage below
	// in a way nobody reading the chart could see. The window and the application bound it instead.
	return sc.FromSub(inner).Columns("f_level", "count() AS f_sessions").GroupBy("f_level").OrderBy("f_level")
}

// rumFunnel serves GET /api/v1/rum/funnel.
func (s *Server) rumFunnel(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	f, err := parseRUMFilter(r, true)
	if err != nil {
		return err
	}
	steps := r.URL.Query()["step"]
	if len(steps) < rumMinFunnelSteps || len(steps) > rumMaxFunnelSteps {
		return badRequest("between %d and %d step parameters are required", rumMinFunnelSteps, rumMaxFunnelSteps)
	}
	for i, st := range steps {
		steps[i] = strings.TrimSpace(st)
		if steps[i] == "" || len(steps[i]) > rum.MaxCustomNameBytes {
			return badRequest("step %d must be 1-%d bytes", i+1, rum.MaxCustomNameBytes)
		}
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	window, err := parseBoundedDuration(r, "window", rumFunnelDefaultWindow, time.Minute, 24*time.Hour)
	if err != nil {
		return err
	}

	rows, err := sc.Query(r.Context(), rumFunnelSelect(sc, f, steps, window, from, to))
	if err != nil {
		return err
	}
	// windowFunnel answers with the furthest step each session reached, so a session that finished is
	// counted once at the last level and has to be added back into every level before it.
	reached := make([]uint64, len(steps)+1)
	for rows.Next() {
		var level uint8
		var sessions uint64
		if err := rows.Scan(&level, &sessions); err != nil {
			rows.Close()
			return err
		}
		if int(level) < len(reached) {
			reached[level] += sessions
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	out := make([]rumFunnelStepJSON, len(steps))
	var cumulative uint64
	for i := len(steps); i >= 1; i-- {
		cumulative += reached[i]
		out[i-1] = rumFunnelStepJSON{Step: steps[i-1], Sessions: cumulative}
	}
	first := out[0].Sessions
	for i := range out {
		out[i].Rate = 1
		if first > 0 {
			out[i].Rate = float64(out[i].Sessions) / float64(first)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"steps": out, "window_seconds": int(window / time.Second)})
	return nil
}
