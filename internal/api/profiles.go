package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/profiles"
)

// Continuous profiling reads (schema 0095_profiles).
//
// Every endpoint reads profiles_local through the tenant-scoped query layer. The stack was expanded at ingest
// (internal/profiles), so a flame graph is `SELECT stack, sum(value) GROUP BY stack` — one aggregation, no
// joins, and the same shape whether the window is a minute or a day.
//
// A profile type is required wherever values are summed. Nanoseconds and bytes do not add up, and a chart
// that mixed them would be a number with no unit: the services endpoint is what tells a caller which types a
// service actually has.

const (
	// profileMaxStacks bounds the distinct stacks one flame graph returns. Ordered by value, so what falls off
	// the end is the narrowest slivers — the ones a flame graph cannot draw a pixel of anyway.
	profileMaxStacks = 20000
	// profileMaxFunctions bounds the self-time table.
	profileMaxFunctions = 500
)

func (s *Server) profileRoutes(mux *http.ServeMux) {
	route := func(pattern string, h handlerFunc) { mux.Handle(pattern, s.wrap(pattern, h)) }
	route("GET /api/v1/profiles/services", s.profileServices)
	route("GET /api/v1/profiles/flame", s.profileFlame)
	route("GET /api/v1/profiles/functions", s.profileFunctions)
}

// ---- filter ----

// profileFilter selects the samples of one service and profile type, optionally narrowed to an environment
// or a single host.
type profileFilter struct {
	service     string
	profileType string
	env         *string
	host        *string
}

func parseProfileFilter(r *http.Request) (profileFilter, error) {
	q := r.URL.Query()
	f := profileFilter{
		service:     strings.TrimSpace(q.Get("service")),
		profileType: strings.TrimSpace(q.Get("type")),
	}
	if f.service == "" {
		return f, badRequest("service is required")
	}
	if len(f.service) > maxServiceNameBytes {
		return f, badRequest("service must be at most %d bytes", maxServiceNameBytes)
	}
	// Not defaulted to "cpu": a service may profile allocations and not CPU, and guessing would answer a
	// question the caller did not ask with numbers in a unit they did not choose.
	if f.profileType == "" {
		return f, badRequest("type is required; GET /api/v1/profiles/services lists the types a service has")
	}
	if len(f.profileType) > maxServiceNameBytes {
		return f, badRequest("type must be at most %d bytes", maxServiceNameBytes)
	}
	f.env = optionalParam(q, "environment")
	f.host = optionalParam(q, "host")
	return f, nil
}

func (f profileFilter) apply(q *query.Select) *query.Select {
	q = q.Where("service_name = {f_svc:String}").Param("f_svc", f.service).
		Where("profile_type = {f_type:String}").Param("f_type", f.profileType)
	if f.env != nil {
		q = q.Where("deployment_environment = {f_env:String}").Param("f_env", *f.env)
	}
	if f.host != nil {
		q = q.Where("host_id = {f_host:String}").Param("f_host", *f.host)
	}
	return q
}

// profileRange bounds a query to [from, to).
func profileRange(q *query.Select, from, to time.Time) *query.Select {
	return q.Where("timestamp >= fromUnixTimestamp64Nano({t_from:Int64})").Param("t_from", from.UnixNano()).
		Where("timestamp < fromUnixTimestamp64Nano({t_to:Int64})").Param("t_to", to.UnixNano())
}

// ---- responses ----

type profileServiceJSON struct {
	Service     string `json:"service"`
	Environment string `json:"environment"`
	Type        string `json:"type"`
	Unit        string `json:"unit"`
	Samples     uint64 `json:"samples"`
	Total       int64  `json:"total"`
	LastSeen    string `json:"last_seen"`
}

type profileFunctionJSON struct {
	Function string `json:"function"`
	Self     int64  `json:"self"`
	Samples  uint64 `json:"samples"`
}

// profileServices lists what has been profiled in the window: one entry per service, environment and profile
// type, which is also what tells a caller which types the other endpoints will accept.
func (s *Server) profileServices(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	q := profileRange(sc.From(query.Profiles).Columns(
		"service_name", "deployment_environment", "profile_type", "unit",
		"toUInt64(count()) AS p_samples",
		"sum(value) AS p_total",
		"max(timestamp) AS p_last",
	), from, to).GroupBy("service_name", "deployment_environment", "profile_type", "unit").
		OrderBy("p_samples DESC").Limit(s.cfg.MaxRows)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []profileServiceJSON{}
	for rows.Next() {
		var e profileServiceJSON
		var last time.Time
		if err := rows.Scan(&e.Service, &e.Environment, &e.Type, &e.Unit, &e.Samples, &e.Total, &last); err != nil {
			return err
		}
		e.LastSeen = formatTime(last)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": out})
	return nil
}

// profileFlame returns the flame graph tree of one service and profile type over the window.
func (s *Server) profileFlame(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	f, err := parseProfileFilter(r)
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	limit, err := s.apmLimit(r, profileMaxStacks, profileMaxStacks)
	if err != nil {
		return err
	}
	// Folding identical stacks in ClickHouse rather than in Go is the whole reason the stack is a column:
	// a minute of samples is thousands of rows and a few hundred distinct stacks.
	q := profileRange(f.apply(sc.From(query.Profiles).Columns(
		"stack",
		"sum(value) AS p_total",
	)), from, to).GroupBy("stack").OrderBy("p_total DESC").Limit(limit)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	var folded []profiles.Row
	var unit string
	for rows.Next() {
		var stack []string
		var total int64
		if err := rows.Scan(&stack, &total); err != nil {
			return err
		}
		folded = append(folded, profiles.Row{Stack: stack, Value: total})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	unit, err = s.profileUnit(r, sc, f, from, to)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"unit":  unit,
		"type":  f.profileType,
		"flame": profiles.Flame(folded),
	})
	return nil
}

// profileFunctions ranks functions by self time: the value attributed to the innermost frame. This is the
// first question asked of a profile, which is why `leaf` is a stored column rather than arrayElement(stack, -1).
func (s *Server) profileFunctions(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	f, err := parseProfileFilter(r)
	if err != nil {
		return err
	}
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	limit, err := s.apmLimit(r, 50, profileMaxFunctions)
	if err != nil {
		return err
	}
	q := profileRange(f.apply(sc.From(query.Profiles).Columns(
		"leaf",
		"sum(value) AS p_self",
		"toUInt64(count()) AS p_samples",
	)), from, to).GroupBy("leaf").OrderBy("p_self DESC").Limit(limit)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []profileFunctionJSON{}
	var total int64
	for rows.Next() {
		var e profileFunctionJSON
		if err := rows.Scan(&e.Function, &e.Self, &e.Samples); err != nil {
			return err
		}
		total += e.Self
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	unit, err := s.profileUnit(r, sc, f, from, to)
	if err != nil {
		return err
	}
	// The total of the returned rows, not of the window: a percentage must be of something the caller can see.
	writeJSON(w, http.StatusOK, map[string]any{"unit": unit, "type": f.profileType, "total": total, "functions": out})
	return nil
}

// profileUnit reads the unit the profile's own ValueType declared, so a chart can say what its numbers mean
// instead of the UI hard-coding "ns" for everything.
func (s *Server) profileUnit(r *http.Request, sc *query.Scope, f profileFilter, from, to time.Time) (string, error) {
	q := profileRange(f.apply(sc.From(query.Profiles).Columns("any(unit) AS p_unit")), from, to).Limit(1)
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var unit string
	for rows.Next() {
		if err := rows.Scan(&unit); err != nil {
			return "", err
		}
	}
	return unit, rows.Err()
}
