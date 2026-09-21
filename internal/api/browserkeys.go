package api

import (
	"net/http"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/rum"
)

// Browser key management (docs/contracts/api.md "Browser keys", rum.md §3, D-136).
//
// These sit next to the license key endpoints in account.go rather than under /rum, because what they manage
// is a credential, not telemetry: the same roles, the same audit trail, the same "signed-in user only" rule.
// The one thing they do differently is say out loud that the value is public (§3.5), so nobody stores it the
// way they would store a license key.

func (s *Server) browserKeyRoutes(mux *http.ServeMux) {
	if s.accounts == nil {
		return // static auth mode has no key management at all
	}
	authed := func(pattern string, h accountFunc) {
		mux.Handle(pattern, s.instrument(pattern, func(rec *statusRecorder, r *http.Request) {
			noStore(rec)
			p, r := s.authenticate(rec, r)
			if p == nil {
				return
			}
			if err := h(rec, r, p); err != nil {
				s.writeAccountError(rec, pattern, err)
			}
		}))
	}
	authed("GET /api/v1/browser-keys", s.listBrowserKeys)
	authed("POST /api/v1/browser-keys", s.createBrowserKey)
	authed("PUT /api/v1/browser-keys/{id}", s.updateBrowserKey)
	authed("DELETE /api/v1/browser-keys/{id}", s.revokeBrowserKey)
}

type browserKeyJSON struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Prefix             string   `json:"prefix"`
	ServiceName        string   `json:"service_name"`
	Environment        string   `json:"environment"`
	Kind               string   `json:"kind"`
	Origins            []string `json:"origins"`
	AppIDs             []string `json:"app_ids"`
	RateLimitPerMinute int      `json:"rate_limit_per_minute"`
	SampleRate         float64  `json:"sample_rate"`
	CreatedByEmail     string   `json:"created_by_email"`
	CreatedAt          string   `json:"created_at"`
	UpdatedAt          string   `json:"updated_at"`
	LastUsedAt         *string  `json:"last_used_at"`
	RevokedAt          *string  `json:"revoked_at"`
}

func browserKeyResponse(k auth.BrowserKey) browserKeyJSON {
	origins, appIDs := k.Origins, k.AppIDs
	if origins == nil {
		origins = []string{}
	}
	if appIDs == nil {
		appIDs = []string{}
	}
	kind := k.Kind
	if kind == "" {
		kind = auth.KeyKindBrowser // rows written before 0098_mobile_keys
	}
	return browserKeyJSON{
		ID: k.ID, Name: k.Name, Prefix: k.Prefix, ServiceName: k.ServiceName, Environment: k.Environment,
		Kind: kind, Origins: origins, AppIDs: appIDs,
		RateLimitPerMinute: k.RateLimitPerMinute, SampleRate: k.SampleRate,
		CreatedByEmail: k.CreatedByEmail, CreatedAt: formatTime(k.CreatedAt), UpdatedAt: formatTime(k.UpdatedAt),
		LastUsedAt: optTime(k.LastUsedAt), RevokedAt: optTime(k.RevokedAt),
	}
}

func (s *Server) listBrowserKeys(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	ks, err := s.accounts.ListBrowserKeys(r.Context(), p)
	if err != nil {
		return err
	}
	out := make([]browserKeyJSON, 0, len(ks))
	for _, k := range ks {
		out = append(out, browserKeyResponse(k))
	}
	writeJSON(w, http.StatusOK, map[string]any{"browser_keys": out})
	return nil
}

// browserKeyInputJSON is the create/update body.
type browserKeyInputJSON struct {
	Name               string   `json:"name"`
	ServiceName        string   `json:"service_name"`
	Environment        string   `json:"environment"`
	Kind               string   `json:"kind"`
	Origins            []string `json:"origins"`
	AppIDs             []string `json:"app_ids"`
	RateLimitPerMinute int      `json:"rate_limit_per_minute"`
	SampleRate         float64  `json:"sample_rate"`
}

// decodeBrowserKeyInput reads and validates the body. Origin syntax is checked here rather than in
// internal/auth because internal/quota already imports auth, so auth importing rum would close a cycle.
func decodeBrowserKeyInput(r *http.Request) (auth.BrowserKeyInput, error) {
	var in browserKeyInputJSON
	if err := decodeJSON(r, &in); err != nil {
		return auth.BrowserKeyInput{}, err
	}
	// The allowlist that is parsed is the one the kind calls for, and only that one: parsing both would
	// accept a key carrying two scopes, and internal/auth then has to decide which of them bounds it.
	out := auth.BrowserKeyInput{
		Name: in.Name, ServiceName: in.ServiceName, Environment: in.Environment, Kind: in.Kind,
		RateLimitPerMinute: in.RateLimitPerMinute, SampleRate: in.SampleRate,
	}
	switch in.Kind {
	case auth.KeyKindMobile:
		appIDs, err := rum.ParseAppIDs(in.AppIDs)
		if err != nil {
			return auth.BrowserKeyInput{}, badRequest("app_ids: %v", err)
		}
		out.AppIDs = appIDs
	case "", auth.KeyKindBrowser:
		out.Kind = auth.KeyKindBrowser
		origins, err := rum.ParseOrigins(in.Origins)
		if err != nil {
			return auth.BrowserKeyInput{}, badRequest("origins: %v", err)
		}
		out.Origins = origins
	default:
		return auth.BrowserKeyInput{}, badRequest("kind: must be %q or %q", auth.KeyKindBrowser, auth.KeyKindMobile)
	}
	return out, nil
}

func (s *Server) createBrowserKey(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	in, err := decodeBrowserKeyInput(r)
	if err != nil {
		return err
	}
	k, secret, err := s.accounts.CreateBrowserKey(r.Context(), p, in, s.accounts.Meta(r))
	if err != nil {
		return err
	}
	k.CreatedByEmail = p.Email
	// The value is returned here and, unlike a license key, is not a secret: it is about to be pasted into a
	// public web page. It is still shown once, so nobody treats the API as a place to read keys back from.
	writeJSON(w, http.StatusCreated, map[string]any{"browser_key": browserKeyResponse(k), "key": secret})
	return nil
}

func (s *Server) updateBrowserKey(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	in, err := decodeBrowserKeyInput(r)
	if err != nil {
		return err
	}
	k, err := s.accounts.UpdateBrowserKey(r.Context(), p, r.PathValue("id"), in, s.accounts.Meta(r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, browserKeyResponse(k))
	return nil
}

func (s *Server) revokeBrowserKey(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if _, err := s.accounts.RevokeBrowserKey(r.Context(), p, r.PathValue("id"), s.accounts.Meta(r)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
