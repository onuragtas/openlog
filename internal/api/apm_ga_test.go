package api

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/onuragtas/openlog/internal/apm"
	"github.com/onuragtas/openlog/internal/auth"
)

func TestTracePath(t *testing.T) {
	fe := apm.ServiceKey{Name: "frontend", Namespace: "shop", Environment: "prod"}
	or := apm.ServiceKey{Name: "orders", Namespace: "shop", Environment: "prod"}
	spans := []pathSpan{
		{traceID: "t1", spanID: "a", kind: "server", key: fe},
		{traceID: "t1", spanID: "b", parentID: "a", kind: "client", peerType: apm.PeerExternal, peerName: "orders:8080", key: fe},
		{traceID: "t1", spanID: "c", parentID: "b", kind: "server", key: or},
		{traceID: "t1", spanID: "d", parentID: "c", kind: "client", peerType: apm.PeerDB, peerName: "postgresql/orders", key: or},
		{traceID: "t2", spanID: "e", kind: "server", key: fe},
		{traceID: "t2", spanID: "f", parentID: "e", kind: "producer", peerType: apm.PeerService, peerName: "orders", key: fe},
		// a missing parent (sampled out) adds no edge
		{traceID: "t2", spanID: "g", parentID: "zz", kind: "consumer", key: or},
	}
	nodes, edges, n := tracePath(spans)
	feID, orID := serviceNodeID(fe), serviceNodeID(or)
	wantNodes := []string{"db:postgresql/orders", "external:orders:8080", feID, orID}
	wantEdges := []string{feID + "->external:orders:8080", feID + "->" + orID, orID + "->db:postgresql/orders"}
	if n != 2 || !reflect.DeepEqual(nodes, wantNodes) || !reflect.DeepEqual(edges, wantEdges) {
		t.Errorf("tracePath = %v %v %d", nodes, edges, n)
	}
	if nodes, edges, n := tracePath(nil); len(nodes) != 0 || len(edges) != 0 || n != 0 || nodes == nil {
		t.Errorf("empty: %v %v %d", nodes, edges, n)
	}
}

func TestParseInboxFilter(t *testing.T) {
	p := &auth.Principal{UserID: "11111111-2222-3333-4444-555555555555"}
	req := func(q string) *http.Request { return httptest.NewRequest(http.MethodGet, "/api/v1/apm/errors?"+q, nil) }
	f, err := parseInboxFilter(req("status=resolved,ignored&assignee=me&q=+Shard+&sort=first_seen"), p)
	if err != nil || !f.statuses[apm.StatusResolved] || !f.statuses[apm.StatusIgnored] || f.statuses[apm.StatusUnresolved] ||
		f.assignee == nil || *f.assignee != p.UserID || f.q != "shard" || f.sort != "first_seen" || !f.needsStateRows() {
		t.Fatalf("filter %+v %v", f, err)
	}
	f, err = parseInboxFilter(req("status=unresolved&assignee=none"), nil)
	if err != nil || f.needsStateRows() || f.assignee == nil || *f.assignee != "" || f.sort != "count" {
		t.Fatalf("unassigned filter %+v %v", f, err)
	}
	if !f.matchAssignee(apm.ErrorGroupState{}) || f.matchAssignee(apm.ErrorGroupState{AssigneeUserID: "x"}) {
		t.Error("matchAssignee(none)")
	}
	for _, bad := range []string{"status=open", "assignee=me", "assignee=bob", "sort=name"} {
		if _, err := parseInboxFilter(req(bad), nil); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestErrorWorkflowRoutesNeedSession(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	// Static auth mode has no accounts: the mutating endpoints do not exist; reads work without the workflow.
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/apm/errors/groups", nil)
	req.Header.Set("openlog-license-key", "key-a")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PATCH without accounts: %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/apm/errors/groups/nothex/comments", nil)
	req.Header.Set("openlog-license-key", "key-a")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid group id: %d %s", rec.Code, rec.Body)
	}
	for _, p := range []string{"/api/v1/apm/services/orders/deployments/compare", "/api/v1/apm/map/path?service=orders", "/api/v1/logs?cursor=bogus",
		"/api/v1/logs?transaction=x", "/api/v1/apm/services/orders/deployments?gap=1m"} {
		req = httptest.NewRequest(http.MethodGet, p, nil)
		req.Header.Set("openlog-license-key", "key-a")
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", p, rec.Code, rec.Body)
		}
	}
}
