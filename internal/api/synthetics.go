package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/synthetics"
)

// Synthetic monitoring (docs/contracts/api.md "Synthetic monitoring", D-132). Definitions live in PostgreSQL
// with the last outcome per location; the runs live in ClickHouse and are read through the tenant-scoped
// query layer, like every other telemetry read.
//
// Reads are telemetry reads (any role, API keys too); writes need a signed-in member or higher and are
// audited by the store (synthetic_check.create/update/delete).

// syntheticsSparklinePoints is how many buckets the list's uptime/latency sparkline has.
const syntheticsSparklinePoints = 24

// syntheticsListWindow is the range the list summarizes when the request names none.
const syntheticsListWindow = 24 * time.Hour

// defaultSyntheticFailures is how many recent failures a results response carries by default.
const defaultSyntheticFailures = 20

// SetSynthetics enables the synthetic monitoring endpoints (postgres auth mode). Must be called before Run.
func (s *Server) SetSynthetics(store synthetics.Store) {
	s.synthetics = store
	s.srv.Handler = s.Handler()
}

func (s *Server) syntheticsRoutes(mux *http.ServeMux) {
	if s.synthetics == nil {
		return
	}
	route := func(pattern string, h handlerFunc) { mux.Handle(pattern, s.wrap(pattern, h)) }
	route("GET /api/v1/synthetics/checks", s.listSyntheticChecks)
	route("GET /api/v1/synthetics/checks/{id}", s.getSyntheticCheck)
	route("GET /api/v1/synthetics/checks/{id}/results", s.syntheticCheckResults)
	if s.accounts != nil {
		s.syntheticsWriteRoute(mux, "POST /api/v1/synthetics/checks", s.createSyntheticCheck)
		s.syntheticsWriteRoute(mux, "PUT /api/v1/synthetics/checks/{id}", s.updateSyntheticCheck)
		s.syntheticsWriteRoute(mux, "DELETE /api/v1/synthetics/checks/{id}", s.deleteSyntheticCheck)
	}
}

type syntheticsWriteHandler func(w http.ResponseWriter, r *http.Request, p *auth.Principal) error

// syntheticsWriteRoute registers a mutating endpoint: wrap authenticates (CSRF included), the handler
// additionally needs a principal whose role may write checks (a member, or an API key with that role).
func (s *Server) syntheticsWriteRoute(mux *http.ServeMux, pattern string, h syntheticsWriteHandler) {
	mux.Handle(pattern, s.wrap(pattern, func(w http.ResponseWriter, r *http.Request, _ *query.Scope) error {
		noStore(w)
		p, _ := auth.PrincipalFrom(r.Context())
		if ae := authorize(p, auth.ActWriteSynthetics); ae != nil {
			return ae
		}
		return syntheticsError(h(w, r, p))
	}))
}

// syntheticsError maps store and validation errors to the API error shape.
func syntheticsError(err error) error {
	var ve *synthetics.ValidationError
	switch {
	case errors.As(err, &ve):
		return &apiError{http.StatusBadRequest, "invalid_argument", ve.Error()}
	case errors.Is(err, synthetics.ErrNotFound):
		return notFound("synthetic check not found")
	case errors.Is(err, synthetics.ErrLimit):
		return &apiError{http.StatusConflict, "failed_precondition",
			fmt.Sprintf("the organization already has the maximum number of synthetic checks (%d); delete some first", synthetics.MaxPerOrg)}
	}
	return err
}

// ---- response shapes ----

type syntheticCheckJSON struct {
	ID              string                  `json:"id"`
	Name            string                  `json:"name"`
	Type            string                  `json:"type"`
	Enabled         bool                    `json:"enabled"`
	URL             string                  `json:"url"`
	Method          string                  `json:"method"`
	Headers         map[string]string       `json:"headers"`
	Body            string                  `json:"body"`
	ExpectedStatus  []int                   `json:"expected_status"`
	AssertionType   string                  `json:"assertion_type"`
	AssertionPath   string                  `json:"assertion_path"`
	AssertionValue  string                  `json:"assertion_value"`
	TimeoutMs       int                     `json:"timeout_ms"`
	IntervalSeconds int                     `json:"interval_seconds"`
	Locations       []string                `json:"locations"`
	Target          string                  `json:"target"`
	DNSRecordType   string                  `json:"dns_record_type"`
	DNSExpected     []string                `json:"dns_expected"`
	TLSWarningDays  int                     `json:"tls_warning_days"`
	CreatedByEmail  string                  `json:"created_by_email"`
	UpdatedByEmail  string                  `json:"updated_by_email"`
	CreatedAt       string                  `json:"created_at"`
	UpdatedAt       string                  `json:"updated_at"`
	Status          []syntheticLocationJSON `json:"status"`
}

// syntheticLocationJSON is the schedule row of one location: the last run and when the next one is due.
type syntheticLocationJSON struct {
	Location       string  `json:"location"`
	NextRunAt      string  `json:"next_run_at"`
	LastRunAt      *string `json:"last_run_at"`
	LastSuccess    *bool   `json:"last_success"`
	LastStatusCode int     `json:"last_status_code"`
	LastDurationMs float64 `json:"last_duration_ms"`
	LastErrorKind  string  `json:"last_error_kind"`
	LastError      string  `json:"last_error"`
}

func syntheticCheckResponse(c *synthetics.Check) syntheticCheckJSON {
	out := syntheticCheckJSON{ID: c.ID, Name: c.Name, Type: c.Type, Enabled: c.Enabled, URL: c.URL,
		Method: c.Method, Headers: nonNilMap(c.Headers), Body: c.Body, ExpectedStatus: c.ExpectedStatus,
		AssertionType: c.AssertionType, AssertionPath: c.AssertionPath, AssertionValue: c.AssertionValue,
		TimeoutMs: c.TimeoutMs, IntervalSeconds: c.IntervalSeconds, Locations: c.Locations,
		Target: c.Target, DNSRecordType: c.DNSRecordType, DNSExpected: c.DNSExpected,
		TLSWarningDays: c.TLSWarningDays, CreatedByEmail: c.CreatedByEmail, UpdatedByEmail: c.UpdatedByEmail,
		CreatedAt: formatTime(c.CreatedAt), UpdatedAt: formatTime(c.UpdatedAt),
		Status: make([]syntheticLocationJSON, 0, len(c.Status))}
	if out.ExpectedStatus == nil {
		out.ExpectedStatus = []int{}
	}
	if out.Locations == nil {
		out.Locations = []string{}
	}
	if out.DNSExpected == nil {
		out.DNSExpected = []string{}
	}
	for _, st := range c.Status {
		out.Status = append(out.Status, syntheticLocationJSON{Location: st.Location,
			NextRunAt: formatTime(st.NextRunAt), LastRunAt: optTime(st.LastRunAt), LastSuccess: st.LastSuccess,
			LastStatusCode: st.LastStatusCode, LastDurationMs: st.LastDurationMs,
			LastErrorKind: st.LastErrorKind, LastError: st.LastError})
	}
	return out
}

// syntheticSummaryJSON is the uptime and latency over a range (the list's sparkline, the detail's chart).
type syntheticSummaryJSON struct {
	From     string               `json:"from"`
	To       string               `json:"to"`
	Step     string               `json:"step"`
	Runs     uint64               `json:"runs"`
	Failures uint64               `json:"failures"`
	Uptime   *float64             `json:"uptime"`
	AvgMs    *float64             `json:"avg_ms"`
	P50Ms    *float64             `json:"p50_ms"`
	P95Ms    *float64             `json:"p95_ms"`
	P99Ms    *float64             `json:"p99_ms"`
	Points   []syntheticPointJSON `json:"points"`
}

type syntheticPointJSON struct {
	T        int64    `json:"t"`
	Runs     uint64   `json:"runs"`
	Failures uint64   `json:"failures"`
	Uptime   *float64 `json:"uptime"`
	P95Ms    *float64 `json:"p95_ms"`
}

type syntheticFailureJSON struct {
	Timestamp  string  `json:"timestamp"`
	Location   string  `json:"location"`
	StatusCode int     `json:"status_code"`
	ErrorKind  string  `json:"error_kind"`
	Error      string  `json:"error"`
	DurationMs float64 `json:"duration_ms"`
}

func syntheticSummaryResponse(s synthetics.Summary, from, to time.Time, step time.Duration) syntheticSummaryJSON {
	out := syntheticSummaryJSON{From: formatTime(from), To: formatTime(to), Step: formatStep(step),
		Runs: s.Runs, Failures: s.Failures, Uptime: optFloat(s.Uptime), AvgMs: optFloat(s.AvgMs),
		P50Ms: optFloat(s.P50Ms), P95Ms: optFloat(s.P95Ms), P99Ms: optFloat(s.P99Ms),
		Points: make([]syntheticPointJSON, 0, len(s.Points))}
	for _, p := range s.Points {
		out.Points = append(out.Points, syntheticPointJSON{T: p.T.UnixMilli(), Runs: p.Runs,
			Failures: p.Failures, Uptime: optFloat(p.Uptime), P95Ms: optFloat(p.P95Ms)})
	}
	return out
}

type syntheticListItemJSON struct {
	syntheticCheckJSON
	Summary *syntheticSummaryJSON `json:"summary"`
}

// ---- CRUD ----

// syntheticsRange is the window the list summarizes: from/to when given, else the last 24 hours (a range
// short enough to be cheap and long enough to mean something as an uptime figure).
func (s *Server) syntheticsRange(r *http.Request) (time.Time, time.Time, error) {
	q := r.URL.Query()
	if q.Get("from") == "" && q.Get("to") == "" {
		now := s.now().UTC()
		return now.Add(-syntheticsListWindow), now, nil
	}
	return s.timeRange(r)
}

// syntheticsStep spreads the range over n buckets, rounded up to whole minutes.
func syntheticsStep(from, to time.Time, n int) time.Duration {
	step := max(to.Sub(from)/time.Duration(n), time.Minute)
	if rem := step % time.Minute; rem != 0 {
		step += time.Minute - rem
	}
	return step
}

func (s *Server) listSyntheticChecks(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	p, _ := auth.PrincipalFrom(r.Context())
	list, err := s.synthetics.List(r.Context(), p.OrgID)
	if err != nil {
		return syntheticsError(err)
	}
	summary := true
	if v := r.URL.Query().Get("summary"); v != "" {
		if summary, err = strconv.ParseBool(v); err != nil {
			return badRequest("summary must be true or false")
		}
	}
	from, to, err := s.syntheticsRange(r)
	if err != nil {
		return err
	}
	step := syntheticsStep(from, to, syntheticsSparklinePoints)
	// One query covers every check of the organization, so the list costs the same with 1 or 100 checks.
	var sums map[string]synthetics.Summary
	if summary && len(list) > 0 {
		if sums, err = synthetics.Summaries(r.Context(), sc, "", from, to, step); err != nil {
			return syntheticsError(err)
		}
	}
	out := make([]syntheticListItemJSON, 0, len(list))
	for i := range list {
		item := syntheticListItemJSON{syntheticCheckJSON: syntheticCheckResponse(&list[i])}
		if sum, ok := sums[list[i].ID]; ok {
			res := syntheticSummaryResponse(sum, from, to, step)
			item.Summary = &res
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": out, "locations": synthetics.Locations})
	return nil
}

func (s *Server) syntheticCheckByID(r *http.Request) (*synthetics.Check, error) {
	p, _ := auth.PrincipalFrom(r.Context())
	c, err := s.synthetics.Get(r.Context(), p.OrgID, r.PathValue("id"))
	if err != nil {
		return nil, syntheticsError(err)
	}
	return c, nil
}

func (s *Server) getSyntheticCheck(w http.ResponseWriter, r *http.Request, _ *query.Scope) error {
	noStore(w)
	c, err := s.syntheticCheckByID(r)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, syntheticCheckResponse(c))
	return nil
}

// syntheticInputJSON decodes an input; enabled is a pointer so that omitting it means "enabled" instead of
// silently creating a check that never runs.
type syntheticInputJSON struct {
	synthetics.Input
	Enabled *bool `json:"enabled"`
}

func decodeSyntheticInput(r *http.Request) (synthetics.Input, error) {
	var in syntheticInputJSON
	if err := decodeJSON(r, &in); err != nil {
		return synthetics.Input{}, err
	}
	out := in.Input
	out.Enabled = in.Enabled == nil || *in.Enabled
	return out, out.Validate()
}

func (s *Server) syntheticsActor(r *http.Request, p *auth.Principal) synthetics.Actor {
	return synthetics.Actor{UserID: p.UserID, Email: p.Email, IP: s.accounts.Meta(r).IP,
		APIKeyID: p.APIKeyID, APIKeyName: p.APIKeyName}
}

func (s *Server) createSyntheticCheck(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	in, err := decodeSyntheticInput(r)
	if err != nil {
		return err
	}
	c, err := s.synthetics.Create(r.Context(), p.OrgID, in, s.syntheticsActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, syntheticCheckResponse(c))
	return nil
}

func (s *Server) updateSyntheticCheck(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	in, err := decodeSyntheticInput(r)
	if err != nil {
		return err
	}
	c, err := s.synthetics.Update(r.Context(), p.OrgID, r.PathValue("id"), in, s.syntheticsActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, syntheticCheckResponse(c))
	return nil
}

func (s *Server) deleteSyntheticCheck(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := s.synthetics.Delete(r.Context(), p.OrgID, r.PathValue("id"), s.syntheticsActor(r, p)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// ---- results ----

// syntheticCheckResults serves GET /synthetics/checks/{id}/results: uptime and latency percentiles over the
// range, the series behind them and the most recent failures.
func (s *Server) syntheticCheckResults(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	c, err := s.syntheticCheckByID(r)
	if err != nil {
		return err
	}
	from, to, err := s.syntheticsRange(r)
	if err != nil {
		return err
	}
	step, err := apmStep(r, from, to)
	if err != nil {
		return err
	}
	limit := defaultSyntheticFailures
	if v := r.URL.Query().Get("failures"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return badRequest("failures must be a non-negative integer")
		}
		limit = min(n, synthetics.MaxFailures)
	}
	sums, err := synthetics.Summaries(r.Context(), sc, c.ID, from, to, step)
	if err != nil {
		return syntheticsError(err)
	}
	summary := syntheticSummaryResponse(sums[c.ID], from, to, step)
	failures := []syntheticFailureJSON{}
	if limit > 0 {
		list, err := synthetics.Failures(r.Context(), sc, c.ID, from, to, limit)
		if err != nil {
			return syntheticsError(err)
		}
		for _, f := range list {
			failures = append(failures, syntheticFailureJSON{Timestamp: formatTime(f.At), Location: f.Location,
				StatusCode: f.StatusCode, ErrorKind: f.ErrorKind, Error: f.Error, DurationMs: f.DurationMs})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"check": syntheticCheckResponse(c), "summary": summary,
		"failures": failures})
	return nil
}
