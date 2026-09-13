package updater

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Docker is the subset of the Docker Engine API the compose engine uses.
type Docker interface {
	ListContainers(ctx context.Context, labels map[string]string) ([]ContainerSummary, error)
	InspectContainer(ctx context.Context, id string) (*Container, error)
	InspectImage(ctx context.Context, ref string) (*Image, error)
	PullImage(ctx context.Context, ref string) error
	CreateContainer(ctx context.Context, spec ContainerSpec) (string, error)
	StartContainer(ctx context.Context, id string) error
	StopContainer(ctx context.Context, id string, timeout time.Duration) error
	RenameContainer(ctx context.Context, id, name string) error
	RemoveContainer(ctx context.Context, id string) error
	WaitContainer(ctx context.Context, id string) (int, error)
	ContainerLogs(ctx context.Context, id string, tail int) (string, error)
	Exec(ctx context.Context, id string, cmd []string, stdout io.Writer) (exitCode int, stderr string, err error)
}

// ContainerSummary is an entry of GET /containers/json.
type ContainerSummary struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
}

// Name is the container name without the leading slash.
func (c ContainerSummary) Name() string {
	if len(c.Names) == 0 {
		return ""
	}
	return strings.TrimPrefix(c.Names[0], "/")
}

// Container is GET /containers/{id}/json. Config and HostConfig are kept as generic JSON so a
// recreated container keeps every setting, including ones this code does not know about.
type Container struct {
	ID              string         `json:"Id"`
	Name            string         `json:"Name"`
	Image           string         `json:"Image"`
	Config          map[string]any `json:"Config"`
	HostConfig      map[string]any `json:"HostConfig"`
	NetworkSettings struct {
		Networks map[string]map[string]any `json:"Networks"`
	} `json:"NetworkSettings"`
	State struct {
		Status   string `json:"Status"`
		Running  bool   `json:"Running"`
		ExitCode int    `json:"ExitCode"`
		Health   *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
}

// Image is GET /images/{name}/json.
type Image struct {
	ID          string         `json:"Id"`
	RepoDigests []string       `json:"RepoDigests"`
	Config      map[string]any `json:"Config"`
}

// ContainerSpec creates a container: Config (image, env, labels, …), HostConfig and endpoint
// settings per network (the first network is set at creation, the others are connected after).
type ContainerSpec struct {
	Name       string
	Config     map[string]any
	HostConfig map[string]any
	Networks   map[string]map[string]any
	// NetworkOrder fixes which network is attached first (map order is random).
	NetworkOrder []string
}

// DockerClient talks to the Docker Engine API with plain net/http.
type DockerClient struct {
	http *http.Client
	base string
}

// NewDockerClient connects to host: unix:///var/run/docker.sock, tcp://host:2375 or http(s)://.
func NewDockerClient(host string) (*DockerClient, error) {
	u, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("DOCKER_HOST: %w", err)
	}
	switch u.Scheme {
	case "unix":
		sock := u.Path
		tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}}
		return &DockerClient{http: &http.Client{Transport: tr}, base: "http://docker"}, nil
	case "tcp", "http":
		return &DockerClient{http: &http.Client{}, base: "http://" + u.Host}, nil
	case "https":
		return &DockerClient{http: &http.Client{}, base: "https://" + u.Host}, nil
	}
	return nil, fmt.Errorf("DOCKER_HOST: unsupported scheme %q", u.Scheme)
}

// DockerError is a non-2xx Engine API response.
type DockerError struct {
	Status  int
	Message string
}

func (e *DockerError) Error() string { return fmt.Sprintf("docker: %d %s", e.Status, e.Message) }

// IsNotFound reports a 404 from the Engine API.
func IsNotFound(err error) bool {
	var de *DockerError
	return errors.As(err, &de) && de.Status == http.StatusNotFound
}

func (c *DockerClient) request(ctx context.Context, method, path string, q url.Values, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var m struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(b, &m) != nil || m.Message == "" {
			m.Message = strings.TrimSpace(string(b))
		}
		return nil, &DockerError{Status: resp.StatusCode, Message: m.Message}
	}
	return resp, nil
}

func (c *DockerClient) call(ctx context.Context, method, path string, q url.Values, body, out any) error {
	resp, err := c.request(ctx, method, path, q, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ListContainers lists all containers (running or not) having every label.
func (c *DockerClient) ListContainers(ctx context.Context, labels map[string]string) ([]ContainerSummary, error) {
	var f []string
	for k, v := range labels {
		f = append(f, k+"="+v)
	}
	filters, _ := json.Marshal(map[string][]string{"label": f})
	var out []ContainerSummary
	err := c.call(ctx, http.MethodGet, "/containers/json", url.Values{"all": {"1"}, "filters": {string(filters)}}, nil, &out)
	return out, err
}

// InspectContainer implements Docker.
func (c *DockerClient) InspectContainer(ctx context.Context, id string) (*Container, error) {
	var out Container
	if err := c.call(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// InspectImage implements Docker.
func (c *DockerClient) InspectImage(ctx context.Context, ref string) (*Image, error) {
	var out Image
	// Image names contain slashes; the Engine API routes /images/{name:.*}/json.
	if err := c.call(ctx, http.MethodGet, "/images/"+ref+"/json", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PullImage pulls ref ("repo@sha256:…" or "repo:tag") and fails on stream errors.
func (c *DockerClient) PullImage(ctx context.Context, ref string) error {
	repo, tag := splitRef(ref)
	resp, err := c.request(ctx, http.MethodPost, "/images/create", url.Values{"fromImage": {repo}, "tag": {tag}}, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var msg struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) == nil && msg.Error != "" {
			return fmt.Errorf("pull %s: %s", ref, msg.Error)
		}
	}
	return sc.Err()
}

func splitRef(ref string) (repo, tag string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}

// CreateContainer implements Docker.
func (c *DockerClient) CreateContainer(ctx context.Context, spec ContainerSpec) (string, error) {
	body := map[string]any{}
	for k, v := range spec.Config {
		body[k] = v
	}
	body["HostConfig"] = spec.HostConfig
	order := spec.NetworkOrder
	if len(order) == 0 {
		for n := range spec.Networks {
			order = append(order, n)
		}
	}
	if len(order) > 0 {
		body["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{order[0]: spec.Networks[order[0]]}}
	}
	var out struct {
		ID string `json:"Id"`
	}
	if err := c.call(ctx, http.MethodPost, "/containers/create", url.Values{"name": {spec.Name}}, body, &out); err != nil {
		return "", err
	}
	for _, n := range order[min(1, len(order)):] {
		err := c.call(ctx, http.MethodPost, "/networks/"+url.PathEscape(n)+"/connect", nil,
			map[string]any{"Container": out.ID, "EndpointConfig": spec.Networks[n]}, nil)
		if err != nil {
			_ = c.RemoveContainer(context.WithoutCancel(ctx), out.ID)
			return "", fmt.Errorf("connect network %s: %w", n, err)
		}
	}
	return out.ID, nil
}

// StartContainer implements Docker (already running is fine).
func (c *DockerClient) StartContainer(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil, nil, nil)
}

// StopContainer implements Docker (already stopped is fine).
func (c *DockerClient) StopContainer(ctx context.Context, id string, timeout time.Duration) error {
	q := url.Values{"t": {strconv.Itoa(int(timeout / time.Second))}}
	return c.call(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/stop", q, nil, nil)
}

// RenameContainer implements Docker.
func (c *DockerClient) RenameContainer(ctx context.Context, id, name string) error {
	return c.call(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/rename", url.Values{"name": {name}}, nil, nil)
}

// RemoveContainer force-removes a container (anonymous volumes are kept).
func (c *DockerClient) RemoveContainer(ctx context.Context, id string) error {
	err := c.call(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id), url.Values{"force": {"1"}}, nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// WaitContainer waits until the container stops and returns its exit code.
func (c *DockerClient) WaitContainer(ctx context.Context, id string) (int, error) {
	var out struct {
		StatusCode int `json:"StatusCode"`
	}
	err := c.call(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/wait", url.Values{"condition": {"not-running"}}, nil, &out)
	return out.StatusCode, err
}

// ContainerLogs returns the last lines of stdout and stderr.
func (c *DockerClient) ContainerLogs(ctx context.Context, id string, tail int) (string, error) {
	resp, err := c.request(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/logs",
		url.Values{"stdout": {"1"}, "stderr": {"1"}, "tail": {strconv.Itoa(tail)}}, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if err := demux(resp.Body, &buf, &buf); err != nil {
		return buf.String(), err
	}
	return buf.String(), nil
}

// Exec runs cmd in a running container, streaming stdout to w.
func (c *DockerClient) Exec(ctx context.Context, id string, cmd []string, w io.Writer) (int, string, error) {
	var created struct {
		ID string `json:"Id"`
	}
	err := c.call(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/exec", nil,
		map[string]any{"Cmd": cmd, "AttachStdout": true, "AttachStderr": true, "Tty": false}, &created)
	if err != nil {
		return -1, "", err
	}
	resp, err := c.request(ctx, http.MethodPost, "/exec/"+created.ID+"/start", nil, map[string]any{"Detach": false, "Tty": false})
	if err != nil {
		return -1, "", err
	}
	var stderr limitedBuffer
	err = demux(resp.Body, w, &stderr)
	resp.Body.Close()
	if err != nil {
		return -1, stderr.String(), err
	}
	var inspect struct {
		ExitCode int  `json:"ExitCode"`
		Running  bool `json:"Running"`
	}
	if err := c.call(ctx, http.MethodGet, "/exec/"+created.ID+"/json", nil, nil, &inspect); err != nil {
		return -1, stderr.String(), err
	}
	return inspect.ExitCode, stderr.String(), nil
}

// demux splits Docker's multiplexed stdout/stderr stream (8-byte frame headers).
func demux(r io.Reader, stdout, stderr io.Writer) error {
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		n := int64(binary.BigEndian.Uint32(hdr[4:]))
		dst := stdout
		if hdr[0] == 2 {
			dst = stderr
		}
		if _, err := io.CopyN(dst, r, n); err != nil {
			return err
		}
	}
}

// limitedBuffer keeps the last 16 KiB written.
type limitedBuffer struct{ b []byte }

func (l *limitedBuffer) Write(p []byte) (int, error) {
	l.b = append(l.b, p...)
	if len(l.b) > 16<<10 {
		l.b = l.b[len(l.b)-16<<10:]
	}
	return len(p), nil
}

func (l *limitedBuffer) String() string { return string(l.b) }
