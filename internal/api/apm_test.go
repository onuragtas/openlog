package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/apm"
)

func agg(calls, errors float64, durMs float64) redAgg {
	b := apm.Bucket(uint64(durMs * 1e6))
	h := apm.NewHist([]int16{b}, []float64{calls})
	ok := apm.NewHist([]int16{b}, []float64{calls - errors})
	return redAgg{requests: calls, errors: errors, durSum: calls * durMs, hist: h, ok: ok, maxMs: durMs}
}

func TestMergeServiceMap(t *testing.T) {
	demo := func(name string) apm.ServiceKey {
		return apm.ServiceKey{Name: name, Namespace: "shop", Environment: "demo"}
	}
	in := serviceMapInput{
		Services: map[apm.ServiceKey]redAgg{demo("frontend"): agg(100, 5, 20), demo("orders"): agg(60, 3, 30), demo("catalog"): agg(40, 0, 5)},
		Apdex:    func(apm.ServiceKey) float64 { return 500 },
		Minutes:  10,
		Edges: []MapEdgeRow{
			{Source: demo("frontend"), TargetType: apm.PeerExternal, TargetName: "orders:8080", agg: agg(60, 3, 35)},
			{Source: demo("frontend"), TargetType: apm.PeerExternal, TargetName: "catalog:8080", agg: agg(40, 0, 8)},
			{Source: demo("frontend"), TargetType: apm.PeerExternal, TargetName: "api.stripe.com", agg: agg(7, 1, 200)},
			{Source: demo("orders"), TargetType: apm.PeerDB, TargetName: "postgresql/orders", agg: agg(120, 0, 2)},
			{Source: demo("catalog"), TargetType: apm.PeerDB, TargetName: "redis", agg: agg(80, 0, 1)},
			// peer.service edge duplicated by a trace-linked edge: dropped.
			{Source: demo("orders"), TargetType: apm.PeerService, TargetName: "catalog", agg: agg(9, 0, 3)},
		},
		Links: []MapLinkRow{
			{Source: demo("frontend"), Target: demo("orders"), Via: "orders:8080", agg: agg(60, 3, 35)},
			{Source: demo("frontend"), Target: demo("catalog"), Via: "catalog:8080", agg: agg(40, 0, 8)},
			{Source: demo("orders"), Target: demo("catalog"), Via: "", agg: agg(9, 0, 3)},
		},
	}
	nodes, edges := mergeServiceMap(in)
	byID := map[string]mapEdgeJSON{}
	for _, e := range edges {
		byID[e.ID] = e
	}
	fe, or, ca := serviceNodeID(demo("frontend")), serviceNodeID(demo("orders")), serviceNodeID(demo("catalog"))
	want := map[string]float64{
		fe + "->" + or:                   60,
		fe + "->" + ca:                   40,
		fe + "->external:api.stripe.com": 7,
		or + "->db:postgresql/orders":    120,
		ca + "->db:redis":                80,
		or + "->" + ca:                   9,
	}
	if len(edges) != len(want) {
		t.Errorf("edges: %+v", edges)
	}
	for id, calls := range want {
		e, ok := byID[id]
		if !ok || e.Calls != calls || e.Throughput != calls/10 {
			t.Errorf("edge %s: %+v (ok %v)", id, e, ok)
		}
	}
	if e := byID[fe+"->"+or]; e.ErrorRate != 0.05 || e.P95Ms == nil {
		t.Errorf("edge RED %+v", e)
	}
	types := map[string]string{}
	for _, n := range nodes {
		types[n.ID] = n.Type
		if n.ID == fe && (n.Requests != 100 || n.Apdex == nil || *n.Apdex != 0.95) {
			t.Errorf("frontend node %+v", n)
		}
		if n.ID == "db:redis" && n.Requests != 80 {
			t.Errorf("redis node %+v", n)
		}
	}
	if len(nodes) != 6 || types["external:orders:8080"] != "" || types["db:postgresql/orders"] != apm.PeerDB {
		t.Errorf("nodes %v", types)
	}

	// Focus on catalog: its edges and neighbours only.
	name := "catalog"
	in.Focus = &svcFilter{name: name}
	nodes, edges = mergeServiceMap(in)
	if len(edges) != 3 || len(nodes) != 4 {
		t.Errorf("focused: %d nodes %d edges: %+v", len(nodes), len(edges), edges)
	}
}

func TestApmStepAndValidation(t *testing.T) {
	from := time.Unix(0, 0)
	for _, c := range []struct {
		step string
		rng  time.Duration
		want time.Duration
		err  bool
	}{
		{"", time.Hour, time.Minute, false},
		{"", 24 * time.Hour, 24 * time.Minute, false},
		{"90s", time.Hour, 2 * time.Minute, false},
		{"30s", time.Hour, 0, true},
		{"", 30 * 24 * time.Hour, 12 * time.Hour, false},
	} {
		r := httptest.NewRequest(http.MethodGet, "/x?step="+c.step, nil)
		got, err := apmStep(r, from, from.Add(c.rng))
		if (err != nil) != c.err || got != c.want {
			t.Errorf("apmStep(%q, %v) = %v %v, want %v", c.step, c.rng, got, err, c.want)
		}
	}
	s, conn := newTestServer(t)
	h := s.Handler()
	for path, code := range map[string]int{
		"/api/v1/apm/services/orders/transaction":             400, // name required
		"/api/v1/apm/services/orders/errors/xyz":              400,
		"/api/v1/apm/services/orders/errors/00000000000000ff": 404,
		"/api/v1/apm/services/orders":                         404,
		"/api/v1/apm/services/orders/transactions?sort=bogus": 400,
		"/api/v1/apm/traces?min_duration_ms=-1":               400,
		"/api/v1/apm/traces?error=maybe":                      400,
		"/api/v1/apm/traces?attr.x=a&attr.x=b":                400,
		"/api/v1/apm/services/orders/settings":                200,
		"/api/v1/apm/services?step=10s":                       400,
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("openlog-license-key", "key-a")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != code {
			t.Errorf("%s: %d %s, want %d", path, rec.Code, rec.Body, code)
		}
	}
	// Static auth mode has no settings store: PUT is not routed (404 before authentication).
	req := httptest.NewRequest(http.MethodPut, "/api/v1/apm/services/orders/settings", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 || len(conn.sql) == 0 {
		t.Errorf("PUT settings in static mode: %d", rec.Code)
	}
}

func TestResolveApdexT(t *testing.T) {
	s, _ := newTestServer(t)
	settings := []apm.Setting{
		{Key: apm.ServiceKey{Name: "orders"}, ApdexTMs: 300},
		{Key: apm.ServiceKey{Name: "orders", Environment: "prod"}, ApdexTMs: 100},
	}
	for key, want := range map[apm.ServiceKey]float64{
		{Name: "orders", Environment: "prod"}:                 100,
		{Name: "orders", Environment: "staging"}:              300,
		{Name: "orders"}:                                      300,
		{Name: "catalog", Environment: "prod"}:                500,
		{Name: "orders", Namespace: "x", Environment: "prod"}: 300,
	} {
		if got, _ := s.apdexTMs(settings, key); got != want {
			t.Errorf("%+v: %v, want %v", key, got, want)
		}
	}
}
