package api

import (
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/symbols"
)

// Symbol files for mobile crashes (mobile-crash.md §3). Unlike source maps these take an API key, because the caller is a build
// machine: a step someone has to sign in for is a step that gets skipped.

// SetSymbols enables the endpoints. Must be called before Handler.
func (s *Server) SetSymbols(svc *symbols.Service) { s.symbolFiles = svc }

var symbolKeyRe = regexp.MustCompile(`^[0-9A-Za-z._+-]{1,128}$`)

func (s *Server) symbolRoutes(mux *http.ServeMux) {
	if s.accounts == nil || s.symbolFiles == nil {
		return
	}
	authed := func(pattern string, act auth.Action, h accountFunc) {
		mux.Handle(pattern, s.instrument(pattern, func(rec *statusRecorder, r *http.Request) {
			noStore(rec)
			p, r := s.authenticate(rec, r)
			if p == nil {
				return
			}
			if ae := authorize(p, act); ae != nil {
				writeError(rec, ae)
				return
			}
			if err := h(rec, r, p); err != nil {
				s.writeAccountError(rec, pattern, err)
			}
		}))
	}
	authed("GET /api/v1/symbols", auth.ActListSourceMaps, s.listSymbols)
	authed("POST /api/v1/symbols", auth.ActManageSymbols, s.uploadSymbols)
	authed("DELETE /api/v1/symbols/{id}", auth.ActManageSymbols, s.deleteSymbols)
}

func symbolResponse(r symbols.Record) map[string]any {
	return map[string]any{
		"id": r.ID, "app": r.App, "kind": r.Kind, "key": r.Key, "size_bytes": r.Size, "sha256": hex.EncodeToString(r.SHA256),
		"created_at": formatTime(r.CreatedAt), "updated_at": formatTime(r.UpdatedAt),
	}
}

func (s *Server) listSymbols(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	recs, err := s.symbolFiles.List(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	out := make([]map[string]any, 0, len(recs))
	for _, rec := range recs {
		out = append(out, symbolResponse(rec))
	}
	writeJSON(w, http.StatusOK, map[string]any{"symbols": out})
	return nil
}

func (s *Server) uploadSymbols(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	q := r.URL.Query()
	app, kind, key := strings.TrimSpace(q.Get("app")), q.Get("kind"), strings.TrimSpace(q.Get("key"))
	switch {
	case app == "" || len(app) > 512:
		return badRequest("app must be the application name (1-512 characters)")
	case kind != symbols.KindR8 && kind != symbols.KindNative:
		return badRequest("kind must be r8 or native")
	case !symbolKeyRe.MatchString(key):
		return badRequest("key must be the version for r8 and the image id for native")
	}
	if kind == symbols.KindNative {
		key = symbols.NormalizeImageID(key)
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, symbols.MaxUploadBytes+1))
	if err != nil {
		return badRequest("the file could not be read (at most 128 MiB compressed)")
	}
	if len(body) == 0 {
		return badRequest("the file is empty")
	}
	rec := &symbols.Record{OrgID: p.OrgID, App: app, Kind: kind, Key: key, CreatedBy: p.UserID}
	if err := s.symbolFiles.Upload(r.Context(), rec, body); err != nil {
		// A file that does not parse is the uploader's mistake, not a server fault.
		if strings.HasPrefix(err.Error(), "symbols:") {
			return badRequest("%s", err.Error())
		}
		return err
	}
	s.accounts.Audit(r.Context(), p, s.accounts.Meta(r), "symbols.upload", "symbol_file", rec.ID, map[string]any{"app": app, "kind": kind, "key": key})
	writeJSON(w, http.StatusCreated, map[string]any{"symbol": symbolResponse(*rec)})
	return nil
}

func (s *Server) deleteSymbols(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	err := s.symbolFiles.Delete(r.Context(), p.OrgID, r.PathValue("id"))
	if errors.Is(err, symbols.ErrNotFound) {
		return &apiError{http.StatusNotFound, "not_found", "symbol file not found"}
	}
	if err != nil {
		return err
	}
	s.accounts.Audit(r.Context(), p, s.accounts.Meta(r), "symbols.delete", "symbol_file", r.PathValue("id"), nil)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// crashResolver finds what was uploaded for a build. Without symbols it finds nothing, and a stack reads as the device sent it.
func (s *Server) crashResolver(r *http.Request, orgID, app, version string) symbols.Resolver {
	if s.symbolFiles == nil || orgID == "" {
		return symbols.Resolver{}
	}
	return s.symbolFiles.Resolver(r.Context(), orgID, app, version)
}
