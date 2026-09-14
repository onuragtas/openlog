package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/auth/memstore"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/dataexport"
	"github.com/onuragtas/openlog/internal/deletion"
	"github.com/onuragtas/openlog/internal/statuspage"
)

type fakeDeletions struct {
	scheduled []deletion.ScheduleInput
	pending   []deletion.OrgDeletion
	cancelled []string
	deleted   []string
	soleOwner bool
}

func (f *fakeDeletions) ScheduleOrg(_ context.Context, in deletion.ScheduleInput) (deletion.OrgDeletion, deletion.Notify, error) {
	f.scheduled = append(f.scheduled, in)
	d := deletion.OrgDeletion{ID: "del-1", OrgID: in.OrgID, TenantID: "tenant-a", OrgName: "Org A", Status: deletion.StatusScheduled,
		Initiator: in.Initiator, RequestedAt: in.Now, PurgeAfter: in.Now.Add(in.Grace)}
	return d, deletion.Notify{OrgName: "Org A"}, nil
}

func (f *fakeDeletions) CancelOrg(_ context.Context, id string, _ time.Time) (deletion.OrgDeletion, deletion.Notify, error) {
	f.cancelled = append(f.cancelled, id)
	return deletion.OrgDeletion{ID: id, Status: deletion.StatusCancelled, Initiator: deletion.InitiatorOwner}, deletion.Notify{}, nil
}

func (f *fakeDeletions) Get(context.Context, string) (deletion.OrgDeletion, error) {
	return deletion.OrgDeletion{}, deletion.ErrNotFound
}

func (f *fakeDeletions) PendingForOwner(context.Context, string) ([]deletion.OrgDeletion, error) {
	return f.pending, nil
}

func (f *fakeDeletions) List(context.Context, int) ([]deletion.OrgDeletion, error) {
	return f.pending, nil
}

func (f *fakeDeletions) FindOrg(_ context.Context, ref string) (string, string, string, bool, error) {
	if ref != "tenant-b" {
		return "", "", "", false, deletion.ErrNotFound
	}
	return "org-b-id", "tenant-b", "Org B", false, nil
}

func (f *fakeDeletions) DeleteUser(_ context.Context, userID, _ string, _ time.Time) (deletion.UserDeletion, error) {
	if f.soleOwner {
		return deletion.UserDeletion{}, &deletion.SoleOwnerError{Orgs: []deletion.OrgRef{{ID: "o", Name: "Org A"}}}
	}
	f.deleted = append(f.deleted, userID)
	return deletion.UserDeletion{Email: "owner@example.com", Pseudonym: "deleted-user-0123456789"}, nil
}

func (f *fakeDeletions) ListCertificates(context.Context, string, int) ([]deletion.Certificate, error) {
	return nil, nil
}

type fakeExports struct {
	byID      map[string]dataexport.Export
	requested []string
}

func (f *fakeExports) RequestOrg(_ context.Context, orgID, userID, _ string, from, to time.Time, signals []string) (dataexport.Export, error) {
	if len(signals) > 0 && !from.Before(to) {
		return dataexport.Export{}, &dataexport.InvalidError{Msg: "from must be before to"}
	}
	f.requested = append(f.requested, orgID+"/"+strings.Join(signals, ","))
	return dataexport.Export{ID: "e-new", Kind: dataexport.KindOrganization, OrgID: orgID, UserID: userID, Status: "pending", Signals: signals}, nil
}

func (f *fakeExports) RequestUser(_ context.Context, userID, _ string) (dataexport.Export, error) {
	return dataexport.Export{}, dataexport.ErrActive
}

func (f *fakeExports) Get(_ context.Context, id string) (dataexport.Export, error) {
	e, ok := f.byID[id]
	if !ok {
		return e, dataexport.ErrNotFound
	}
	return e, nil
}

func (f *fakeExports) GetByToken(_ context.Context, token string) (dataexport.Export, error) {
	if token != "olx_valid-token-000000000" {
		return dataexport.Export{}, dataexport.ErrNotFound
	}
	return f.byID["e-own"], nil
}

func (f *fakeExports) ListOrg(context.Context, string, int) ([]dataexport.Export, error) {
	return nil, nil
}

func (f *fakeExports) ListUser(context.Context, string, int) ([]dataexport.Export, error) {
	return nil, nil
}

func (f *fakeExports) OpenArchive(_ context.Context, e dataexport.Export) (io.ReadCloser, int64, error) {
	return io.NopCloser(strings.NewReader("PK-archive-" + e.ID)), int64(len("PK-archive-" + e.ID)), nil
}

type fakeStatus struct {
	incidents   []statuspage.Incident
	invalidated int
}

func (f *fakeStatus) Page(context.Context) (statuspage.Page, error) {
	return statuspage.Page{Status: statuspage.Operational, Components: []statuspage.ComponentPage{{ID: "ingest", Status: statuspage.Operational}}}, nil
}
func (f *fakeStatus) Invalidate() { f.invalidated++ }
func (f *fakeStatus) List(context.Context, int) ([]statuspage.Incident, error) {
	return f.incidents, nil
}
func (f *fakeStatus) Get(context.Context, string) (statuspage.Incident, error) {
	return statuspage.Incident{}, statuspage.ErrNotFound
}
func (f *fakeStatus) Create(_ context.Context, in statuspage.Incident, _ string, _ string, _ time.Time) (statuspage.Incident, error) {
	in.ID = "inc-1"
	f.incidents = append(f.incidents, in)
	return in, nil
}
func (f *fakeStatus) Update(_ context.Context, in statuspage.Incident, _ time.Time) (statuspage.Incident, error) {
	return in, nil
}
func (f *fakeStatus) AddUpdate(context.Context, string, string, string, time.Time) (statuspage.Incident, error) {
	return statuspage.Incident{}, statuspage.ErrNotFound
}
func (f *fakeStatus) Delete(context.Context, string) error { return statuspage.ErrNotFound }

func newPrivacyEnv(t *testing.T) (*accountEnv, *fakeDeletions, *fakeExports, *fakeStatus, auth.Membership) {
	t.Helper()
	st := memstore.New()
	svc := auth.NewService(st, auth.Config{CookieSecure: true, LoginMaxFailures: 50}, quietLog())
	ctx := context.Background()
	for _, spec := range []auth.BootstrapSpec{
		{TenantID: "tenant-a", OrgName: "Org A", OwnerEmail: "owner@example.com", OwnerPassword: ownerPassword},
		{TenantID: "tenant-b", OrgName: "Org B", OwnerEmail: "other@example.com", OwnerPassword: ownerPassword},
	} {
		if _, err := svc.Bootstrap(ctx, spec); err != nil {
			t.Fatal(err)
		}
	}
	conn := &recordingConn{}
	s := New(config.API{QueryTimeout: time.Second, MaxRows: 1000}, query.New(conn, "openlog", time.Second), svc, quietLog(), nil)
	s.SetAccounts(svc)
	s.SetUsage(UsageDeps{Superadmin: func(email string) bool { return email == "owner@example.com" }})
	fd := &fakeDeletions{}
	u, _ := st.GetUserByEmail(ctx, "owner@example.com")
	ms, _ := st.ListMemberships(ctx, u.ID)
	fe := &fakeExports{byID: map[string]dataexport.Export{
		"e-own":   {ID: "e-own", Kind: dataexport.KindOrganization, OrgID: ms[0].Org.ID, Status: dataexport.StatusCompleted},
		"e-other": {ID: "e-other", Kind: dataexport.KindOrganization, OrgID: "someone-else", Status: dataexport.StatusCompleted},
		"e-mine":  {ID: "e-mine", Kind: dataexport.KindUser, UserID: u.ID, Status: dataexport.StatusCompleted},
	}}
	fs := &fakeStatus{}
	s.SetPrivacy(PrivacyDeps{Exports: fe, Cleaner: nil, Deletions: fd, Grace: 7 * 24 * time.Hour})
	s.SetStatusPage(StatusPageDeps{Service: fs, Store: fs})
	return &accountEnv{h: s.srv.Handler, svc: svc, st: st, conn: conn}, fd, fe, fs, ms[0]
}

func TestOrgDeletionEndpoints(t *testing.T) {
	env, fd, _, _, m := newPrivacyEnv(t)
	c := env.login(t, "owner@example.com", ownerPassword)

	if rec := c.do("POST", "/api/v1/orgs/current/deletion", map[string]string{"confirm_name": "Org X", "password": ownerPassword}); rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong confirmation: %d %s", rec.Code, rec.Body)
	}
	if rec := c.do("POST", "/api/v1/orgs/current/deletion", map[string]string{"confirm_name": "Org A", "password": "wrong"}); rec.Code != http.StatusForbidden {
		t.Fatalf("wrong password: %d %s", rec.Code, rec.Body)
	}
	rec := c.do("POST", "/api/v1/orgs/current/deletion", map[string]string{"confirm_name": "Org A", "password": ownerPassword})
	if rec.Code != http.StatusAccepted || len(fd.scheduled) != 1 || fd.scheduled[0].OrgID != m.Org.ID || fd.scheduled[0].Initiator != deletion.InitiatorOwner ||
		fd.scheduled[0].Grace != 7*24*time.Hour {
		t.Fatalf("schedule: %d %s %+v", rec.Code, rec.Body, fd.scheduled)
	}
	body := decode[map[string]map[string]any](t, rec)
	if body["deletion"]["cancellable"] != true || body["deletion"]["status"] != "scheduled" {
		t.Fatalf("deletion json %v", body)
	}

	// Cancelling needs a scheduled deletion of an organization the caller owns, created by an owner.
	if rec := c.do("POST", "/api/v1/org-deletions/del-1/cancel", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("cancel unknown: %d", rec.Code)
	}
	fd.pending = []deletion.OrgDeletion{{ID: "del-op", Initiator: deletion.InitiatorOperator, Status: deletion.StatusScheduled},
		{ID: "del-1", Initiator: deletion.InitiatorOwner, Status: deletion.StatusScheduled}}
	if rec := c.do("POST", "/api/v1/org-deletions/del-op/cancel", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("cancel operator deletion: %d", rec.Code)
	}
	if rec := c.do("POST", "/api/v1/org-deletions/del-1/cancel", nil); rec.Code != http.StatusOK || len(fd.cancelled) != 1 {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body)
	}
	priv := decode[map[string]any](t, c.do("GET", "/api/v1/account/privacy", nil))
	if priv["has_password"] != true || priv["data_export_enabled"] != true || len(priv["org_deletions"].([]any)) != 2 {
		t.Fatalf("privacy %v", priv)
	}

	// Operators (superadmins) delete other organizations with a reason.
	other := env.login(t, "other@example.com", ownerPassword)
	if rec := other.do("POST", "/api/v1/admin/orgs/tenant-b/deletion", map[string]any{"reason": "abuse"}); rec.Code != http.StatusForbidden {
		t.Fatalf("non-superadmin: %d", rec.Code)
	}
	if rec := c.do("POST", "/api/v1/admin/orgs/tenant-b/deletion", map[string]any{"reason": " "}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing reason: %d", rec.Code)
	}
	rec = c.do("POST", "/api/v1/admin/orgs/tenant-b/deletion", map[string]any{"reason": "phishing", "immediate": true})
	last := fd.scheduled[len(fd.scheduled)-1]
	if rec.Code != http.StatusAccepted || last.OrgID != "org-b-id" || last.Grace != 0 || last.Initiator != deletion.InitiatorOperator || last.Reason != "phishing" {
		t.Fatalf("operator schedule: %d %s %+v", rec.Code, rec.Body, last)
	}
}

func TestAccountDeletionEndpoint(t *testing.T) {
	env, fd, _, _, _ := newPrivacyEnv(t)
	c := env.login(t, "owner@example.com", ownerPassword)
	if rec := c.do("POST", "/api/v1/account/delete", map[string]string{"confirm_email": "nope@example.com", "password": ownerPassword}); rec.Code != http.StatusBadRequest {
		t.Fatalf("confirmation: %d", rec.Code)
	}
	fd.soleOwner = true
	rec := c.do("POST", "/api/v1/account/delete", map[string]string{"confirm_email": "Owner@Example.com", "password": ownerPassword})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "only owner of Org A") {
		t.Fatalf("sole owner: %d %s", rec.Code, rec.Body)
	}
	fd.soleOwner = false
	rec = c.do("POST", "/api/v1/account/delete", map[string]string{"confirm_email": "owner@example.com", "password": ownerPassword})
	if rec.Code != http.StatusNoContent || len(fd.deleted) != 1 || !strings.Contains(rec.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("delete: %d %v %q", rec.Code, fd.deleted, rec.Header().Get("Set-Cookie"))
	}
}

func TestExportEndpoints(t *testing.T) {
	env, _, fe, _, m := newPrivacyEnv(t)
	c := env.login(t, "owner@example.com", ownerPassword)
	rec := c.do("POST", "/api/v1/data-exports", map[string]any{"from": "2026-09-01T00:00:00Z", "to": "2026-09-02T00:00:00Z", "signals": []string{"logs"}})
	if rec.Code != http.StatusAccepted || len(fe.requested) != 1 || fe.requested[0] != m.Org.ID+"/logs" {
		t.Fatalf("request: %d %s %v", rec.Code, rec.Body, fe.requested)
	}
	if rec := c.do("POST", "/api/v1/data-exports", map[string]any{"from": "2026-09-02T00:00:00Z", "to": "2026-09-01T00:00:00Z", "signals": []string{"logs"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid range: %d", rec.Code)
	}
	if rec := c.do("POST", "/api/v1/account/data-exports", nil); rec.Code != http.StatusConflict {
		t.Fatalf("active personal export: %d", rec.Code)
	}
	for id, want := range map[string]int{"e-own": 200, "e-mine": 200, "e-other": 404, "missing": 404} {
		if rec := c.do("GET", "/api/v1/data-exports/"+id, nil); rec.Code != want {
			t.Errorf("GET %s: %d, want %d", id, rec.Code, want)
		}
	}
	rec = c.do("GET", "/api/v1/data-exports/e-own/download", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" || rec.Body.String() != "PK-archive-e-own" {
		t.Fatalf("download: %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	// Another organization's owner cannot read it.
	other := env.login(t, "other@example.com", ownerPassword)
	if rec := other.do("GET", "/api/v1/data-exports/e-own/download", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("foreign download: %d", rec.Code)
	}
	// The e-mailed link works without a session; a wrong token does not.
	anon := &client{t: t, h: env.h}
	if rec := anon.do("GET", "/api/v1/data-exports/download?token=olx_valid-token-000000000", nil); rec.Code != http.StatusOK || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("token download: %d", rec.Code)
	}
	if rec := anon.do("GET", "/api/v1/data-exports/download?token=olx_wrong-token-000000000", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("wrong token: %d", rec.Code)
	}
	if rec := anon.do("GET", "/api/v1/data-exports/e-own", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous export read: %d", rec.Code)
	}
}

func TestStatusPageEndpoints(t *testing.T) {
	env, _, _, fs, _ := newPrivacyEnv(t)
	anon := &client{t: t, h: env.h}
	rec := anon.do("GET", "/api/v1/status", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "public, max-age=30" || !strings.Contains(rec.Body.String(), `"status":"operational"`) {
		t.Fatalf("public status: %d %q %s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body)
	}
	if rec := anon.do("GET", "/api/v1/admin/status/incidents", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous admin: %d", rec.Code)
	}
	other := env.login(t, "other@example.com", ownerPassword)
	if rec := other.do("POST", "/api/v1/admin/status/incidents", map[string]any{"kind": "incident"}); rec.Code != http.StatusForbidden {
		t.Fatalf("non-superadmin: %d", rec.Code)
	}
	c := env.login(t, "owner@example.com", ownerPassword)
	if rec := c.do("POST", "/api/v1/admin/status/incidents", map[string]any{"kind": "incident", "title": "x", "status": "scheduled"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid incident: %d %s", rec.Code, rec.Body)
	}
	rec = c.do("POST", "/api/v1/admin/status/incidents", map[string]any{"kind": "incident", "title": "Delayed logs", "status": "investigating",
		"impact": "major", "components": []string{"processing"}, "message": "Looking into it"})
	if rec.Code != http.StatusCreated || len(fs.incidents) != 1 || fs.invalidated != 1 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if rec := c.do("DELETE", "/api/v1/admin/status/incidents/nope", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing: %d", rec.Code)
	}
}
