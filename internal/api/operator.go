package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/operator"
	"github.com/onuragtas/openlog/internal/quota"
	"github.com/onuragtas/openlog/internal/usage"
)

// SaaS operator console and organization lifecycle endpoints (docs/contracts/api.md "SaaS operations",
// docs/operations/saas.md, D-105, D-106).
//
//	GET    /api/v1/operator/me                                   whether the caller is an operator (any session)
//	GET    /api/v1/operator/orgs?q=&plan=&state=&sort=&limit=&offset=   organizations (superadmin)
//	GET    /api/v1/operator/orgs/{org}                           organization detail
//	POST   /api/v1/operator/orgs/{org}/suspend|unsuspend|trial|reset-quota-notifications|force-logout|resend-verification
//	POST   /api/v1/operator/orgs/{org}/support-sessions          open a read-only support view (needs owner grant)
//	GET    /api/v1/operator/support-sessions                     the operator's open support sessions
//	DELETE /api/v1/operator/support-sessions/{id}
//	POST   /api/v1/operator/support-sessions/{id}/views          audit a page view of the support UI
//	GET    /api/v1/operator/flags?status=                        abuse flags
//	POST   /api/v1/operator/flags/{id}/resolve
//	GET    /api/v1/orgs/current/saas                             suspension, trial and support access of the organization
//	PUT    /api/v1/orgs/current/support-access                   owner grants support access (24h | 7d)
//	DELETE /api/v1/orgs/current/support-access                   owner revokes it

// OperatorDeps enables the operator endpoints (postgres auth mode with usage metering).
type OperatorDeps struct {
	Store   operator.Store
	Catalog *quota.Catalog
	SaaS    bool
	// SupportTTL bounds one support session (OPENLOG_SAAS_SUPPORT_SESSION_TTL).
	SupportTTL time.Duration
}

type saasState struct {
	d OperatorDeps

	mu        sync.RWMutex
	suspended map[string]string // org id -> reason
	loadedAt  time.Time

	viewMu sync.Mutex
	views  map[string]time.Time // audited support requests (dedupe)
}

// SetOperator enables the operator console, suspension read-only mode and support sessions. Must be called before
// Run; call RunOperatorCache to keep the suspension cache fresh.
func (s *Server) SetOperator(d OperatorDeps) {
	if d.SupportTTL <= 0 {
		d.SupportTTL = 2 * time.Hour
	}
	s.saas = &saasState{d: d, suspended: map[string]string{}, views: map[string]time.Time{}}
	s.srv.Handler = s.Handler()
}

// ReloadSuspensions reloads this pod's suspension cache.
func (s *Server) ReloadSuspensions(ctx context.Context) error {
	if s.saas == nil {
		return nil
	}
	_, byOrg, err := s.saas.d.Store.SuspendedTenants(ctx)
	if err != nil {
		return err
	}
	s.saas.mu.Lock()
	s.saas.suspended, s.saas.loadedAt = byOrg, time.Now()
	s.saas.mu.Unlock()
	return nil
}

// RunOperatorCache reloads the suspension cache every 15 seconds until ctx is done.
func (s *Server) RunOperatorCache(ctx context.Context) {
	for {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := s.ReloadSuspensions(rctx); err != nil && ctx.Err() == nil {
			s.log.Warn("cannot reload suspended organizations; keeping the last known state", "err", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-time.After(15 * time.Second):
		}
	}
}

func (s *Server) orgSuspended(orgID string) bool {
	if s.saas == nil || orgID == "" {
		return false
	}
	s.saas.mu.RLock()
	defer s.saas.mu.RUnlock()
	_, ok := s.saas.suspended[orgID]
	return ok
}

func (s *Server) operatorRoutes(mux *http.ServeMux) {
	if s.saas == nil || s.accounts == nil || s.usage == nil {
		return
	}
	super := func(pattern string, h usageFunc) {
		mux.Handle(pattern, s.instrument(pattern, func(rec *statusRecorder, r *http.Request) {
			noStore(rec)
			p, r := s.authenticate(rec, r)
			if p == nil {
				return
			}
			if !s.isSuperadmin(p) {
				writeError(rec, &apiError{http.StatusForbidden, "permission_denied", "only openlog operators (OPENLOG_SUPERADMIN_EMAILS) can use the operator console"})
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), s.cfg.QueryTimeout+10*time.Second)
			defer cancel()
			if err := h(rec, r.WithContext(ctx), p); err != nil {
				s.writeOperatorError(rec, pattern, err)
			}
		}))
	}
	session := func(pattern string, h usageFunc) {
		mux.Handle(pattern, s.instrument(pattern, func(rec *statusRecorder, r *http.Request) {
			noStore(rec)
			p, r := s.authenticate(rec, r)
			if p == nil {
				return
			}
			if err := h(rec, r, p); err != nil {
				s.writeOperatorError(rec, pattern, err)
			}
		}))
	}
	session("GET /api/v1/operator/me", s.operatorMe)
	super("GET /api/v1/operator/orgs", s.operatorListOrgs)
	super("GET /api/v1/operator/orgs/{org}", s.operatorGetOrg)
	super("POST /api/v1/operator/orgs/{org}/suspend", s.operatorSuspend)
	super("POST /api/v1/operator/orgs/{org}/unsuspend", s.operatorUnsuspend)
	super("POST /api/v1/operator/orgs/{org}/trial", s.operatorTrial)
	super("POST /api/v1/operator/orgs/{org}/reset-quota-notifications", s.operatorResetNotifications)
	super("POST /api/v1/operator/orgs/{org}/force-logout", s.operatorForceLogout)
	super("POST /api/v1/operator/orgs/{org}/resend-verification", s.operatorResendVerification)
	super("POST /api/v1/operator/orgs/{org}/support-sessions", s.operatorStartSupport)
	super("GET /api/v1/operator/support-sessions", s.operatorListSupport)
	super("DELETE /api/v1/operator/support-sessions/{id}", s.operatorEndSupport)
	super("POST /api/v1/operator/support-sessions/{id}/views", s.operatorSupportView)
	super("GET /api/v1/operator/flags", s.operatorListFlags)
	super("POST /api/v1/operator/flags/{id}/resolve", s.operatorResolveFlag)
	session("GET /api/v1/orgs/current/saas", s.orgSaaSState)
	session("PUT /api/v1/orgs/current/support-access", s.grantSupportAccess)
	session("DELETE /api/v1/orgs/current/support-access", s.revokeSupportAccess)
}

func (s *Server) writeOperatorError(w http.ResponseWriter, route string, err error) {
	switch {
	case errors.Is(err, operator.ErrNotFound), errors.Is(err, quota.ErrOrgNotFound):
		writeError(w, &apiError{http.StatusNotFound, "not_found", "not found"})
	case errors.Is(err, operator.ErrNoAccess):
		writeError(w, &apiError{http.StatusForbidden, "support_access_required", "the organization has not granted openlog support access, or it has ended"})
	case errors.Is(err, operator.ErrInvalidState):
		msg := strings.TrimPrefix(err.Error(), operator.ErrInvalidState.Error()+": ")
		if msg == operator.ErrInvalidState.Error() {
			msg = "the organization is not in a state that allows this action"
		}
		writeError(w, &apiError{http.StatusConflict, "failed_precondition", msg})
	default:
		s.writeAccountError(w, route, err)
	}
}

func (s *Server) operatorActor(r *http.Request, p *auth.Principal) operator.Actor {
	return operator.Actor{UserID: p.UserID, Email: p.Email, IP: s.accounts.Meta(r).IP}
}

type reasonRequest struct {
	Reason string `json:"reason"`
}

func cleanReason(v string) (string, error) {
	v = strings.TrimSpace(v)
	if len(v) < 3 || len(v) > 1000 {
		return "", badRequest("reason is required (3-1000 characters); it is written to the organization's audit log")
	}
	return v, nil
}

func (s *Server) decodeReason(r *http.Request) (string, error) {
	var req reasonRequest
	if err := decodeJSON(r, &req); err != nil {
		return "", err
	}
	return cleanReason(req.Reason)
}

// ---- operator ----

func (s *Server) operatorMe(w http.ResponseWriter, _ *http.Request, p *auth.Principal) error {
	writeJSON(w, http.StatusOK, map[string]any{"operator": s.isSuperadmin(p), "saas_mode": s.saas.d.SaaS})
	return nil
}

func (s *Server) operatorListOrgs(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	q := r.URL.Query()
	f := operator.OrgFilter{Query: q.Get("q"), PlanID: q.Get("plan"), State: q.Get("state"), Sort: q.Get("sort"), DefaultPlan: s.saas.d.Catalog.Default}
	for name, dst := range map[string]*int{"limit": &f.Limit, "offset": &f.Offset} {
		if v := q.Get(name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return badRequest("%s must be a non-negative integer", name)
			}
			*dst = n
		}
	}
	orgs, total, err := s.saas.d.Store.ListOrgs(r.Context(), f)
	if errors.Is(err, operator.ErrInvalidState) {
		return badRequest("%s", strings.TrimPrefix(err.Error(), operator.ErrInvalidState.Error()+": "))
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"organizations": orgs, "total": total, "saas_mode": s.saas.d.SaaS})
	return nil
}

func (s *Server) operatorGetOrg(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	ctx := r.Context()
	st := s.saas.d.Store
	d, err := st.GetOrg(ctx, r.PathValue("org"), s.saas.d.Catalog.Default)
	if err != nil {
		return err
	}
	resp := map[string]any{"organization": d, "saas_mode": s.saas.d.SaaS}
	if s.usage.Store != nil {
		op, err := s.usage.Store.FindOrgPlan(ctx, d.OrgID)
		if err != nil {
			return err
		}
		resp["plan"] = s.orgPlanResponse(op)
		q, found, err := s.usage.Store.GetStatus(ctx, d.TenantID)
		if err != nil {
			return err
		}
		if found {
			resp["quota"] = map[string]any{"level": q.Level, "ingest_blocked": q.IngestBlocked, "metrics": nonNilMetrics(q.Metrics),
				"evaluated_at": formatTime(q.EvaluatedAt), "period_start": q.PeriodStart.UTC().Format(time.DateOnly)}
		} else {
			resp["quota"] = nil
		}
	}
	l, err := st.GetLifecycle(ctx, d.OrgID)
	if err != nil {
		return err
	}
	resp["lifecycle"] = l
	audit, err := st.RecentAudit(ctx, d.OrgID, 50)
	if err != nil {
		return err
	}
	resp["audit"] = audit
	now := s.now().UTC()
	period := usage.PeriodOf(now)
	usageResp := map[string]any{"period": toPeriodJSON(period, now), "days": []usage.Day{}, "available": true}
	if days, err := s.usage.Reader.Daily(ctx, d.TenantID, period.Start, now); err != nil {
		s.log.Warn("operator: usage unavailable", "org_id", d.OrgID, "err", err)
		usageResp["available"] = false
	} else if days != nil {
		usageResp["days"] = days
	}
	resp["usage"] = usageResp
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func nonNilMetrics(m []quota.MetricStatus) []quota.MetricStatus {
	if m == nil {
		return []quota.MetricStatus{}
	}
	return m
}

func (s *Server) operatorSuspend(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	reason, err := s.decodeReason(r)
	if err != nil {
		return err
	}
	l, err := s.saas.d.Store.Suspend(r.Context(), r.PathValue("org"), reason, s.operatorActor(r, p), false)
	if err != nil {
		return err
	}
	_ = s.ReloadSuspensions(r.Context())
	s.log.Warn("organization suspended by operator", "org_id", l.OrgID, "by", p.Email)
	writeJSON(w, http.StatusOK, l)
	return nil
}

func (s *Server) operatorUnsuspend(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	reason, err := s.decodeReason(r)
	if err != nil {
		return err
	}
	l, err := s.saas.d.Store.Unsuspend(r.Context(), r.PathValue("org"), reason, s.operatorActor(r, p))
	if errors.Is(err, operator.ErrInvalidState) {
		return &apiError{http.StatusConflict, "failed_precondition", "the organization is not suspended"}
	}
	if err != nil {
		return err
	}
	_ = s.ReloadSuspensions(r.Context())
	s.log.Info("organization unsuspended by operator", "org_id", l.OrgID, "by", p.Email)
	writeJSON(w, http.StatusOK, l)
	return nil
}

type trialRequest struct {
	PlanID string `json:"plan_id"`
	Days   int    `json:"days"`
	EndsAt string `json:"ends_at"`
	Reason string `json:"reason"`
}

func (s *Server) operatorTrial(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var req trialRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	reason, err := cleanReason(req.Reason)
	if err != nil {
		return err
	}
	ctx := r.Context()
	st := s.saas.d.Store
	l, err := st.GetLifecycle(ctx, r.PathValue("org"))
	if err != nil {
		return err
	}
	now := s.now()
	var ends time.Time
	switch {
	case req.EndsAt != "" && req.Days != 0:
		return badRequest("set days or ends_at, not both")
	case req.EndsAt != "":
		if ends, err = parseTime(req.EndsAt); err != nil {
			return badRequest("ends_at: %v", err)
		}
	case req.Days != 0:
		if req.Days < 1 || req.Days > 365 {
			return badRequest("days must be between 1 and 365")
		}
	}
	if l.TrialActive() {
		if req.PlanID != "" && req.PlanID != l.TrialPlanID {
			return badRequest("a trial of plan %q is running; extend it or change the plan with PUT /api/v1/admin/orgs/{org}/plan", l.TrialPlanID)
		}
		if ends.IsZero() {
			if req.Days == 0 {
				return badRequest("days or ends_at is required to extend the trial")
			}
			ends = l.TrialEndsAt.Add(time.Duration(req.Days) * 24 * time.Hour)
		}
		if !ends.After(now) || ends.Sub(now) > 366*24*time.Hour {
			return badRequest("the trial end must be in the future and within a year")
		}
		out, err := st.ExtendTrial(ctx, l.OrgID, ends, reason, s.operatorActor(r, p))
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, out)
		return nil
	}
	plan, ok := s.saas.d.Catalog.Plan(req.PlanID)
	if !ok {
		return badRequest("plan_id: unknown plan %q (plans: %v)", req.PlanID, s.saas.d.Catalog.PlanIDs())
	}
	if ends.IsZero() {
		days := req.Days
		if days == 0 {
			days = plan.TrialDays
		}
		if days <= 0 {
			return badRequest("plan %q has no trial_days; pass days or ends_at", plan.ID)
		}
		ends = now.Add(time.Duration(days) * 24 * time.Hour)
	}
	if !ends.After(now) || ends.Sub(now) > 366*24*time.Hour {
		return badRequest("the trial end must be in the future and within a year")
	}
	if s.saas.d.Catalog.TrialFallback(plan) == plan.ID {
		return badRequest("plan %q is the trial fallback plan; set trial_fallback_plan in the catalog", plan.ID)
	}
	out, err := st.StartTrialWithPlan(ctx, l.OrgID, plan.ID, ends, reason, s.operatorActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) operatorResetNotifications(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	reason, err := s.decodeReason(r)
	if err != nil {
		return err
	}
	period := usage.PeriodOf(s.now())
	n, err := s.saas.d.Store.ResetQuotaNotifications(r.Context(), r.PathValue("org"), period.Start, reason, s.operatorActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n, "period": period.ID()})
	return nil
}

func (s *Server) operatorForceLogout(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	reason, err := s.decodeReason(r)
	if err != nil {
		return err
	}
	n, err := s.saas.d.Store.ForceLogout(r.Context(), r.PathValue("org"), reason, s.operatorActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions_revoked": n})
	return nil
}

func (s *Server) operatorResendVerification(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	reason, err := s.decodeReason(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	orgID, owners, err := s.saas.d.Store.UnverifiedOwners(ctx, r.PathValue("org"))
	if err != nil {
		return err
	}
	if len(owners) == 0 {
		return &apiError{http.StatusConflict, "failed_precondition", "every owner has already confirmed their e-mail address"}
	}
	sent := []string{}
	for _, o := range owners {
		if err := s.accounts.SendVerificationTo(ctx, o.UserID); err != nil {
			return err
		}
		sent = append(sent, o.Email)
	}
	if err := s.saas.d.Store.Audit(ctx, orgID, s.operatorActor(r, p), "org.owner_verification_resend", "organization", orgID,
		map[string]any{"recipients": sent, "reason": reason}); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent_to": sent})
	return nil
}

func (s *Server) operatorStartSupport(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	reason, err := s.decodeReason(r)
	if err != nil {
		return err
	}
	ss, err := s.saas.d.Store.StartSupportSession(r.Context(), r.PathValue("org"), reason, p.SessionID, s.saas.d.SupportTTL, s.operatorActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, ss)
	return nil
}

func (s *Server) operatorListSupport(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	out, err := s.saas.d.Store.ListOperatorSupportSessions(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"support_sessions": out})
	return nil
}

func (s *Server) operatorEndSupport(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	ss, err := s.saas.d.Store.EndSupportSession(r.Context(), r.PathValue("id"), s.operatorActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, ss)
	return nil
}

func (s *Server) operatorSupportView(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var req struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	path := strings.TrimSpace(req.Path)
	if path == "" || len(path) > 2048 || !strings.HasPrefix(path, "/") {
		return badRequest("path must be an absolute UI path (at most 2048 characters)")
	}
	ss, err := s.saas.d.Store.ResolveSupportSession(r.Context(), r.PathValue("id"), p.UserID, p.SessionID, s.now())
	if err != nil {
		return err
	}
	if err := s.saas.d.Store.Audit(r.Context(), ss.OrgID, s.operatorActor(r, p), "support.page_view", "support_session", ss.ID, map[string]any{"path": path}); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) operatorListFlags(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			return badRequest("limit must be between 1 and 500")
		}
		limit = n
	}
	flags, err := s.saas.d.Store.ListFlags(r.Context(), r.URL.Query().Get("status"), limit)
	if errors.Is(err, operator.ErrInvalidState) {
		return badRequest("status must be open, dismissed, actioned or all")
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"flags": flags})
	return nil
}

func (s *Server) operatorResolveFlag(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return notFound("flag not found")
	}
	var req struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.Status != "dismissed" && req.Status != "actioned" {
		return badRequest("status must be dismissed or actioned")
	}
	note, err := cleanReason(req.Note)
	if err != nil {
		return badRequest("note is required (3-1000 characters)")
	}
	f, err := s.saas.d.Store.ResolveFlag(r.Context(), id, req.Status, note, s.operatorActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, f)
	return nil
}

// ---- organization side ----

func (s *Server) orgSaaSState(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if !p.HasOrg() {
		return &apiError{http.StatusForbidden, "permission_denied", "you are not a member of any organization"}
	}
	l, err := s.saas.d.Store.GetLifecycle(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	now := s.now()
	ss := supportSessionFrom(r.Context())
	resp := map[string]any{"saas_mode": s.saas.d.SaaS, "suspended": l.Suspended, "trial": nil, "support_access": nil, "support_session": nil,
		"can_manage_support_access": allowed(p, auth.ActManageSupportAccess) && ss == nil}
	if l.TrialActive() {
		plan := s.saas.d.Catalog.Resolve(l.TrialPlanID)
		resp["trial"] = map[string]any{"plan_id": l.TrialPlanID, "plan_name": plan.Name, "ends_at": formatTime(*l.TrialEndsAt),
			"fallback_plan_id": s.saas.d.Catalog.TrialFallback(plan)}
	}
	if l.SupportAccessActive(now) {
		resp["support_access"] = map[string]any{"until": formatTime(*l.SupportAccessUntil), "granted_at": optTime(l.SupportAccessGrantedAt)}
	}
	if ss != nil {
		resp["support_session"] = map[string]any{"id": ss.ID, "operator_email": ss.OperatorEmail, "expires_at": formatTime(ss.ExpiresAt), "org_name": ss.OrgName}
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func (s *Server) requireOwnerSession(r *http.Request, p *auth.Principal) error {
	if !allowed(p, auth.ActManageSupportAccess) || supportSessionFrom(r.Context()) != nil {
		return &apiError{http.StatusForbidden, "permission_denied", "only owners can manage openlog support access"}
	}
	return nil
}

func (s *Server) grantSupportAccess(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := s.requireOwnerSession(r, p); err != nil {
		return err
	}
	var req struct {
		Duration string `json:"duration"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	d, ok := operator.SupportAccessDurations[req.Duration]
	if !ok {
		return badRequest("duration must be 24h or 7d")
	}
	if _, err := s.saas.d.Store.GrantSupportAccess(r.Context(), p.OrgID, d, s.operatorActor(r, p)); err != nil {
		return err
	}
	return s.orgSaaSState(w, r, p)
}

func (s *Server) revokeSupportAccess(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := s.requireOwnerSession(r, p); err != nil {
		return err
	}
	if _, err := s.saas.d.Store.RevokeSupportAccess(r.Context(), p.OrgID, s.operatorActor(r, p)); err != nil {
		return err
	}
	return s.orgSaaSState(w, r, p)
}
