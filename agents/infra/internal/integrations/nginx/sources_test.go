package nginx

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/internal/testutil"
)

// testdata/plus_*.json are NGINX Plus API (version 9) responses recorded from
// demo.nginx.com; testdata/vts.json follows the nginx-module-vts v0.2.x JSON schema.
var plusFiles = map[string]string{
	"/api/":                    "plus_api.json",
	"/api/9":                   "plus_api_9.json",
	"/api/9/nginx":             "plus_nginx.json",
	"/api/9/connections":       "plus_connections.json",
	"/api/9/http/requests":     "plus_http_requests.json",
	"/api/9/http/server_zones": "plus_http_server_zones.json",
	"/api/9/http/upstreams":    "plus_http_upstreams.json",
}

func serveFiles(t *testing.T, files map[string]string, status map[string]int) integrations.Endpoint {
	return serve(t, func(w http.ResponseWriter, r *http.Request) {
		if code := status[r.URL.Path]; code != 0 {
			http.Error(w, `{"error":{"status":403,"text":"forbidden"}}`, code)
			return
		}
		name, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	})
}

func collectFrom(t *testing.T, ep integrations.Endpoint) (*collector, *integrations.Batch, error) {
	inst := testutil.Instance()
	inst.Target.AutoEnable = true
	c, err := Integration{}.New(inst, ep)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	b := integrations.NewBatch(time.Now(), 0)
	err = c.Collect(context.Background(), b)
	return c.(*collector), b, err
}

func TestDetect(t *testing.T) {
	vts, _ := os.ReadFile("testdata/vts.json")
	for _, tc := range []struct {
		url, body, source, apiURL string
	}{
		{"http://h/nginx_status", fixture, SourceStubStatus, "http://h/nginx_status"},
		{"http://h/api/", "[1,2,3,4,5,6,7,8,9]", SourcePlus, "http://h/api/9"},
		{"http://h/api", "[4,2]", SourcePlus, "http://h/api/4"},
		{"http://h/api/8/", `["nginx","processes","connections","slabs","http","stream"]`, SourcePlus, "http://h/api/8"},
		{"http://h/status/format/json", string(vts), SourceVTS, "http://h/status/format/json"},
		{"http://h/x", "<html>Welcome to nginx!</html>", "", ""},
		{"http://h/x", `{"status":"ok"}`, "", ""},
		{"http://h/x", `["a","b"]`, "", ""},
		{"http://h/x", "", "", ""},
	} {
		src, u, err := Detect(tc.url, []byte(tc.body))
		if src != tc.source || u != tc.apiURL || (tc.source == "") != (err != nil) {
			t.Errorf("Detect(%s, %.30q) = %q %q %v", tc.url, tc.body, src, u, err)
		}
	}
}

func TestPlusProbeAndCollect(t *testing.T) {
	ep := serveFiles(t, plusFiles, nil)
	c, b, err := collectFrom(t, ep)
	if err != nil {
		t.Fatal(err)
	}
	if c.url != "http://"+ep.Address+"/api/9" || c.source != SourcePlus {
		t.Errorf("url = %s source = %s", c.url, c.source)
	}
	ps := testutil.Points(b)
	testutil.Expect(t, ps, "nginx.requests", "{requests}", true, true, 79325998, nil)
	testutil.Expect(t, ps, "nginx.connections_accepted", "{connections}", true, true, 16160623, nil)
	testutil.Expect(t, ps, "nginx.connections_handled", "{connections}", true, true, 16160623, nil)
	testutil.Expect(t, ps, "nginx.connections_current", "{connections}", true, false, 18, map[string]string{"state": "active"})
	testutil.Expect(t, ps, "nginx.connections_current", "{connections}", true, false, 15, map[string]string{"state": "waiting"})
	if n := len(testutil.Find(ps, "nginx.connections_current", map[string]string{"state": "reading"})); n != 0 {
		t.Error("the Plus API has no reading/writing connection states")
	}
	zone := map[string]string{"nginx.zone.name": "hg.nginx.org", "nginx.zone.type": "SERVER"}
	testutil.Expect(t, ps, "nginx.http.requests", "requests", true, true, 67177, zone)
	testutil.Expect(t, ps, "nginx.http.response.status", "responses", true, true, 67176, merge(zone, "nginx.status_range", "2xx"))
	testutil.Expect(t, ps, "nginx.http.response.status", "responses", true, true, 14319,
		map[string]string{"nginx.zone.name": "trac.nginx.org", "nginx.status_range": "4xx"})
	if n := len(testutil.Find(ps, "nginx.http.requests", nil)); n != 3 {
		t.Errorf("zones = %d", n)
	}
	peer := map[string]string{"nginx.upstream.name": "demo-backend", "nginx.zone.name": "demo-backend",
		"nginx.peer.name": "10.0.0.41:8084", "nginx.peer.address": "10.0.0.41:8084"}
	testutil.Expect(t, ps, "nginx.http.upstream.peer.requests", "requests", true, true, 3387821, peer)
	testutil.Expect(t, ps, "nginx.http.upstream.peer.responses", "responses", true, true, 204468, merge(peer, "nginx.status_range", "4xx"))
	testutil.Expect(t, ps, "nginx.http.upstream.peer.fails", "attempts", true, true, 0, peer)
	testutil.Expect(t, ps, "nginx.http.upstream.peer.state", "is_deployed", false, false, 1, merge(peer, "nginx.peer.state", "UP"))
	if n := len(testutil.Find(ps, "nginx.http.upstream.peer.requests", nil)); n != 8 {
		t.Errorf("peers = %d", n)
	}
	if p := testutil.One(t, ps, "nginx.requests", nil); p.Resource[AttrSource] != SourcePlus {
		t.Errorf("resource = %v", p.Resource)
	}

	// A configured versioned API URL is used as is.
	inst := testutil.Instance()
	cc, _ := Integration{}.New(inst, integrations.Endpoint{Network: "url", Address: "http://" + ep.Address + "/api/9", Display: "x"})
	defer cc.Close()
	if err := cc.Collect(context.Background(), integrations.NewBatch(time.Now(), 0)); err != nil || cc.(*collector).source != SourcePlus {
		t.Errorf("configured plus url: %v", err)
	}
}

func TestPlusPartialAndFakeAPI(t *testing.T) {
	ep := serveFiles(t, plusFiles, map[string]int{"/api/9/http/upstreams": http.StatusForbidden})
	_, b, err := collectFrom(t, ep)
	var pe *integrations.PartialError
	if !errors.As(err, &pe) || b.Points() == 0 {
		t.Fatalf("err = %v points = %d", err, b.Points())
	}
	ps := testutil.Points(b)
	if len(testutil.Find(ps, "nginx.http.upstream.peer.requests", nil)) != 0 || len(testutil.Find(ps, "nginx.http.requests", nil)) != 3 {
		t.Error("upstream failure must only drop peer metrics")
	}

	// An application answering /api/ with a version-like array is not the Plus API.
	ep = serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/":
			w.Write([]byte("[1,2,3]"))
		case "/stub_status":
			w.Write([]byte(fixture))
		default:
			http.NotFound(w, r)
		}
	})
	c, _, err := collectFrom(t, ep)
	if err != nil || c.source != SourceStubStatus {
		t.Errorf("fake api: source = %s err = %v", c.source, err)
	}
}

func TestVTSProbeAndCollect(t *testing.T) {
	ep := serveFiles(t, map[string]string{"/status/format/json": "vts.json"}, nil)
	c, b, err := collectFrom(t, ep)
	if err != nil {
		t.Fatal(err)
	}
	if c.url != "http://"+ep.Address+"/status/format/json" || c.source != SourceVTS {
		t.Errorf("url = %s source = %s", c.url, c.source)
	}
	ps := testutil.Points(b)
	testutil.Expect(t, ps, "nginx.requests", "{requests}", true, true, 9800, nil)
	testutil.Expect(t, ps, "nginx.connections_handled", "{connections}", true, true, 1198, nil)
	testutil.Expect(t, ps, "nginx.connections_current", "{connections}", true, false, 2, map[string]string{"state": "writing"})
	testutil.Expect(t, ps, "nginx.http.response.status", "responses", true, true, 50,
		map[string]string{"nginx.zone.name": "example.com", "nginx.status_range": "5xx"})
	if len(testutil.Find(ps, "nginx.http.requests", nil)) != 2 || len(testutil.Find(ps, "nginx.http.requests", map[string]string{"nginx.zone.name": "*"})) != 0 {
		t.Error("the * aggregate zone must be skipped")
	}
	peer := map[string]string{"nginx.upstream.name": "backend", "nginx.peer.name": "10.0.0.5:8080"}
	testutil.Expect(t, ps, "nginx.http.upstream.peer.responses", "responses", true, true, 40, merge(peer, "nginx.status_range", "5xx"))
	testutil.Expect(t, ps, "nginx.http.upstream.peer.requests", "requests", true, true, 100, map[string]string{"nginx.upstream.name": "::nogroups"})
	if len(testutil.Find(ps, "nginx.http.upstream.peer.state", nil))+len(testutil.Find(ps, "nginx.http.upstream.peer.fails", nil)) != 0 {
		t.Error("VTS has no peer state or fails")
	}
	if p := testutil.One(t, ps, "nginx.requests", nil); p.Resource[AttrSource] != SourceVTS {
		t.Errorf("resource = %v", p.Resource)
	}
	_, b, _ = collectFrom(t, serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/nginx_status" {
			w.Write([]byte(fixture))
			return
		}
		http.NotFound(w, r)
	}))
	if p := testutil.One(t, testutil.Points(b), "nginx.requests", nil); p.Resource[AttrSource] != SourceStubStatus {
		t.Errorf("stub resource = %v", p.Resource)
	}
}

func TestZoneAndPeerCardinality(t *testing.T) {
	var zones []Zone
	var peers []Peer
	for i := range 200 {
		zones = append(zones, Zone{Name: "z" + strconv.Itoa(i), Requests: int64(i)})
		peers = append(peers, Peer{Upstream: "u", Name: "p" + strconv.Itoa(i), Address: "a", Requests: int64(i)})
	}
	b := integrations.NewBatch(time.Now(), 0)
	RecordZones(b.Resource(), zones)
	RecordPeers(b.Resource(), peers)
	ps := testutil.Points(b)
	if len(testutil.Find(ps, "nginx.http.requests", nil)) != MaxZones || len(testutil.Find(ps, "nginx.http.requests", map[string]string{"nginx.zone.name": "z199"})) != 1 ||
		len(testutil.Find(ps, "nginx.http.requests", map[string]string{"nginx.zone.name": "z0"})) != 0 {
		t.Error("zones: top MaxZones by requests expected")
	}
	if len(testutil.Find(ps, "nginx.http.upstream.peer.requests", nil)) != MaxPeers {
		t.Error("peers: MaxPeers expected")
	}
}

func merge(m map[string]string, k, v string) map[string]string {
	out := map[string]string{k: v}
	for a, b := range m {
		out[a] = b
	}
	return out
}
