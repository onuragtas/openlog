package api

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/jobs"
)

// Cron and heartbeat monitoring (docs/contracts/api.md "Job monitoring", D-141). The definitions and the
// current state live in PostgreSQL; the concluded runs live in ClickHouse and are read through the
// tenant-scoped query layer, like every other telemetry read.
//
// Two very different doors lead here. The management endpoints are ordinary API endpoints (session or API
// key, member or higher to write). The ping endpoint is not authenticated at all: it is called by a shell
// line in a crontab on a machine that has no openlog credentials, and the token in the URL is what
// identifies the monitor. What that token authorizes is one monitor's own status.

// jobsListWindow is the range the list summarizes when the request names none.
const jobsListWindow = 7 * 24 * time.Hour

// defaultJobRuns is how many runs a history response carries by default.
const defaultJobRuns = 50

// maxPingBody bounds the output a ping may attach.
const maxPingBody = jobs.MaxMessageBytes

// SetJobs enables the job monitoring endpoints (postgres auth mode). runs may be nil, in which case the
// current state is still kept but no history is written. Must be called before Run.
func (s *Server) SetJobs(store jobs.Store, pings jobs.PingStore, runs jobs.RunSink) {
	s.jobs, s.jobPings, s.jobRuns = store, pings, runs
	s.srv.Handler = s.Handler()
}

func (s *Server) jobRoutes(mux *http.ServeMux) {
	if s.jobs == nil {
		return
	}
	route := func(pattern string, h handlerFunc) { mux.Handle(pattern, s.wrap(pattern, h)) }
	route("GET /api/v1/jobs/monitors", s.listJobMonitors)
	route("GET /api/v1/jobs/monitors/{id}", s.getJobMonitor)
	route("GET /api/v1/jobs/monitors/{id}/runs", s.jobMonitorRuns)
	if s.accounts != nil {
		s.jobWriteRoute(mux, "POST /api/v1/jobs/monitors", s.createJobMonitor)
		s.jobWriteRoute(mux, "PUT /api/v1/jobs/monitors/{id}", s.updateJobMonitor)
		s.jobWriteRoute(mux, "DELETE /api/v1/jobs/monitors/{id}", s.deleteJobMonitor)
		s.jobWriteRoute(mux, "POST /api/v1/jobs/monitors/{id}/rotate", s.rotateJobMonitorToken)
	}
	if s.jobPings == nil {
		return
	}
	// The ping endpoint: no session, no API key, no CSRF — a crontab line has none of them. GET is the
	// method `curl` uses by default and the one a `wget -qO-` sends, so both are accepted; POST carries the
	// job's output.
	for _, pattern := range []string{
		"GET /api/v1/jobs/ping/{token}", "POST /api/v1/jobs/ping/{token}", "HEAD /api/v1/jobs/ping/{token}",
		"GET /api/v1/jobs/ping/{token}/{event}", "POST /api/v1/jobs/ping/{token}/{event}", "HEAD /api/v1/jobs/ping/{token}/{event}",
	} {
		mux.Handle(pattern, s.instrument(pattern, s.jobPing))
	}
}

type jobWriteHandler func(w http.ResponseWriter, r *http.Request, p *auth.Principal) error

// jobWriteRoute registers a mutating endpoint: wrap authenticates (CSRF included), the handler additionally
// needs a principal whose role may write monitors.
func (s *Server) jobWriteRoute(mux *http.ServeMux, pattern string, h jobWriteHandler) {
	mux.Handle(pattern, s.wrap(pattern, func(w http.ResponseWriter, r *http.Request, _ *query.Scope) error {
		noStore(w)
		p, _ := auth.PrincipalFrom(r.Context())
		if ae := authorize(p, auth.ActWriteSynthetics); ae != nil {
			return ae
		}
		return jobsError(h(w, r, p))
	}))
}

// jobsError maps store and validation errors to the API error shape.
func jobsError(err error) error {
	var ve *jobs.ValidationError
	switch {
	case errors.As(err, &ve):
		return &apiError{http.StatusBadRequest, "invalid_argument", ve.Error()}
	case errors.Is(err, jobs.ErrNotFound):
		return notFound("job monitor not found")
	case errors.Is(err, jobs.ErrLimit):
		return &apiError{http.StatusConflict, "failed_precondition",
			fmt.Sprintf("the organization already has the maximum number of job monitors (%d); delete some first", jobs.MaxPerOrg)}
	}
	return err
}

// ---- response shapes ----

type jobMonitorJSON struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	Kind            string          `json:"kind"`
	Cron            string          `json:"cron"`
	TimeZone        string          `json:"time_zone"`
	IntervalSeconds int             `json:"interval_seconds"`
	GraceSeconds    int             `json:"grace_seconds"`
	Enabled         bool            `json:"enabled"`
	Tags            []string        `json:"tags"`
	PingURL         string          `json:"ping_url"`
	CreatedByEmail  string          `json:"created_by_email"`
	UpdatedByEmail  string          `json:"updated_by_email"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       string          `json:"updated_at"`
	State           jobStateJSON    `json:"state"`
	Summary         *jobSummaryJSON `json:"summary,omitempty"`
}

type jobStateJSON struct {
	Status              string  `json:"status"`
	LastPingAt          *string `json:"last_ping_at"`
	LastStartedAt       *string `json:"last_started_at"`
	LastFinishedAt      *string `json:"last_finished_at"`
	LastDurationMs      float64 `json:"last_duration_ms"`
	LastExitCode        int     `json:"last_exit_code"`
	LastMessage         string  `json:"last_message"`
	ExpectedAt          string  `json:"expected_at"`
	ConsecutiveFailures int     `json:"consecutive_failures"`
	// Late is the state the list colours the row by: the monitor is past its expected time plus grace and
	// the sweeper has not concluded it yet (or the installation's leader is down).
	Late bool `json:"late"`
}

type jobSummaryJSON struct {
	Runs     uint64   `json:"runs"`
	Failures uint64   `json:"failures"`
	Missed   uint64   `json:"missed"`
	AvgMs    *float64 `json:"avg_ms"`
	MaxMs    *float64 `json:"max_ms"`
	LastAt   *string  `json:"last_at"`
}

type jobRunJSON struct {
	Timestamp   string  `json:"timestamp"`
	Status      string  `json:"status"`
	StartedAt   *string `json:"started_at"`
	DurationMs  float64 `json:"duration_ms"`
	ExitCode    int     `json:"exit_code"`
	LateSeconds float64 `json:"late_seconds"`
	Message     string  `json:"message"`
	Source      string  `json:"source"`
}

// pingURL is the URL a job calls. It is built from the request so that an installation behind a proxy shows
// the address its users actually reach.
func (s *Server) pingURL(r *http.Request, token string) string {
	scheme := "https"
	if r.TLS == nil && !strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "http"
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	return fmt.Sprintf("%s://%s/api/v1/jobs/ping/%s", scheme, host, token)
}

func (s *Server) jobMonitorResponse(r *http.Request, m jobs.Monitor, now time.Time) jobMonitorJSON {
	tags := m.Tags
	if tags == nil {
		tags = []string{}
	}
	return jobMonitorJSON{ID: m.ID, Name: m.Name, Description: m.Description, Kind: m.Kind, Cron: m.Cron,
		TimeZone: m.TimeZone, IntervalSeconds: m.IntervalSeconds, GraceSeconds: m.GraceSeconds,
		Enabled: m.Enabled, Tags: tags, PingURL: s.pingURL(r, m.Token),
		CreatedByEmail: m.CreatedByEmail, UpdatedByEmail: m.UpdatedByEmail,
		CreatedAt: formatTime(m.CreatedAt), UpdatedAt: formatTime(m.UpdatedAt),
		State: jobStateJSON{Status: m.State.Status, LastPingAt: optTime(m.State.LastPingAt),
			LastStartedAt: optTime(m.State.LastStartedAt), LastFinishedAt: optTime(m.State.LastFinishedAt),
			LastDurationMs: m.State.LastDurationMs, LastExitCode: m.State.LastExitCode,
			LastMessage: m.State.LastMessage, ExpectedAt: formatTime(m.State.ExpectedAt),
			ConsecutiveFailures: m.State.ConsecutiveFailures, Late: m.Late(now)},
	}
}

func jobSummaryResponse(s jobs.Summary) *jobSummaryJSON {
	out := &jobSummaryJSON{Runs: s.Runs, Failures: s.Failures, Missed: s.Missed, AvgMs: s.AvgMs, MaxMs: s.MaxMs}
	out.LastAt = optTime(s.LastAt)
	return out
}

// ---- management endpoints ----

func (s *Server) listJobMonitors(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	p, _ := auth.PrincipalFrom(r.Context())
	monitors, err := s.jobs.List(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	out := make([]jobMonitorJSON, 0, len(monitors))
	// The summary is one grouped query over the range for every monitor at once, as in the synthetics list.
	var summaries map[string]jobs.Summary
	if r.URL.Query().Get("summary") != "false" && len(monitors) > 0 {
		summaries, err = jobs.Summaries(r.Context(), sc, "", now.Add(-jobsListWindow), now)
		if err != nil {
			return err
		}
	}
	for _, m := range monitors {
		item := s.jobMonitorResponse(r, m, now)
		if sum, ok := summaries[m.ID]; ok {
			item.Summary = jobSummaryResponse(sum)
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"monitors": out})
	return nil
}

func (s *Server) getJobMonitor(w http.ResponseWriter, r *http.Request, _ *query.Scope) error {
	noStore(w)
	p, _ := auth.PrincipalFrom(r.Context())
	m, err := s.jobs.Get(r.Context(), p.OrgID, r.PathValue("id"))
	if err != nil {
		return jobsError(err)
	}
	writeJSON(w, http.StatusOK, s.jobMonitorResponse(r, *m, s.now().UTC()))
	return nil
}

func (s *Server) jobMonitorRuns(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	p, _ := auth.PrincipalFrom(r.Context())
	m, err := s.jobs.Get(r.Context(), p.OrgID, r.PathValue("id"))
	if err != nil {
		return jobsError(err)
	}
	now := s.now().UTC()
	from, to := now.Add(-jobsListWindow), now
	if v := r.URL.Query().Get("from"); v != "" {
		if from, err = parseTime(v); err != nil {
			return &apiError{http.StatusBadRequest, "invalid_argument", "from: " + err.Error()}
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if to, err = parseTime(v); err != nil {
			return &apiError{http.StatusBadRequest, "invalid_argument", "to: " + err.Error()}
		}
	}
	if !to.After(from) {
		return &apiError{http.StatusBadRequest, "invalid_argument", "to must be after from"}
	}
	if to.Sub(from) > jobs.HistoryMaxRange {
		return &apiError{http.StatusBadRequest, "invalid_argument", "the range is longer than the 90 days of run history"}
	}
	limit := defaultJobRuns
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return &apiError{http.StatusBadRequest, "invalid_argument", "limit must be a positive number"}
		}
		limit = n
	}
	runs, err := jobs.History(r.Context(), sc, m.ID, from, to, limit)
	if err != nil {
		return err
	}
	summaries, err := jobs.Summaries(r.Context(), sc, m.ID, from, to)
	if err != nil {
		return err
	}
	out := make([]jobRunJSON, 0, len(runs))
	for _, run := range runs {
		item := jobRunJSON{Timestamp: formatTime(run.FinishedAt), Status: run.Status,
			DurationMs: run.DurationMs, ExitCode: run.ExitCode, LateSeconds: run.LateSeconds,
			Message: run.Message, Source: run.Source}
		if !run.StartedAt.IsZero() {
			item.StartedAt = optTime(&run.StartedAt)
		}
		out = append(out, item)
	}
	body := map[string]any{"monitor": s.jobMonitorResponse(r, *m, now), "runs": out}
	if sum, ok := summaries[m.ID]; ok {
		body["summary"] = jobSummaryResponse(sum)
	} else {
		body["summary"] = jobSummaryResponse(jobs.Summary{})
	}
	writeJSON(w, http.StatusOK, body)
	return nil
}

func (s *Server) jobsActor(r *http.Request, p *auth.Principal) jobs.Actor {
	return jobs.Actor{UserID: p.UserID, Email: p.Email, IP: s.accounts.Meta(r).IP,
		APIKeyID: p.APIKeyID, APIKeyName: p.APIKeyName}
}

type jobInputJSON struct {
	jobs.Input
	Enabled *bool `json:"enabled"`
}

func decodeJobInput(r *http.Request) (jobs.Input, error) {
	var in jobInputJSON
	if err := decodeJSON(r, &in); err != nil {
		return jobs.Input{}, err
	}
	out := in.Input
	out.Enabled = in.Enabled == nil || *in.Enabled
	return out, out.Validate()
}

func (s *Server) createJobMonitor(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	in, err := decodeJobInput(r)
	if err != nil {
		return err
	}
	m, err := s.jobs.Create(r.Context(), p.OrgID, in, s.jobsActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, s.jobMonitorResponse(r, *m, s.now().UTC()))
	return nil
}

func (s *Server) updateJobMonitor(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	in, err := decodeJobInput(r)
	if err != nil {
		return err
	}
	m, err := s.jobs.Update(r.Context(), p.OrgID, r.PathValue("id"), in, s.jobsActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.jobMonitorResponse(r, *m, s.now().UTC()))
	return nil
}

func (s *Server) deleteJobMonitor(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := s.jobs.Delete(r.Context(), p.OrgID, r.PathValue("id"), s.jobsActor(r, p)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) rotateJobMonitorToken(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	m, err := s.jobs.Rotate(r.Context(), p.OrgID, r.PathValue("id"), s.jobsActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.jobMonitorResponse(r, *m, s.now().UTC()))
	return nil
}

// ---- the ping endpoint ----

// jobPing records one report from a job. It answers text, not JSON: the caller is a shell line, and what it
// can do with the answer is print it into a log.
func (s *Server) jobPing(rec *statusRecorder, r *http.Request) {
	noStore(rec)
	rec.Header().Set("Content-Type", "text/plain; charset=utf-8")
	event, ok := jobs.ParseEvent(r.PathValue("event"))
	if !ok {
		rec.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(rec, "unknown event; use /start, /fail or no suffix\n")
		return
	}
	token := jobs.NormalizeToken(r.PathValue("token"))
	if token == "" {
		rec.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(rec, "unknown job token\n")
		return
	}
	m, tenantID, err := s.jobPings.ByToken(r.Context(), token)
	if errors.Is(err, jobs.ErrUnknownToken) {
		// A wrong token is not told apart from a deleted monitor: both are "this URL does nothing", and a
		// probe must not learn which tokens exist.
		rec.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(rec, "unknown job token\n")
		return
	}
	if err != nil {
		s.log.Error("job ping lookup failed", "err", err)
		rec.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(rec, "temporarily unavailable\n")
		return
	}
	if r.Method == http.MethodHead {
		rec.WriteHeader(http.StatusOK)
		return
	}

	p := jobs.Ping{Event: event, At: s.now().UTC(), Source: pingSource(r)}
	if v := r.URL.Query().Get("exit"); v != "" {
		code, err := strconv.Atoi(v)
		if err != nil || code < 0 || code > jobs.MaxExitCode {
			rec.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(rec, "exit must be a number between 0 and 255\n")
			return
		}
		p.ExitCode = code
	}
	if body, err := io.ReadAll(io.LimitReader(r.Body, maxPingBody)); err == nil {
		p.Message = strings.TrimSpace(string(body))
	}
	if msg := r.URL.Query().Get("msg"); msg != "" && p.Message == "" {
		p.Message = msg
	}

	run, updated, err := s.jobPings.RecordPing(r.Context(), m.ID, p)
	if err != nil {
		s.log.Error("job ping failed", "monitor_id", m.ID, "err", err)
		rec.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(rec, "temporarily unavailable\n")
		return
	}
	// A start ping concludes no run; a finishing one does, and it carries the tenant the runs belong to.
	if run != nil && s.jobRuns != nil {
		run.TenantID = tenantID
		s.jobRuns.Add([]jobs.Run{*run})
	}
	rec.WriteHeader(http.StatusOK)
	next := ""
	if updated != nil {
		next = " next expected " + formatTime(updated.State.ExpectedAt)
	}
	_, _ = io.WriteString(rec, "ok "+event+next+"\n")
}

// pingSource is the address the ping came from, so a job running on the wrong host is visible on the run.
func pingSource(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
