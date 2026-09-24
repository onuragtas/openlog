// Package phpfpm implements the PHP-FPM integration: it reads every pool's status page over FastCGI and
// emits the metrics a pool is sized with — listen queue, idle and active workers, how often max_children
// was reached and how many requests ran past request_slowlog_timeout (semantic-conventions §6.18).
//
// Pools are found in the PHP-FPM configuration rather than guessed from well-known socket paths. A host
// running one pool per site (HestiaCP, cPanel, Plesk) has dozens of sockets named after the sites, which no
// fixed list can enumerate, and each pool is a separate resource: one pool's queue says nothing about
// another's. The well-known paths remain as a fallback for hosts whose pool files cannot be read, such as a
// container that mounts only the socket.
//
// The status page is read over FastCGI rather than HTTP on purpose: PHP-FPM speaks FastCGI, and reaching
// /status over HTTP would mean the web server in front of it is configured to proxy that path, which is a
// change to somebody else's configuration. Talking to the pool socket needs only pm.status_path, which is
// PHP-FPM's own setting.
package phpfpm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
	"github.com/onuragtas/openlog/agents/infra/internal/phpaccess"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// DefaultPort is the port of a TCP pool (the listen = 9000 of the official images).
const DefaultPort = 9000

// SocketPaths are well-known pool sockets, used only when no pool file could be read. Only paths that exist
// become endpoint candidates, so naming the per-version Debian/Ubuntu sockets costs nothing on a host that
// has none.
var SocketPaths = []string{
	"/run/php/php-fpm.sock", "/run/php/php8.4-fpm.sock", "/run/php/php8.3-fpm.sock", "/run/php/php8.2-fpm.sock",
	"/run/php/php8.1-fpm.sock", "/run/php/php8.0-fpm.sock", "/run/php/php7.4-fpm.sock",
	"/var/run/php/php-fpm.sock", "/run/php-fpm/www.sock", "/var/run/php-fpm/www.sock", "/run/php-fpm.sock",
	"/var/run/php-fpm.sock", "/run/php-fpm/php-fpm.sock", "/tmp/php-fpm.sock",
}

// StatusPaths are the pm.status_path values tried when the pool file does not name one. There is no
// default: a pool without the setting serves no status page at all, which is why "not configured" is a
// status of its own.
var StatusPaths = []string{"/status", "/fpm-status", "/php-fpm-status", "/php_status", "/fpm_status"}

// Resource attributes of a pool.
const (
	AttrPool           = "phpfpm.pool.name"
	AttrProcessManager = "phpfpm.process_manager"
)

// memoPath is the Instance.Memo key of the status path found for one listen address.
const memoPath = "phpfpm.status_path "

// Integration is the PHP-FPM integration.
type Integration struct{}

// ID implements integrations.Integration.
func (Integration) ID() string { return config.IntegrationPHPFPM }

// Spec implements integrations.Integration.
func (Integration) Spec() integrations.EndpointSpec {
	return integrations.EndpointSpec{DefaultPort: DefaultPort, UnixSockets: SocketPaths}
}

// Hint implements integrations.Integration.
func (Integration) Hint(inst *integrations.Instance) string {
	where := "127.0.0.1:9000"
	for _, e := range inst.Endpoints {
		where = strings.TrimPrefix(e.Display, "unix:")
		if e.Network == "unix" {
			where = "unix:" + where
		}
		break
	}
	return fmt.Sprintf(`# No pool answered with a status page. The agent reads every pool it finds in the PHP-FPM
# configuration (/etc/php/<version>/fpm/pool.d/*.conf, /etc/php-fpm.d/*.conf on RHEL) and never changes it.
#
# "permission denied" on a socket: pool sockets are 0660 owned by the web server's user, and the agent runs
# as openlog-agent. One group membership covers every pool on the host:
#
#   usermod -aG www-data openlog-agent && systemctl restart openlog-infra-agent
#
# (per pool, without widening anything else: listen.acl_users = openlog-agent)
#
# No status page: pm.status_path is unset in the pool, so it has nothing to answer with. Add it to the pool
# file — or, on a panel that generates them, to the template it generates from:
#
#   pm.status_path = /status
#
# Then reload PHP-FPM ("systemctl reload php8.3-fpm"). A reload is enough; no requests are dropped. The page
# is read over the pool's own socket, so it is never reachable from the internet and the web server in front
# of PHP-FPM needs no location block.
#
# A host whose pool files cannot be read falls back to one endpoint, which can be set in openlog
# (host -> Integrations -> PHP-FPM) or in config.yaml:
integrations:
  php-fpm:
    endpoint: %s`, where)
}

// New implements integrations.Integration.
func (Integration) New(inst *integrations.Instance, ep integrations.Endpoint) (integrations.Collector, error) {
	switch ep.Network {
	case "unix", "tcp":
		return &collector{inst: inst, ep: ep}, nil
	default:
		return nil, fmt.Errorf("unsupported endpoint %s: %w", ep.Display, integrations.ErrTryNext)
	}
}

type collector struct {
	inst *integrations.Instance
	ep   integrations.Endpoint
	// path is the status path that answered in fallback mode; "" until the first successful fetch.
	path string
}

func (c *collector) Close() {}

// target is one pool to read: where it listens and what it calls its status page.
type target struct {
	pool       string // pool name from the file (the status page reports its own, which wins)
	network    string
	address    string
	display    string
	statusPath string // from pm.status_path; "" means probe StatusPaths
}

// Collect implements integrations.Collector.
func (c *collector) Collect(ctx context.Context, b *integrations.Batch) error {
	if targets := c.discover(); len(targets) > 0 {
		return c.collectPools(ctx, b, targets)
	}
	// No pool file could be read: the endpoint the framework derived is all there is.
	st, err := c.fetch(ctx)
	if err != nil {
		return err
	}
	Record(b, st)
	return nil
}

// discover lists the pools of the PHP-FPM configuration, deduplicated by listen address and bounded by
// phpaccess.MaxPools (which the pool reader already applies).
func (c *collector) discover() []target {
	if c.inst.FS == nil {
		return nil
	}
	var out []target
	seen := map[string]bool{}
	for _, p := range phpaccess.DiscoverPools(c.inst.FS.Root()) {
		network, address, display, ok := c.dialTarget(p.Listen)
		if !ok || seen[display] {
			continue
		}
		seen[display] = true
		out = append(out, target{pool: p.Name, network: network, address: address, display: display, statusPath: p.StatusPath})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].display < out[j].display })
	return out
}

// dialTarget turns a pool's listen value into something to dial: a unix socket path (mapped through the
// host root), "host:port", or a bare port on loopback.
func (c *collector) dialTarget(listen string) (network, address, display string, ok bool) {
	listen = strings.TrimSpace(listen)
	switch {
	case listen == "":
		return "", "", "", false
	case strings.HasPrefix(listen, "/"):
		return "unix", c.inst.FS.Path(listen), "unix:" + listen, true
	case strings.Contains(listen, ":"):
		return "tcp", listen, listen, true
	default:
		if n, err := strconv.Atoi(listen); err == nil && n > 0 && n < 65536 {
			a := net.JoinHostPort("127.0.0.1", listen)
			return "tcp", a, a, true
		}
		return "", "", "", false
	}
}

// collectPools reads every pool and records one resource each. Pools that answer are recorded even when
// others fail, because a host that runs one pool per site will always have a few that are down or
// unconfigured, and losing the rest with them would make the integration useless there.
func (c *collector) collectPools(ctx context.Context, b *integrations.Batch, targets []target) error {
	var ok int
	var denied, noStatus, failed []string
	for _, t := range targets {
		if ctx.Err() != nil {
			break
		}
		st, err := c.readPool(ctx, t)
		switch {
		case err == nil:
			ok++
			Record(b, st)
		case isPermission(err):
			denied = append(denied, t.display)
		case errorIsNotStatus(err):
			noStatus = append(noStatus, t.pool)
		default:
			failed = append(failed, t.display+": "+err.Error())
		}
	}

	if ok > 0 {
		var parts []string
		if len(denied) > 0 {
			parts = append(parts, fmt.Sprintf("%d pool sockets refused the connection", len(denied)))
		}
		if len(noStatus) > 0 {
			parts = append(parts, fmt.Sprintf("%d pools have no pm.status_path (%s)", len(noStatus), list(noStatus)))
		}
		if len(failed) > 0 {
			parts = append(parts, list(failed))
		}
		if len(parts) > 0 {
			return integrations.Partial(errors.New(strings.Join(parts, "; ")))
		}
		return nil
	}

	switch {
	case len(denied) > 0:
		return integrations.NeedsConfiguration(fmt.Sprintf(
			"the agent may not connect to any of the %d pool sockets (permission denied): add openlog-agent to the group that owns them, or set listen.acl_users in each pool", len(denied)), false)
	case len(noStatus) > 0:
		return integrations.NeedsConfiguration(fmt.Sprintf(
			"pm.status_path is not set on any of the %d pools (%s)", len(noStatus), list(noStatus)), false)
	case len(failed) > 0:
		return errors.New(list(failed))
	}
	return ctx.Err()
}

// list joins at most five names, saying how many more there are.
func list(v []string) string {
	if len(v) <= 5 {
		return strings.Join(v, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(v[:5], ", "), len(v)-5)
}

// readPool reads one pool, using pm.status_path when the file named one and probing otherwise.
func (c *collector) readPool(ctx context.Context, t target) (Status, error) {
	if t.statusPath != "" {
		return c.read(ctx, t.network, t.address, t.statusPath)
	}
	key := memoPath + t.display
	paths := StatusPaths
	if m := c.inst.Memo.Get(key); m != "" {
		paths = append([]string{m}, paths...)
	}
	var last error
	for _, p := range paths {
		st, err := c.read(ctx, t.network, t.address, p)
		if err == nil {
			c.inst.Memo.Set(key, p)
			return st, nil
		}
		last = err
		if !errorIsNotStatus(err) {
			return Status{}, err // unreachable or refused: the next path will not help
		}
	}
	c.inst.Memo.Set(key, "")
	return Status{}, last
}

// fetch reads the status page of the single derived endpoint, finding its path once and remembering it.
// Only hosts whose pool files cannot be read get here.
func (c *collector) fetch(ctx context.Context) (Status, error) {
	if c.path != "" {
		st, err := c.read(ctx, c.ep.Network, c.ep.Address, c.path)
		if err == nil {
			return st, nil
		}
		// The path stopped working (the pool was reconfigured): look again on the next collection.
		c.path = ""
		if integrations.IsUnreachable(err) {
			return Status{}, err
		}
	}
	key := memoPath + c.ep.Address
	paths := StatusPaths
	if m := c.inst.Memo.Get(key); m != "" {
		paths = append([]string{m}, paths...)
	}
	var refused bool
	for _, p := range paths {
		st, err := c.read(ctx, c.ep.Network, c.ep.Address, p)
		switch {
		case err == nil:
			c.path = p
			c.inst.Memo.Set(key, p)
			c.inst.Log.Info("php-fpm status page found", "endpoint", c.ep.Display, "status_path", p)
			return st, nil
		case isPermission(err):
			// Another endpoint candidate may be readable, so let the framework try the rest; when none
			// works it turns the try-next error into needs_configuration itself.
			return Status{}, fmt.Errorf("the agent may not connect to %s (permission denied); grant openlog-agent access to the pool socket: %w",
				c.ep.Display, integrations.ErrTryNext)
		case integrations.IsUnreachable(err):
			return Status{}, err
		case errorIsNotStatus(err):
			refused = true // the pool answered, but not with a status page
		default:
			return Status{}, err
		}
	}
	if refused {
		c.inst.Memo.Set(key, "")
		return Status{}, integrations.NeedsConfiguration(
			"pm.status_path is not set on this pool (tried "+strings.Join(StatusPaths, ", ")+")", false)
	}
	return Status{}, fmt.Errorf("no PHP-FPM status page on %s: %w", c.ep.Display, integrations.ErrTryNext)
}

// errNotStatus marks a response that is not a status page, so the next path is tried.
type errNotStatus struct{ error }

func errorIsNotStatus(err error) bool {
	var e errNotStatus
	return errors.As(err, &e)
}

// errPermission marks a socket the agent may not connect to.
type errPermission struct{ error }

func isPermission(err error) bool {
	var e errPermission
	return errors.As(err, &e)
}

// read performs one status request against one listen address.
func (c *collector) read(ctx context.Context, network, address, path string) (Status, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err != nil {
		// A pool socket the agent may not connect to is a configuration answer, not an unreachable
		// endpoint: the socket is there and PHP-FPM is listening on it. Pool sockets are 0660 owned by
		// the web server's user, and the agent runs as openlog-agent.
		if errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
			return Status{}, errPermission{err}
		}
		return Status{}, fmt.Errorf("%w: %v", integrations.ErrUnreachable, err)
	}
	defer conn.Close()
	params := map[string]string{
		"GATEWAY_INTERFACE": "CGI/1.1",
		"REQUEST_METHOD":    "GET",
		"SCRIPT_NAME":       path,
		"SCRIPT_FILENAME":   path,
		"REQUEST_URI":       path + "?json",
		"QUERY_STRING":      "json",
		"SERVER_PROTOCOL":   "HTTP/1.1",
		"SERVER_SOFTWARE":   "openlog-infra-agent",
		"CONTENT_LENGTH":    "0",
		"REMOTE_ADDR":       "127.0.0.1",
	}
	raw, stderr, err := fcgiDo(ctx, conn, params)
	if err != nil {
		return Status{}, err
	}
	if len(stderr) > 0 {
		// "Access to the script '/status' has been denied" and friends: the pool answered, the page is not here.
		return Status{}, errNotStatus{fmt.Errorf("%s: %s", path, firstLine(stderr))}
	}
	// A pool without this status path answers 404 ("File not found"), which is a configuration answer,
	// not a broken endpoint.
	if code := cgiStatus(raw); code != 200 {
		return Status{}, errNotStatus{fmt.Errorf("%s: the pool answered HTTP %d", path, code)}
	}
	st, err := Parse(splitCGI(raw))
	if err != nil {
		return Status{}, errNotStatus{fmt.Errorf("%s: %w", path, err)}
	}
	return st, nil
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// Status is a parsed PHP-FPM status page (the JSON format, pm.status_path with ?json).
type Status struct {
	Pool               string `json:"pool"`
	ProcessManager     string `json:"process manager"`
	StartTime          int64  `json:"start time"`
	StartSince         int64  `json:"start since"`
	AcceptedConn       int64  `json:"accepted conn"`
	ListenQueue        int64  `json:"listen queue"`
	MaxListenQueue     int64  `json:"max listen queue"`
	ListenQueueLen     int64  `json:"listen queue len"`
	IdleProcesses      int64  `json:"idle processes"`
	ActiveProcesses    int64  `json:"active processes"`
	TotalProcesses     int64  `json:"total processes"`
	MaxActiveProcesses int64  `json:"max active processes"`
	MaxChildrenReached int64  `json:"max children reached"`
	SlowRequests       int64  `json:"slow requests"`
}

// Parse parses a status page body. A body without a pool name is not a status page.
func Parse(body []byte) (Status, error) {
	var st Status
	if err := json.Unmarshal(body, &st); err != nil {
		return Status{}, fmt.Errorf("response is not a PHP-FPM status page: %w", err)
	}
	if st.Pool == "" {
		return Status{}, fmt.Errorf("response is not a PHP-FPM status page: no pool name")
	}
	return st, nil
}

// Record emits one pool's metrics on its own resource, so a host with a pool per site keeps them apart.
//
// The cumulative sums carry no start time: every pool starts when its master forks it, and one batch holds
// several pools, so a batch-wide start would label most of them with another pool's. nginx and PostgreSQL
// sums carry none either (semantic-conventions §6.1).
// poolAttrs are the resource attributes that tell one pool from another. The name comes from the status
// page rather than the pool file: the page reports what PHP-FPM itself calls the pool.
func poolAttrs(st Status) []*commonpb.KeyValue {
	attrs := []*commonpb.KeyValue{otlputil.Str(AttrPool, st.Pool)}
	if st.ProcessManager != "" {
		attrs = append(attrs, otlputil.Str(AttrProcessManager, st.ProcessManager))
	}
	return attrs
}

func Record(b *integrations.Batch, st Status) {
	s := b.Resource(poolAttrs(st)...)
	s.SumInt("phpfpm.uptime", "s", true, st.StartSince)
	s.SumInt("phpfpm.connections.accepted", "{connections}", true, st.AcceptedConn)
	s.SumInt("phpfpm.requests.slow", "{requests}", true, st.SlowRequests)
	s.SumInt("phpfpm.max_children_reached", "{events}", true, st.MaxChildrenReached)
	s.GaugeInt("phpfpm.listen_queue.current", "{requests}", st.ListenQueue)
	s.GaugeInt("phpfpm.listen_queue.max", "{requests}", st.MaxListenQueue)
	// listen queue len is the backlog of the listening socket; 0 on a unix socket without a backlog setting.
	if st.ListenQueueLen > 0 {
		s.GaugeInt("phpfpm.listen_queue.limit", "{requests}", st.ListenQueueLen)
	}
	s.SumInt("phpfpm.processes.current", "{processes}", false, st.IdleProcesses, otlputil.Str("state", "idle"))
	s.SumInt("phpfpm.processes.current", "{processes}", false, st.ActiveProcesses, otlputil.Str("state", "active"))
	s.GaugeInt("phpfpm.processes.max_active", "{processes}", st.MaxActiveProcesses)
}
