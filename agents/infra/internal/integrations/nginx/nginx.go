// Package nginx implements the nginx integration: it reads the stub_status page
// (ngx_http_stub_status_module), the NGINX Plus API or the nginx-module-vts
// JSON page and emits the metrics of the OpenTelemetry Collector nginxreceiver,
// plus per-zone and per-upstream-peer metrics named like the NGINX agent's
// nginxplusreceiver (semantic-conventions §6.3).
package nginx

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// ProbePaths are the stub_status paths tried on every discovered port when the rule has auto_enable.
var ProbePaths = []string{"/nginx_status", "/stub_status", "/basic_status", "/status", "/server_status"}

// PlusProbePaths are tried first: the NGINX Plus API root (a JSON array of API versions).
var PlusProbePaths = []string{"/api/"}

// VTSProbePaths are the nginx-module-vts JSON pages, tried after the Plus API.
var VTSProbePaths = []string{"/status/format/json", "/vts/format/json", "/vts_status/format/json"}

// Status sources (resource attribute nginx.status.source).
const (
	AttrSource       = "nginx.status.source"
	SourceStubStatus = "stub_status"
	SourcePlus       = "plus"
	SourceVTS        = "vts"
)

// Cardinality guards of the zone and upstream peer metrics.
const (
	MaxZones = 50  // server zones with the most requests
	MaxPeers = 100 // upstream peers with the most requests
)

const maxBody = 4 << 20

// memoURL is the Instance.Memo key of the status page found on an endpoint ("<source> <url>").
const memoURL = "nginx.status_url "

// Integration is the nginx integration.
type Integration struct{}

// ID implements integrations.Integration.
func (Integration) ID() string { return config.IntegrationNginx }

// Spec implements integrations.Integration.
func (Integration) Spec() integrations.EndpointSpec {
	return integrations.EndpointSpec{DefaultPort: 80}
}

// Hint implements integrations.Integration.
func (Integration) Hint(inst *integrations.Instance) string {
	port := 80
	var tried []string
	for _, e := range inst.Endpoints {
		if e.Network == "tcp" {
			if len(tried) == 0 {
				port = e.Port()
			}
			tried = append(tried, e.Display)
		}
	}
	where := ""
	if len(tried) > 0 {
		where = " on " + strings.Join(tried, ", ")
	}
	all := append(append(slices.Clone(PlusProbePaths), VTSProbePaths...), ProbePaths...)
	return fmt.Sprintf(`# stub_status is not enabled on this nginx: no status page answered%s
# (tried %s over http and https). The agent never changes the nginx configuration.
# Add a status location to the server block that listens on port %d, then reload nginx
# ("nginx -s reload", or "docker exec <container> nginx -s reload" for a container):
#
#   location = /nginx_status {
#       stub_status;
#       allow 127.0.0.1;
#       allow 172.16.0.0/12;   # Docker networks (published ports and container addresses)
#       deny all;
#   }
#
# NGINX Plus: "location /api/ { api; allow 127.0.0.1; deny all; }" adds per-zone response
# codes and upstream peer metrics (status_zone in server blocks, zone in upstreams).
# nginx-module-vts: "location /status { vhost_traffic_status_display; }" (JSON at /status/format/json).
#
# The agent finds the page automatically on its next attempt. If the page is on another
# URL, set it in openlog (host → Integrations → nginx) or in config.yaml:
integrations:
  nginx:
    instances:
      - match: { port: %d }
        endpoint: http://127.0.0.1:%d/nginx_status`, where, strings.Join(all, ", "), port, port, port)
}

// New implements integrations.Integration.
func (Integration) New(inst *integrations.Instance, ep integrations.Endpoint) (integrations.Collector, error) {
	c := &collector{inst: inst}
	switch ep.Network {
	case "url":
		c.url = ep.Address
	case "tcp":
		if !inst.Target.AutoEnable {
			return nil, integrations.NeedsConfiguration("stub_status URL not configured (auto_enable is off for this rule)", true)
		}
		c.hostPort = ep.Address
	default:
		return nil, fmt.Errorf("unsupported endpoint %s: %w", ep.Display, integrations.ErrTryNext)
	}
	tr := &http.Transport{DisableKeepAlives: false, MaxIdleConns: 1, TLSClientConfig: &tls.Config{}}
	if t := inst.Settings.TLS; t != nil {
		tr.TLSClientConfig.InsecureSkipVerify = t.InsecureSkipVerify
		tr.TLSClientConfig.ServerName = t.ServerName
		if t.CAFile != "" {
			pem, err := os.ReadFile(t.CAFile)
			if err != nil {
				return nil, integrations.NeedsConfiguration("tls.ca_file: "+err.Error(), true)
			}
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM(pem)
			tr.TLSClientConfig.RootCAs = pool
		}
	}
	c.client = &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return c, nil
}

type collector struct {
	inst     *integrations.Instance
	client   *http.Client
	url      string // status page URL; for the Plus API the versioned base (…/api/9)
	source   string // detected source; "" until the first successful fetch
	hostPort string // probe target when url is empty
}

func (c *collector) Close() { c.client.CloseIdleConnections() }

// found is a detected status page; body is the fetched page (nil for the Plus API).
type found struct {
	source, url string
	body        []byte
}

func (c *collector) Collect(ctx context.Context, b *integrations.Batch) error {
	var body []byte
	if c.source == "" {
		var f found
		var err error
		if c.url == "" {
			f, err = c.probe(ctx)
		} else {
			f, err = c.try(ctx, c.client, c.url)
		}
		if err != nil {
			return err
		}
		c.source, c.url, body = f.source, f.url, f.body
	}
	b.SetResourceAttr(otlputil.Str(AttrSource, c.source))
	if c.source == SourcePlus {
		return c.collectPlus(ctx, b)
	}
	if body == nil {
		var err error
		if body, err = fetchBody(ctx, c.client, c.url); err != nil {
			return err
		}
	}
	if c.source == SourceVTS {
		v, err := ParseVTS(body)
		if err != nil {
			return fmt.Errorf("GET %s: %w", c.url, err)
		}
		RecordVTS(b, v)
		return nil
	}
	st, err := Parse(string(body))
	if err != nil {
		return fmt.Errorf("GET %s: %w", c.url, err)
	}
	Record(b, st)
	return nil
}

// probe finds a status page on the endpoint: plain HTTP first, then HTTPS;
// per scheme the Plus API, the VTS JSON pages, then the stub_status paths.
// Certificates are not verified only on loopback (probing reads public status
// formats; a remote address keeps verification). A page found earlier for the
// endpoint (Instance.Memo) is tried first; only a body that parses as one of
// the formats is accepted.
func (c *collector) probe(ctx context.Context) (found, error) {
	httpsClient := c.client
	if isLoopback(c.hostPort) {
		httpsClient = &http.Client{
			Transport:     &http.Transport{DisableKeepAlives: true, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
			CheckRedirect: c.client.CheckRedirect,
		}
	}
	clientFor := func(u string) *http.Client {
		if strings.HasPrefix(u, "https:") {
			return httpsClient
		}
		return c.client
	}
	key := memoURL + c.hostPort
	if m := c.inst.Memo.Get(key); m != "" {
		if src, u, ok := strings.Cut(m, " "); ok {
			if f, err := c.try(ctx, clientFor(u), u); err == nil && f.source == src {
				c.client = clientFor(u)
				return f, nil
			}
		}
		c.inst.Memo.Set(key, "")
	}
	paths := append(append(slices.Clone(PlusProbePaths), VTSProbePaths...), ProbePaths...)
	reachable := false
	for _, scheme := range []string{"http", "https"} {
		cl := clientFor(scheme + ":")
		for _, p := range paths {
			u := scheme + "://" + c.hostPort + p
			f, err := c.try(ctx, cl, u)
			if err == nil {
				c.inst.Log.Info("nginx status page found", "url", f.url, "source", f.source)
				c.client = cl
				c.inst.Memo.Set(key, f.source+" "+f.url)
				return f, nil
			}
			if errors.Is(err, integrations.ErrUnreachable) {
				break // this scheme does not work on the port
			}
			reachable = true
		}
	}
	if !reachable {
		return found{}, fmt.Errorf("%s: %w", c.hostPort, integrations.ErrUnreachable)
	}
	return found{}, fmt.Errorf("no stub_status, NGINX Plus API or VTS page on %s (tried %s): %w", c.hostPort, strings.Join(paths, ", "), integrations.ErrTryNext)
}

// try fetches u and detects its format. A Plus API root is accepted only when
// its versioned connections endpoint answers.
func (c *collector) try(ctx context.Context, cl *http.Client, u string) (found, error) {
	body, err := fetchBody(ctx, cl, u)
	if err != nil {
		return found{}, err
	}
	src, su, err := Detect(u, body)
	if err != nil {
		return found{}, fmt.Errorf("GET %s: %w", u, err)
	}
	f := found{source: src, url: su, body: body}
	if src == SourcePlus {
		f.body = nil
		var conns PlusConnections
		if err := getJSON(ctx, cl, su+"/connections", &conns); err != nil {
			if errors.Is(err, integrations.ErrUnreachable) {
				return found{}, err
			}
			return found{}, fmt.Errorf("NGINX Plus API %s: %w", su, err)
		}
	}
	return f, nil
}

func isLoopback(hostPort string) bool {
	h, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		return false
	}
	ip := net.ParseIP(h)
	return (ip != nil && ip.IsLoopback()) || strings.EqualFold(h, "localhost")
}

var errNotStatus = errors.New("response is not an nginx status page (stub_status, NGINX Plus API or VTS JSON)")

func fetchBody(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "openlog-infra-agent")
	resp, err := client.Do(req)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) || strings.Contains(err.Error(), "connection refused") {
			return nil, fmt.Errorf("%w: %v", integrations.ErrUnreachable, err)
		}
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d: %w", url, resp.StatusCode, errNotStatus)
	}
	return body, nil
}

func getJSON(ctx context.Context, client *http.Client, url string, v any) error {
	body, err := fetchBody(ctx, client, url)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("GET %s: invalid JSON: %w", url, errNotStatus)
	}
	return nil
}

// Detect identifies a status page body fetched from u. For the Plus API it
// returns the versioned API base: the root (/api/, a JSON array of versions)
// selects the highest version; a versioned root (/api/9, a JSON array of
// endpoint names) is used as is.
func Detect(u string, body []byte) (source, url string, err error) {
	t := bytes.TrimSpace(body)
	if len(t) == 0 {
		return "", "", errNotStatus
	}
	switch t[0] {
	case '[':
		base := strings.TrimRight(u, "/")
		var versions []int
		if json.Unmarshal(t, &versions) == nil && len(versions) > 0 {
			return SourcePlus, base + "/" + strconv.Itoa(slices.Max(versions)), nil
		}
		var endpoints []string
		if json.Unmarshal(t, &endpoints) == nil && slices.Contains(endpoints, "connections") && slices.Contains(endpoints, "http") {
			return SourcePlus, base, nil
		}
	case '{':
		if _, err := ParseVTS(t); err == nil {
			return SourceVTS, u, nil
		}
	default:
		if _, err := Parse(string(body)); err == nil {
			return SourceStubStatus, u, nil
		}
	}
	return "", "", errNotStatus
}

// Stats is a parsed stub_status page.
type Stats struct {
	Active, Accepted, Handled, Requests, Reading, Writing, Waiting int64
}

// Parse parses a stub_status body:
//
//	Active connections: 291
//	server accepts handled requests
//	 16630948 16630948 31070465
//	Reading: 6 Writing: 179 Waiting: 106
func Parse(body string) (Stats, error) {
	var st Stats
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) < 4 {
		return st, errNotStatus
	}
	f := strings.Fields(lines[0])
	if len(f) != 3 || f[0] != "Active" || f[1] != "connections:" {
		return st, errNotStatus
	}
	var err error
	num := func(s string) int64 {
		v, e := strconv.ParseInt(s, 10, 64)
		if e != nil && err == nil {
			err = errNotStatus
		}
		return v
	}
	st.Active = num(f[2])
	f = strings.Fields(lines[2])
	if len(f) != 3 {
		return st, errNotStatus
	}
	st.Accepted, st.Handled, st.Requests = num(f[0]), num(f[1]), num(f[2])
	f = strings.Fields(lines[3])
	if len(f) != 6 || f[0] != "Reading:" || f[2] != "Writing:" || f[4] != "Waiting:" {
		return st, errNotStatus
	}
	st.Reading, st.Writing, st.Waiting = num(f[1]), num(f[3]), num(f[5])
	return st, err
}

// Record emits the nginxreceiver metrics.
func Record(b *integrations.Batch, st Stats) {
	s := b.Resource()
	s.SumInt("nginx.requests", "{requests}", true, st.Requests)
	s.SumInt("nginx.connections_accepted", "{connections}", true, st.Accepted)
	s.SumInt("nginx.connections_handled", "{connections}", true, st.Handled)
	for _, p := range []struct {
		state string
		v     int64
	}{{"active", st.Active}, {"reading", st.Reading}, {"writing", st.Writing}, {"waiting", st.Waiting}} {
		s.SumInt("nginx.connections_current", "{connections}", false, p.v, otlputil.Str("state", p.state))
	}
}

// Responses are response counters by status class (Plus API and VTS use the same keys).
type Responses struct {
	R1xx int64 `json:"1xx"`
	R2xx int64 `json:"2xx"`
	R3xx int64 `json:"3xx"`
	R4xx int64 `json:"4xx"`
	R5xx int64 `json:"5xx"`
}

var statusRanges = [5]string{"1xx", "2xx", "3xx", "4xx", "5xx"}

func (r Responses) byRange() [5]int64 { return [5]int64{r.R1xx, r.R2xx, r.R3xx, r.R4xx, r.R5xx} }

// Zone holds the counters of a server zone.
type Zone struct {
	Name      string
	Requests  int64
	Responses Responses
}

// Peer holds the counters of an upstream server.
type Peer struct {
	Upstream, Zone, Name, Address string
	Requests                      int64
	Responses                     Responses
	Fails                         *int64 // Plus only
	State                         string // Plus only: UP, DOWN, DRAINING, UNAVAILABLE, UNHEALTHY, CHECKING
}

// RecordZones emits nginx.http.requests and nginx.http.response.status for the
// MaxZones zones with the most requests.
func RecordZones(s *integrations.Scope, zones []Zone) {
	sort.Slice(zones, func(i, j int) bool {
		if zones[i].Requests != zones[j].Requests {
			return zones[i].Requests > zones[j].Requests
		}
		return zones[i].Name < zones[j].Name
	})
	if len(zones) > MaxZones {
		zones = zones[:MaxZones]
	}
	for _, z := range zones {
		name, typ := otlputil.Str("nginx.zone.name", z.Name), otlputil.Str("nginx.zone.type", "SERVER")
		s.SumInt("nginx.http.requests", "requests", true, z.Requests, name, typ)
		for i, v := range z.Responses.byRange() {
			s.SumInt("nginx.http.response.status", "responses", true, v, otlputil.Str("nginx.status_range", statusRanges[i]), name, typ)
		}
	}
}

// RecordPeers emits nginx.http.upstream.peer.* for the MaxPeers peers with the most requests.
func RecordPeers(s *integrations.Scope, peers []Peer) {
	sort.Slice(peers, func(i, j int) bool {
		a, b := peers[i], peers[j]
		if a.Requests != b.Requests {
			return a.Requests > b.Requests
		}
		if a.Upstream != b.Upstream {
			return a.Upstream < b.Upstream
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Address < b.Address
	})
	if len(peers) > MaxPeers {
		peers = peers[:MaxPeers]
	}
	for _, p := range peers {
		attrs := []*commonpb.KeyValue{otlputil.Str("nginx.peer.address", p.Address), otlputil.Str("nginx.peer.name", p.Name),
			otlputil.Str("nginx.upstream.name", p.Upstream)}
		if p.Zone != "" {
			attrs = append(attrs, otlputil.Str("nginx.zone.name", p.Zone))
		}
		with := func(k, v string) []*commonpb.KeyValue { return append(slices.Clone(attrs), otlputil.Str(k, v)) }
		s.SumInt("nginx.http.upstream.peer.requests", "requests", true, p.Requests, attrs...)
		for i, v := range p.Responses.byRange() {
			s.SumInt("nginx.http.upstream.peer.responses", "responses", true, v, with("nginx.status_range", statusRanges[i])...)
		}
		if p.Fails != nil {
			s.SumInt("nginx.http.upstream.peer.fails", "attempts", true, *p.Fails, attrs...)
		}
		if p.State != "" {
			s.GaugeInt("nginx.http.upstream.peer.state", "is_deployed", 1, with("nginx.peer.state", p.State)...)
		}
	}
}

// VTSStatus is the nginx-module-vts JSON document (the fields used).
type VTSStatus struct {
	Connections *struct {
		Active   int64 `json:"active"`
		Reading  int64 `json:"reading"`
		Writing  int64 `json:"writing"`
		Waiting  int64 `json:"waiting"`
		Accepted int64 `json:"accepted"`
		Handled  int64 `json:"handled"`
		Requests int64 `json:"requests"`
	} `json:"connections"`
	ServerZones map[string]struct {
		RequestCounter int64     `json:"requestCounter"`
		Responses      Responses `json:"responses"`
	} `json:"serverZones"`
	UpstreamZones map[string][]struct {
		Server         string    `json:"server"`
		RequestCounter int64     `json:"requestCounter"`
		Responses      Responses `json:"responses"`
	} `json:"upstreamZones"`
}

// ParseVTS parses a VTS JSON document.
func ParseVTS(body []byte) (VTSStatus, error) {
	var v VTSStatus
	if err := json.Unmarshal(body, &v); err != nil || v.Connections == nil || v.ServerZones == nil {
		return VTSStatus{}, errNotStatus
	}
	return v, nil
}

// RecordVTS emits the core metrics from VTS connections and the zone and peer
// metrics (the "*" aggregate server zone is skipped: it would double-count).
func RecordVTS(b *integrations.Batch, v VTSStatus) {
	cn := v.Connections
	Record(b, Stats{Active: cn.Active, Accepted: cn.Accepted, Handled: cn.Handled, Requests: cn.Requests,
		Reading: cn.Reading, Writing: cn.Writing, Waiting: cn.Waiting})
	var zones []Zone
	for name, z := range v.ServerZones {
		if name != "*" {
			zones = append(zones, Zone{Name: name, Requests: z.RequestCounter, Responses: z.Responses})
		}
	}
	RecordZones(b.Resource(), zones)
	var peers []Peer
	for up, list := range v.UpstreamZones {
		for _, p := range list {
			peers = append(peers, Peer{Upstream: up, Name: p.Server, Address: p.Server, Requests: p.RequestCounter, Responses: p.Responses})
		}
	}
	RecordPeers(b.Resource(), peers)
}

// PlusConnections is /api/<v>/connections.
type PlusConnections struct {
	Accepted int64 `json:"accepted"`
	Dropped  int64 `json:"dropped"`
	Active   int64 `json:"active"`
	Idle     int64 `json:"idle"`
}

// PlusRequests is /api/<v>/http/requests.
type PlusRequests struct {
	Total   int64 `json:"total"`
	Current int64 `json:"current"`
}

// PlusServerZone is one entry of /api/<v>/http/server_zones.
type PlusServerZone struct {
	Requests  int64     `json:"requests"`
	Responses Responses `json:"responses"`
}

// PlusUpstream is one entry of /api/<v>/http/upstreams.
type PlusUpstream struct {
	Zone  string `json:"zone"`
	Peers []struct {
		Server    string    `json:"server"`
		Name      string    `json:"name"`
		State     string    `json:"state"`
		Requests  int64     `json:"requests"`
		Responses Responses `json:"responses"`
		Fails     int64     `json:"fails"`
	} `json:"peers"`
}

// PlusStatus is the data read from the NGINX Plus API.
type PlusStatus struct {
	Connections PlusConnections
	Requests    PlusRequests
	ServerZones map[string]PlusServerZone
	Upstreams   map[string]PlusUpstream
}

var plusPeerStates = map[string]string{"up": "UP", "down": "DOWN", "draining": "DRAINING", "unavail": "UNAVAILABLE", "unhealthy": "UNHEALTHY", "checking": "CHECKING"}

// RecordPlus emits the core metrics (nginx.connections_current only with
// state active = active + idle and waiting = idle; handled = accepted - dropped)
// and the zone and peer metrics.
func RecordPlus(b *integrations.Batch, p PlusStatus) {
	s := b.Resource()
	s.SumInt("nginx.requests", "{requests}", true, p.Requests.Total)
	s.SumInt("nginx.connections_accepted", "{connections}", true, p.Connections.Accepted)
	s.SumInt("nginx.connections_handled", "{connections}", true, p.Connections.Accepted-p.Connections.Dropped)
	s.SumInt("nginx.connections_current", "{connections}", false, p.Connections.Active+p.Connections.Idle, otlputil.Str("state", "active"))
	s.SumInt("nginx.connections_current", "{connections}", false, p.Connections.Idle, otlputil.Str("state", "waiting"))
	var zones []Zone
	for name, z := range p.ServerZones {
		zones = append(zones, Zone{Name: name, Requests: z.Requests, Responses: z.Responses})
	}
	RecordZones(s, zones)
	var peers []Peer
	for up, u := range p.Upstreams {
		for _, pr := range u.Peers {
			fails := pr.Fails
			state := plusPeerStates[pr.State]
			if state == "" && pr.State != "" {
				state = strings.ToUpper(pr.State)
			}
			peers = append(peers, Peer{Upstream: up, Zone: u.Zone, Name: pr.Name, Address: pr.Server, Requests: pr.Requests,
				Responses: pr.Responses, Fails: &fails, State: state})
		}
	}
	RecordPeers(s, peers)
}

// collectPlus reads connections and requests (required) and server zones and
// upstreams (a failure makes the collection partial).
func (c *collector) collectPlus(ctx context.Context, b *integrations.Batch) error {
	var p PlusStatus
	if err := getJSON(ctx, c.client, c.url+"/connections", &p.Connections); err != nil {
		return err
	}
	if err := getJSON(ctx, c.client, c.url+"/http/requests", &p.Requests); err != nil {
		return err
	}
	var partial []string
	if err := getJSON(ctx, c.client, c.url+"/http/server_zones", &p.ServerZones); err != nil {
		partial = append(partial, "server_zones: "+err.Error())
	}
	if err := getJSON(ctx, c.client, c.url+"/http/upstreams", &p.Upstreams); err != nil {
		partial = append(partial, "upstreams: "+err.Error())
	}
	RecordPlus(b, p)
	if len(partial) > 0 {
		return integrations.Partial(errors.New(strings.Join(partial, "; ")))
	}
	return nil
}
