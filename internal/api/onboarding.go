package api

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/version"
	lib "github.com/onuragtas/openlog/libs/release"
)

// GET /api/v1/onboarding (docs/contracts/api.md "Onboarding"): what the web UI's "Add data" page needs to prefill
// agent install commands — public endpoints, release version and channel, CORS and feature flags. It never returns
// secrets: license key values are only shown once by POST /api/v1/license-keys.

// Default OTLP ports used when an endpoint is derived rather than configured.
const (
	onboardingHTTPPort = "4318"
	onboardingGRPCPort = "4317"
)

// Endpoint sources reported with every derived address, so the UI can ask operators to confirm guesses.
const (
	endpointConfigured  = "configured"         // OPENLOG_PUBLIC_URL / OPENLOG_INGEST_PUBLIC_URL / OPENLOG_INGEST_PUBLIC_GRPC_URL
	endpointFromPublic  = "derived_public_url" // host of OPENLOG_PUBLIC_URL with the default port
	endpointFromIngest  = "derived_ingest_url" // host of OPENLOG_INGEST_PUBLIC_URL with the default gRPC port
	endpointFromRequest = "derived_request"    // host the browser used to reach the API
)

// OnboardingConfig is the configuration behind GET /api/v1/onboarding.
type OnboardingConfig struct {
	PublicURL           string   // OPENLOG_PUBLIC_URL (web UI base URL)
	IngestPublicURL     string   // OPENLOG_INGEST_PUBLIC_URL (OTLP/HTTP)
	IngestPublicGRPCURL string   // OPENLOG_INGEST_PUBLIC_GRPC_URL (OTLP/gRPC)
	CORSAllowedOrigins  []string // OPENLOG_INGEST_CORS_ALLOWED_ORIGINS (browser OTLP senders)
	Channel             string   // OPENLOG_UPDATE_CHANNEL
	// PackageRegistries checks whether npm, PyPI and nuget.org serve the language agent packages (agentpackages.go);
	// nil reports "unknown" without network access, so the commands install from the GitHub release.
	PackageRegistries *PackageRegistryChecker
}

// SetOnboarding configures GET /api/v1/onboarding. Without it the endpoint derives everything from the request.
// Must be called before Run.
func (s *Server) SetOnboarding(c OnboardingConfig) {
	s.onboarding = &c
	s.srv.Handler = s.Handler()
}

type onboardingEndpointJSON struct {
	URL    string `json:"url"`
	Source string `json:"source"`
}

type onboardingOrgJSON struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	Name     string `json:"name"`
}

type onboardingFeaturesJSON struct {
	// LicenseKeys: the license key management endpoints exist (OPENLOG_AUTH_MODE=postgres).
	LicenseKeys bool `json:"license_keys"`
	// CanCreateLicenseKeys: this caller may create a key (signed-in admin or owner).
	CanCreateLicenseKeys bool `json:"can_create_license_keys"`
	// CanListLicenseKeys: this caller may list key names and prefixes (signed-in member or higher).
	CanListLicenseKeys bool `json:"can_list_license_keys"`
	// FleetPHPInstall: the fleet can install the PHP agent through infra agents (php-agent.md §7.3).
	FleetPHPInstall bool `json:"fleet_php_install"`
	// TailSampling: OPENLOG_TAILSAMPLING_ENABLED (D-075).
	TailSampling bool `json:"tail_sampling"`
}

type onboardingJSON struct {
	UIURL          onboardingEndpointJSON `json:"ui_url"`
	OTLPHTTP       onboardingEndpointJSON `json:"otlp_http"`
	OTLPGRPC       onboardingEndpointJSON `json:"otlp_grpc"`
	ServerVersion  string                 `json:"server_version"`
	AgentVersion   *string                `json:"agent_version"`
	ReleaseChannel string                 `json:"release_channel"`
	CORSEnabled    bool                   `json:"cors_enabled"`
	CORSOrigins    []string               `json:"cors_allowed_origins"`
	AuthMode       string                 `json:"auth_mode"`
	Organization   onboardingOrgJSON      `json:"organization"`
	Role           string                 `json:"role"`
	Features       onboardingFeaturesJSON `json:"features"`
	// AgentPackages: Node.js, Python and .NET agent packages of agent_version (registry availability, release
	// assets); null when agent_version is null.
	AgentPackages *onboardingPackagesJSON `json:"agent_packages"`
}

func (s *Server) onboardingRoutes(mux *http.ServeMux) {
	const pattern = "GET /api/v1/onboarding"
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
		writeJSON(rec, http.StatusOK, s.onboardingInfo(r, p))
	}))
}

func (s *Server) onboardingInfo(r *http.Request, p *auth.Principal) onboardingJSON {
	var c OnboardingConfig
	if s.onboarding != nil {
		c = *s.onboarding
	}
	ui, httpEP, grpcEP := deriveOnboardingEndpoints(c, r)
	channel := c.Channel
	if channel == "" {
		channel = "stable"
	}
	origins := make([]string, 0, len(c.CORSAllowedOrigins))
	for _, o := range c.CORSAllowedOrigins {
		if o = strings.TrimSpace(o); o != "" {
			origins = append(origins, o)
		}
	}
	authMode := "static"
	if s.accounts != nil {
		authMode = "postgres"
	}
	agentVersion := s.onboardingAgentVersion(r.Context())
	return onboardingJSON{
		UIURL: ui, OTLPHTTP: httpEP, OTLPGRPC: grpcEP,
		ServerVersion:  version.String(),
		AgentVersion:   agentVersion,
		AgentPackages:  agentPackages(r.Context(), c.PackageRegistries, agentVersion),
		ReleaseChannel: channel,
		CORSEnabled:    len(origins) > 0,
		CORSOrigins:    origins,
		AuthMode:       authMode,
		Organization:   onboardingOrgJSON{ID: p.OrgID, TenantID: p.TenantID, Name: p.OrgName},
		Role:           string(p.Role),
		Features: onboardingFeaturesJSON{
			LicenseKeys: s.accounts != nil,
			// Both actions are signed-in-user-only, so an API key sees them as unavailable.
			CanCreateLicenseKeys: s.accounts != nil && allowed(p, auth.ActManageLicenseKeys),
			CanListLicenseKeys:   s.accounts != nil && allowed(p, auth.ActListLicenseKeys),
			FleetPHPInstall:      s.fleet != nil,
			TailSampling:         s.tailSamplingConf().enabled,
		},
	}
}

// onboardingAgentVersion is the release install commands pin (Java jar URL, container image tag): the newest
// verified release of the channel when the release check found one, else this server's own release version.
// Development builds without a release check result report null (the UI then uses "latest" download URLs).
func (s *Server) onboardingAgentVersion(ctx context.Context) *string {
	if s.versions != nil {
		if a := s.versions.Info(ctx).LatestAvailable; a != nil && a.Version != "" {
			v := a.Version
			return &v
		}
	}
	return releaseVersion(version.String())
}

// releaseVersion returns v without build metadata when it is a SemVer release, nil for dev builds (0.0.0-dev…).
func releaseVersion(v string) *string {
	v, _, _ = strings.Cut(strings.TrimPrefix(strings.TrimSpace(v), "v"), "+")
	if v == "" || strings.HasPrefix(v, "0.0.0") {
		return nil
	}
	if _, err := lib.ParseVersion(v); err != nil {
		return nil
	}
	return &v
}

// deriveOnboardingEndpoints resolves the UI URL and the OTLP/HTTP and OTLP/gRPC endpoints: configured values win;
// otherwise the host of OPENLOG_PUBLIC_URL (or of the request) with the default ingest ports.
func deriveOnboardingEndpoints(c OnboardingConfig, r *http.Request) (ui, httpEP, grpcEP onboardingEndpointJSON) {
	reqScheme, reqHost := requestOrigin(r)

	if pub := strings.TrimRight(c.PublicURL, "/"); pub != "" {
		ui = onboardingEndpointJSON{URL: pub, Source: endpointConfigured}
	} else {
		ui = onboardingEndpointJSON{URL: reqScheme + "://" + reqHost, Source: endpointFromRequest}
	}

	// Scheme and host name the ingest ports are derived from.
	baseScheme, baseHost, baseSource := reqScheme, hostOnly(reqHost), endpointFromRequest
	if u, err := url.Parse(c.PublicURL); c.PublicURL != "" && err == nil && u.Hostname() != "" {
		baseScheme, baseHost, baseSource = u.Scheme, u.Hostname(), endpointFromPublic
	}

	if v := strings.TrimRight(c.IngestPublicURL, "/"); v != "" {
		httpEP = onboardingEndpointJSON{URL: v, Source: endpointConfigured}
		if u, err := url.Parse(v); err == nil && u.Hostname() != "" {
			baseScheme, baseHost, baseSource = u.Scheme, u.Hostname(), endpointFromIngest
		}
	} else {
		httpEP = onboardingEndpointJSON{URL: baseScheme + "://" + net.JoinHostPort(baseHost, onboardingHTTPPort), Source: baseSource}
	}

	if v := strings.TrimRight(c.IngestPublicGRPCURL, "/"); v != "" {
		grpcEP = onboardingEndpointJSON{URL: v, Source: endpointConfigured}
	} else {
		grpcEP = onboardingEndpointJSON{URL: baseScheme + "://" + net.JoinHostPort(baseHost, onboardingGRPCPort), Source: baseSource}
	}
	return ui, httpEP, grpcEP
}

// requestOrigin is the scheme and host (with port) the browser used: X-Forwarded-Proto/-Host from a reverse proxy
// when they are well-formed, else the request itself. The result is only echoed to the authenticated caller.
func requestOrigin(r *http.Request) (scheme, host string) {
	scheme = "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := strings.ToLower(firstHeaderValue(r.Header.Get("X-Forwarded-Proto"))); p == "http" || p == "https" {
		scheme = p
	}
	host = r.Host
	if h := firstHeaderValue(r.Header.Get("X-Forwarded-Host")); validHostPort(h) {
		host = h
	}
	if !validHostPort(host) {
		host = "localhost"
	}
	return scheme, host
}

func firstHeaderValue(v string) string {
	first, _, _ := strings.Cut(v, ",")
	return strings.TrimSpace(first)
}

func validHostPort(h string) bool {
	if h == "" || strings.ContainsAny(h, "/\\@?#'\"` \t$") {
		return false
	}
	u, err := url.Parse("http://" + h)
	return err == nil && u.Host == h && u.Hostname() != ""
}

// hostOnly strips the port of host:port, keeping IPv6 literals bare (JoinHostPort adds the brackets).
func hostOnly(hostport string) string {
	if u, err := url.Parse("http://" + hostport); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return hostport
}
