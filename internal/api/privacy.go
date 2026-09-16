package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/dataexport"
	"github.com/onuragtas/openlog/internal/deletion"
	mailtemplates "github.com/onuragtas/openlog/internal/mail/templates"
)

// Data subject requests (docs/contracts/api.md "Data export and deletion", D-107).
//
//	GET  /api/v1/account/privacy                    password/re-authentication info, export flag, scheduled deletions of owned orgs
//	GET  /api/v1/account/data-exports               personal exports · POST: request one
//	POST /api/v1/account/delete                     {"confirm_email", "password"?}: delete the account
//	GET  /api/v1/data-exports                       organization exports (owner) · POST {"from","to","signals"}: request one
//	GET  /api/v1/data-exports/{id}                  one export (owner of its organization, or the subject of a personal export)
//	GET  /api/v1/data-exports/{id}/download         the archive (same)
//	GET  /api/v1/data-exports/download?token=       the archive through the e-mailed, expiring link (no session)
//	POST /api/v1/orgs/current/deletion              {"confirm_name", "password"?}: schedule the current organization's deletion (owner)
//	POST /api/v1/org-deletions/{id}/cancel          cancel during the grace period (owner)
//	POST /api/v1/admin/orgs/{org}/deletion          {"reason", "immediate"?} (superadmin)
//	GET  /api/v1/admin/org-deletions                (superadmin) · POST /api/v1/admin/org-deletions/{id}/cancel
//	GET  /api/v1/admin/deletion-certificates?subject_hash=&limit=  (superadmin)

// ExportAPI queues and reads data exports (dataexport.Service).
type ExportAPI interface {
	RequestOrg(ctx context.Context, orgID, userID, locale string, from, to time.Time, signals []string) (dataexport.Export, error)
	RequestUser(ctx context.Context, userID, locale string) (dataexport.Export, error)
	Get(ctx context.Context, id string) (dataexport.Export, error)
	GetByToken(ctx context.Context, token string) (dataexport.Export, error)
	ListOrg(ctx context.Context, orgID string, limit int) ([]dataexport.Export, error)
	ListUser(ctx context.Context, userID string, limit int) ([]dataexport.Export, error)
	OpenArchive(ctx context.Context, e dataexport.Export) (io.ReadCloser, int64, error)
}

// DeletionAPI schedules and performs deletions (deletion.PGStore).
type DeletionAPI interface {
	ScheduleOrg(ctx context.Context, in deletion.ScheduleInput) (deletion.OrgDeletion, deletion.Notify, error)
	CancelOrg(ctx context.Context, id string, now time.Time) (deletion.OrgDeletion, deletion.Notify, error)
	Get(ctx context.Context, id string) (deletion.OrgDeletion, error)
	PendingForOwner(ctx context.Context, userID string) ([]deletion.OrgDeletion, error)
	List(ctx context.Context, limit int) ([]deletion.OrgDeletion, error)
	FindOrg(ctx context.Context, ref string) (id, tenantID, name string, deleted bool, err error)
	DeleteUser(ctx context.Context, userID, initiator string, now time.Time) (deletion.UserDeletion, error)
	ListCertificates(ctx context.Context, subjectHash string, limit int) ([]deletion.Certificate, error)
}

// PrivacyDeps enables the data subject request endpoints (postgres auth mode).
type PrivacyDeps struct {
	Exports   ExportAPI // nil: exports disabled (OPENLOG_DATA_EXPORT_ENABLED=false)
	Cleaner   deletion.ExportCleaner
	Deletions DeletionAPI
	Grace     time.Duration
	Mailer    auth.Mailer
	PublicURL string
}

// SetPrivacy enables the data export and deletion endpoints. Must be called before Run.
func (s *Server) SetPrivacy(d PrivacyDeps) {
	s.privacy = &d
	s.srv.Handler = s.Handler()
}

// reauthWindow: users without a password (single sign-on) confirm destructive operations with a session this recent.
const reauthWindow = 10 * time.Minute

func (s *Server) privacyRoutes(mux *http.ServeMux) {
	if s.privacy == nil || s.accounts == nil || s.privacy.Deletions == nil {
		return
	}
	session := func(pattern string, h accountFunc) {
		mux.Handle(pattern, s.instrument(pattern, func(rec *statusRecorder, r *http.Request) {
			noStore(rec)
			p, r := s.authenticate(rec, r)
			if p == nil {
				return
			}
			// Exports and deletions act on a person's own data and account, never on behalf of a key.
			if ae := authorize(p, auth.ActManageOwnAccount); ae != nil {
				writeError(rec, ae)
				return
			}
			if err := h(rec, r, p); err != nil {
				s.writePrivacyError(rec, pattern, err)
			}
		}))
	}
	super := func(pattern string, h accountFunc) {
		session(pattern, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
			if !s.isSuperadmin(p) {
				return &apiError{http.StatusForbidden, "permission_denied", "only openlog operators (OPENLOG_SUPERADMIN_EMAILS) can do this"}
			}
			return h(w, r, p)
		})
	}
	session("GET /api/v1/account/privacy", s.accountPrivacy)
	session("POST /api/v1/account/delete", s.deleteAccount)
	session("POST /api/v1/orgs/current/deletion", s.scheduleOrgDeletion)
	session("POST /api/v1/org-deletions/{id}/cancel", s.cancelOrgDeletion)
	super("POST /api/v1/admin/orgs/{org}/deletion", s.adminScheduleOrgDeletion)
	super("GET /api/v1/admin/org-deletions", s.adminListOrgDeletions)
	super("POST /api/v1/admin/org-deletions/{id}/cancel", s.adminCancelOrgDeletion)
	super("GET /api/v1/admin/deletion-certificates", s.adminListCertificates)
	if s.privacy.Exports == nil {
		return
	}
	session("GET /api/v1/account/data-exports", s.listPersonalExports)
	session("POST /api/v1/account/data-exports", s.requestPersonalExport)
	session("GET /api/v1/data-exports", s.listOrgExports)
	session("POST /api/v1/data-exports", s.requestOrgExport)
	session("GET /api/v1/data-exports/{id}", s.getExport)
	session("GET /api/v1/data-exports/{id}/download", s.downloadExport)
	mux.Handle("GET /api/v1/data-exports/download", s.instrument("GET /api/v1/data-exports/download", func(rec *statusRecorder, r *http.Request) {
		noStore(rec)
		if err := s.downloadExportByToken(rec, r); err != nil {
			s.writePrivacyError(rec, "GET /api/v1/data-exports/download", err)
		}
	}))
}

func (s *Server) writePrivacyError(w http.ResponseWriter, route string, err error) {
	var inv *dataexport.InvalidError
	var sole *deletion.SoleOwnerError
	switch {
	case errors.As(err, &inv):
		err = badRequest("%s", inv.Msg)
	case errors.As(err, &sole):
		err = &apiError{http.StatusConflict, "failed_precondition", sole.Error()}
	case errors.Is(err, dataexport.ErrNotFound), errors.Is(err, deletion.ErrNotFound):
		err = notFound("not found")
	case errors.Is(err, dataexport.ErrActive):
		err = &apiError{http.StatusConflict, "failed_precondition", dataexport.ErrActive.Error()}
	case errors.Is(err, dataexport.ErrRateLimited):
		err = &apiError{http.StatusTooManyRequests, "resource_exhausted", dataexport.ErrRateLimited.Error()}
	case errors.Is(err, deletion.ErrAlreadyScheduled):
		err = &apiError{http.StatusConflict, "already_exists", deletion.ErrAlreadyScheduled.Error()}
	case errors.Is(err, deletion.ErrNotCancellable):
		err = &apiError{http.StatusConflict, "failed_precondition", deletion.ErrNotCancellable.Error()}
	}
	s.writeAccountError(w, route, err)
}

func requireOwner(p *auth.Principal) error {
	if !allowed(p, auth.ActDeleteOrganization) {
		return &apiError{http.StatusForbidden, "permission_denied", "only owners of the organization can do this"}
	}
	return nil
}

// reauthenticate confirms a destructive operation: the current password, or for users without one a session created
// within reauthWindow. Wrong passwords count toward a per-user limit.
func (s *Server) reauthenticate(ctx context.Context, r *http.Request, p *auth.Principal, password string) error {
	st := s.accounts.Store()
	now := s.now()
	u, err := st.GetUser(ctx, p.UserID)
	if err != nil {
		return err
	}
	if u.PasswordHash == "" {
		sessions, err := st.ListSessions(ctx, p.UserID, now)
		if err != nil {
			return err
		}
		for _, x := range sessions {
			if x.ID == p.SessionID && now.Sub(x.CreatedAt) <= reauthWindow {
				return nil
			}
		}
		return &apiError{http.StatusForbidden, "permission_denied", "sign in again with single sign-on, then confirm within 10 minutes"}
	}
	key := sha256.Sum256([]byte("reauth|" + p.UserID))
	n, err := st.CountLoginFailures(ctx, key[:], now.Add(-15*time.Minute))
	if err != nil {
		return err
	}
	if n >= 5 {
		return &apiError{http.StatusTooManyRequests, "resource_exhausted", "too many wrong passwords; try again later"}
	}
	if ok, _ := auth.VerifyPassword(u.PasswordHash, password); !ok {
		_ = st.AddLoginFailure(ctx, key[:], now)
		return &apiError{http.StatusForbidden, "permission_denied", "the password is incorrect"}
	}
	return nil
}

func (s *Server) hasPassword(ctx context.Context, userID string) (bool, error) {
	u, err := s.accounts.Store().GetUser(ctx, userID)
	return u.PasswordHash != "", err
}

// installationAudit records an operator or account event outside any organization.
func (s *Server) installationAudit(ctx context.Context, r *http.Request, actorID, actorEmail, action, targetType, targetID string, details map[string]any) {
	e := &auth.AuditEvent{ActorUserID: actorID, ActorEmail: actorEmail, Action: action, TargetType: targetType, TargetID: targetID,
		Details: details, IP: s.accounts.Meta(r).IP, CreatedAt: s.now()}
	if err := s.accounts.Store().AddAuditEvent(context.WithoutCancel(ctx), e); err != nil {
		s.log.Error("cannot write audit log", "action", action, "err", err)
	}
}

func mailLocale(p *auth.Principal, r *http.Request) string {
	return mailtemplates.Resolve(p.Language, mailtemplates.FromAcceptLanguage(r.Header.Get("Accept-Language")))
}

// ---- response shapes ----

type exportJSON struct {
	ID                string   `json:"id"`
	Kind              string   `json:"kind"`
	Status            string   `json:"status"`
	Signals           []string `json:"signals"`
	From              *string  `json:"from"`
	To                *string  `json:"to"`
	RequestedBy       string   `json:"requested_by"`
	SizeBytes         int64    `json:"size_bytes"`
	TelemetryRows     int64    `json:"telemetry_rows"`
	Truncated         bool     `json:"truncated"`
	Error             string   `json:"error"`
	CreatedAt         string   `json:"created_at"`
	StartedAt         *string  `json:"started_at"`
	CompletedAt       *string  `json:"completed_at"`
	ExpiresAt         *string  `json:"expires_at"`
	DownloadAvailable bool     `json:"download_available"`
}

func (s *Server) exportResponse(e dataexport.Export) exportJSON {
	return exportJSON{ID: e.ID, Kind: e.Kind, Status: e.Status, Signals: e.Signals, From: optTime(e.From), To: optTime(e.To), RequestedBy: e.UserEmail,
		SizeBytes: e.SizeBytes, TelemetryRows: e.TelemetryRows, Truncated: e.Truncated, Error: e.Error, CreatedAt: formatTime(e.CreatedAt),
		StartedAt: optTime(e.StartedAt), CompletedAt: optTime(e.CompletedAt), ExpiresAt: optTime(e.ExpiresAt),
		DownloadAvailable: e.Status == dataexport.StatusCompleted && e.ExpiresAt != nil && e.ExpiresAt.After(s.now())}
}

func (s *Server) exportsResponse(es []dataexport.Export) map[string]any {
	out := make([]exportJSON, 0, len(es))
	for _, e := range es {
		out = append(out, s.exportResponse(e))
	}
	return map[string]any{"exports": out}
}

type orgDeletionJSON struct {
	ID               string  `json:"id"`
	OrganizationID   *string `json:"organization_id"`
	OrganizationName string  `json:"organization_name"`
	TenantID         string  `json:"tenant_id"`
	Status           string  `json:"status"`
	Initiator        string  `json:"initiator"`
	Reason           string  `json:"reason,omitempty"`
	RequestedByEmail string  `json:"requested_by_email,omitempty"`
	RequestedAt      string  `json:"requested_at"`
	PurgeAfter       string  `json:"purge_after"`
	CancelledAt      *string `json:"cancelled_at"`
	StartedAt        *string `json:"started_at"`
	CompletedAt      *string `json:"completed_at"`
	Cancellable      bool    `json:"cancellable"`
	CertificateID    *string `json:"certificate_id"`
	LastError        string  `json:"last_error,omitempty"`
}

func orgDeletionResponse(d deletion.OrgDeletion, operator bool) orgDeletionJSON {
	out := orgDeletionJSON{ID: d.ID, OrganizationName: d.OrgName, TenantID: d.TenantID, Status: d.Status, Initiator: d.Initiator,
		RequestedAt: formatTime(d.RequestedAt), PurgeAfter: formatTime(d.PurgeAfter), CancelledAt: optTime(d.CancelledAt),
		StartedAt: optTime(d.StartedAt), CompletedAt: optTime(d.CompletedAt),
		Cancellable: d.Status == deletion.StatusScheduled && (operator || d.Initiator == deletion.InitiatorOwner)}
	if d.OrgID != "" {
		out.OrganizationID = &d.OrgID
	}
	if d.CertificateID != "" {
		out.CertificateID = &d.CertificateID
	}
	if operator {
		out.Reason, out.RequestedByEmail, out.LastError = d.Reason, d.RequestedByEmail, d.LastError
	}
	return out
}

// ---- account ----

func (s *Server) accountPrivacy(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	hasPW, err := s.hasPassword(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	ds, err := s.privacy.Deletions.PendingForOwner(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	out := make([]orgDeletionJSON, 0, len(ds))
	for _, d := range ds {
		out = append(out, orgDeletionResponse(d, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"has_password": hasPW, "data_export_enabled": s.privacy.Exports != nil,
		"org_deletion_grace_seconds": int64(s.privacy.Grace.Seconds()), "reauth_max_age_seconds": int64(reauthWindow.Seconds()), "org_deletions": out})
	return nil
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		ConfirmEmail string `json:"confirm_email"`
		Password     string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if auth.NormalizeEmail(in.ConfirmEmail) != auth.NormalizeEmail(p.Email) {
		return badRequest("type your e-mail address to confirm")
	}
	if err := s.reauthenticate(r.Context(), r, p, in.Password); err != nil {
		return err
	}
	res, err := s.privacy.Deletions.DeleteUser(r.Context(), p.UserID, deletion.InitiatorSelf, s.now())
	if err != nil {
		return err
	}
	if s.privacy.Cleaner != nil && len(res.Exports) > 0 {
		if err := s.privacy.Cleaner.DeleteObjects(context.WithoutCancel(r.Context()), res.Exports); err != nil {
			s.log.Warn("cannot delete personal export archives of a deleted account", "err", err)
		}
	}
	s.installationAudit(r.Context(), r, "", res.Pseudonym, "user.delete", "user", res.Pseudonym, map[string]any{"certificate_id": res.Certificate.ID})
	if m := s.privacy.Mailer; m != nil {
		msg := mailtemplates.AccountDeletion(res.Locale, mailtemplates.AccountDeletionData{DeletedAt: s.now()})
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := m.Send(ctx, auth.Mail{To: res.Email, Subject: msg.Subject, Text: msg.Text, HTML: msg.HTML}); err != nil {
				s.log.Warn("cannot send account deletion e-mail", "err", err)
			}
		}()
	}
	http.SetCookie(w, s.accounts.ClearedCookie())
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// ---- organization deletion ----

func (s *Server) notifyDeletion(n deletion.Notify, d mailtemplates.OrgDeletionData) {
	if s.privacy.Mailer == nil {
		return
	}
	d.OrgName = n.OrgName
	go deletion.SendOrgDeletionMail(context.Background(), s.privacy.Mailer, s.log, n, d)
}

func (s *Server) scheduleOrgDeletion(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := requireOwner(p); err != nil {
		return err
	}
	var in struct {
		ConfirmName string `json:"confirm_name"`
		Password    string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if strings.TrimSpace(in.ConfirmName) != p.OrgName {
		return badRequest("type the organization name to confirm")
	}
	if err := s.reauthenticate(r.Context(), r, p, in.Password); err != nil {
		return err
	}
	d, n, err := s.privacy.Deletions.ScheduleOrg(r.Context(), deletion.ScheduleInput{OrgID: p.OrgID, Initiator: deletion.InitiatorOwner,
		ActorUserID: p.UserID, ActorEmail: p.Email, Grace: s.privacy.Grace, Now: s.now()})
	if err != nil {
		return err
	}
	s.accounts.Audit(r.Context(), p, s.accounts.Meta(r), "org.deletion_schedule", "organization", p.OrgID,
		map[string]any{"deletion_id": d.ID, "purge_after": formatTime(d.PurgeAfter)})
	s.notifyDeletion(n, mailtemplates.OrgDeletionData{Stage: mailtemplates.StageScheduled, PurgeAt: d.PurgeAfter,
		Link: deletion.SettingsLink(s.privacy.PublicURL, "/settings/profile")})
	writeJSON(w, http.StatusAccepted, map[string]any{"deletion": orgDeletionResponse(d, false)})
	return nil
}

func (s *Server) cancelOrgDeletion(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	id := r.PathValue("id")
	owned, err := s.privacy.Deletions.PendingForOwner(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	var target *deletion.OrgDeletion
	for i := range owned {
		if owned[i].ID == id {
			target = &owned[i]
		}
	}
	if target == nil {
		return notFound("no scheduled deletion of an organization you own has this id")
	}
	if target.Initiator != deletion.InitiatorOwner {
		return &apiError{http.StatusForbidden, "permission_denied", "this deletion was scheduled by an openlog operator; contact support to cancel it"}
	}
	d, n, err := s.privacy.Deletions.CancelOrg(r.Context(), id, s.now())
	if err != nil {
		return err
	}
	pp := *p
	pp.OrgID = d.OrgID
	s.accounts.Audit(r.Context(), &pp, s.accounts.Meta(r), "org.deletion_cancel", "organization", d.OrgID, map[string]any{"deletion_id": d.ID})
	s.notifyDeletion(n, mailtemplates.OrgDeletionData{Stage: mailtemplates.StageCancelled})
	writeJSON(w, http.StatusOK, map[string]any{"deletion": orgDeletionResponse(d, false)})
	return nil
}

func (s *Server) adminScheduleOrgDeletion(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		Reason    string `json:"reason"`
		Immediate bool   `json:"immediate"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Reason == "" || len(in.Reason) > 1000 {
		return badRequest("reason is required (at most 1000 characters)")
	}
	orgID, tenantID, _, _, err := s.privacy.Deletions.FindOrg(r.Context(), r.PathValue("org"))
	if err != nil {
		return err
	}
	grace := s.privacy.Grace
	if in.Immediate {
		grace = 0
	}
	d, n, err := s.privacy.Deletions.ScheduleOrg(r.Context(), deletion.ScheduleInput{OrgID: orgID, Initiator: deletion.InitiatorOperator, Reason: in.Reason,
		ActorUserID: p.UserID, ActorEmail: p.Email, Grace: grace, Now: s.now()})
	if err != nil {
		return err
	}
	s.installationAudit(r.Context(), r, p.UserID, p.Email, "admin.org_deletion_schedule", "organization", orgID,
		map[string]any{"tenant_id": tenantID, "reason": in.Reason, "immediate": in.Immediate, "deletion_id": d.ID})
	s.notifyDeletion(n, mailtemplates.OrgDeletionData{Stage: mailtemplates.StageScheduled, PurgeAt: d.PurgeAfter, Operator: true})
	writeJSON(w, http.StatusAccepted, map[string]any{"deletion": orgDeletionResponse(d, true)})
	return nil
}

func (s *Server) adminCancelOrgDeletion(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	d, n, err := s.privacy.Deletions.CancelOrg(r.Context(), r.PathValue("id"), s.now())
	if err != nil {
		return err
	}
	s.installationAudit(r.Context(), r, p.UserID, p.Email, "admin.org_deletion_cancel", "organization", d.OrgID,
		map[string]any{"tenant_id": d.TenantID, "deletion_id": d.ID})
	s.notifyDeletion(n, mailtemplates.OrgDeletionData{Stage: mailtemplates.StageCancelled})
	writeJSON(w, http.StatusOK, map[string]any{"deletion": orgDeletionResponse(d, true)})
	return nil
}

func queryLimit(r *http.Request, def, max int) (int, error) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, badRequest("limit must be a positive integer")
	}
	return min(n, max), nil
}

func (s *Server) adminListOrgDeletions(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	limit, err := queryLimit(r, 100, 500)
	if err != nil {
		return err
	}
	ds, err := s.privacy.Deletions.List(r.Context(), limit)
	if err != nil {
		return err
	}
	out := make([]orgDeletionJSON, 0, len(ds))
	for _, d := range ds {
		out = append(out, orgDeletionResponse(d, true))
	}
	writeJSON(w, http.StatusOK, map[string]any{"deletions": out})
	return nil
}

func (s *Server) adminListCertificates(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	limit, err := queryLimit(r, 100, 500)
	if err != nil {
		return err
	}
	subject := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("subject_hash")))
	if subject != "" && !isHex(subject, 64) {
		return badRequest("subject_hash must be a hex sha256 of a tenant id or user id")
	}
	cs, err := s.privacy.Deletions.ListCertificates(r.Context(), subject, limit)
	if err != nil {
		return err
	}
	if cs == nil {
		cs = []deletion.Certificate{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"certificates": cs})
	return nil
}

// ---- exports ----

func (s *Server) listPersonalExports(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	es, err := s.privacy.Exports.ListUser(r.Context(), p.UserID, 20)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.exportsResponse(es))
	return nil
}

func (s *Server) requestPersonalExport(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	e, err := s.privacy.Exports.RequestUser(r.Context(), p.UserID, mailLocale(p, r))
	if err != nil {
		return err
	}
	e.UserEmail = p.Email
	s.installationAudit(r.Context(), r, p.UserID, p.Email, "user.data_export_request", "data_export", e.ID, nil)
	writeJSON(w, http.StatusAccepted, map[string]any{"export": s.exportResponse(e)})
	return nil
}

func (s *Server) listOrgExports(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := requireOwner(p); err != nil {
		return err
	}
	es, err := s.privacy.Exports.ListOrg(r.Context(), p.OrgID, 50)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.exportsResponse(es))
	return nil
}

func (s *Server) requestOrgExport(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := requireOwner(p); err != nil {
		return err
	}
	var in struct {
		From    string   `json:"from"`
		To      string   `json:"to"`
		Signals []string `json:"signals"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	var from, to time.Time
	if len(in.Signals) > 0 {
		var err error
		if from, err = parseTime(in.From); err != nil {
			return badRequest("from: %v", err)
		}
		if to, err = parseTime(in.To); err != nil {
			return badRequest("to: %v", err)
		}
	}
	e, err := s.privacy.Exports.RequestOrg(r.Context(), p.OrgID, p.UserID, mailLocale(p, r), from, to, in.Signals)
	if err != nil {
		return err
	}
	e.UserEmail = p.Email
	s.accounts.Audit(r.Context(), p, s.accounts.Meta(r), "data_export.request", "data_export", e.ID,
		map[string]any{"signals": e.Signals, "from": optTime(e.From), "to": optTime(e.To)})
	writeJSON(w, http.StatusAccepted, map[string]any{"export": s.exportResponse(e)})
	return nil
}

// visibleExport returns the export when p may read it: an owner of its organization (current organization) or the
// subject of a personal export. Anything else is not found.
func (s *Server) visibleExport(ctx context.Context, p *auth.Principal, id string) (dataexport.Export, error) {
	e, err := s.privacy.Exports.Get(ctx, id)
	if err != nil {
		return e, err
	}
	switch e.Kind {
	case dataexport.KindOrganization:
		if allowed(p, auth.ActReadOrgExports) && p.OrgID == e.OrgID {
			return e, nil
		}
	case dataexport.KindUser:
		if e.UserID == p.UserID {
			return e, nil
		}
	}
	return dataexport.Export{}, dataexport.ErrNotFound
}

func (s *Server) getExport(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	e, err := s.visibleExport(r.Context(), p, r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"export": s.exportResponse(e)})
	return nil
}

func (s *Server) streamArchive(w http.ResponseWriter, r *http.Request, e dataexport.Export) error {
	rc, size, err := s.privacy.Exports.OpenArchive(r.Context(), e)
	if err != nil {
		return err
	}
	defer rc.Close()
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", `attachment; filename="openlog-export-`+e.ID+`.zip"`)
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	if size > 0 {
		h.Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, rc); err != nil {
		s.log.Warn("data export download interrupted", "export_id", e.ID, "err", err)
	}
	return nil
}

func (s *Server) downloadExport(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	e, err := s.visibleExport(r.Context(), p, r.PathValue("id"))
	if err != nil {
		return err
	}
	if e.Kind == dataexport.KindOrganization {
		s.accounts.Audit(r.Context(), p, s.accounts.Meta(r), "data_export.download", "data_export", e.ID, nil)
	}
	return s.streamArchive(w, r, e)
}

func (s *Server) downloadExportByToken(w http.ResponseWriter, r *http.Request) error {
	e, err := s.privacy.Exports.GetByToken(r.Context(), r.URL.Query().Get("token"))
	if err != nil {
		return err
	}
	if e.Kind == dataexport.KindOrganization {
		s.accounts.Audit(r.Context(), &auth.Principal{OrgID: e.OrgID}, s.accounts.Meta(r), "data_export.download", "data_export", e.ID,
			map[string]any{"via": "link"})
	}
	return s.streamArchive(w, r, e)
}
