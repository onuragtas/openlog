package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/updatecheck"
)

func TestOnboardingEndpoint(t *testing.T) {
	const orgID = "11111111-1111-1111-1111-111111111111"
	authn := switchAuth{
		"viewer": {Kind: auth.KindSession, UserID: "u1", OrgID: orgID, OrgName: "Acme", TenantID: "t1", Role: auth.RoleViewer},
		"admin":  {Kind: auth.KindSession, UserID: "u2", OrgID: orgID, OrgName: "Acme", TenantID: "t1", Role: auth.RoleAdmin},
		"noorg":  {Kind: auth.KindSession, UserID: "u3"},
	}
	newServer := func() *Server {
		return New(config.API{QueryTimeout: time.Second, MaxRows: 10}, query.New(&recordingConn{}, "openlog", time.Second), authn,
			slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	}
	get := func(s *Server, as string, mutate func(*http.Request)) (*httptest.ResponseRecorder, onboardingJSON) {
		req := httptest.NewRequest(http.MethodGet, "http://openlog.internal:8080/api/v1/onboarding", nil)
		req.Header.Set("X-Test-As", as)
		if mutate != nil {
			mutate(req)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		var body onboardingJSON
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec, body
	}

	s := newServer()
	if rec, _ := get(s, "nobody", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", rec.Code)
	}
	if rec, _ := get(s, "noorg", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("no organization: %d", rec.Code)
	}

	// Without configuration: everything is derived from the request host.
	rec, body := get(s, "viewer", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("viewer: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	want := onboardingJSON{
		UIURL:    onboardingEndpointJSON{URL: "http://openlog.internal:8080", Source: endpointFromRequest},
		OTLPHTTP: onboardingEndpointJSON{URL: "http://openlog.internal:4318", Source: endpointFromRequest},
		OTLPGRPC: onboardingEndpointJSON{URL: "http://openlog.internal:4317", Source: endpointFromRequest},
	}
	if body.UIURL != want.UIURL || body.OTLPHTTP != want.OTLPHTTP || body.OTLPGRPC != want.OTLPGRPC {
		t.Errorf("derived endpoints = %+v %+v %+v", body.UIURL, body.OTLPHTTP, body.OTLPGRPC)
	}
	if body.ReleaseChannel != "stable" || body.CORSEnabled || len(body.CORSOrigins) != 0 || body.AuthMode != "static" {
		t.Errorf("defaults: channel %q cors %v %v auth %q", body.ReleaseChannel, body.CORSEnabled, body.CORSOrigins, body.AuthMode)
	}
	if body.Organization != (onboardingOrgJSON{ID: orgID, TenantID: "t1", Name: "Acme"}) || body.Role != "viewer" {
		t.Errorf("organization = %+v role %q", body.Organization, body.Role)
	}
	if body.Features != (onboardingFeaturesJSON{}) {
		t.Errorf("features without accounts/fleet = %+v", body.Features)
	}
	if body.AgentVersion != nil { // test binaries are dev builds
		t.Errorf("agent_version = %q, want null for a dev build", *body.AgentVersion)
	}
	if body.AgentPackages != nil {
		t.Errorf("agent_packages = %+v, want null without agent_version", body.AgentPackages)
	}
	var raw map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	for _, k := range []string{"agent_version", "agent_packages", "cors_allowed_origins", "features", "server_version"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("response has no %q: %s", k, rec.Body)
		}
	}

	// Behind a TLS reverse proxy.
	_, body = get(s, "viewer", func(r *http.Request) {
		r.Header.Set("X-Forwarded-Proto", "https, http")
		r.Header.Set("X-Forwarded-Host", "openlog.example.com")
	})
	if body.UIURL.URL != "https://openlog.example.com" || body.OTLPHTTP.URL != "https://openlog.example.com:4318" || body.OTLPGRPC.URL != "https://openlog.example.com:4317" {
		t.Errorf("forwarded = %+v %+v %+v", body.UIURL, body.OTLPHTTP, body.OTLPGRPC)
	}
	// Malformed forwarded hosts are ignored.
	_, body = get(s, "viewer", func(r *http.Request) { r.Header.Set("X-Forwarded-Host", "evil.example/$(id)") })
	if body.UIURL.URL != "http://openlog.internal:8080" {
		t.Errorf("malformed X-Forwarded-Host used: %+v", body.UIURL)
	}
	// IPv6 literal.
	_, body = get(s, "viewer", func(r *http.Request) { r.Host = "[::1]:8080" })
	if body.OTLPHTTP.URL != "http://[::1]:4318" || body.UIURL.URL != "http://[::1]:8080" {
		t.Errorf("ipv6 = %+v %+v", body.UIURL, body.OTLPHTTP)
	}

	// OPENLOG_PUBLIC_URL: its host and scheme with the default ports.
	s = newServer()
	s.SetOnboarding(OnboardingConfig{PublicURL: "https://openlog.example.com/", Channel: "beta", CORSAllowedOrigins: []string{" https://app.example.com ", ""}})
	s.SetTailSampling(nil, true)
	s.SetVersionSource(fakeVersions{info: updatecheck.Info{LatestAvailable: &updatecheck.Available{Version: "0.9.2"}}})
	_, body = get(s, "admin", nil)
	if body.UIURL != (onboardingEndpointJSON{URL: "https://openlog.example.com", Source: endpointConfigured}) ||
		body.OTLPHTTP != (onboardingEndpointJSON{URL: "https://openlog.example.com:4318", Source: endpointFromPublic}) ||
		body.OTLPGRPC != (onboardingEndpointJSON{URL: "https://openlog.example.com:4317", Source: endpointFromPublic}) {
		t.Errorf("public url = %+v %+v %+v", body.UIURL, body.OTLPHTTP, body.OTLPGRPC)
	}
	if body.ReleaseChannel != "beta" || !body.CORSEnabled || len(body.CORSOrigins) != 1 || body.CORSOrigins[0] != "https://app.example.com" {
		t.Errorf("channel %q cors %v %v", body.ReleaseChannel, body.CORSEnabled, body.CORSOrigins)
	}
	if body.AgentVersion == nil || *body.AgentVersion != "0.9.2" {
		t.Errorf("agent_version = %v, want 0.9.2", body.AgentVersion)
	}
	// No registry checker configured: unknown, and the release assets of agent_version.
	if p := body.AgentPackages; p == nil || p.Node.Registry != registryUnknown || p.Python.Registry != registryUnknown ||
		p.Dotnet.ReleaseAssetURL != "https://github.com/onuragtas/openlog/releases/download/v0.9.2/OpenLog.Agent.0.9.2.nupkg" {
		t.Errorf("agent_packages = %+v", p)
	}
	if !body.Features.TailSampling || body.Features.CanCreateLicenseKeys || body.Features.FleetPHPInstall {
		t.Errorf("features = %+v", body.Features)
	}

	// Configured ingest URLs win; gRPC falls back to the ingest host.
	s = newServer()
	s.SetOnboarding(OnboardingConfig{PublicURL: "https://openlog.example.com", IngestPublicURL: "https://ingest.example.com/otlp"})
	_, body = get(s, "viewer", nil)
	if body.OTLPHTTP != (onboardingEndpointJSON{URL: "https://ingest.example.com/otlp", Source: endpointConfigured}) ||
		body.OTLPGRPC != (onboardingEndpointJSON{URL: "https://ingest.example.com:4317", Source: endpointFromIngest}) {
		t.Errorf("ingest url = %+v %+v", body.OTLPHTTP, body.OTLPGRPC)
	}
	s.SetOnboarding(OnboardingConfig{IngestPublicURL: "https://ingest.example.com:4318", IngestPublicGRPCURL: "https://grpc.example.com:443"})
	_, body = get(s, "viewer", nil)
	if body.OTLPGRPC != (onboardingEndpointJSON{URL: "https://grpc.example.com:443", Source: endpointConfigured}) {
		t.Errorf("grpc url = %+v", body.OTLPGRPC)
	}
}

func TestReleaseVersion(t *testing.T) {
	for in, want := range map[string]string{"0.9.1": "0.9.1", "v1.2.3": "1.2.3", "1.0.0-rc.1+abc": "1.0.0-rc.1", "0.0.0-dev+abc": "", "": "", "nope": ""} {
		got := releaseVersion(in)
		if (got == nil && want != "") || (got != nil && *got != want) {
			t.Errorf("releaseVersion(%q) = %v, want %q", in, got, want)
		}
	}
}
