package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":              {Data: []byte("<html>app</html>")},
		"favicon.svg":             {Data: []byte("<svg/>")},
		"mockServiceWorker.js":    {Data: []byte("// sw")},
		"assets/index-abc123.js":  {Data: []byte("console.log(1)")},
		"assets/index-abc123.css": {Data: []byte("body{}")},
	}
}

func get(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestHandler(t *testing.T) {
	h := Handler(testFS())
	tests := []struct {
		name, method, path string
		wantCode           int
		wantBody           string
		wantCache          string
	}{
		{"root serves index", "GET", "/", 200, "<html>app</html>", "no-cache, no-store, must-revalidate"},
		{"index.html direct", "GET", "/index.html", 200, "<html>app</html>", "no-cache, no-store, must-revalidate"},
		{"spa route falls back", "GET", "/hosts/abc", 200, "<html>app</html>", "no-cache, no-store, must-revalidate"},
		{"deep spa route with dots in segment", "GET", "/traces/0af7651916cd43dd8448eb211c80319c", 200, "<html>app</html>", "no-cache, no-store, must-revalidate"},
		{"hashed asset is immutable", "GET", "/assets/index-abc123.js", 200, "console.log(1)", "public, max-age=31536000, immutable"},
		{"css asset", "GET", "/assets/index-abc123.css", 200, "body{}", "public, max-age=31536000, immutable"},
		{"unhashed root file no-cache", "GET", "/favicon.svg", 200, "<svg/>", "no-cache"},
		{"missing file with extension 404", "GET", "/assets/missing.js", 404, "", "no-cache"},
		{"missing root file 404", "GET", "/robots.txt", 404, "", "no-cache"},
		{"api paths are not handled", "GET", "/api/v1/nope", 404, "", ""},
		{"api root not handled", "GET", "/api", 404, "", ""},
		{"head on route", "HEAD", "/hosts", 200, "", "no-cache, no-store, must-revalidate"},
		{"post rejected", "POST", "/", 405, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := get(t, h, tt.method, tt.path)
			if rec.Code != tt.wantCode {
				t.Fatalf("code = %d, want %d (body %q)", rec.Code, tt.wantCode, rec.Body.String())
			}
			if tt.wantBody != "" && rec.Body.String() != tt.wantBody {
				t.Errorf("body = %q, want %q", rec.Body.String(), tt.wantBody)
			}
			if got := rec.Header().Get("Cache-Control"); got != tt.wantCache {
				t.Errorf("Cache-Control = %q, want %q", got, tt.wantCache)
			}
		})
	}
}

func TestHandlerContentTypes(t *testing.T) {
	h := Handler(testFS())
	if ct := get(t, h, "GET", "/hosts").Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("index content type = %q", ct)
	}
	if ct := get(t, h, "GET", "/assets/index-abc123.js").Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("js content type = %q", ct)
	}
}

func TestHandlerSharedDashboardHeaders(t *testing.T) {
	h := Handler(testFS())
	rec := get(t, h, "GET", "/shared/dashboards/olds_abc")
	if rec.Code != http.StatusOK || rec.Header().Get("Referrer-Policy") != "no-referrer" || rec.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Errorf("shared page: %d %v", rec.Code, rec.Header())
	}
	if rec := get(t, h, "GET", "/dashboards/x"); rec.Header().Get("Referrer-Policy") != "" {
		t.Errorf("other pages keep the default referrer policy: %v", rec.Header())
	}
}

func TestHandlerNoIndex(t *testing.T) {
	h := Handler(fstest.MapFS{})
	if rec := get(t, h, "GET", "/"); rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

func TestDistHasIndex(t *testing.T) {
	if _, err := fs.Stat(Dist(), "index.html"); err != nil {
		t.Fatalf("embedded dist has no index.html: %v", err)
	}
}
