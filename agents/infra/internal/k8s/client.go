// Package k8s is the Kubernetes support of the infra agent (semantic-conventions §7, D-070, D-071): a small typed
// REST client for the API server (list, watch, lease updates) and the kubelet, pod metadata for container metrics
// and logs (node mode), kubelet /stats/summary metrics (node mode), kube-state style cluster metrics and Kubernetes
// events (cluster mode, leader-elected through a Lease).
//
// k8s.io/client-go is not used: it would multiply the binary size and dependency tree of an agent that runs on every
// node, while the agent only needs a handful of read-only GETs, two watches and one Lease. Only the object fields the
// agent reads are decoded.
package k8s

import (
	"bufio"
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
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// In-cluster ServiceAccount files.
const (
	ServiceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"
	tokenFile         = "token"
	caFile            = "ca.crt"
	namespaceFile     = "namespace"
)

// tokenReload is how often the (projected, rotating) ServiceAccount token file is read again.
const tokenReload = time.Minute

// maxResponse bounds a non-watch response body.
const maxResponse = 256 << 20

// StatusError is a non-2xx API response.
type StatusError struct {
	Code    int
	Reason  string
	Message string
}

func (e *StatusError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("kubernetes API: HTTP %d %s: %s", e.Code, e.Reason, e.Message)
	}
	return fmt.Sprintf("kubernetes API: HTTP %d", e.Code)
}

// IsStatus reports whether err is a StatusError with the given HTTP code.
func IsStatus(err error, code int) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Code == code
}

// ErrGone is returned by Watch when the resource version is too old (410 Gone): the caller lists again.
var ErrGone = errors.New("kubernetes API: resource version expired")

// Client talks to one HTTPS endpoint (API server or kubelet) with a bearer token.
type Client struct {
	// Base is the endpoint URL without a trailing slash, e.g. https://10.96.0.1:443.
	Base string
	HTTP *http.Client
	// TokenPath is re-read every tokenReload; Token is used when TokenPath is empty.
	TokenPath string
	Token     string
	UserAgent string

	mu       sync.Mutex
	tokenAt  time.Time
	tokenVal string
}

// InClusterConfig describes the in-cluster API server connection.
type InClusterConfig struct {
	Host, Port string
	Dir        string // ServiceAccount directory
}

// InCluster returns a client for the API server from KUBERNETES_SERVICE_HOST/PORT and the ServiceAccount files.
func InCluster(getenv func(string) string, dir, userAgent string) (*Client, error) {
	host, port := getenv("KUBERNETES_SERVICE_HOST"), getenv("KUBERNETES_SERVICE_PORT")
	if host == "" {
		return nil, errors.New("kubernetes: KUBERNETES_SERVICE_HOST is not set (not running in a pod?)")
	}
	if port == "" {
		port = "443"
	}
	if dir == "" {
		dir = ServiceAccountDir
	}
	pool, err := loadCA(dir + "/" + caFile)
	if err != nil {
		return nil, err
	}
	c := &Client{
		Base:      "https://" + net.JoinHostPort(host, port),
		HTTP:      newHTTPClient(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}),
		TokenPath: dir + "/" + tokenFile,
		UserAgent: userAgent,
	}
	if _, err := c.token(); err != nil {
		return nil, err
	}
	return c, nil
}

// PodNamespace returns the namespace of the agent's pod from the ServiceAccount directory ("" when unknown).
func PodNamespace(dir string) string {
	if dir == "" {
		dir = ServiceAccountDir
	}
	b, err := os.ReadFile(dir + "/" + namespaceFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func loadCA(path string) (*x509.CertPool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("kubernetes: service account CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("kubernetes: no certificates in %s", path)
	}
	return pool, nil
}

// NewKubeletClient returns a client for a kubelet endpoint. With insecure the kubelet serving certificate is not
// verified (self-signed kubelet certificates are the default on kubeadm and kind); otherwise it must chain to the
// cluster CA in dir.
func NewKubeletClient(endpoint, dir string, insecure bool, userAgent string) (*Client, error) {
	if dir == "" {
		dir = ServiceAccountDir
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if insecure {
		cfg.InsecureSkipVerify = true //nolint:gosec // opt-in: kubernetes.kubelet.insecure_skip_verify
	} else {
		pool, err := loadCA(dir + "/" + caFile)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = pool
	}
	return &Client{Base: strings.TrimRight(endpoint, "/"), HTTP: newHTTPClient(cfg), TokenPath: dir + "/" + tokenFile, UserAgent: userAgent}, nil
}

func newHTTPClient(cfg *tls.Config) *http.Client {
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig:     cfg,
		Proxy:               nil, // in-cluster traffic never goes through HTTP(S)_PROXY
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConnsPerHost: 4,
	}}
}

func (c *Client) token() (string, error) {
	if c.TokenPath == "" {
		return c.Token, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tokenVal != "" && time.Since(c.tokenAt) < tokenReload {
		return c.tokenVal, nil
	}
	b, err := os.ReadFile(c.TokenPath)
	if err != nil {
		if c.tokenVal != "" {
			return c.tokenVal, nil // keep the last token on a transient read error
		}
		return "", fmt.Errorf("kubernetes: service account token: %w", err)
	}
	c.tokenVal, c.tokenAt = strings.TrimSpace(string(b)), time.Now()
	return c.tokenVal, nil
}

func (c *Client) request(ctx context.Context, method, path string, q url.Values, body any) (*http.Response, error) {
	u := c.Base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	tok, err := c.token()
	if err != nil {
		return nil, err
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return nil, statusError(resp)
	}
	return resp, nil
}

func statusError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	se := &StatusError{Code: resp.StatusCode}
	var st struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &st) == nil {
		se.Reason, se.Message = st.Reason, st.Message
	}
	if resp.StatusCode == http.StatusGone {
		return fmt.Errorf("%w: %s", ErrGone, se.Error())
	}
	return se
}

// Get decodes a GET response into out.
func (c *Client) Get(ctx context.Context, path string, q url.Values, out any) error {
	return c.Do(ctx, http.MethodGet, path, q, nil, out)
}

// Do sends a request with an optional JSON body and decodes the response into out (when not nil).
func (c *Client) Do(ctx context.Context, method, path string, q url.Values, body, out any) error {
	resp, err := c.request(ctx, method, path, q, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponse))
		return nil
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxResponse))
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("kubernetes API %s: decode: %w", path, err)
	}
	return nil
}

// WatchEvent is one event of a watch stream.
type WatchEvent struct {
	Type   string          `json:"type"` // ADDED, MODIFIED, DELETED, BOOKMARK, ERROR
	Object json.RawMessage `json:"object"`
}

// watchTimeout is the server-side timeout of one watch request; the caller watches again from the last version.
const watchTimeout = 5 * time.Minute

// Watch streams the watch of path (a collection) from resourceVersion rv and calls fn for every ADDED, MODIFIED and
// DELETED event. It returns the resource version to continue from when the server ends the stream (nil error), ErrGone
// when rv expired, or another error.
func (c *Client) Watch(ctx context.Context, path string, q url.Values, rv string, fn func(ev WatchEvent) error) (string, error) {
	params := url.Values{}
	for k, v := range q {
		params[k] = v
	}
	params.Set("watch", "1")
	params.Set("allowWatchBookmarks", "true")
	params.Set("timeoutSeconds", fmt.Sprint(int(watchTimeout/time.Second)))
	if rv != "" {
		params.Set("resourceVersion", rv)
	}
	resp, err := c.request(ctx, http.MethodGet, path, params, nil)
	if err != nil {
		return rv, err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(bufio.NewReaderSize(resp.Body, 64<<10))
	for {
		var ev WatchEvent
		if err := dec.Decode(&ev); err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return rv, ctx.Err()
			}
			return rv, fmt.Errorf("kubernetes watch %s: %w", path, err)
		}
		var meta struct {
			Metadata struct {
				ResourceVersion string `json:"resourceVersion"`
			} `json:"metadata"`
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(ev.Object, &meta)
		switch ev.Type {
		case "ERROR":
			if meta.Code == http.StatusGone {
				return rv, ErrGone
			}
			return rv, &StatusError{Code: meta.Code, Message: meta.Message}
		case "BOOKMARK":
		default:
			if err := fn(ev); err != nil {
				return rv, err
			}
		}
		if meta.Metadata.ResourceVersion != "" {
			rv = meta.Metadata.ResourceVersion
		}
	}
}
