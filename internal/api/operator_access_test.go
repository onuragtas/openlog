package api

import (
	"net/http"
	"testing"
)

func TestSuspendedAndSupportAllowlists(t *testing.T) {
	for _, tc := range []struct {
		method, path      string
		readOnly, allowed bool
	}{
		{http.MethodPost, "/api/v1/query", true, true},
		{http.MethodPost, "/api/v1/query/validate", true, true},
		{http.MethodPost, "/api/v1/alerts/rules/preview", true, true},
		{http.MethodPost, "/api/v1/alerts/templates/host-cpu/render", true, true},
		{http.MethodPut, "/api/v1/query", false, false},
		{http.MethodPost, "/api/v1/auth/logout", false, true},
		{http.MethodDelete, "/api/v1/sessions/abc", false, true},
		{http.MethodPut, "/api/v1/orgs/current/support-access", false, true},
		{http.MethodGet, "/api/v1/usage/export", false, true},
		{http.MethodPost, "/api/v1/data-exports", false, true},
		{http.MethodPost, "/api/v1/account/data-exports", false, true},
		{http.MethodPost, "/api/v1/orgs/current/deletion", false, true},
		{http.MethodPost, "/api/v1/org-deletions/abc/cancel", false, true},
		{http.MethodPost, "/api/v1/license-keys", false, false},
		{http.MethodPost, "/api/v1/invitations", false, false},
		{http.MethodPost, "/api/v1/dashboards", false, false},
		{http.MethodPatch, "/api/v1/orgs/current", false, false},
	} {
		if got := readOnlyPOST(tc.method, tc.path); got != tc.readOnly {
			t.Errorf("readOnlyPOST(%s %s) = %v", tc.method, tc.path, got)
		}
		if got := allowedWhileSuspended(tc.method, tc.path); got != tc.allowed {
			t.Errorf("allowedWhileSuspended(%s %s) = %v", tc.method, tc.path, got)
		}
	}
}

func TestCleanReason(t *testing.T) {
	if _, err := cleanReason("  x "); err == nil {
		t.Error("short reason accepted")
	}
	if v, err := cleanReason("  fraud report #12 "); err != nil || v != "fraud report #12" {
		t.Errorf("%q %v", v, err)
	}
}
