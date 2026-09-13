package api

import (
	"context"
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
	"github.com/onuragtas/openlog/internal/tenant"
	"github.com/onuragtas/openlog/internal/updatecheck"
	"github.com/onuragtas/openlog/internal/version"
)

type fakeVersions struct{ info updatecheck.Info }

func (f fakeVersions) Info(context.Context) updatecheck.Info { return f.info }

func TestVersionEndpoint(t *testing.T) {
	res, err := tenant.ParseStatic("key-1=tenant1")
	if err != nil {
		t.Fatal(err)
	}
	s := New(config.API{QueryTimeout: time.Second, MaxRows: 10}, query.New(&recordingConn{}, "openlog", time.Second),
		auth.StaticAuthenticator{Resolver: res}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	get := func(key string) (*httptest.ResponseRecorder, map[string]any) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		s.Handler().ServeHTTP(rec, req)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec, body
	}

	rec, _ := get("")
	if rec.Code != http.StatusUnauthorized || rec.Header().Get(version.Header) != version.String() {
		t.Errorf("unauthenticated: %d, header %q", rec.Code, rec.Header().Get(version.Header))
	}
	rec, body := get("key-1")
	if rec.Code != http.StatusOK || body["version"] != version.String() || body["update_check"] != "disabled" ||
		body["latest_available"] != nil || body["updater"] != nil {
		t.Errorf("build only: %d %v", rec.Code, body)
	}
	if _, ok := body["commit"]; !ok {
		t.Errorf("commit missing: %v", body)
	}

	checked := time.Date(2026, 9, 13, 3, 0, 0, 0, time.UTC)
	s.SetVersionSource(fakeVersions{updatecheck.Info{
		UpdateCheck:     updatecheck.StatusEnabled,
		LatestAvailable: &updatecheck.Available{Version: "0.9.1", NotesURL: "https://notes", CheckedAt: checked},
		Updater:         json.RawMessage(`{"state":"available","target_version":"0.9.1"}`),
	}})
	_, body = get("key-1")
	la, _ := body["latest_available"].(map[string]any)
	up, _ := body["updater"].(map[string]any)
	if body["update_check"] != "enabled" || la["version"] != "0.9.1" || la["checked_at"] != "2026-09-13T03:00:00Z" || up["state"] != "available" {
		t.Errorf("with source: %v", body)
	}
	// Every other API response carries the header too.
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/nope", nil))
	if rec.Header().Get(version.Header) == "" {
		t.Error("404 without version header")
	}
}
