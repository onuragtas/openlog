package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

// An updater document without updater_version comes from an updater older than 0.1.25, which cannot report that it is
// older than the running release: the API adds the notice for it.
func TestLegacyUpdaterNotice(t *testing.T) {
	legacy := json.RawMessage(`{"engine":"compose","mode":"auto","state":"up_to_date","checked_at":"2026-09-14T10:00:00Z",` +
		`"notices":[{"code":"compose_outdated_bundle","message":"m","params":{"files_version":"0.1.21","running_version":"0.1.25"}}]}`)
	var doc struct {
		Engine  string `json:"engine"`
		Notices []struct {
			Code    string            `json:"code"`
			Message string            `json:"message"`
			Params  map[string]string `json:"params"`
		} `json:"notices"`
	}
	if err := json.Unmarshal(withLegacyUpdaterNotice(legacy, "0.1.25"), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Engine != "compose" || len(doc.Notices) != 2 || doc.Notices[0].Code != "compose_outdated_bundle" {
		t.Fatalf("document %+v", doc)
	}
	if n := doc.Notices[1]; n.Code != "updater_outdated" || n.Params["updater_version"] != "< 0.1.25" || n.Params["running_version"] != "0.1.25" ||
		!strings.Contains(n.Message, "docker compose --profile updater up -d openlog-updater") {
		t.Fatalf("notice %+v", n)
	}
	k8s := withLegacyUpdaterNotice(json.RawMessage(`{"engine":"kubernetes","mode":"notify","state":"available"}`), "0.1.25")
	if !strings.Contains(string(k8s), `"code":"updater_outdated_kubernetes"`) {
		t.Fatalf("kubernetes document %s", k8s)
	}
	for _, tc := range []struct {
		doc, running string
	}{
		{`{"engine":"compose","updater_version":"0.1.25","state":"up_to_date"}`, "0.1.25"}, // a current updater decides itself
		{`{"engine":"compose","state":"up_to_date"}`, "0.0.0-dev+abc"},                     // development build
		{`null`, "0.1.25"},
		{`{"state":"x"}`, "0.1.25"},
		{`{"engine":"compose","notices":"garbage"}`, "0.1.25"},
	} {
		if got := withLegacyUpdaterNotice(json.RawMessage(tc.doc), tc.running); string(got) != tc.doc {
			t.Errorf("%s (%s) changed: %s", tc.doc, tc.running, got)
		}
	}
}
