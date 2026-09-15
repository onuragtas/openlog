package iis

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/discovery"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/internal/testutil"
)

type fakeSource struct {
	sites    []WebService
	pools    []AppPool
	sitesErr error
	poolsErr error
}

func (f fakeSource) WebServices(context.Context) ([]WebService, error) { return f.sites, f.sitesErr }
func (f fakeSource) AppPools(context.Context) ([]AppPool, error)       { return f.pools, f.poolsErr }

func collect(t *testing.T, src Source) ([]testutil.Point, error) {
	t.Helper()
	c, err := Integration{Source: src}.New(testutil.Instance(), integrations.Endpoint{})
	if err != nil {
		t.Fatal(err)
	}
	b := integrations.NewBatch(time.Now(), 0)
	err = c.Collect(context.Background(), b)
	return testutil.Points(b), err
}

func TestCollectSitesAndPools(t *testing.T) {
	src := fakeSource{
		sites: []WebService{
			{Name: "_Total", CurrentConnections: 99},
			{Name: "Default Web Site", CurrentConnections: 3, TotalBytesSent: 5_000_000_000, TotalBytesReceived: 42, TotalGetRequests: 10,
				TotalPostRequests: 4, TotalNotFoundErrors: 2, TotalConnectionAttemptsallinstances: 20},
			{Name: "api", TotalGetRequests: 1},
		},
		pools: []AppPool{{Name: "DefaultAppPool", CurrentApplicationPoolState: 3}, {Name: "_Total", CurrentApplicationPoolState: 1}},
	}
	ps, err := collect(t, src)
	if err != nil {
		t.Fatal(err)
	}
	site := map[string]string{"iis.site": "Default Web Site"}
	check := func(name string, match map[string]string, want int64, monotonic bool) {
		t.Helper()
		f := testutil.Find(ps, name, match)
		if len(f) != 1 || f[0].Int != want || (f[0].IsSum && f[0].Monotonic != monotonic) {
			t.Errorf("%s %v: %+v", name, match, f)
		}
	}
	check("iis.connection.active", site, 3, false)
	check("iis.network.io", map[string]string{"iis.site": "Default Web Site", "direction": "sent"}, 5_000_000_000, true)
	check("iis.request.count", map[string]string{"iis.site": "Default Web Site", "request": "post"}, 4, true)
	check("iis.request.count", map[string]string{"iis.site": "api", "request": "get"}, 1, true)
	check("iis.request.not_found.count", site, 2, true)
	check("iis.connection.attempt.count", site, 20, true)
	check("iis.application_pool.state", map[string]string{"iis.application_pool": "DefaultAppPool"}, 3, false)
	if len(testutil.Find(ps, "iis.connection.active", map[string]string{"iis.site": "_Total"})) != 0 ||
		len(testutil.Find(ps, "iis.application_pool.state", map[string]string{"iis.application_pool": "_Total"})) != 0 {
		t.Error("_Total instances must not be emitted")
	}
}

func TestPartialAndErrors(t *testing.T) {
	sites := []WebService{{Name: "Default Web Site"}}
	_, err := collect(t, fakeSource{sites: sites, poolsErr: errors.New("Invalid class")})
	var pe *integrations.PartialError
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "WAS") {
		t.Fatalf("pool error: %v", err)
	}
	_, err = collect(t, fakeSource{sites: sites, sitesErr: fmt.Errorf("%w (TotalBlockedBandwidthBytes)", ErrMissingFields)})
	if !errors.As(err, &pe) {
		t.Fatalf("missing fields: %v", err)
	}
	if _, err = collect(t, fakeSource{sitesErr: errors.New("Invalid class")}); err == nil || errors.As(err, &pe) {
		t.Fatalf("web service error must fail the collection: %v", err)
	}
	if _, err = collect(t, fakeSource{sites: []WebService{{Name: "_Total"}}}); err == nil || !strings.Contains(err.Error(), "W3SVC") {
		t.Fatalf("no sites: %v", err)
	}
}

func TestNotAvailableOutsideWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the WMI source is used on Windows")
	}
	_, err := Integration{}.New(testutil.Instance(), integrations.Endpoint{})
	var se *integrations.StatusError
	if !errors.As(err, &se) || se.Status != discovery.StatusNotAvailable {
		t.Fatalf("got %v", err)
	}
}
