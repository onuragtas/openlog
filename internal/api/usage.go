package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/billing"
	"github.com/onuragtas/openlog/internal/quota"
	"github.com/onuragtas/openlog/internal/usage"
)

// Usage & plan endpoints (docs/contracts/api.md "Usage and plans", docs/contracts/usage.md).
//
//	GET  /api/v1/usage?period=              current period usage vs limits, projection (any role, API keys)
//	GET  /api/v1/usage/daily?period=        daily breakdown per signal
//	GET  /api/v1/usage/top?period=&by=      top services or hosts by stored bytes
//	GET  /api/v1/usage/export?period=&format=csv|json   invoice-period export (admin, owner)
//	GET  /api/v1/usage/status               latest quota evaluation (banner)
//	GET  /api/v1/plans                      plan catalog
//	GET  /api/v1/admin/orgs/{org}/plan      plan assignment of any organization (superadmin)
//	PUT  /api/v1/admin/orgs/{org}/plan
//	POST /api/v1/billing/webhooks/{provider}   provider webhooks (public, signature verified by the provider)

// UsageReader reads usage metering data (usage.Reader).
type UsageReader interface {
	Totals(ctx context.Context, tenant string, from, to time.Time) (usage.Totals, error)
	Daily(ctx context.Context, tenant string, from, to time.Time) ([]usage.Day, error)
	Top(ctx context.Context, tenant, dim string, from, to time.Time, limit int) ([]usage.TopEntry, error)
	SignalBytesSince(ctx context.Context, tenant string, since map[string]time.Time, to time.Time) (map[string]uint64, error)
	CompressionRatios(ctx context.Context) (map[string]float64, error)
}

// UsagePlanStore persists plan assignments and quota status (quota.PGStore).
type UsagePlanStore interface {
	FindOrgPlan(ctx context.Context, ref string) (quota.OrgPlan, error)
	PutOrgPlan(ctx context.Context, op quota.OrgPlan, actor quota.Actor) error
	GetStatus(ctx context.Context, tenantID string) (quota.StoredStatus, bool, error)
	MemberCounts(ctx context.Context) (map[string]int64, error)
	FindOrgByBillingCustomer(ctx context.Context, provider, customerID string) (quota.OrgPlan, error)
}

// UsageDeps enables the usage endpoints.
type UsageDeps struct {
	Reader  UsageReader
	Catalog *quota.Catalog
	// Store is nil in static auth mode (every organization has the default plan, no status, no admin endpoints).
	Store      UsagePlanStore
	SaaS       bool
	Superadmin func(email string) bool
	Thresholds []int
	// Billing is nil without a provider (OPENLOG_BILLING_PROVIDER=none).
	Billing billing.Provider
}

// SetUsage enables the usage endpoints. Must be called before Run.
func (s *Server) SetUsage(d UsageDeps) {
	s.usage = &d
	s.srv.Handler = s.Handler()
}

type usageFunc func(w http.ResponseWriter, r *http.Request, p *auth.Principal) error

func (s *Server) usageRoutes(mux *http.ServeMux) {
	if s.usage == nil {
		return
	}
	// org: any member role or API key of an organization.
	org := func(pattern string, h usageFunc) {
		mux.Handle(pattern, s.instrument(pattern, func(rec *statusRecorder, r *http.Request) {
			noStore(rec)
			p, r := s.authenticate(rec, r)
			if p == nil {
				return
			}
			if !p.HasOrg() {
				writeError(rec, &apiError{http.StatusForbidden, "permission_denied", "you are not a member of any organization"})
				return
			}
			if !p.Role.Can(auth.ActReadOrg) {
				writeError(rec, &apiError{http.StatusForbidden, "permission_denied", "your role does not allow reading usage"})
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), s.cfg.QueryTimeout+5*time.Second)
			defer cancel()
			if err := h(rec, r.WithContext(ctx), p); err != nil {
				s.writeUsageError(rec, pattern, err)
			}
		}))
	}
	super := func(pattern string, h usageFunc) {
		mux.Handle(pattern, s.instrument(pattern, func(rec *statusRecorder, r *http.Request) {
			noStore(rec)
			p, r := s.authenticate(rec, r)
			if p == nil {
				return
			}
			if !s.isSuperadmin(p) {
				writeError(rec, &apiError{http.StatusForbidden, "permission_denied", "only openlog operators (OPENLOG_SUPERADMIN_EMAILS) can change plans"})
				return
			}
			if err := h(rec, r, p); err != nil {
				s.writeUsageError(rec, pattern, err)
			}
		}))
	}
	org("GET /api/v1/usage", s.getUsage)
	org("GET /api/v1/usage/daily", s.getUsageDaily)
	org("GET /api/v1/usage/top", s.getUsageTop)
	org("GET /api/v1/usage/export", s.exportUsage)
	org("GET /api/v1/usage/status", s.getUsageStatus)
	org("GET /api/v1/plans", s.listPlans)
	if s.usage.Store != nil {
		super("GET /api/v1/admin/orgs/{org}/plan", s.getOrgPlan)
		super("PUT /api/v1/admin/orgs/{org}/plan", s.putOrgPlan)
		mux.Handle("POST /api/v1/billing/webhooks/{provider}", s.instrument("POST /api/v1/billing/webhooks/{provider}", func(rec *statusRecorder, r *http.Request) {
			noStore(rec)
			if err := s.billingWebhook(rec, r); err != nil {
				s.writeUsageError(rec, "POST /api/v1/billing/webhooks/{provider}", err)
			}
		}))
	}
}

func (s *Server) writeUsageError(w http.ResponseWriter, route string, err error) {
	switch {
	case errors.Is(err, quota.ErrOrgNotFound):
		writeError(w, &apiError{http.StatusNotFound, "not_found", "organization not found"})
	default:
		ae := s.toAPIError(err)
		if ae.status >= 500 {
			s.log.Error("api request failed", "route", route, "err", err)
		}
		writeError(w, ae)
	}
}

// isSuperadmin: a signed-in user with a verified e-mail address in OPENLOG_SUPERADMIN_EMAILS.
func (s *Server) isSuperadmin(p *auth.Principal) bool {
	u := s.usage
	return u != nil && u.Superadmin != nil && p.Kind == auth.KindSession && p.EmailVerified && u.Superadmin(p.Email)
}

// ---- response shapes ----

type planJSON struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Limits      quota.Limits      `json:"limits"`
	Enforcement quota.Enforcement `json:"enforcement"`
}

func toPlanJSON(p quota.Plan) planJSON {
	l := p.Limits
	if l.RetentionDays == nil {
		l.RetentionDays = map[string]int{}
	}
	return planJSON{ID: p.ID, Name: p.Name, Description: p.Description, Limits: l, Enforcement: p.Enforcement}
}

type usagePeriodJSON struct {
	ID    string `json:"id"`
	Start string `json:"start"`
	End   string `json:"end"`
	// DataUntil is the end of the range the usage covers (now for the current period).
	DataUntil string `json:"data_until"`
}

func toPeriodJSON(p usage.Period, until time.Time) usagePeriodJSON {
	return usagePeriodJSON{ID: p.ID(), Start: formatTime(p.Start), End: formatTime(p.End), DataUntil: formatTime(until)}
}

type storedJSON struct {
	Signal        string `json:"signal"`
	RetentionDays int    `json:"retention_days"`
	// Bytes is the estimated uncompressed size of the rows inside the retention; CompressedBytes applies the
	// table's current compression ratio.
	Bytes           uint64 `json:"bytes"`
	CompressedBytes uint64 `json:"compressed_bytes"`
}

// ---- helpers ----

func (s *Server) period(r *http.Request) (usage.Period, time.Time, error) {
	now := s.now().UTC()
	p, err := usage.ParsePeriod(r.URL.Query().Get("period"), now)
	if err != nil {
		return p, now, badRequest("%v", err)
	}
	until := p.Elapsed(now)
	return p, until, nil
}

// orgPlan returns the principal's organization assignment (default plan without a store or row).
func (s *Server) orgPlan(ctx context.Context, p *auth.Principal) (quota.OrgPlan, error) {
	if s.usage.Store == nil {
		return quota.OrgPlan{OrgID: p.OrgID, TenantID: p.TenantID, OrgName: p.OrgName}, nil
	}
	return s.usage.Store.FindOrgPlan(ctx, p.OrgID)
}

func (s *Server) getUsage(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	u := s.usage
	period, until, err := s.period(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	op, err := s.orgPlan(ctx, p)
	if err != nil {
		return err
	}
	plan := u.Catalog.EffectivePlan(op)
	totals, err := u.Reader.Totals(ctx, p.TenantID, period.Start, until)
	if err != nil {
		return err
	}
	days, err := u.Reader.Daily(ctx, p.TenantID, period.Start, until)
	if err != nil {
		return err
	}
	var users int64
	if u.Store != nil {
		counts, err := u.Store.MemberCounts(ctx)
		if err != nil {
			return err
		}
		users = counts[p.OrgID]
	}
	st := quota.Evaluate(plan, quota.Usage{IngestBytes: int64(totals.IngestBytes), ActiveHosts: int64(totals.ActiveHosts), Users: users},
		u.SaaS, quota.WarnPercent(u.Thresholds))

	// Projection of the period's ingest from the last 7 complete days.
	daily := make([]float64, 0, len(days))
	for _, d := range days {
		daily = append(daily, float64(d.IngestBytes))
	}
	projected := usage.Project(float64(totals.IngestBytes), usage.RecentDailyAverage(daily, 7), period, s.now().UTC())
	projection := map[string]any{"ingest_bytes": projected}
	if st.IngestLimitBytes > 0 {
		projection["ingest_percent"] = projected / float64(st.IngestLimitBytes) * 100
	}

	stored, err := s.storedEstimate(ctx, p.TenantID, plan, s.now().UTC())
	if err != nil {
		return err
	}
	resp := map[string]any{
		"organization":    map[string]string{"id": p.OrgID, "name": p.OrgName, "tenant_id": p.TenantID},
		"period":          toPeriodJSON(period, until),
		"saas_mode":       u.SaaS,
		"plan":            toPlanJSON(plan),
		"plan_assigned":   op.Assigned,
		"usage":           totals,
		"stored":          stored,
		"limits":          st.Metrics,
		"level":           st.Level,
		"ingest_blocked":  st.IngestBlocked,
		"projection":      projection,
		"can_manage_plan": s.isSuperadmin(p),
		"billing_enabled": u.Billing != nil,
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

// storedEstimate returns the bytes still inside each signal's retention (effective plan retention or the table
// default) and a compressed estimate.
func (s *Server) storedEstimate(ctx context.Context, tenant string, plan quota.Plan, now time.Time) ([]storedJSON, error) {
	since := map[string]time.Time{}
	days := map[string]int{}
	for _, sig := range usage.Signals {
		d := quota.DefaultRetentionDays[sig]
		if pd := plan.Limits.RetentionDays[sig]; pd > 0 {
			d = pd
		}
		days[sig] = d
		since[sig] = now.Truncate(time.Hour).AddDate(0, 0, -d)
	}
	b, err := s.usage.Reader.SignalBytesSince(ctx, tenant, since, now.Add(time.Hour))
	if err != nil {
		return nil, err
	}
	ratios, err := s.usage.Reader.CompressionRatios(ctx)
	if err != nil {
		s.log.Debug("compression ratios unavailable", "err", err)
		ratios = map[string]float64{}
	}
	out := make([]storedJSON, 0, len(usage.Signals))
	for _, sig := range usage.Signals {
		st := storedJSON{Signal: sig, RetentionDays: days[sig], Bytes: b[sig]}
		if r, ok := ratios[sig]; ok {
			st.CompressedBytes = uint64(float64(b[sig]) * r)
		}
		out = append(out, st)
	}
	return out, nil
}

func (s *Server) getUsageDaily(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	period, until, err := s.period(r)
	if err != nil {
		return err
	}
	days, err := s.usage.Reader.Daily(r.Context(), p.TenantID, period.Start, until)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"period": toPeriodJSON(period, until), "days": days})
	return nil
}

func (s *Server) getUsageTop(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	period, until, err := s.period(r)
	if err != nil {
		return err
	}
	by := r.URL.Query().Get("by")
	if by == "" {
		by = "service"
	}
	if _, ok := usage.TopDimensions[by]; !ok {
		return badRequest("by must be service or host")
	}
	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return badRequest("limit must be between 1 and 100")
		}
		limit = n
	}
	entries, err := s.usage.Reader.Top(r.Context(), p.TenantID, by, period.Start, until, limit)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"period": toPeriodJSON(period, until), "by": by, "entries": entries})
	return nil
}

func (s *Server) exportUsage(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if !p.Role.AtLeast(auth.RoleAdmin) {
		return &apiError{http.StatusForbidden, "permission_denied", "only admins and owners can export usage"}
	}
	period, until, err := s.period(r)
	if err != nil {
		return err
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "json" {
		return badRequest("format must be csv or json")
	}
	days, err := s.usage.Reader.Daily(r.Context(), p.TenantID, period.Start, until)
	if err != nil {
		return err
	}
	name := "openlog-usage-" + p.TenantID + "-" + period.ID() + "." + format
	if format == "json" {
		op, err := s.orgPlan(r.Context(), p)
		if err != nil {
			return err
		}
		totals, err := s.usage.Reader.Totals(r.Context(), p.TenantID, period.Start, until)
		if err != nil {
			return err
		}
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		writeJSON(w, http.StatusOK, map[string]any{
			"organization": map[string]string{"id": p.OrgID, "name": p.OrgName, "tenant_id": p.TenantID},
			"plan_id":      s.usage.Catalog.EffectivePlan(op).ID, "period": toPeriodJSON(period, until), "totals": totals, "days": days,
		})
		return nil
	}
	var buf bytes.Buffer
	if err := usage.WriteCSV(&buf, p.TenantID, period.ID(), days); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
	return nil
}

func (s *Server) getUsageStatus(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	resp := map[string]any{"level": quota.LevelOK, "ingest_blocked": false, "metrics": []quota.MetricStatus{}, "saas_mode": s.usage.SaaS}
	if s.usage.Store != nil {
		st, found, err := s.usage.Store.GetStatus(r.Context(), p.TenantID)
		if err != nil {
			return err
		}
		if found {
			metrics := st.Metrics
			if metrics == nil {
				metrics = []quota.MetricStatus{}
			}
			resp["level"], resp["ingest_blocked"], resp["metrics"] = st.Level, st.IngestBlocked, metrics
			resp["plan_id"], resp["evaluated_at"] = st.PlanID, formatTime(st.EvaluatedAt)
			resp["period_start"] = st.PeriodStart.UTC().Format(time.DateOnly)
		}
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func (s *Server) listPlans(w http.ResponseWriter, _ *http.Request, _ *auth.Principal) error {
	plans := make([]planJSON, 0, len(s.usage.Catalog.Plans))
	for _, p := range s.usage.Catalog.Plans {
		plans = append(plans, toPlanJSON(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"plans": plans, "default": s.usage.Catalog.Default})
	return nil
}

type orgPlanJSON struct {
	Organization map[string]string `json:"organization"`
	PlanID       string            `json:"plan_id"`
	Assigned     bool              `json:"assigned"`
	Overrides    quota.Overrides   `json:"overrides"`
	Billing      map[string]string `json:"billing"`
	Note         string            `json:"note"`
	UpdatedAt    *string           `json:"updated_at"`
	UpdatedBy    string            `json:"updated_by"`
	Effective    planJSON          `json:"effective"`
}

func (s *Server) orgPlanResponse(op quota.OrgPlan) orgPlanJSON {
	eff := s.usage.Catalog.EffectivePlan(op)
	planID := op.PlanID
	if !op.Assigned {
		planID = eff.ID
	}
	return orgPlanJSON{
		Organization: map[string]string{"id": op.OrgID, "name": op.OrgName, "tenant_id": op.TenantID},
		PlanID:       planID, Assigned: op.Assigned, Overrides: op.Overrides, Note: op.Note,
		Billing: map[string]string{"provider": op.BillingProvider, "customer_id": op.BillingCustomerID,
			"subscription_id": op.BillingSubscriptionID},
		UpdatedAt: optTime(op.UpdatedAt), UpdatedBy: op.UpdatedByEmail, Effective: toPlanJSON(eff),
	}
}

func (s *Server) getOrgPlan(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	op, err := s.usage.Store.FindOrgPlan(r.Context(), r.PathValue("org"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.orgPlanResponse(op))
	return nil
}

type putOrgPlanRequest struct {
	PlanID    string           `json:"plan_id"`
	Overrides *quota.Overrides `json:"overrides"`
	Billing   *struct {
		Provider       string `json:"provider"`
		CustomerID     string `json:"customer_id"`
		SubscriptionID string `json:"subscription_id"`
	} `json:"billing"`
	Note *string `json:"note"`
}

func (s *Server) putOrgPlan(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var req putOrgPlanRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if _, ok := s.usage.Catalog.Plan(req.PlanID); !ok {
		return badRequest("unknown plan_id %q (plans: %v)", req.PlanID, s.usage.Catalog.PlanIDs())
	}
	op, err := s.usage.Store.FindOrgPlan(r.Context(), r.PathValue("org"))
	if err != nil {
		return err
	}
	op.PlanID = req.PlanID
	if req.Overrides != nil {
		if err := req.Overrides.Validate(); err != nil {
			return badRequest("overrides: %v", err)
		}
		tableDays := quota.TableRetentionDays(s.usage.Catalog)
		for sig, d := range req.Overrides.RetentionDays {
			if d > tableDays[sig] {
				return badRequest("overrides: retention_days.%s = %d exceeds the longest plan retention (%d days); define a plan with a longer retention", sig, d, tableDays[sig])
			}
		}
		op.Overrides = *req.Overrides
	}
	if req.Billing != nil {
		if len(req.Billing.Provider) > 64 || len(req.Billing.CustomerID) > 255 || len(req.Billing.SubscriptionID) > 255 {
			return badRequest("billing ids are too long")
		}
		op.BillingProvider, op.BillingCustomerID, op.BillingSubscriptionID = req.Billing.Provider, req.Billing.CustomerID, req.Billing.SubscriptionID
	}
	if req.Note != nil {
		if len(*req.Note) > 1000 {
			return badRequest("note must be at most 1000 characters")
		}
		op.Note = *req.Note
	}
	actor := quota.Actor{UserID: p.UserID, Email: p.Email, IP: auth.ClientIP(r, nil)}
	if err := s.usage.Store.PutOrgPlan(r.Context(), op, actor); err != nil {
		return err
	}
	op, err = s.usage.Store.FindOrgPlan(r.Context(), op.OrgID)
	if err != nil {
		return err
	}
	s.log.Info("organization plan changed", "org_id", op.OrgID, "plan_id", op.PlanID, "by", p.Email)
	writeJSON(w, http.StatusOK, s.orgPlanResponse(op))
	return nil
}

func (s *Server) billingWebhook(w http.ResponseWriter, r *http.Request) error {
	prov := s.usage.Billing
	if prov == nil || r.PathValue("provider") != prov.Name() {
		return notFound("no such billing provider")
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return badRequest("cannot read body")
	}
	ev, err := prov.ParseWebhook(payload, r.Header)
	switch {
	case errors.Is(err, billing.ErrNotSupported):
		return notFound("the billing provider has no webhooks")
	case err != nil:
		return badRequest("invalid webhook: %v", err)
	}
	store, ok := s.usage.Store.(billing.PlanStore)
	if !ok {
		return &apiError{http.StatusServiceUnavailable, "unavailable", "plans are not stored"}
	}
	changed, err := billing.ApplyEvent(r.Context(), store, s.usage.Catalog, prov.Name(), ev)
	if err != nil {
		s.log.Warn("billing webhook not applied", "provider", prov.Name(), "event", ev.ID, "err", err)
		return badRequest("%v", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"received": true, "changed": changed})
	return nil
}
