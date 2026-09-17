package ingest

import (
	"net/http"
	"strings"
)

// CORS for browser OTLP/HTTP senders (docs/contracts/config.md, OPENLOG_INGEST_CORS_ALLOWED_ORIGINS).
// Authentication is by license key header, never cookies, so credentials are not allowed.

// corsDefaultHeaders are allowed in preflights when the browser does not list any.
const corsDefaultHeaders = "Content-Type, Content-Encoding, Openlog-License-Key, X-Api-Key, Authorization, Traceparent, Tracestate"

type corsPolicy struct {
	any      bool
	exact    map[string]bool
	wildcard []string // "https://.example.com": scheme + "://" + "." + domain
}

// newCORSPolicy parses allowed origins: "*", exact origins ("https://app.example.com") and
// subdomain wildcards ("https://*.example.com"). It returns nil when CORS is disabled.
func newCORSPolicy(origins []string) *corsPolicy {
	p := &corsPolicy{exact: map[string]bool{}}
	for _, o := range origins {
		o = strings.ToLower(strings.TrimRight(strings.TrimSpace(o), "/"))
		switch {
		case o == "":
		case o == "*":
			p.any = true
		case strings.Contains(o, "://*."):
			p.wildcard = append(p.wildcard, strings.Replace(o, "://*.", "://.", 1))
		default:
			p.exact[o] = true
		}
	}
	if !p.any && len(p.exact) == 0 && len(p.wildcard) == 0 {
		return nil
	}
	return p
}

func (p *corsPolicy) allowed(origin string) bool {
	o := strings.ToLower(strings.TrimRight(origin, "/"))
	if o == "" || o == "null" {
		return p.any && o != ""
	}
	if p.any || p.exact[o] {
		return true
	}
	for _, w := range p.wildcard {
		scheme, domain, _ := strings.Cut(w, "://") // domain starts with "."
		rest, ok := strings.CutPrefix(o, scheme+"://")
		if ok && strings.HasSuffix(rest, domain) && len(rest) > len(domain) && !strings.ContainsAny(rest[:len(rest)-len(domain)], "/:") {
			return true
		}
	}
	return false
}

// withCORS answers preflights and adds CORS response headers for allowed origins. Requests from
// other origins still reach next (non-browser clients send no Origin; a browser drops the response).
func withCORS(origins []string, next http.Handler) http.Handler {
	p := newCORSPolicy(origins)
	if p == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /v1/rum carries its own CORS (rum.go): its allowlist is the browser key's, not the operator's
		// server-wide one, so a RUM key works without OPENLOG_INGEST_CORS_ALLOWED_ORIGINS being set and is
		// not silently widened by it either.
		if isRUMPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Add("Vary", "Origin")
		preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
		if !p.allowed(origin) {
			if preflight {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		h.Set("Access-Control-Allow-Origin", origin)
		if preflight {
			h.Add("Vary", "Access-Control-Request-Method")
			h.Add("Vary", "Access-Control-Request-Headers")
			h.Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			if req := r.Header.Get("Access-Control-Request-Headers"); req != "" {
				h.Set("Access-Control-Allow-Headers", req)
			} else {
				h.Set("Access-Control-Allow-Headers", corsDefaultHeaders)
			}
			h.Set("Access-Control-Max-Age", "7200")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.Set("Access-Control-Expose-Headers", "Retry-After")
		next.ServeHTTP(w, r)
	})
}
