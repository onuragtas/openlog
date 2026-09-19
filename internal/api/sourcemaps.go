package api

import (
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/sourcemaps"
)

// Source maps of browser applications (docs/contracts/rum.md §8, api.md "Source maps").
//
//	GET    /api/v1/source-maps              the maps of the organization
//	POST   /api/v1/source-maps?app=&script= upload (the raw document as the body), replacing the one held
//	DELETE /api/v1/source-maps/{id}         remove one
//
// This is the product's only endpoint that takes a binary body rather than JSON: a map is a document a
// build produced, and base64 in JSON would cost a third more bytes for nothing. The body is still bounded
// the way every other body is.

// SetSourceMaps enables the endpoints. Nil (the default) leaves them answering 404, like every other
// optional service.
func (s *Server) SetSourceMaps(svc *sourcemaps.Service) { s.sourceMaps = svc }

func (s *Server) sourceMapRoutes(mux *http.ServeMux) {
	if s.accounts == nil {
		return // static auth mode has no organizations to scope maps to
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
	authed("GET /api/v1/source-maps", s.listSourceMaps)
	authed("POST /api/v1/source-maps", s.uploadSourceMap)
	authed("DELETE /api/v1/source-maps/{id}", s.deleteSourceMap)
}

type sourceMapJSON struct {
	ID             string `json:"id"`
	App            string `json:"app"`
	Script         string `json:"script"`
	Size           int64  `json:"size_bytes"`
	SHA256         string `json:"sha256"`
	CreatedByEmail string `json:"created_by_email"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

func sourceMapResponse(r sourcemaps.Record) sourceMapJSON {
	return sourceMapJSON{ID: r.ID, App: r.App, Script: r.Script, Size: r.Size, SHA256: hex.EncodeToString(r.SHA256),
		CreatedByEmail: r.CreatedByEmail, CreatedAt: formatTime(r.CreatedAt), UpdatedAt: formatTime(r.UpdatedAt)}
}

func (s *Server) sourceMapService(p *auth.Principal, act auth.Action) (*sourcemaps.Service, error) {
	if s.sourceMaps == nil {
		return nil, &apiError{http.StatusNotFound, "not_found", "source maps are not enabled on this installation"}
	}
	// authorize returns *apiError, so it is checked as its own type: a typed nil in an error interface
	// would make every caller here fail authorization.
	if ae := authorize(p, act); ae != nil {
		return nil, ae
	}
	return s.sourceMaps, nil
}

// symbolicate un-minifies a stored stack with the maps of one application, returning the rewritten stack
// and how many frames resolved. It never fails the caller: a stack nobody uploaded a map for, a map that
// storage lost, an application that is not a browser app at all — each is simply zero resolved frames, and
// the minified stack the error inbox already had.
func (s *Server) symbolicate(r *http.Request, orgID, app, stack string) (string, int) {
	if s.sourceMaps == nil || stack == "" || app == "" {
		return stack, 0
	}
	return sourcemaps.Symbolicate(stack, s.sourceMaps.Resolver(r.Context(), orgID, app))
}

func (s *Server) listSourceMaps(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	svc, err := s.sourceMapService(p, auth.ActListSourceMaps)
	if err != nil {
		return err
	}
	recs, err := svc.List(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	out := make([]sourceMapJSON, 0, len(recs))
	for _, rec := range recs {
		out = append(out, sourceMapResponse(rec))
	}
	writeJSON(w, http.StatusOK, map[string]any{"source_maps": out})
	return nil
}

func (s *Server) uploadSourceMap(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	svc, err := s.sourceMapService(p, auth.ActManageSourceMaps)
	if err != nil {
		return err
	}
	app := strings.TrimSpace(r.URL.Query().Get("app"))
	// The script is the file name a stack frame carries, so a path or a URL is a caller mistake worth
	// naming rather than silently trimming: uploading "assets/main.js" and matching "main.js" later would
	// look like it worked.
	script := strings.TrimSpace(r.URL.Query().Get("script"))
	switch {
	case app == "" || len(app) > 512:
		return badRequest("app must be the browser application name (1-512 characters)")
	case script == "" || len(script) > 255:
		return badRequest("script must be the generated file name (1-255 characters)")
	case script != sourcemaps.ScriptName(script):
		return badRequest("script must be a file name without a path, query string or fragment")
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, sourcemaps.MaxMapBytes+1))
	if err != nil {
		return badRequest("the source map could not be read (at most 32 MiB)")
	}
	rec := &sourcemaps.Record{OrgID: p.OrgID, App: app, Script: script, CreatedBy: p.UserID}
	if err := svc.Upload(r.Context(), rec, body); err != nil {
		// A map that does not parse is the uploader's mistake, not a server fault: say which.
		if errors.Is(err, sourcemaps.ErrIndexMap) || strings.HasPrefix(err.Error(), "source map:") ||
			strings.HasPrefix(err.Error(), "the source map") {
			return badRequest("%s", err.Error())
		}
		return err
	}
	rec.CreatedByEmail = p.Email
	writeJSON(w, http.StatusCreated, map[string]any{"source_map": sourceMapResponse(*rec)})
	return nil
}

func (s *Server) deleteSourceMap(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	svc, err := s.sourceMapService(p, auth.ActManageSourceMaps)
	if err != nil {
		return err
	}
	if err := svc.Delete(r.Context(), p.OrgID, r.PathValue("id")); err != nil {
		if errors.Is(err, sourcemaps.ErrNotFound) {
			return notFound("source map not found")
		}
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
