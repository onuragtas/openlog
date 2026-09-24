// Package phpfpm implements the PHP-FPM integration: it reads a pool's status page over FastCGI and emits
// the pool metrics an operator sizes a pool with — listen queue, idle and active workers, how often
// max_children was reached and how many requests ran past request_slowlog_timeout
// (semantic-conventions §6.18).
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
	"strings"
	"syscall"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"
)

// DefaultPort is the port of a TCP pool (the listen = 9000 of the official images).
const DefaultPort = 9000

// SocketPaths are well-known pool sockets, tried after TCP ports. Only paths that exist become endpoint
// candidates, so naming the per-version Debian/Ubuntu sockets costs nothing on a host that has none.
var SocketPaths = []string{
	"/run/php/php-fpm.sock", "/run/php/php8.4-fpm.sock", "/run/php/php8.3-fpm.sock", "/run/php/php8.2-fpm.sock",
	"/run/php/php8.1-fpm.sock", "/run/php/php8.0-fpm.sock", "/run/php/php7.4-fpm.sock",
	"/var/run/php/php-fpm.sock", "/run/php-fpm/www.sock", "/var/run/php-fpm/www.sock", "/run/php-fpm.sock",
	"/var/run/php-fpm.sock", "/run/php-fpm/php-fpm.sock", "/tmp/php-fpm.sock",
}

// StatusPaths are the pm.status_path values tried, in order. There is no default: a pool without the
// setting serves no status page at all, which is why "not configured" is a status of its own.
var StatusPaths = []string{"/status", "/fpm-status", "/php-fpm-status", "/php_status", "/fpm_status"}

// Resource attributes of the pool.
const (
	AttrPool           = "phpfpm.pool.name"
	AttrProcessManager = "phpfpm.process_manager"
)

// memoPath is the Instance.Memo key of the status path found for an endpoint.
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
	return fmt.Sprintf(`# The pool's status page could not be read (tried %s on %s). The agent never changes the
# PHP-FPM configuration; both causes are fixed in the pool file
# (/etc/php/<version>/fpm/pool.d/www.conf, /etc/php-fpm.d/www.conf on RHEL).
#
# "permission denied" on the socket: the pool socket is 0660 owned by the web server's user, and the
# agent runs as openlog-agent. Let it connect, without widening anything else:
#
#   listen.acl_users = openlog-agent
#
# (a group also works: usermod -aG www-data openlog-agent, then restart openlog-infra-agent.)
#
# No status page at all: pm.status_path is unset, so the pool has nothing to answer with. Add:
#
#   pm.status_path = /status
#
# Then reload PHP-FPM ("systemctl reload php8.3-fpm"). A reload is enough; no requests are dropped.
# The page is read over the pool's own socket, so it is never reachable from the internet, and the
# web server in front of PHP-FPM needs no location block.
#
# The agent finds the page automatically on its next attempt. If the pool listens elsewhere, or
# pm.status_path has another value, set the endpoint in openlog (host -> Integrations -> PHP-FPM)
# or in config.yaml:
integrations:
  php-fpm:
    endpoint: %s`, strings.Join(StatusPaths, ", "), where, where)
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
	// path is the status path that answered; "" until the first successful fetch.
	path string
}

func (c *collector) Close() {}

// Collect implements integrations.Collector.
func (c *collector) Collect(ctx context.Context, b *integrations.Batch) error {
	st, err := c.fetch(ctx)
	if err != nil {
		return err
	}
	Record(b, st)
	return nil
}

// fetch reads the status page, finding its path on the first collection and remembering it.
func (c *collector) fetch(ctx context.Context) (Status, error) {
	if c.path != "" {
		st, err := c.read(ctx, c.path)
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
		st, err := c.read(ctx, p)
		switch {
		case err == nil:
			c.path = p
			c.inst.Memo.Set(key, p)
			c.inst.Log.Info("php-fpm status page found", "endpoint", c.ep.Display, "status_path", p)
			return st, nil
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

// read performs one status request for path.
func (c *collector) read(ctx context.Context, path string) (Status, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, c.ep.Network, c.ep.Address)
	if err != nil {
		// A pool socket the agent may not connect to is a configuration answer, not an unreachable
		// endpoint: the socket is there and PHP-FPM is listening on it. Pool sockets are 0660 owned by
		// the web server's user, and the agent runs as openlog-agent (docker does the same, D-031).
		if errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
			return Status{}, integrations.NeedsConfiguration(
				"the agent may not connect to "+c.ep.Display+" (permission denied); grant openlog-agent access to the pool socket", false)
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

// Record emits the pool metrics. Counters are cumulative from the pool's start, which "start time" gives.
func Record(b *integrations.Batch, st Status) {
	b.SetResourceAttr(otlputil.Str(AttrPool, st.Pool))
	if st.ProcessManager != "" {
		b.SetResourceAttr(otlputil.Str(AttrProcessManager, st.ProcessManager))
	}
	if st.StartTime > 0 {
		b.SetStartTime(time.Unix(st.StartTime, 0))
	}
	s := b.Resource()
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
