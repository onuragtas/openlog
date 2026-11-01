package api

import (
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/rum"
)

// Each of these builds a query directly: a handler test runs against an empty result set, so it would not catch a
// fragment the query layer refuses, the same reason rum_query_test.go exists.
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
	if params["r_app"] != "shop-android" || params["r_env"] != env {
		t.Errorf("application or environment not bound: %v", params)
	}
	for _, want := range []string{"rum_release_sessions_1h", "uniqCombined64Merge(crashed_sessions)", "uniqCombined64Merge(capturing_sessions)"} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q: %s", want, sql)
		}
	}
}

func TestRUMCrashesQueryBuilds(t *testing.T) {
	s, _ := newTestServer(t)
	sc, err := s.db.Scope("tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	sql, params, err := rumCrashesSelect(sc, rumFilter{app: "shop-android"}, now.Add(-time.Hour), now, 200).Build()
	if err != nil {
		t.Fatalf("crash query does not build: %v", err)
	}
	if params["a_kind"] != rum.AttrCrashKind {
		t.Errorf("the attribute key is not bound: %v", params)
	}
	if strings.Contains(sql, rum.AttrCrashKind) {
		t.Errorf("attribute key embedded in the statement: %s", sql)
	}
}

func TestSessionsFreeOf(t *testing.T) {
	for _, tc := range []struct {
		bad, total uint64
		want       float64
	}{
		{0, 0, 1},  // nothing known is reported as nothing broke, as it always was for errors
		{0, 10, 1}, //
		{1, 4, 0.75},
		{4, 4, 0},
		{5, 4, 0}, // two sketches may disagree slightly, and a rate must not go negative
	} {
		if got := sessionsFreeOf(tc.bad, tc.total); got != tc.want {
			t.Errorf("sessionsFreeOf(%d, %d) = %v, want %v", tc.bad, tc.total, got, tc.want)
		}
	}
}
