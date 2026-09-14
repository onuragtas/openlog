package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestSSOIdPLogoutEndpoints covers the HTTP shape of the logout endpoints the identity provider calls (OIDC
// back-channel and front-channel logout, SAML SOAP single logout) and the metadata confirmation (D-098); the
// validation itself is tested in internal/sso.
func TestSSOIdPLogoutEndpoints(t *testing.T) {
	e := newSSOEnv(t)
	admin := e.login(t, "admin@example.com")
	rec := admin.do(http.MethodPost, "/api/v1/sso/connections", map[string]any{"protocol": "oidc", "enabled": true, "default_role": "member",
		"oidc": map[string]any{"issuer": "http://127.0.0.1:1", "client_id": "openlog"}})
	var st struct {
		Connection      struct{ ID string } `json:"connection"`
		ServiceProvider map[string]any      `json:"service_provider"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	id := st.Connection.ID
	base := "https://openlog.example/api/v1/sso/oidc/" + id
	if st.ServiceProvider["oidc_backchannel_logout_uri"] != base+"/backchannel-logout" || st.ServiceProvider["oidc_frontchannel_logout_uri"] != base+"/frontchannel-logout" ||
		st.ServiceProvider["saml_slo_soap_url"] != nil {
		t.Fatalf("service provider values: %v", st.ServiceProvider)
	}
	serve := func(req *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}

	// Back-channel: form body only; a refused token is 400 with an OAuth-style error.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sso/oidc/"+id+"/backchannel-logout", strings.NewReader(`{"logout_token":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	if rec := serve(req); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"invalid_request"`) || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("back-channel with JSON: %d %s %v", rec.Code, rec.Body, rec.Header())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/sso/oidc/"+id+"/backchannel-logout", strings.NewReader(url.Values{"logout_token": {"a.b.c"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := serve(req); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "error_description") {
		t.Fatalf("back-channel with an invalid token: %d %s", rec.Code, rec.Body)
	}

	// Front-channel: an empty page only the issuer's origin may frame, never cached.
	rec = serve(httptest.NewRequest(http.MethodGet, "/api/v1/sso/oidc/"+id+"/frontchannel-logout?"+url.Values{"iss": {"http://127.0.0.1:1"}, "sid": {"unknown"}}.Encode(), nil))
	h := rec.Header()
	if rec.Code != http.StatusOK || !strings.HasSuffix(h.Get("Content-Security-Policy"), "frame-ancestors http://127.0.0.1:1") || h.Get("X-Frame-Options") != "" ||
		h.Get("Cache-Control") != "no-cache, no-store" || !strings.HasPrefix(h.Get("Content-Type"), "text/html") {
		t.Fatalf("front-channel: %d %v", rec.Code, h)
	}
	if rec := serve(httptest.NewRequest(http.MethodGet, "/api/v1/sso/oidc/"+id+"/frontchannel-logout", nil)); rec.Code != http.StatusBadRequest {
		t.Fatalf("front-channel without iss and sid: %d", rec.Code)
	}

	// SOAP: faults are text/xml with 500.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/sso/saml/"+id+"/slo/soap", strings.NewReader("<x/>"))
	req.Header.Set("Content-Type", "text/xml")
	if rec := serve(req); rec.Code != http.StatusInternalServerError || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/xml") ||
		!strings.Contains(rec.Body.String(), "Fault") {
		t.Fatalf("SOAP to an OIDC connection: %d %s", rec.Code, rec.Body)
	}

	// Metadata confirmation exists only for SAML connections with a metadata URL.
	if rec := admin.do(http.MethodPost, "/api/v1/sso/connections/"+id+"/metadata/accept", map[string]string{"digest": "x"}); rec.Code != http.StatusConflict {
		t.Fatalf("accept on an OIDC connection: %d %s", rec.Code, rec.Body)
	}
	member := e.login(t, "member@example.com")
	if rec := member.do(http.MethodPost, "/api/v1/sso/connections/"+id+"/metadata/accept", map[string]string{"digest": "x"}); rec.Code != http.StatusForbidden {
		t.Fatalf("member accept: %d", rec.Code)
	}
}
