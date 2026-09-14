package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/operator"
)

// HeaderSupportSession selects an operator's read-only support view of an organization (D-105). It is honoured only
// for superadmin session users whose support session belongs to this very session and whose organization still grants
// support access; the principal then acts as a viewer of that organization and every request is audited there.
const HeaderSupportSession = "X-Openlog-Support-Session"

type supportCtxKey struct{}

func supportSessionFrom(ctx context.Context) *operator.SupportSession {
	ss, _ := ctx.Value(supportCtxKey{}).(*operator.SupportSession)
	return ss
}

// readOnlyPOST reports whether an unsafe request only reads data (queries and previews).
func readOnlyPOST(method, path string) bool {
	if method != http.MethodPost {
		return false
	}
	return path == "/api/v1/query" || path == "/api/v1/query/validate" || strings.HasSuffix(path, "/preview") ||
		(strings.HasPrefix(path, "/api/v1/alerts/templates/") && strings.HasSuffix(path, "/render"))
}

// allowedWhileSuspended: account, session and data export/deletion requests of members of a suspended organization
// (their sessions stay valid so they can take their data out).
func allowedWhileSuspended(method, path string) bool {
	if readOnlyPOST(method, path) {
		return true
	}
	for _, prefix := range []string{"/api/v1/auth/", "/api/v1/sessions", "/api/v1/orgs/current/support-access", "/api/v1/operator/",
		"/api/v1/account/", "/api/v1/data-exports", "/api/v1/orgs/current/deletion", "/api/v1/org-deletions/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return strings.Contains(path, "/export")
}

const supportViewDedupe = time.Minute

// saasGate applies support sessions and suspension read-only mode to an authenticated request.
func (s *Server) saasGate(r *http.Request, p *auth.Principal) (*auth.Principal, *http.Request, error) {
	path := r.URL.Path
	if id := strings.TrimSpace(r.Header.Get(HeaderSupportSession)); id != "" && !strings.HasPrefix(path, "/api/v1/operator/") &&
		!(unsafe(r.Method) && strings.HasPrefix(path, "/api/v1/auth/")) {
		if p.Kind != auth.KindSession || !s.isSuperadmin(p) {
			return p, r, &apiError{http.StatusForbidden, "permission_denied", "support sessions are for openlog operators only"}
		}
		ss, err := s.saas.d.Store.ResolveSupportSession(r.Context(), id, p.UserID, p.SessionID, s.now())
		if errors.Is(err, operator.ErrNotFound) || errors.Is(err, operator.ErrNoAccess) {
			return p, r, &apiError{http.StatusForbidden, "support_session_ended", "the support session has ended or the organization revoked support access"}
		}
		if err != nil {
			return p, r, err
		}
		if unsafe(r.Method) && !readOnlyPOST(r.Method, path) {
			return p, r, &apiError{http.StatusForbidden, "support_read_only", "support views are read-only"}
		}
		np := *p
		np.OrgID, np.OrgName, np.TenantID, np.Role = ss.OrgID, ss.OrgName, ss.TenantID, auth.RoleViewer
		s.auditSupportRequest(r, p, &ss)
		return &np, r.WithContext(context.WithValue(r.Context(), supportCtxKey{}, &ss)), nil
	}
	if unsafe(r.Method) && p.HasOrg() && s.orgSuspended(p.OrgID) && !s.isSuperadmin(p) && !allowedWhileSuspended(r.Method, path) {
		return p, r, &apiError{http.StatusForbidden, "org_suspended",
			"this organization is suspended: it is read-only and ingest is disabled; you can still sign in and export your data. Contact openlog support."}
	}
	return p, r, nil
}

func unsafe(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

// auditSupportRequest writes support.request into the organization's audit log, at most once a minute per
// (support session, method, path) on this pod.
func (s *Server) auditSupportRequest(r *http.Request, p *auth.Principal, ss *operator.SupportSession) {
	key := ss.ID + " " + r.Method + " " + r.URL.Path
	now := time.Now()
	st := s.saas
	st.viewMu.Lock()
	if at, ok := st.views[key]; ok && now.Sub(at) < supportViewDedupe {
		st.viewMu.Unlock()
		return
	}
	if len(st.views) > 10000 {
		for k, at := range st.views {
			if now.Sub(at) >= supportViewDedupe {
				delete(st.views, k)
			}
		}
	}
	st.views[key] = now
	st.viewMu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if err := st.d.Store.Audit(ctx, ss.OrgID, s.operatorActor(r, p), "support.request", "support_session", ss.ID,
		map[string]any{"method": r.Method, "path": r.URL.Path}); err != nil {
		s.log.Warn("cannot audit support request", "support_session_id", ss.ID, "err", err)
	}
}
