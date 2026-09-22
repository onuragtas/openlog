package api

import (
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/rum"
)

// The release query reads the raw spans, so every RUM attribute key it names starts with "openlog." — the
// token the tenant guard refuses anywhere in a fragment. A handler test would not catch a fragment the
// query layer refuses, because it runs against an empty result set; this builds the query directly, the
// same reason rum_query_test.go exists.
func TestRUMReleasesQueryBuilds(t *testing.T) {
	s, _ := newTestServer(t)
	sc, err := s.db.Scope("tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	env := "production"
	now := time.Now()
	sql, params, err := rumReleasesSelect(sc, rumFilter{app: "shop-android", env: &env}, now.Add(-time.Hour), now, 200).Build()
	if err != nil {
		t.Fatalf("release query does not build: %v", err)
	}
	if params["a_session"] != rum.AttrSessionID || params["a_event"] != rum.AttrEvent || params["v_error"] != rum.EventError {
		t.Errorf("attribute keys are not bound as parameters: %v", params)
	}
	// The keys must reach ClickHouse as parameters, not as text in the statement.
	if strings.Contains(sql, rum.AttrEvent) || strings.Contains(sql, rum.AttrSessionID) {
		t.Errorf("attribute key embedded in the statement: %s", sql)
	}
	// Distinct sessions, not spans: a session with twelve errors is one unhappy session, not twelve.
	if !strings.Contains(sql, "uniqExactIf") || !strings.Contains(sql, "uniqExact(") {
		t.Errorf("sessions are not counted distinctly: %s", sql)
	}
}
