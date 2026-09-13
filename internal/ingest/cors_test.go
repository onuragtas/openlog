package ingest

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func corsRequest(method, origin string, headers map[string]string) *httptest.ResponseRecorder {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := withCORS([]string{"https://iptv.resoft.org/", "https://*.example.com"}, next)
	r := httptest.NewRequest(method, "/v1/logs", nil)
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestCORSPreflight(t *testing.T) {
	w := corsRequest(http.MethodOptions, "https://iptv.resoft.org", map[string]string{
		"Access-Control-Request-Method":  "POST",
		"Access-Control-Request-Headers": "content-type,x-api-key",
	})
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d", w.Code)
	}
	h := w.Header()
	if h.Get("Access-Control-Allow-Origin") != "https://iptv.resoft.org" || h.Get("Access-Control-Allow-Headers") != "content-type,x-api-key" ||
		h.Get("Access-Control-Allow-Methods") != "POST, OPTIONS" || h.Get("Access-Control-Allow-Credentials") != "" {
		t.Errorf("headers = %v", h)
	}

	if w := corsRequest(http.MethodOptions, "https://evil.test", map[string]string{"Access-Control-Request-Method": "POST"}); w.Code != http.StatusForbidden || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("disallowed preflight = %d %v", w.Code, w.Header())
	}
}

func TestCORSActualRequests(t *testing.T) {
	cases := []struct {
		origin string
		allow  bool
	}{
		{"https://iptv.resoft.org", true},
		{"https://app.example.com", true},
		{"https://a.b.example.com", true},
		{"https://example.com", false},
		{"http://app.example.com", false},
		{"https://app.example.com.evil.test", false},
		{"https://evil.test/https://app.example.com", false},
	}
	for _, c := range cases {
		w := corsRequest(http.MethodPost, c.origin, nil)
		if w.Code != http.StatusOK {
			t.Errorf("%s: status %d (requests must still reach the handler)", c.origin, w.Code)
		}
		if got := w.Header().Get("Access-Control-Allow-Origin") != ""; got != c.allow {
			t.Errorf("%s: allowed = %v, want %v", c.origin, got, c.allow)
		}
	}
	if w := corsRequest(http.MethodPost, "", nil); w.Header().Get("Access-Control-Allow-Origin") != "" || w.Header().Get("Vary") != "" {
		t.Errorf("no Origin: headers %v", w.Header())
	}
}

func TestCORSDisabledAndAny(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	if h := withCORS(nil, next); h == nil {
		t.Fatal("nil handler")
	}
	r := httptest.NewRequest(http.MethodOptions, "/v1/logs", nil)
	r.Header.Set("Origin", "https://x.test")
	r.Header.Set("Access-Control-Request-Method", "POST")
	w := httptest.NewRecorder()
	withCORS([]string{" "}, next).ServeHTTP(w, r)
	if w.Code != http.StatusTeapot || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("disabled CORS must pass through: %d %v", w.Code, w.Header())
	}
	w = httptest.NewRecorder()
	withCORS([]string{"*"}, next).ServeHTTP(w, r)
	if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "https://x.test" {
		t.Errorf("* preflight: %d %v", w.Code, w.Header())
	}
}
