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

func TestCrashesGroupOnTheFrameOnceSymbolsNamedOne(t *testing.T) {
	groups, order := map[string]*rumCrashJSON{}, []string{}
	add := func(group, typ, frame string, resolved int, sessions ...string) {
		c := rumCrashJSON{GroupID: group, Type: typ, Frame: frame, SymbolicatedFrames: resolved, Count: 1, sessions: map[string]struct{}{}}
		for _, s := range sessions {
			c.sessions[s] = struct{}{}
		}
		c.Sessions = uint64(len(c.sessions))
		mergeCrash(groups, &order, c)
	}
	add("a", "SIGSEGV", "Game::Load", 2, "s1")
	add("b", "SIGSEGV", "Game::Load", 2, "s1", "s2") // renamed between builds, so another error group, and the same crash
	add("a", "SIGSEGV", "Game::Save", 2, "s3")       // one error group, and another crash
	add("c", "SIGABRT", "abort", 0, "s4")            // the device's name for the system's frame: says nothing of whose abort
	add("d", "SIGABRT", "abort", 0, "s5")
	if len(order) != 4 {
		t.Fatalf("%d crashes: %v", len(order), order)
	}
	load := groups[crashKey(rumCrashJSON{Type: "SIGSEGV", Frame: "Game::Load", SymbolicatedFrames: 1})]
	if load.Count != 2 || load.Sessions != 2 {
		t.Errorf("one session on two stacks is one session: %+v", load)
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
