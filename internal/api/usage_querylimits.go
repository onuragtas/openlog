package api

import (
	"context"
	"net/http"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/quota"
)

// Organization query limits (docs/contracts/api.md "Usage and plans", usage.md §4.5):
//
//	GET    /api/v1/usage/query-limits   layers, effective limits and their sources (any role, API keys)
//	PUT    /api/v1/usage/query-limits   set the organization layer (see canManageQueryLimits)
//	DELETE /api/v1/usage/query-limits   remove it

// QueryLimitsStore persists organization query limit settings (quota.PGStore).
type QueryLimitsStore interface {
	GetOrgQueryLimits(ctx context.Context, orgID string) (quota.StoredOrgQueryLimits, bool, error)
	PutOrgQueryLimits(ctx context.Context, orgID string, l quota.OrgQueryLimits, actor quota.Actor) error
}

type queryLimitValuesJSON struct {
	MaxMemoryUsage int64 `json:"max_memory_usage"`
	MaxRowsToRead  int64 `json:"max_rows_to_read"`
	MaxBytesToRead int64 `json:"max_bytes_to_read"`
}

func toQueryLimitValues(l config.QueryLimits) queryLimitValuesJSON {
	return queryLimitValuesJSON{l.MaxMemoryUsage, l.MaxRowsToRead, l.MaxBytesToRead}
}

type orgQueryLimitSettingJSON struct {
	quota.OrgQueryLimits
	UpdatedAt string `json:"updated_at"`
	UpdatedBy string `json:"updated_by"`
}

type queryLimitsJSON struct {
	Defaults     queryLimitValuesJSON      `json:"defaults"`
	Plan         queryLimitValuesJSON      `json:"plan"`
	Organization *orgQueryLimitSettingJSON `json:"organization"`
	Environment  *queryLimitValuesJSON     `json:"environment"`
	Effective    queryLimitValuesJSON      `json:"effective"`
	Sources      quota.QueryLimitSources   `json:"sources"`
	CanManage    bool                      `json:"can_manage"`
	// RefreshSeconds is how long other api/alert pods may keep the previous limits after a change.
	RefreshSeconds int `json:"refresh_seconds"`
}

// canManageQueryLimits: superadmins always; owners (session users) unless OPENLOG_SAAS_MODE=true, where the limits are
// part of what the operator sells (D-080).
func (s *Server) canManageQueryLimits(p *auth.Principal) bool {
	if s.isSuperadmin(p) {
		return true
	}
	return !s.usage.SaaS && allowed(p, auth.ActManageQueryLimits)
}

func (s *Server) queryLimitsResponse(ctx context.Context, p *auth.Principal) (queryLimitsJSON, error) {
	u := s.usage
	op, err := s.orgPlan(ctx, p)
	if err != nil {
		return queryLimitsJSON{}, err
	}
	plan := u.Catalog.EffectivePlan(op).Limits.Query
	stored, found, err := u.QueryLimits.GetOrgQueryLimits(ctx, p.OrgID)
	if err != nil {
		return queryLimitsJSON{}, err
	}
	var env *config.QueryLimits
	if l, ok := u.Query.Tenants[p.TenantID]; ok {
		env = &l
	}
	eff, src := quota.ResolveQueryLimits(u.Query.Defaults, plan, stored.OrgQueryLimits, env)
	resp := queryLimitsJSON{
		Defaults:       toQueryLimitValues(u.Query.Defaults),
		Plan:           queryLimitValuesJSON{plan.MaxMemoryUsage, plan.MaxRowsToRead, plan.MaxBytesToRead},
		Effective:      toQueryLimitValues(eff),
		Sources:        src,
		CanManage:      s.canManageQueryLimits(p),
		RefreshSeconds: int(quota.DefaultQueryLimitsRefresh.Seconds()),
	}
	if found {
		resp.Organization = &orgQueryLimitSettingJSON{OrgQueryLimits: stored.OrgQueryLimits, UpdatedAt: formatTime(stored.UpdatedAt),
			UpdatedBy: stored.UpdatedByEmail}
	}
	if env != nil {
		v := toQueryLimitValues(*env)
		resp.Environment = &v
	}
	return resp, nil
}

func (s *Server) getQueryLimits(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	resp, err := s.queryLimitsResponse(r.Context(), p)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func (s *Server) putQueryLimits(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var req quota.OrgQueryLimits
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	return s.storeQueryLimits(w, r, p, req)
}

func (s *Server) deleteQueryLimits(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	return s.storeQueryLimits(w, r, p, quota.OrgQueryLimits{})
}

func (s *Server) storeQueryLimits(w http.ResponseWriter, r *http.Request, p *auth.Principal, l quota.OrgQueryLimits) error {
	if !s.canManageQueryLimits(p) {
		msg := "only organization owners can change query limits"
		if s.usage.SaaS {
			msg = "query limits are managed by the openlog operators (OPENLOG_SUPERADMIN_EMAILS)"
		}
		return &apiError{http.StatusForbidden, "permission_denied", msg}
	}
	if err := l.Validate(); err != nil {
		return badRequest("%v", err)
	}
	actor := quota.Actor{UserID: p.UserID, Email: p.Email, IP: auth.ClientIP(r, nil)}
	if err := s.usage.QueryLimits.PutOrgQueryLimits(r.Context(), p.OrgID, l, actor); err != nil {
		return err
	}
	if s.usage.QueryLimitsChanged != nil {
		s.usage.QueryLimitsChanged(r.Context())
	}
	s.log.Info("organization query limits changed", "org_id", p.OrgID, "by", p.Email, "cleared", l.Empty())
	resp, err := s.queryLimitsResponse(r.Context(), p)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}
