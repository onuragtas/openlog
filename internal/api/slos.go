package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/slo"
)

// Service level objectives (docs/contracts/slo.md, api.md "Service level objectives"). Definitions live in
// PostgreSQL; the SLI, the error budget and the burn rates are computed at query time from
// apm_transactions_1m through the tenant-scoped query layer (nothing is precomputed).
//
// Reads are telemetry reads (any role, API keys too); writes need a signed-in member or higher and are
// audited by the store (slo.create/update/delete).

// sloStatusLimit bounds how many SLOs of a list response get their current budget computed (one small
// aggregate query each); the rest are returned without a status.
const sloStatusLimit = 50

// SetSLOs enables the SLO endpoints (postgres auth mode). Must be called before Run.
func (s *Server) SetSLOs(store slo.Store) {
	s.slos = store
	s.srv.Handler = s.Handler()
}

func (s *Server) sloRoutes(mux *http.ServeMux) {
	if s.slos == nil {
		return
	}
	route := func(pattern string, h handlerFunc) { mux.Handle(pattern, s.wrap(pattern, h)) }
	route("GET /api/v1/slos", s.listSLOs)
	route("GET /api/v1/slos/{id}", s.getSLO)
	route("GET /api/v1/slos/{id}/results", s.sloResults)
	if s.accounts != nil {
		s.sloWriteRoute(mux, "POST /api/v1/slos", s.createSLO)
		s.sloWriteRoute(mux, "PUT /api/v1/slos/{id}", s.updateSLO)
		s.sloWriteRoute(mux, "DELETE /api/v1/slos/{id}", s.deleteSLO)
	}
}

type sloWriteHandler func(w http.ResponseWriter, r *http.Request, p *auth.Principal) error

// sloWriteRoute registers a mutating endpoint: wrap authenticates (CSRF included), the handler additionally
// needs a principal whose role may write SLOs (a member, or an API key with that role).
func (s *Server) sloWriteRoute(mux *http.ServeMux, pattern string, h sloWriteHandler) {
	mux.Handle(pattern, s.wrap(pattern, func(w http.ResponseWriter, r *http.Request, _ *query.Scope) error {
		noStore(w)
		p, _ := auth.PrincipalFrom(r.Context())
		if ae := authorize(p, auth.ActWriteSLOs); ae != nil {
			return ae
		}
		return sloError(h(w, r, p))
	}))
}

// sloError maps store and validation errors to the API error shape.
func sloError(err error) error {
	var ve *slo.ValidationError
	switch {
	case errors.As(err, &ve):
		return &apiError{http.StatusBadRequest, "invalid_argument", ve.Error()}
	case errors.Is(err, slo.ErrNotFound):
		return notFound("slo not found")
	case errors.Is(err, slo.ErrLimit):
		return &apiError{http.StatusConflict, "failed_precondition",
			fmt.Sprintf("the organization already has the maximum number of SLOs (%d); delete some first", slo.MaxPerOrg)}
	}
	return err
}

// ---- response shapes ----

type sloJSON struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Description        string   `json:"description"`
	ServiceName        string   `json:"service_name"`
	ServiceNamespace   *string  `json:"service_namespace"`
	Environment        *string  `json:"environment"`
	SLIType            string   `json:"sli_type"`
	LatencyThresholdMs *float64 `json:"latency_threshold_ms"`
	Objective          float64  `json:"objective"`
	WindowDays         int      `json:"window_days"`
	CreatedByEmail     string   `json:"created_by_email"`
	UpdatedByEmail     string   `json:"updated_by_email"`
	CreatedAt          string   `json:"created_at"`
	UpdatedAt          string   `json:"updated_at"`
}

func sloResponse(x *slo.SLO) sloJSON {
	out := sloJSON{ID: x.ID, Name: x.Name, Description: x.Description, ServiceName: x.ServiceName,
		ServiceNamespace: x.ServiceNamespace, Environment: x.Environment, SLIType: x.SLIType,
		Objective: x.Objective, WindowDays: x.WindowDays, CreatedByEmail: x.CreatedByEmail,
		UpdatedByEmail: x.UpdatedByEmail, CreatedAt: formatTime(x.CreatedAt), UpdatedAt: formatTime(x.UpdatedAt)}
	if x.SLIType == slo.SLILatency {
		out.LatencyThresholdMs = optFloat(x.LatencyThresholdMs)
	}
	return out
}

// sloBudgetJSON is the error budget over one range (slo.md §2). Ratios are null without requests.
type sloBudgetJSON struct {
	Requests        float64  `json:"requests"`
	Good            float64  `json:"good"`
	Bad             float64  `json:"bad"`
	SLI             *float64 `json:"sli"`
	BudgetRequests  float64  `json:"budget_requests"`
	BudgetConsumed  float64  `json:"budget_consumed"`
	BudgetRemaining float64  `json:"budget_remaining"`
	RemainingRatio  *float64 `json:"remaining_ratio"`
	BurnRate        *float64 `json:"burn_rate"`
	Met             bool     `json:"met"`
}

func budgetResponse(b slo.Budget) sloBudgetJSON {
	return sloBudgetJSON{Requests: b.Requests, Good: b.Good, Bad: b.Bad, SLI: optFloat(b.SLI),
		BudgetRequests: b.BudgetRequests, BudgetConsumed: b.BudgetConsumed, BudgetRemaining: b.BudgetRemaining,
		RemainingRatio: optFloat(b.RemainingRatio), BurnRate: optFloat(b.BurnRate), Met: b.Met()}
}

type sloStatusJSON struct {
	From       string        `json:"from"`
	To         string        `json:"to"`
	WindowDays int           `json:"window_days"`
	Budget     sloBudgetJSON `json:"budget"`
}

type sloListItemJSON struct {
	sloJSON
	Status *sloStatusJSON `json:"status"`
}

// ---- CRUD ----

func (s *Server) listSLOs(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	p, _ := auth.PrincipalFrom(r.Context())
	list, err := s.slos.List(r.Context(), p.OrgID)
	if err != nil {
		return sloError(err)
	}
	status := true
	if v := r.URL.Query().Get("status"); v != "" {
		if status, err = strconv.ParseBool(v); err != nil {
			return badRequest("status must be true or false")
		}
	}
	out := make([]sloListItemJSON, 0, len(list))
	truncated := false
	for i := range list {
		item := sloListItemJSON{sloJSON: sloResponse(&list[i])}
		switch {
		case !status:
		case i < sloStatusLimit:
			st, err := s.sloStatus(r, sc, &list[i], list[i].Window())
			if err != nil {
				return err
			}
			item.Status = st
		default:
			truncated = true
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"slos": out, "status_truncated": truncated})
	return nil
}

// sloStatus computes the budget over the rolling window ending at the last complete minute with a single
// bucket (sums and merged histograms are exact whatever the step is, slo.md §2).
func (s *Server) sloStatus(r *http.Request, sc *query.Scope, x *slo.SLO, window time.Duration) (*sloStatusJSON, error) {
	to := s.now().UTC().Truncate(time.Minute)
	from := to.Add(-window)
	buckets, err := slo.Load(r.Context(), sc, x, from, to, window)
	if err != nil {
		return nil, err
	}
	b := slo.Compute(slo.Sum(buckets), x.ObjectiveFraction())
	return &sloStatusJSON{From: formatTime(from), To: formatTime(to), WindowDays: x.WindowDays, Budget: budgetResponse(b)}, nil
}

func (s *Server) sloByID(r *http.Request) (*slo.SLO, error) {
	p, _ := auth.PrincipalFrom(r.Context())
	x, err := s.slos.Get(r.Context(), p.OrgID, r.PathValue("id"))
	if err != nil {
		return nil, sloError(err)
	}
	return x, nil
}

func (s *Server) getSLO(w http.ResponseWriter, r *http.Request, _ *query.Scope) error {
	noStore(w)
	x, err := s.sloByID(r)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, sloResponse(x))
	return nil
}

func decodeSLOInput(r *http.Request) (slo.Input, error) {
	var in slo.Input
	if err := decodeJSON(r, &in); err != nil {
		return in, err
	}
	return in, in.Validate()
}

func (s *Server) sloActor(r *http.Request, p *auth.Principal) slo.Actor {
	return slo.Actor{UserID: p.UserID, Email: p.Email, IP: s.accounts.Meta(r).IP}
}

func (s *Server) createSLO(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	in, err := decodeSLOInput(r)
	if err != nil {
		return err
	}
	x, err := s.slos.Create(r.Context(), p.OrgID, in, s.sloActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, sloResponse(x))
	return nil
}

func (s *Server) updateSLO(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	in, err := decodeSLOInput(r)
	if err != nil {
		return err
	}
	x, err := s.slos.Update(r.Context(), p.OrgID, r.PathValue("id"), in, s.sloActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, sloResponse(x))
	return nil
}

func (s *Server) deleteSLO(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := s.slos.Delete(r.Context(), p.OrgID, r.PathValue("id"), s.sloActor(r, p)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// ---- results ----

type sloBurnJSON struct {
	Name         string        `json:"name"`
	Factor       float64       `json:"factor"`
	LongSeconds  int           `json:"long_seconds"`
	ShortSeconds int           `json:"short_seconds"`
	Long         sloBudgetJSON `json:"long"`
	Short        sloBudgetJSON `json:"short"`
	Rate         *float64      `json:"rate"`
	Ratio        *float64      `json:"ratio"`
	Breaching    bool          `json:"breaching"`
}

type sloPointJSON struct {
	T              int64    `json:"t"`
	Requests       float64  `json:"requests"`
	Good           float64  `json:"good"`
	Bad            float64  `json:"bad"`
	SLI            *float64 `json:"sli"`
	BurnRate       *float64 `json:"burn_rate"`
	RemainingRatio *float64 `json:"remaining_ratio"`
}

// sloResults serves GET /slos/{id}/results: the budget over the SLO's rolling window, the burn-rate windows
// of the slo_burn defaults and the burndown series (one point per step).
func (s *Server) sloResults(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	x, err := s.sloByID(r)
	if err != nil {
		return err
	}
	to := s.now().UTC().Truncate(time.Minute)
	from := to.Add(-x.Window())
	step, err := apmStep(r, from, to)
	if err != nil {
		return err
	}
	buckets, err := slo.Load(r.Context(), sc, x, from, to, step)
	if err != nil {
		return err
	}
	objective := x.ObjectiveFraction()
	status := sloStatusJSON{From: formatTime(from), To: formatTime(to), WindowDays: x.WindowDays,
		Budget: budgetResponse(slo.Compute(slo.Sum(buckets), objective))}

	burnBuckets, err := slo.LoadBurn(r.Context(), sc, x, to, slo.DefaultBurnWindows)
	if err != nil {
		return err
	}
	burn := make([]sloBurnJSON, 0, len(slo.DefaultBurnWindows))
	for _, bw := range slo.DefaultBurnWindows {
		b := slo.BurnAt(burnBuckets, slo.BurnStep, to, objective, bw)
		burn = append(burn, sloBurnJSON{Name: bw.Name, Factor: bw.Factor, LongSeconds: int(bw.Long / time.Second),
			ShortSeconds: int(bw.Short / time.Second), Long: budgetResponse(b.Long), Short: budgetResponse(b.Short),
			Rate: optFloat(b.Rate), Ratio: optFloat(b.Ratio), Breaching: b.Breaching()})
	}

	points := slo.Series(buckets, objective)
	series := make([]sloPointJSON, 0, len(points))
	for _, p := range points {
		series = append(series, sloPointJSON{T: p.T.UnixMilli(), Requests: p.Requests, Good: p.Good, Bad: p.Bad,
			SLI: optFloat(p.SLI), BurnRate: optFloat(p.BurnRate), RemainingRatio: optFloat(p.RemainingRatio)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"slo": sloResponse(x), "status": status, "step": formatStep(step),
		"burn": burn, "series": series})
	return nil
}
