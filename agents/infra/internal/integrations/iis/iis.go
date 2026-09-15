// Package iis implements the Microsoft IIS integration: the raw Windows performance counters of the Web Service (W3SVC)
// and application pool (WAS) providers, read through WMI, as metrics of the OpenTelemetry Collector iisreceiver
// (semantic-conventions §6.8). The collector is Windows-only (iis_windows.go); elsewhere the integration is not_available.
package iis

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
)

// WebService is one instance (site, or _Total) of Win32_PerfRawData_W3SVC_WebService. Raw counters are cumulative.
type WebService struct {
	Name                                string
	CurrentConnections                  uint32
	TotalAnonymousUsers                 uint32
	TotalConnectionAttemptsallinstances uint32
	TotalBytesReceived                  uint64
	TotalBytesSent                      uint64
	TotalFilesReceived                  uint32
	TotalFilesSent                      uint32
	TotalDeleteRequests                 uint32
	TotalGetRequests                    uint32
	TotalHeadRequests                   uint32
	TotalOptionsRequests                uint32
	TotalPostRequests                   uint32
	TotalPutRequests                    uint32
	TotalTraceRequests                  uint32
	TotalNotFoundErrors                 uint32
	TotalBlockedBandwidthBytes          uint32
}

// AppPool is one instance of Win32_PerfRawData_APPPOOLCountersProvider_APPPOOLWAS.
type AppPool struct {
	Name                        string
	CurrentApplicationPoolState uint32
}

// Source reads the counters.
type Source interface {
	WebServices(ctx context.Context) ([]WebService, error)
	AppPools(ctx context.Context) ([]AppPool, error)
}

// ErrMissingFields reports counters that this Windows version does not provide (the rest was read).
var ErrMissingFields = errors.New("some counters are not available")

// Integration is the IIS integration.
type Integration struct {
	// Source overrides the platform source (tests).
	Source Source
}

// ID implements integrations.Integration.
func (Integration) ID() string { return config.IntegrationIIS }

// Spec implements integrations.Integration: the counters are local, no endpoint.
func (Integration) Spec() integrations.EndpointSpec {
	return integrations.EndpointSpec{NoEndpoint: true}
}

// Hint implements integrations.Integration.
func (Integration) Hint(*integrations.Instance) string {
	return `# IIS metrics come from the Web Service (W3SVC) and WAS performance counters: no configuration or credentials.
# If they are missing, rebuild the performance counter registry (elevated): lodctr /R && winmgmt /resyncperf`
}

// New implements integrations.Integration.
func (i Integration) New(*integrations.Instance, integrations.Endpoint) (integrations.Collector, error) {
	src := i.Source
	if src == nil {
		src = platformSource() // nil outside Windows
	}
	if src == nil {
		return nil, integrations.NotAvailable("IIS performance counters are only available on Windows")
	}
	return &collector{src: src}, nil
}

type collector struct{ src Source }

func (c *collector) Close() {}

// Collect implements integrations.Collector.
func (c *collector) Collect(ctx context.Context, b *integrations.Batch) error {
	sites, err := c.src.WebServices(ctx)
	var partial []string
	switch {
	case errors.Is(err, ErrMissingFields):
		partial = append(partial, "web service: "+err.Error())
	case err != nil:
		return fmt.Errorf("web service performance counters (W3SVC): %w", err)
	}
	n := 0
	for _, s := range sites {
		if s.Name == "" || strings.EqualFold(s.Name, "_Total") {
			continue
		}
		n++
		RecordSite(b.Resource(otlputil.Str("iis.site", s.Name)), s)
	}
	if n == 0 {
		return errors.New("no Web Service counter instances: is the World Wide Web Publishing Service (W3SVC) running?")
	}
	pools, err := c.src.AppPools(ctx)
	switch {
	case errors.Is(err, ErrMissingFields):
		partial = append(partial, "application pools: "+err.Error())
	case err != nil:
		partial = append(partial, "application pool counters (WAS): "+err.Error())
	}
	for _, p := range pools {
		if p.Name == "" || strings.EqualFold(p.Name, "_Total") {
			continue
		}
		b.Resource(otlputil.Str("iis.application_pool", p.Name)).GaugeInt("iis.application_pool.state", "{state}", int64(p.CurrentApplicationPoolState))
	}
	if len(partial) > 0 {
		return integrations.Partial(errors.New(strings.Join(partial, "; ")))
	}
	return nil
}

// RecordSite emits the iisreceiver metrics of one site.
func RecordSite(s *integrations.Scope, w WebService) {
	s.SumInt("iis.connection.active", "{connections}", false, int64(w.CurrentConnections))
	s.SumInt("iis.connection.anonymous", "{connections}", true, int64(w.TotalAnonymousUsers))
	s.SumInt("iis.connection.attempt.count", "{attempts}", true, int64(w.TotalConnectionAttemptsallinstances))
	s.SumInt("iis.network.blocked", "By", true, int64(w.TotalBlockedBandwidthBytes))
	s.SumInt("iis.network.io", "By", true, int64(w.TotalBytesSent), otlputil.Str("direction", "sent"))
	s.SumInt("iis.network.io", "By", true, int64(w.TotalBytesReceived), otlputil.Str("direction", "received"))
	s.SumInt("iis.network.file.count", "{files}", true, int64(w.TotalFilesSent), otlputil.Str("direction", "sent"))
	s.SumInt("iis.network.file.count", "{files}", true, int64(w.TotalFilesReceived), otlputil.Str("direction", "received"))
	for _, r := range []struct {
		method string
		v      uint32
	}{
		{"delete", w.TotalDeleteRequests}, {"get", w.TotalGetRequests}, {"head", w.TotalHeadRequests}, {"options", w.TotalOptionsRequests},
		{"post", w.TotalPostRequests}, {"put", w.TotalPutRequests}, {"trace", w.TotalTraceRequests},
	} {
		s.SumInt("iis.request.count", "{requests}", true, int64(r.v), otlputil.Str("request", r.method))
	}
	s.SumInt("iis.request.not_found.count", "{requests}", true, int64(w.TotalNotFoundErrors)) // openlog, not in OTel
}
