package apm

import (
	"testing"
	"time"
)

func TestNormalizeMessageV2(t *testing.T) {
	cases := map[string]string{
		"request req_8f3a9c2b1d failed":                         "request req_<id> failed",
		"order_123456 not found":                                "order_<id> not found",
		"inventory_shard_98765 unavailable":                     "inventory_shard_<id> unavailable",
		"shard_4 is down":                                       "shard_4 is down", // short ids stay
		"user_not_found":                                        "user_not_found",  // words stay
		"deadline 2026-09-14T10:11:12.345Z exceeded":            "deadline <ts> exceeded",
		"at 2026-09-14 10:11:12+03:00 lock timeout":             "at <ts> lock timeout",
		"id 4bf92f35-77b3-4da6-a3ce-929d0e0e4736 missing":       "id <uuid> missing",
		"order 42: inventory shard 0 unavailable":               "order <n>: inventory shard <n> unavailable",
		"pointer 0xc000312f00 invalid, token deadbeef12":        "pointer <hex> invalid, token <hex>",
		"can't reach 10.0.0.12:5432 for 'orders'":               "can't reach <ip> for '?'",
		"Cannot read properties of undefined (reading 'price')": "Cannot read properties of undefined (reading '?')",
	}
	for in, want := range cases {
		if got := NormalizeMessage(in); got != want {
			t.Errorf("NormalizeMessage(%q) = %q, want %q", in, got, want)
		}
	}
	// v1 group ids are unchanged for messages without the new patterns.
	if NormalizeMessage("user-12345 blocked") != "user-<n> blocked" {
		t.Error("v1 number normalization changed")
	}
}

func TestTopFrameV2(t *testing.T) {
	goStack := "goroutine 1 [running]:\nmain.handler.func1.2({0x1})\n\t/app/releases/20260914101112/main.go:12 +0x1f\n"
	if got := TopFrame(goStack); got != "main.handler.func@/app/releases/<id>/main.go" {
		t.Errorf("go: %q", got)
	}
	generic := "main.Map[...](0x1)\n\t/src/x.go:3 +0x1\n"
	if a, b := TopFrame(generic), TopFrame("main.Map[go.shape.int](0x1)\n\t/src/x.go:3 +0x1\n"); a != b {
		t.Errorf("generic instantiations differ: %q %q", a, b)
	}
	js1 := "TypeError: x\n    at priceOf (/app/dist/main.3f2a1b9c.js:137:20)\n"
	js2 := "TypeError: x\n    at priceOf (/app/dist/main.77aa01ffe2.js:140:2)\n"
	if a, b := TopFrame(js1), TopFrame(js2); a != b || a != "priceOf@/app/dist/main.js" {
		t.Errorf("bundle hash: %q %q", a, b)
	}
	java1 := "java.lang.IllegalStateException: x\n\tat com.shop.Orders$$Lambda$123/0x0000000800c02a38.apply(Unknown Source)\n"
	java2 := "java.lang.IllegalStateException: x\n\tat com.shop.Orders$$Lambda$987/0x0000000800d00000.apply(Unknown Source)\n"
	if a, b := TopFrame(java1), TopFrame(java2); a != b {
		t.Errorf("java lambda: %q %q", a, b)
	}
	if a, b := TopFrame("x\n\tat com.shop.Orders.lambda$handle$0(Orders.java:12)\n"), TopFrame("x\n\tat com.shop.Orders.lambda$handle$3(Orders.java:14)\n"); a != b {
		t.Errorf("java lambda method: %q %q", a, b)
	}
	uuidDir := "x\n    at run (/srv/4bf92f35-77b3-4da6-a3ce-929d0e0e4736/app.js:1:1)\n"
	if got := TopFrame(uuidDir); got != "run@/srv/<id>/app.js" {
		t.Errorf("uuid dir: %q", got)
	}
	// Plain frames keep the v1 key.
	if got := TopFrame("x\n    at priceOf (/app/server.js:137:20)\n"); got != "priceOf@/app/server.js" {
		t.Errorf("v1 frame: %q", got)
	}
}

func ptr[T any](v T) *T { return &v }

func TestPatchValidationAndApply(t *testing.T) {
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	bad := []ErrorGroupPatch{
		{},
		{Status: ptr(ErrorStatus("done"))},
		{Status: ptr(StatusIgnored), ResolvedInVersion: ptr("1.2.3")},
		{ResolvedInVersion: ptr("1.2.3")},
	}
	for i, p := range bad {
		if p.Validate() == nil {
			t.Errorf("patch %d accepted", i)
		}
	}
	st := ApplyPatch(ErrorGroupState{}, ErrorGroupPatch{Status: ptr(StatusResolved), ResolvedInVersion: ptr(" 1.4.3 ")}, now)
	if st.Status != StatusResolved || st.ResolvedAt == nil || !st.ResolvedAt.Equal(now) || st.ResolvedInVersion != "1.4.3" {
		t.Fatalf("resolve: %+v", st)
	}
	// Re-resolving with the same version keeps resolved_at.
	again := ApplyPatch(st, ErrorGroupPatch{Status: ptr(StatusResolved), ResolvedInVersion: ptr("1.4.3")}, now.Add(time.Hour))
	if !again.ResolvedAt.Equal(now) {
		t.Errorf("re-resolve moved resolved_at: %v", again.ResolvedAt)
	}
	ign := ApplyPatch(st, ErrorGroupPatch{Status: ptr(StatusIgnored), AssigneeUserID: ptr("u1")}, now)
	if ign.Status != StatusIgnored || ign.ResolvedAt != nil || ign.ResolvedInVersion != "" || ign.AssigneeUserID != "u1" {
		t.Errorf("ignore: %+v", ign)
	}
	un := ApplyPatch(ign, ErrorGroupPatch{AssigneeUserID: ptr("")}, now)
	if un.AssigneeUserID != "" || un.Status != StatusIgnored {
		t.Errorf("unassign: %+v", un)
	}
	if (ErrorGroupState{}).EffectiveStatus() != StatusUnresolved {
		t.Error("no row must be unresolved")
	}
}

func TestDetectRegression(t *testing.T) {
	t0 := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	resolved := func(version string) ErrorGroupState {
		at := t0
		return ErrorGroupState{Status: StatusResolved, ResolvedAt: &at, ResolvedInVersion: version}
	}
	first := map[string]time.Time{"1.0": t0.Add(-48 * time.Hour), "1.1": t0.Add(-2 * time.Hour), "1.2": t0.Add(time.Hour), "1.3": t0.Add(3 * time.Hour)}

	// Not resolved: never.
	if _, ok := DetectRegression(ErrorGroupState{Status: StatusIgnored}, t0.Add(time.Hour), nil, first); ok {
		t.Error("ignored group regressed")
	}
	// Without a version: any occurrence after resolved_at, also without version data.
	if r, ok := DetectRegression(resolved(""), t0.Add(5*time.Minute), nil, first); !ok || !r.At.Equal(t0.Add(5*time.Minute)) {
		t.Errorf("plain regression: %+v %v", r, ok)
	}
	if _, ok := DetectRegression(resolved(""), t0, nil, first); ok {
		t.Error("occurrence at resolved_at must not regress")
	}
	// Resolved in 1.2 (first seen after resolve): old 1.1 draining does not regress ...
	occ := []VersionOccurrence{{Version: "1.1", LastSeen: t0.Add(2 * time.Hour)}}
	if _, ok := DetectRegression(resolved("1.2"), t0.Add(2*time.Hour), occ, first); ok {
		t.Error("old version regressed")
	}
	// ... but 1.2 itself and newer 1.3 do.
	occ = append(occ, VersionOccurrence{Version: "1.3", LastSeen: t0.Add(4 * time.Hour)}, VersionOccurrence{Version: "1.2", LastSeen: t0.Add(90 * time.Minute)})
	if r, ok := DetectRegression(resolved("1.2"), t0.Add(4*time.Hour), occ, first); !ok || r.Version != "1.3" || !r.At.Equal(t0.Add(4*time.Hour)) {
		t.Errorf("newer version: %+v %v", r, ok)
	}
	// Resolved in a version that never reported: versions first seen after resolved_at count.
	occ = []VersionOccurrence{{Version: "1.1", LastSeen: t0.Add(time.Hour)}, {Version: "1.2", LastSeen: t0.Add(2 * time.Hour)}}
	if r, ok := DetectRegression(resolved("2.0"), t0.Add(2*time.Hour), occ, first); !ok || r.Version != "1.2" {
		t.Errorf("unseen fixed version: %+v %v", r, ok)
	}
	// Unknown occurrence versions (not in firstSeen) do not qualify.
	if _, ok := DetectRegression(resolved("2.0"), t0.Add(time.Hour), []VersionOccurrence{{Version: "x", LastSeen: t0.Add(time.Hour)}}, first); ok {
		t.Error("unknown version regressed")
	}
}

func TestDetectDeployments(t *testing.T) {
	base := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
	m := func(min int) time.Time { return base.Add(time.Duration(min) * time.Minute) }
	var rows []VersionMinute
	add := func(from, to int, version string, spans uint64) {
		for i := from; i <= to; i++ {
			rows = append(rows, VersionMinute{Minute: m(i), Version: version, Spans: spans})
		}
	}
	add(0, 60, "1.0", 100)   // initial version
	add(50, 55, "1.1", 20)   // rolling update 1.0 -> 1.1 at minute 50 ...
	add(56, 120, "1.1", 100) // ... completed
	add(121, 130, "1.0", 90) // rollback to 1.0 at 121
	add(200, 210, "1.0", 50) // restart of 1.0 after a pause: not a deployment
	add(0, 210, "", 5)       // spans without service.version never count
	first := map[string]time.Time{"1.0": m(0), "1.1": m(50), "": m(0)}

	got := DetectDeployments(rows, first, m(0), DeploymentGap)
	want := []Deployment{
		{Time: m(0), Version: "1.0", Initial: true},
		{Time: m(50), Version: "1.1", PreviousVersion: "1.0"},
		{Time: m(121), Version: "1.0", PreviousVersion: "1.1", Rollback: true},
	}
	if len(got) != len(want) {
		t.Fatalf("deployments: %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("deployment %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// from filters out older events but keeps the previous version from the lookback rows.
	late := DetectDeployments(rows, first, m(100), DeploymentGap)
	if len(late) != 1 || late[0].Version != "1.0" || late[0].PreviousVersion != "1.1" {
		t.Errorf("from filter: %+v", late)
	}
	// A version whose first span is before the rows (older than the lookback) is not "initial".
	if d := DetectDeployments([]VersionMinute{{Minute: m(5), Version: "0.9", Spans: 1}}, map[string]time.Time{"0.9": m(-10000)}, m(0), 0); len(d) != 0 {
		t.Errorf("pre-lookback version: %+v", d)
	}
}
