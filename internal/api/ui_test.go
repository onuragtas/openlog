package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/onuragtas/openlog/web"
)

// TestUIMount verifies that the embedded UI is served for non-API paths while
// /api paths keep their JSON errors and authentication.
func TestUIMount(t *testing.T) {
	s, _ := newTestServer(t)
	s.SetUI(web.Handler(fstest.MapFS{
		"index.html":       {Data: []byte("<html>ui</html>")},
		"assets/app-1a.js": {Data: []byte("js")},
	}))
	h := s.srv.Handler

	do := func(path string, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if key != "" {
			req.Header.Set("openlog-license-key", key)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := do("/", ""); rec.Code != 200 || rec.Body.String() != "<html>ui</html>" {
		t.Fatalf("/ = %d %q", rec.Code, rec.Body.String())
	}
	if rec := do("/hosts/abc?tab=services", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "ui") {
		t.Fatalf("SPA fallback = %d %q", rec.Code, rec.Body.String())
	}
	if rec := do("/assets/app-1a.js", ""); rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset = %d cache %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if rec := do("/api/v1/nope", "key-a"); rec.Code != 404 || !strings.Contains(rec.Body.String(), `"not_found"`) {
		t.Fatalf("unknown api = %d %q", rec.Code, rec.Body.String())
	}
	if rec := do("/api/v1/hosts", ""); rec.Code != 401 || !strings.Contains(rec.Body.String(), `"unauthenticated"`) {
		t.Fatalf("unauthenticated api = %d %q", rec.Code, rec.Body.String())
	}
}

// TestNoUIByDefault verifies that without SetUI, "/" is not served.
func TestNoUIByDefault(t *testing.T) {
	s, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("/ without UI = %d, want 404", rec.Code)
	}
}
