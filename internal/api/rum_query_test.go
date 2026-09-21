package api

import (
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/rum"
)

// TestRUMTimelineQueryBuilds proves the session timeline query passes the query layer's own validation.
//
// It did not: every RUM attribute key starts with "openlog.", and the tenant guard refuses that token
// anywhere in a fragment (internal/api/query: it is the database name), so GET /rum/sessions/{id} answered
// 500 for every session that existed. No handler test caught it, because a handler test runs against an
// empty result set and the timeline query is reached only after the session lookup returns a row — which is
// why this test builds the query directly instead of going through the endpoint.
func TestRUMTimelineQueryBuilds(t *testing.T) {
	s, _ := newTestServer(t)
	sc, err := s.db.Scope("tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	sql, params, err := rumTimelineSelect(sc, "9f2c41b7a80d4e6fb35c1d8e07a4b620", now.Add(-time.Hour), now, 500).Build()
	if err != nil {
		t.Fatalf("timeline query does not build: %v", err)
	}
	if params["a_event"] != rum.AttrEvent || params["a_route"] != rum.AttrRoute {
		t.Errorf("attribute keys are not bound as parameters: %v", params)
	}
	// Identity and country travel the same way. Neither key happens to contain a token the guard refuses
	// today, which is exactly why they are pinned here: the next key added by copying these might.
	if params["a_user"] != rum.AttrUserID || params["a_country"] != rum.AttrGeoCountry {
		t.Errorf("identity keys are not bound as parameters: %v", params)
	}
	// The keys must reach ClickHouse as parameters, not as text in the statement.
	if strings.Contains(sql, rum.AttrEvent) || strings.Contains(sql, rum.AttrRoute) {
		t.Errorf("attribute key embedded in the statement: %s", sql)
	}
}
