// Package containers lists containers through the Docker Engine API (unix
// socket) and locates their cgroup v2 directories. It is used by container
// inventory, container metrics and discovery's container matcher.
package containers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"net"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// Limits applied to container metadata.
const (
	MaxLabels          = 100
	MaxLabelValueBytes = 256
)

// Port is a published or exposed container port.
type Port struct {
	IP          string `json:"ip,omitempty" yaml:"ip"`
	PrivatePort int    `json:"private_port" yaml:"private_port"`
	PublicPort  int    `json:"public_port,omitempty" yaml:"public_port"`
	Protocol    string `json:"protocol" yaml:"protocol"`
}

// Container is the body of a "container" inventory item.
type Container struct {
	ID      string            `json:"id" yaml:"id"`
	Name    string            `json:"name" yaml:"name"`
	Runtime string            `json:"runtime" yaml:"runtime"`
	Image   string            `json:"image" yaml:"image"`
	ImageID string            `json:"image_id" yaml:"image_id"`
	State   string            `json:"state" yaml:"state"`
	Created string            `json:"created,omitempty" yaml:"created"`
	Labels  map[string]string `json:"labels" yaml:"labels"`
	Ports   []Port            `json:"ports" yaml:"ports"`
	// Health is healthy, unhealthy or starting; empty without a healthcheck.
	Health string `json:"health,omitempty" yaml:"health"`
	// From GET /containers/{id}/json (inspect.go); empty until inspected.
	StartedAt    string `json:"started_at,omitempty" yaml:"started_at"`
	FinishedAt   string `json:"finished_at,omitempty" yaml:"finished_at"`
	RestartCount int    `json:"restart_count" yaml:"restart_count"`
	ExitCode     int    `json:"exit_code,omitempty" yaml:"exit_code"`
	// IPs are the container's network addresses (Docker NetworkSettings); used
	// by integrations to reach services, not part of the inventory body.
	IPs []string `json:"-" yaml:"ips"`
	// Log source of the container (container log collection), not part of the inventory body.
	LogPath   string `json:"-" yaml:"-"`
	LogDriver string `json:"-" yaml:"-"`
	Tty       bool   `json:"-" yaml:"-"`

	inspected bool
}

// Inspected reports whether the inspect details (start time, restart count, log source) are set.
func (c *Container) Inspected() bool { return c.inspected }

// Container attribute keys derived from Docker metadata (semantic-conventions §2 container metrics).
const (
	AttrID             = "container.id"
	AttrName           = "container.name"
	AttrImageName      = "container.image.name"
	AttrImageTags      = "container.image.tags"
	AttrRuntime        = "container.runtime"
	AttrComposeProject = "docker.compose.project"
	AttrComposeService = "docker.compose.service"
	AttrK8sPod         = "k8s.pod.name"
	AttrK8sNamespace   = "k8s.namespace.name"
	AttrK8sContainer   = "k8s.container.name"
)

// labelAttributes maps container labels to attributes: Docker Compose labels and the CRI
// labels that Kubernetes (dockershim, cri-dockerd) sets on Docker containers.
var labelAttributes = []struct{ label, attr string }{
	{"com.docker.compose.project", AttrComposeProject},
	{"com.docker.compose.service", AttrComposeService},
	{"io.kubernetes.pod.name", AttrK8sPod},
	{"io.kubernetes.pod.namespace", AttrK8sNamespace},
	{"io.kubernetes.container.name", AttrK8sContainer},
}

// Attributes returns the container identity attributes: container.id always, name, image,
// compose and Kubernetes names when Docker metadata (meta) is available, container.runtime when known.
func Attributes(id, runtime string, meta *Container) []*commonpb.KeyValue {
	attrs := []*commonpb.KeyValue{otlputil.Str(AttrID, id)}
	if meta != nil && meta.ID != "" {
		runtime = meta.Runtime
		if meta.Name != "" {
			attrs = append(attrs, otlputil.Str(AttrName, meta.Name))
		}
		if meta.Image != "" {
			name, tags := ImageName(meta.Image)
			attrs = append(attrs, otlputil.Str(AttrImageName, name))
			if len(tags) > 0 {
				attrs = append(attrs, otlputil.StrSlice(AttrImageTags, tags))
			}
		}
		for _, la := range labelAttributes {
			if v := meta.Labels[la.label]; v != "" {
				attrs = append(attrs, otlputil.Str(la.attr, v))
			}
		}
	}
	if runtime != "" {
		attrs = append(attrs, otlputil.Str(AttrRuntime, runtime))
	}
	return attrs
}

// ImageName splits an image reference into the name and tags used for the
// container.image.name / container.image.tags attributes:
// "docker.io/library/nginx:1.25" → ("docker.io/library/nginx", ["1.25"]),
// "redis@sha256:…" → ("redis", nil), "sha256:abc…" → ("sha256:abc…", nil).
func ImageName(ref string) (string, []string) {
	if strings.HasPrefix(ref, "sha256:") {
		return ref, nil
	}
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndexByte(ref, ':'); i > strings.LastIndexByte(ref, '/') {
		return ref[:i], []string{ref[i+1:]}
	}
	return ref, nil
}

// TODO(containerd): container inventory for containerd/CRI-O/Podman hosts
// (containerd gRPC socket or CRI). Until then their containers only get
// cgroup metrics keyed by container.id, without names or images.

// ErrNoRuntime means no container runtime socket exists on the host.
var ErrNoRuntime = errors.New("containers: no docker socket")

// Source lists Docker containers with a small cache.
type Source struct {
	FS *hostfs.FS
	// Socket is the host path of the Docker socket (resolved under the root).
	Socket  string
	Timeout time.Duration
	// Permission is called when the socket exists but may not be opened.
	Permission func()

	mu      sync.Mutex
	at      time.Time
	cached  []Container
	lastErr error
	details map[string]detailEntry
	// sock is the socket path of the last successful listing (used for log streams).
	sock string
}

// NewSource returns a source for the Docker socket at a host path.
func NewSource(fsys *hostfs.FS, socket string) *Source {
	return &Source{FS: fsys, Socket: socket, Timeout: 5 * time.Second}
}

func (s *Source) socketCandidates() []string {
	var out []string
	add := func(p string) {
		for _, o := range out {
			if o == p {
				return
			}
		}
		out = append(out, p)
	}
	add(s.FS.Path(s.Socket))
	// /var/run is usually a symlink to /run; under a root path an absolute
	// link would resolve inside the agent's container instead of the host.
	if rest, ok := strings.CutPrefix(s.Socket, "/var/run/"); ok {
		add(s.FS.Path("/run/" + rest))
	}
	return out
}

// List returns all containers (running and stopped). A cached result younger
// than maxAge is reused. When no socket exists, ErrNoRuntime is returned.
func (s *Source) List(ctx context.Context, maxAge time.Duration) ([]Container, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.at.IsZero() && time.Since(s.at) < maxAge {
		return s.cached, s.lastErr
	}
	s.cached, s.lastErr = s.list(ctx)
	s.at = time.Now()
	return s.cached, s.lastErr
}

// Cached returns the last listing without refreshing.
func (s *Source) Cached() []Container {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cached
}

// Fingerprint hashes the cached container set (id, state, image) for change detection.
func (s *Source) Fingerprint() uint64 {
	cs := s.Cached()
	keys := make([]string, 0, len(cs))
	for _, c := range cs {
		keys = append(keys, c.ID+"|"+c.State+"|"+c.Image)
	}
	sort.Strings(keys)
	h := fnv.New64a()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
	}
	return h.Sum64()
}

func (s *Source) list(ctx context.Context) ([]Container, error) {
	var lastErr error = ErrNoRuntime
	for _, sock := range s.socketCandidates() {
		body, err := s.get(ctx, sock, "/containers/json?all=1")
		if err == nil {
			cs, err := ParseDockerList(body)
			if err != nil {
				return nil, err
			}
			s.sock = sock
			s.inspect(ctx, sock, cs)
			return cs, nil
		}
		switch {
		case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOENT), errors.Is(err, syscall.ECONNREFUSED):
			continue
		case errors.Is(err, fs.ErrPermission), errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
			if s.Permission != nil {
				s.Permission()
			}
			// Candidates usually are the same socket (/var/run → /run): report once.
			return nil, fmt.Errorf("containers: %s: %w", sock, err)
		default:
			lastErr = fmt.Errorf("containers: %s: %w", sock, err)
		}
	}
	return nil, lastErr
}

func unixClient(sock string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
		DisableKeepAlives: true,
	}}
}

// Stream issues a streaming GET (e.g. /containers/{id}/logs?follow=1) and returns the
// response body; only ctx bounds it. The socket of the last listing is used, else the
// candidates in order.
func (s *Source) Stream(ctx context.Context, uri string) (io.ReadCloser, error) {
	s.mu.Lock()
	socks := s.socketCandidates()
	if s.sock != "" {
		socks = append([]string{s.sock}, socks...)
	}
	s.mu.Unlock()
	var lastErr error = ErrNoRuntime
	for _, sock := range socks {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+uri, nil)
		if err != nil {
			return nil, err
		}
		resp, err := unixClient(sock).Do(req)
		if err != nil {
			lastErr = fmt.Errorf("containers: %s: %w", sock, err)
			if errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
				return nil, lastErr
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return nil, fmt.Errorf("docker API %s: HTTP %d: %s", uri, resp.StatusCode, strings.TrimSpace(string(msg)))
		}
		return resp.Body, nil
	}
	return nil, lastErr
}

func (s *Source) get(ctx context.Context, sock, uri string) ([]byte, error) {
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+uri, nil)
	if err != nil {
		return nil, err
	}
	resp, err := unixClient(sock).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker API %s: HTTP %d", uri, resp.StatusCode)
	}
	return b, nil
}

type dockerContainer struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	ImageID string            `json:"ImageID"`
	Created int64             `json:"Created"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Labels  map[string]string `json:"Labels"`
	Ports   []struct {
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress         string `json:"IPAddress"`
			GlobalIPv6Address string `json:"GlobalIPv6Address"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

// ParseDockerList parses a GET /containers/json response.
func ParseDockerList(body []byte) ([]Container, error) {
	var raw []dockerContainer
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("containers: decode docker list: %w", err)
	}
	out := make([]Container, 0, len(raw))
	for _, r := range raw {
		c := Container{ID: r.ID, Runtime: "docker", Image: r.Image, ImageID: r.ImageID, State: r.State,
			Health: HealthFromStatus(r.Status), Labels: map[string]string{}, Ports: []Port{}}
		if len(r.Names) > 0 {
			c.Name = strings.TrimPrefix(r.Names[0], "/")
		}
		if r.Created > 0 {
			c.Created = time.Unix(r.Created, 0).UTC().Format(time.RFC3339)
		}
		keys := make([]string, 0, len(r.Labels))
		for k := range r.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			if i >= MaxLabels {
				break
			}
			c.Labels[k] = truncate(r.Labels[k], MaxLabelValueBytes)
		}
		nets := make([]string, 0, len(r.NetworkSettings.Networks))
		for n := range r.NetworkSettings.Networks {
			nets = append(nets, n)
		}
		sort.Strings(nets)
		for _, n := range nets {
			nw := r.NetworkSettings.Networks[n]
			for _, ip := range []string{nw.IPAddress, nw.GlobalIPv6Address} {
				if ip != "" {
					c.IPs = append(c.IPs, ip)
				}
			}
		}
		seen := map[Port]bool{}
		for _, p := range r.Ports {
			port := Port{IP: p.IP, PrivatePort: p.PrivatePort, PublicPort: p.PublicPort, Protocol: p.Type}
			if !seen[port] {
				seen[port] = true
				c.Ports = append(c.Ports, port)
			}
		}
		sort.Slice(c.Ports, func(i, j int) bool {
			a, b := c.Ports[i], c.Ports[j]
			if a.PrivatePort != b.PrivatePort {
				return a.PrivatePort < b.PrivatePort
			}
			if a.Protocol != b.Protocol {
				return a.Protocol < b.Protocol
			}
			return a.IP < b.IP
		})
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}

// CgroupRoot is the cgroup v2 mount point on the host.
const CgroupRoot = "/sys/fs/cgroup"

var idSegment = regexp.MustCompile(`^(docker-|cri-containerd-|crio-|libpod-|containerd-)?([0-9a-f]{64})(\.scope)?$`)

// Cgroup is the cgroup v2 directory of a container.
type Cgroup struct {
	ID      string
	Path    string // host path, e.g. /sys/fs/cgroup/system.slice/docker-<id>.scope
	Runtime string // docker, containerd, cri-o, podman; "" when unknown
}

// FindCgroups walks the cgroup v2 hierarchy (depth-limited) and returns the
// directories named after a 64-hex container id. It returns nil on cgroup v1 hosts.
func FindCgroups(fsys *hostfs.FS) map[string]Cgroup {
	if _, err := fsys.Stat(CgroupRoot + "/cgroup.controllers"); err != nil {
		return nil // not cgroup v2
	}
	out := map[string]Cgroup{}
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > 6 {
			return
		}
		entries, err := fsys.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			p := path.Join(dir, e.Name())
			if m := idSegment.FindStringSubmatch(e.Name()); m != nil {
				if _, dup := out[m[2]]; !dup {
					out[m[2]] = Cgroup{ID: m[2], Path: p, Runtime: runtimeOf(m[1], dir)}
				}
				continue // do not descend into a container's own hierarchy
			}
			walk(p, depth+1)
		}
	}
	walk(CgroupRoot, 0)
	return out
}

func runtimeOf(prefix, parent string) string {
	switch prefix {
	case "docker-":
		return "docker"
	case "cri-containerd-", "containerd-":
		return "containerd"
	case "crio-":
		return "cri-o"
	case "libpod-":
		return "podman"
	}
	if path.Base(parent) == "docker" {
		return "docker"
	}
	return ""
}
