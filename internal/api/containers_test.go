package api

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestBuildContainerSeries(t *testing.T) {
	rows := []containerSeriesRow{
		{metric: "container.cpu.utilization", t: 0, avg: 0.5},
		{metric: "container.network.io", dir: "receive", series: 1, t: 0, last: 1000},
		{metric: "container.network.io", dir: "transmit", series: 2, t: 0, last: 50},
		{metric: "container.memory.usage", t: 0, avg: 100},
		{metric: "container.cpu.utilization", t: 10_000, avg: 0.25},
		// A second series in the same bucket (e.g. the container was renamed) is averaged for gauges.
		{metric: "container.cpu.utilization", series: 9, t: 10_000, avg: 0.75},
		{metric: "container.network.io", dir: "receive", series: 1, t: 10_000, last: 3000},
		// Counter reset: clamps to 0.
		{metric: "container.network.io", dir: "transmit", series: 2, t: 10_000, last: 10},
		{metric: "container.blockio.io", dir: "write", series: 3, t: 0, last: 0},
		{metric: "container.blockio.io", dir: "write", series: 3, t: 20_000, last: 4096},
		{metric: "container.memory.limit", t: 20_000, avg: 1 << 30},
	}
	got := buildContainerSeries(rows)
	want := containerSeriesJSON{
		CPUUtilization:  [][2]float64{{0, 0.5}, {10_000, 0.5}},
		MemoryUsage:     [][2]float64{{0, 100}},
		MemoryLimit:     [][2]float64{{20_000, 1 << 30}},
		NetworkReceive:  [][2]float64{{10_000, 200}},
		NetworkTransmit: [][2]float64{{10_000, 0}},
		BlockIORead:     [][2]float64{},
		BlockIOWrite:    [][2]float64{{20_000, 204.8}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("series = %+v", got)
	}
}

func ptr(v float64) *float64 { return &v }

func TestGroupAndFilterContainers(t *testing.T) {
	cs := []*containerJSON{
		{ContainerID: "a", Name: "shop-orders-1", HostID: "h1", ComposeProject: "shop", ComposeService: "orders", State: "running", Reporting: true, CPUUtilization: ptr(0.1), MemoryUsage: ptr(100)},
		{ContainerID: "b", Name: "shop-orders-2", HostID: "h2", ComposeProject: "shop", ComposeService: "orders", State: "running", Reporting: true, CPUUtilization: ptr(0.2)},
		{ContainerID: "c", Name: "shop-catalog-1", HostID: "h1", ComposeProject: "shop", ComposeService: "catalog", State: "exited", Reporting: true},
		{ContainerID: "d", Name: "stray", HostID: "h1", ImageName: "busybox", ImageTags: []string{"1.36"}},
	}
	sortContainers(cs)
	if cs[0].ContainerID != "d" || cs[1].ComposeService != "catalog" {
		t.Fatalf("order %s %s", cs[0].ContainerID, cs[1].ContainerID)
	}
	groups := groupContainers(cs)
	if len(groups) != 2 || groups[0].ComposeProject != "" || groups[1].Containers != 3 || groups[1].Running != 2 || !reflect.DeepEqual(groups[1].HostIDs, []string{"h1", "h2"}) {
		t.Fatalf("groups %+v", groups)
	}
	orders := groups[1].Services[1]
	if orders.ComposeService != "orders" || orders.Running != 2 || *orders.CPUUtilization < 0.2999 || *orders.CPUUtilization > 0.3001 || *orders.MemoryUsage != 100 {
		t.Errorf("orders %+v", orders)
	}

	empty := ""
	shop := "shop"
	cases := []struct {
		f    containerFilter
		want []string
	}{
		{containerFilter{project: &shop, state: "running"}, []string{"a", "b"}},
		{containerFilter{project: &empty}, []string{"d"}},
		{containerFilter{state: "unknown"}, []string{"d"}},
		{containerFilter{q: "BUSYBOX 1.36"}, []string{"d"}},
		{containerFilter{q: "orders h2"}, []string{"b"}},
	}
	for _, c := range cases {
		var ids []string
		for _, x := range cs {
			if c.f.match(x) {
				ids = append(ids, x.ContainerID)
			}
		}
		if !reflect.DeepEqual(ids, c.want) {
			t.Errorf("filter %+v → %v, want %v", c.f, ids, c.want)
		}
	}
	if got := parseImageTags(`["1.25","stable"]`); !reflect.DeepEqual(got, []string{"1.25", "stable"}) {
		t.Errorf("tags %v", got)
	}
	if got := parseImageTags("a, b"); !reflect.DeepEqual(got, []string{"a", "b"}) || len(parseImageTags("")) != 0 {
		t.Errorf("tags %v", got)
	}
}

func TestContainerParameterErrors(t *testing.T) {
	s, conn := newTestServer(t)
	h := s.Handler()
	do := func(path string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer key-a")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	id := strings.Repeat("ab", 32)
	for path, want := range map[string]int{
		"/api/v1/containers?state=sleeping":                400,
		"/api/v1/containers?q=" + strings.Repeat("x", 300): 400,
		"/api/v1/containers/xyz":                           400,
		"/api/v1/containers/" + id + "/timeseries?step=1s": 400,
		"/api/v1/containers/" + id:                         404,
		"/api/v1/containers/" + id + "/services":           200,
		"/api/v1/containers":                               200,
		"/api/v1/containers/groups":                        200,
	} {
		if got := do(path); got != want {
			t.Errorf("%s: %d, want %d", path, got, want)
		}
	}
	if len(conn.sql) == 0 {
		t.Error("no statements")
	}
}
