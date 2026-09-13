// Package integrations runs metric integrations (nginx, Redis, MySQL,
// PostgreSQL, Docker, …) bound to discovered services (D-031,
// semantic-conventions §6). An integration instance starts when a discovered
// service with a matching integration id appears and stops when it disappears.
// Every instance collects on its own goroutine with a timeout and backoff;
// failures never affect the rest of the agent.
package integrations

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/containers"
	"github.com/onuragtas/openlog/agents/infra/internal/discovery"
	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
)

// Integration is one integration type (e.g. redis). Implementations must be
// safe for concurrent use; per-instance state lives in the Collector.
type Integration interface {
	// ID is the integration id used by discovery rules (integration.id) and config.
	ID() string
	// Spec describes how endpoints are derived from a discovered service.
	Spec() EndpointSpec
	// New creates a collector for an instance and one endpoint candidate. It
	// must not block on the network; connections are opened by Collect.
	// Returning a *StatusError reports a status.
	New(inst *Instance, ep Endpoint) (Collector, error)
	// Hint returns a config.yaml snippet that configures the instance.
	Hint(inst *Instance) string
}

// Collector collects one instance.
type Collector interface {
	// Collect gathers one sample into b. A returned error (possibly with
	// partial data in b) marks the collection as failed.
	Collect(ctx context.Context, b *Batch) error
	// Close releases connections; Collect may be called again afterwards.
	Close()
}

// EndpointSpec tells the framework how to find an instance's endpoint.
type EndpointSpec struct {
	// DefaultPort is tried first among the service's ports (0: none).
	DefaultPort int
	// SkipPort excludes ports that are not the protocol port (e.g. MySQL X 33060).
	SkipPort func(port int) bool
	// UnixSockets are well-known socket paths (host paths) tried after TCP ports.
	UnixSockets []string
	// NoEndpoint means the integration does not connect to the service (docker).
	NoEndpoint bool
}

// Endpoint is a network address of an instance.
type Endpoint struct {
	Network string // "tcp" or "unix"
	Address string // host:port, or the socket path as seen by the agent (under host.root_path)
	Display string // host:port or unix:<host path>; never contains credentials
}

// String returns the display form.
func (e Endpoint) String() string { return e.Display }

// Host returns the host part of a TCP endpoint.
func (e Endpoint) Host() string {
	h, _, _ := net.SplitHostPort(e.Address)
	return h
}

// Port returns the port of a TCP endpoint (0 for unix sockets).
func (e Endpoint) Port() int {
	_, p, err := net.SplitHostPort(e.Address)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(p)
	return n
}

// TCP builds a TCP endpoint.
func TCP(host string, port int) Endpoint {
	a := net.JoinHostPort(host, strconv.Itoa(port))
	return Endpoint{Network: "tcp", Address: a, Display: a}
}

// Target is a discovered service bound to an integration.
type Target struct {
	Key           string // <rule_id>:<instance>
	RuleID        string
	Instance      string
	IntegrationID string
	AutoEnable    bool
	Requires      []string
	Ports         []discovery.PortRef
	Units         []string
	Containers    []containers.Container
}

// Requires reports whether the rule lists a requirement.
func (t Target) RequiresAny(req string) bool {
	for _, r := range t.Requires {
		if r == req {
			return true
		}
	}
	return false
}

// Instance is everything a collector needs about its service.
type Instance struct {
	Target   Target
	Settings config.InstanceSettings
	// Endpoints are candidates in order of preference; an explicit configured
	// endpoint is the only candidate.
	Endpoints []Endpoint
	// Explicit is true when Settings.Endpoint was configured.
	Explicit bool
	Timeout  time.Duration
	FS       *hostfs.FS
	Log      *slog.Logger
	// HostName is the host.name used for service.instance.id of local endpoints.
	HostName string
	// Memo keeps values across collector re-creation (e.g. the probed nginx
	// stub_status URL); nil-safe.
	Memo *Memo
}

// Memo is a small per-instance string store.
type Memo struct {
	mu sync.Mutex
	m  map[string]string
}

// Get returns a stored value ("" when unset or m is nil).
func (m *Memo) Get(k string) string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.m[k]
}

// Set stores a value; "" deletes it. No-op on a nil Memo.
func (m *Memo) Set(k, v string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if v == "" {
		delete(m.m, k)
		return
	}
	if m.m == nil {
		m.m = map[string]string{}
	}
	m.m[k] = v
}

// Password resolves the configured password.
func (in *Instance) Password() (string, error) { return in.Settings.Password.Resolve() }

// StatusError carries an integration status other than "error".
type StatusError struct {
	Status string // discovery.StatusNeedsConfiguration, discovery.StatusNotAvailable, discovery.StatusError
	Msg    string // sanitized reason
	// Static errors come from configuration and are not retried until the
	// configuration or the service changes.
	Static bool
}

func (e *StatusError) Error() string { return e.Msg }

// NeedsConfiguration returns a needs_configuration status error.
func NeedsConfiguration(msg string, static bool) error {
	return &StatusError{Status: discovery.StatusNeedsConfiguration, Msg: msg, Static: static}
}

// NotAvailable returns a static not_available status error.
func NotAvailable(msg string) error {
	return &StatusError{Status: discovery.StatusNotAvailable, Msg: msg, Static: true}
}

// ErrUnreachable wraps connection failures to one endpoint candidate so that
// the next candidate is tried.
var ErrUnreachable = errors.New("unreachable")

// ErrTryNext marks an error after which the next endpoint candidate is tried
// (e.g. nginx: the port answers HTTP but serves no stub_status page).
var ErrTryNext = errors.New("try next endpoint")

// PartialError reports a collection that produced data but missed some of it
// (e.g. a permission is missing for one query). The status stays "enabled"
// and the sanitized message is reported as integration.error.
type PartialError struct{ Err error }

func (e *PartialError) Error() string { return e.Err.Error() }
func (e *PartialError) Unwrap() error { return e.Err }

// Partial wraps a non-nil error as a PartialError.
func Partial(err error) error {
	if err == nil {
		return nil
	}
	return &PartialError{Err: err}
}

// IsUnreachable reports a connection-level failure (refused, timeout, no such socket).
func IsUnreachable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrUnreachable) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	var oe *net.OpError
	if errors.As(err, &oe) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "connection refused") || strings.Contains(s, "no such file or directory") ||
		strings.Contains(s, "i/o timeout") || strings.Contains(s, "no route to host") || strings.Contains(s, "connect: ")
}
