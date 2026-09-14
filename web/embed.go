// Package web embeds the built openlog UI (web/dist) and serves it as a
// single-page application.
//
// The repository contains a placeholder dist/index.html so `go build` works
// without Node; `make web` replaces it with the real Vite build.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the embedded build output rooted at dist/.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // unreachable: dist is embedded at build time
	}
	return sub
}

// Handler serves the SPA from fsys:
//   - existing files are served as-is; files under assets/ (content-hashed by
//     Vite) get a one-year immutable cache, everything else "no-cache";
//   - paths under /api/ are never handled here (404) so API typos are not
//     masked by index.html;
//   - any other path without a matching file falls back to index.html
//     (client-side routing), except paths that look like missing files
//     (have an extension), which return 404.
func Handler(fsys fs.FS) http.Handler {
	fileServer := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if name != "" && name != "index.html" {
			if st, err := fs.Stat(fsys, name); err == nil && !st.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
			if path.Ext(name) != "" {
				w.Header().Set("Cache-Control", "no-cache")
				http.NotFound(w, r)
				return
			}
		}
		serveIndex(w, r, fsys)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, fsys fs.FS) {
	b, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		http.Error(w, "UI not available", http.StatusNotFound)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	h.Set("X-Frame-Options", "DENY")
	if strings.HasPrefix(r.URL.Path, "/shared/") {
		// Public dashboard share links (docs/contracts/api.md "Share links"): the token in the path must not leak
		// through the Referer of links in markdown widgets, and the page must not be indexed.
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Robots-Tag", "noindex, nofollow")
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(b)
	}
}
