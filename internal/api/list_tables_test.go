package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/onuragtas/openlog/internal/cost"
)

func TestHostMatchesEveryWordInAnyField(t *testing.T) {
	h := hostJSON{HostName: "web-1", HostID: "abc", OSDescription: "Ubuntu 24.04", ResourceAttributes: map[string]string{"env": "prod"}}
	for q, want := range map[string]bool{"": true, "WEB ubuntu": true, "env=prod": true, "web-2": false, "web env=dev": false} {
		if got := hostMatches(h, q); got != want {
			t.Errorf("%q: got %v", q, got)
		}
	}
}

func TestHostsSortByUsageWithBlanksLastEitherWay(t *testing.T) {
	cpu := func(v float64) *hostUsageJSON { return &hostUsageJSON{CPU: &v} }
	rows := func() []hostJSON {
		return []hostJSON{{HostID: "none"}, {HostID: "low", Usage: cpu(0.1)}, {HostID: "high", Usage: cpu(0.9)}, {HostID: "empty", Usage: &hostUsageJSON{}}}
	}
	tie := func(a, b hostJSON) int { return strings.Compare(a.HostID, b.HostID) }
	ids := func(l listRequest) string {
		page, _ := sortPage(rows(), l, hostKeys, tie)
		out := []string{}
		for _, h := range page {
			out = append(out, h.HostID)
		}
		return strings.Join(out, " ")
	}
	if got := ids(listRequest{sort: "cpu", limit: 10}); got != "high low empty none" {
		t.Errorf("descending: %s", got)
	}
	if got := ids(listRequest{sort: "cpu", asc: true, limit: 10}); got != "low high empty none" {
		t.Errorf("ascending: %s", got)
	}
}

func TestCostHostsWithoutAPriceSortLast(t *testing.T) {
	rows := []cost.HostCost{{HostID: "free", Priced: false}, {HostID: "a", Priced: true, Total: 1}, {HostID: "b", Priced: true, Total: 5}}
	page, more := sortPage(rows, listRequest{sort: "cost", asc: true, limit: 2}, costHostKeys, func(a, b cost.HostCost) int { return strings.Compare(a.HostID, b.HostID) })
	if page[0].HostID != "a" || page[1].HostID != "b" || !more {
		t.Errorf("%+v more=%v", page, more)
	}
}

func TestListEndpointsAskForTheirSort(t *testing.T) {
	cases := []struct{ path, order, limit string }{
		{"/api/v1/vulnerabilities?sort=score&dir=asc&limit=25&offset=50", "ORDER BY sc ASC, host_count DESC, id", "LIMIT 26"},
		{"/api/v1/profiles/services?sort=service&limit=25", "ORDER BY service_name ASC, service_name, deployment_environment, profile_type, unit", "LIMIT 26"},
	}
	for _, c := range cases {
		s, conn := newTestServer(t)
		if code, body := costGet(t, s, c.path); code != http.StatusOK {
			t.Errorf("%s: %d %s", c.path, code, body)
			continue
		}
		found := false
		for _, q := range conn.sql {
			if strings.Contains(q, c.order) && strings.Contains(q, c.limit) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no statement has %q and %q in %q", c.path, c.order, c.limit, conn.sql)
		}
	}
}

func TestListEndpointsRefuseAnUnknownSort(t *testing.T) {
	for _, path := range []string{"/api/v1/hosts?sort=nope", "/api/v1/vulnerabilities?sort=nope", "/api/v1/profiles/services?sort=nope", "/api/v1/hosts?dir=up"} {
		s, _ := newTestServer(t)
		if code, _ := costGet(t, s, path); code != http.StatusBadRequest {
			t.Errorf("%s: %d", path, code)
		}
	}
}
