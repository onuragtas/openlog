package api

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/updatemsg"
	"github.com/onuragtas/openlog/internal/updatereq"
	"github.com/onuragtas/openlog/internal/version"
	lib "github.com/onuragtas/openlog/libs/release"
)

// SetUpdateRequests enables POST /api/v1/version/check and /update ("Check now" / "Update now",
// docs/contracts/api.md "Version") and update_requests in GET /api/v1/version. checkNow (nil when
// OPENLOG_UPDATE_CHECK=disabled) runs the api release check immediately. Needs SetAccounts; must be
// called before Run.
func (s *Server) SetUpdateRequests(q updatereq.Queue, checkNow func(ctx context.Context) error) {
	s.updates, s.checkNow = q, checkNow
	s.srv.Handler = s.Handler()
}

// updateAllowed reports whether p may request checks and updates of this installation.
//
//   - Single-organization installations (OPENLOG_SIGNUP_ENABLED unset/false): a signed-in admin or owner.
//   - Multi-tenant installations (OPENLOG_SIGNUP_ENABLED=true), where an organization's admins are not the operators of
//     the server: only superadmins (a signed-in user with a verified e-mail in OPENLOG_SUPERADMIN_EMAILS), whatever
//     their role in the current organization.
//
// superadmin reports that the permission comes from OPENLOG_SUPERADMIN_EMAILS (recorded in the audit entry).
func (s *Server) updateAllowed(p *auth.Principal) (superadmin bool, ae *apiError) {
	// Updating the backend is user-only whatever an API key's role is; the check is stated
	// here as well as in the matrix so a key gets that answer on both paths below.
	if err := auth.Allow(p, auth.Permission{UserOnly: true}); err != nil {
		return false, denyError(err)
	}
	if s.accounts.Config().SignupEnabled {
		if s.isSuperadmin(p) {
			return true, nil
		}
		return false, &apiError{http.StatusForbidden, "permission_denied",
			"server updates can only be requested by superadmins (OPENLOG_SUPERADMIN_EMAILS, verified e-mail) when OPENLOG_SIGNUP_ENABLED=true"}
	}
	if ae := authorize(p, auth.ActRequestUpdate); ae != nil {
		return false, ae
	}
	return false, nil
}

// updateAuditDetails adds the superadmin flag to the details of an update request audit entry (only when the
// permission came from OPENLOG_SUPERADMIN_EMAILS, so entries of single-organization installations are unchanged).
func updateAuditDetails(details map[string]any, superadmin bool) map[string]any {
	if !superadmin {
		return details
	}
	if details == nil {
		details = map[string]any{}
	}
	details["superadmin"] = true
	return details
}

func (s *Server) updateRoutes(mux *http.ServeMux) {
	if s.updates == nil || s.accounts == nil {
		return
	}
	route := func(pattern string, h func(w http.ResponseWriter, r *http.Request, p *auth.Principal, superadmin bool) error) {
		mux.Handle(pattern, s.instrument(pattern, func(rec *statusRecorder, r *http.Request) {
			noStore(rec)
			p, r := s.authenticate(rec, r)
			if p == nil {
				return
			}
			superadmin, ae := s.updateAllowed(p)
			if ae != nil {
				writeError(rec, ae)
				return
			}
			if err := h(rec, r, p, superadmin); err != nil {
				s.writeUpdateError(rec, pattern, err)
			}
		}))
	}
	route("POST /api/v1/version/check", s.requestUpdateCheck)
	route("POST /api/v1/version/update", s.requestUpdateApply)
}

func (s *Server) writeUpdateError(w http.ResponseWriter, route string, err error) {
	var tooSoon *updatereq.TooSoonError
	var ae *apiError
	switch {
	case errors.As(err, &tooSoon):
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(tooSoon.RetryAfter.Seconds()))))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": map[string]string{"code": "resource_exhausted", "message": tooSoon.Error()}})
	case errors.Is(err, updatereq.ErrBusy):
		writeError(w, &apiError{http.StatusConflict, "failed_precondition", err.Error()})
	case errors.As(err, &ae):
		writeError(w, ae)
	case updatereq.IsUndefinedTable(err):
		writeError(w, &apiError{http.StatusServiceUnavailable, "unavailable", "update requests are not available until openlog-migrate has run (0009_update_requests)"})
	default:
		s.writeAccountError(w, route, err)
	}
}

func (s *Server) requestUpdateCheck(w http.ResponseWriter, r *http.Request, p *auth.Principal, superadmin bool) error {
	req := updatereq.Request{Action: updatereq.ActionCheck, OrgID: p.OrgID, RequestedBy: p.UserID, RequestedByEmail: p.Email}
	if err := s.updates.Enqueue(r.Context(), &req, updatereq.MinGap); err != nil {
		return err
	}
	s.accounts.Audit(r.Context(), p, s.accounts.Meta(r), "update.check_requested", "update_request", req.ID, updateAuditDetails(nil, superadmin))
	if s.checkNow != nil {
		cctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		if err := s.checkNow(cctx); err != nil {
			// The result (update_check "failed") is stored by the checker when it got that far.
			s.log.Warn("update check requested from the UI failed", "err", err)
		}
		cancel()
	}
	if inv, ok := s.versions.(interface{ Invalidate() }); ok {
		inv.Invalidate()
	}
	writeJSON(w, http.StatusOK, s.versionInfo(r, p))
	return nil
}

func (s *Server) requestUpdateApply(w http.ResponseWriter, r *http.Request, p *auth.Principal, superadmin bool) error {
	var in struct {
		TargetVersion           string `json:"target_version"`
		IgnoreMaintenanceWindow bool   `json:"ignore_maintenance_window"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	tv, err := lib.ParseVersion(strings.TrimSpace(in.TargetVersion))
	if err != nil {
		return badRequest("target_version: %v", err)
	}
	cur, err := lib.ParseVersion(version.String())
	if err != nil {
		cur = lib.Version{}
	}
	if !cur.Less(tv) {
		return &apiError{http.StatusConflict, "failed_precondition", "target_version must be newer than the running version " + version.String()}
	}
	var up struct {
		Engine string `json:"engine"`
		Mode   string `json:"mode"`
		State  string `json:"state"`
	}
	if s.versions != nil {
		if raw := s.versions.Info(r.Context()).Updater; len(raw) > 0 {
			_ = json.Unmarshal(raw, &up)
		}
	}
	switch {
	case up.Mode == "":
		return &apiError{http.StatusConflict, "failed_precondition", "no openlog-updater is reporting; start it (docs/operations/upgrading.md) or update manually"}
	case up.Mode == "off":
		return &apiError{http.StatusConflict, "failed_precondition", "openlog-updater runs with OPENLOG_UPDATER_MODE=off"}
	case up.State == "updating":
		return &apiError{http.StatusConflict, "failed_precondition", "an update is already running"}
	}
	req := updatereq.Request{Action: updatereq.ActionApply, TargetVersion: tv.String(), IgnoreMaintenanceWindow: in.IgnoreMaintenanceWindow,
		OrgID: p.OrgID, RequestedBy: p.UserID, RequestedByEmail: p.Email}
	if err := s.updates.Enqueue(r.Context(), &req, updatereq.MinGap); err != nil {
		return err
	}
	s.accounts.Audit(r.Context(), p, s.accounts.Meta(r), "update.apply_requested", "update_request", req.ID, updateAuditDetails(map[string]any{
		"from": version.String(), "to": req.TargetVersion, "ignore_maintenance_window": req.IgnoreMaintenanceWindow, "engine": up.Engine,
	}, superadmin))
	writeJSON(w, http.StatusAccepted, updateRequestResponse(&req, true))
	return nil
}

type updateRequestJSON struct {
	ID                      string `json:"id"`
	Action                  string `json:"action"`
	TargetVersion           string `json:"target_version"`
	IgnoreMaintenanceWindow bool   `json:"ignore_maintenance_window"`
	State                   string `json:"state"`
	Message                 string `json:"message"`
	// MessageCode/MessageParams identify a fixed updater message for translation (derived from
	// Message, internal/updatemsg); omitted for free text.
	MessageCode      string            `json:"message_code,omitempty"`
	MessageParams    map[string]string `json:"message_params,omitempty"`
	RequestedByEmail *string           `json:"requested_by_email"`
	RequestedAt      string            `json:"requested_at"`
	PickedAt         *string           `json:"picked_at"`
	FinishedAt       *string           `json:"finished_at"`
}

// updateRequestResponse renders a request; the requester's email only for principals that may
// request updates themselves (the version endpoint is readable by every member).
func updateRequestResponse(r *updatereq.Request, withEmail bool) *updateRequestJSON {
	if r == nil {
		return nil
	}
	out := &updateRequestJSON{ID: r.ID, Action: r.Action, TargetVersion: r.TargetVersion, IgnoreMaintenanceWindow: r.IgnoreMaintenanceWindow,
		State: r.State, Message: r.Message, RequestedAt: formatTime(r.RequestedAt), PickedAt: optTime(r.PickedAt), FinishedAt: optTime(r.FinishedAt)}
	if code, params, ok := updatemsg.Parse(r.Message); ok {
		out.MessageCode, out.MessageParams = code, params
	}
	if withEmail {
		out.RequestedByEmail = optString(r.RequestedByEmail)
	}
	return out
}

type updateRequestsJSON struct {
	CanRequest       bool               `json:"can_request"`
	UpdaterListening bool               `json:"updater_listening"`
	UpdaterPolledAt  *string            `json:"updater_polled_at"`
	Latest           *updateRequestJSON `json:"latest"`
}

// updateRequestsInfo is update_requests in GET /api/v1/version (nil without the request channel).
func (s *Server) updateRequestsInfo(ctx context.Context, p *auth.Principal) *updateRequestsJSON {
	if s.updates == nil || s.accounts == nil {
		return nil
	}
	_, denied := s.updateAllowed(p)
	out := &updateRequestsJSON{CanRequest: denied == nil}
	lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if latest, err := s.updates.Latest(lctx); err == nil {
		out.Latest = updateRequestResponse(latest, out.CanRequest)
	} else if !updatereq.IsUndefinedTable(err) {
		s.log.Debug("cannot load update requests", "err", err)
	}
	if hb, err := s.updates.Heartbeat(lctx); err == nil && hb != nil {
		out.UpdaterListening = hb.Listening(s.now())
		t := formatTime(hb.PolledAt)
		out.UpdaterPolledAt = &t
	}
	return out
}
