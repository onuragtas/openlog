// Package jolokia reads JMX attributes over Jolokia's HTTP/JSON bridge, which is how this agent reaches a
// JVM's MBeans (D-144).
//
// The alternative was writing a JMX client: Java RMI plus object serialization, thousands of lines that
// break on a Java release, with no mature Go implementation to lean on. Jolokia turns the same MBeans into
// one HTTP request, which this agent already knows how to make — the cost is moved to a `-javaagent` line
// the operator adds once, and it is visible and revertible rather than hidden in our code.
//
// Everything here is a *bulk* read: one POST carrying every attribute an integration wants, so a collection
// is one request whatever the number of MBeans. A per-attribute request would multiply a collection by
// fifty and make the JVM's own HTTP thread the bottleneck of monitoring it.
package jolokia

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/httpx"
)

// DefaultPort is the port the standalone Jolokia JVM agent listens on.
const DefaultPort = 8778

// ProbePaths are the paths a Jolokia endpoint is looked for at, in order. The agent's own default is
// `/jolokia`; an application that embeds it usually mounts it under its own context path.
var ProbePaths = []string{"/jolokia", "/actuator/jolokia", "/jmx", "/api/jolokia"}

// MaxBody bounds one answer: a bulk read of a few hundred attributes is tens of kilobytes.
const MaxBody = 8 << 20

// MaxReads bounds one bulk request, so a misconfigured integration cannot ask a JVM for ten thousand
// attributes in one go.
const MaxReads = 200

// Read is one attribute (or one MBean's worth of them) to read.
type Read struct {
	// MBean is the object name, e.g. "java.lang:type=Memory" or a pattern
	// ("kafka.server:type=BrokerTopicMetrics,name=*").
	MBean string
	// Attribute is the attribute to read; empty reads every attribute of the MBean.
	Attribute string
	// Path narrows the answer inside a composite attribute ("used" of HeapMemoryUsage).
	Path string
}

// Response is one answer of a bulk read.
type Response struct {
	// Value is the attribute's value: a number, a string, or a map (a composite attribute, or one entry per
	// MBean when the request named a pattern).
	Value any
	// Status is Jolokia's own status code (200 when the read worked).
	Status int
	// Error is what Jolokia said when it did not; an MBean that does not exist on this JVM is an error on
	// that one read, not on the request.
	Error string
	// Request echoes what was asked, so an answer can be matched to its read even when the order changes.
	MBean     string
	Attribute string
}

// OK reports whether the read produced a value.
func (r Response) OK() bool { return r.Status == http.StatusOK && r.Error == "" }

// Client reads attributes from one Jolokia endpoint.
type Client struct {
	inst *integrations.Instance
	http *http.Client
	// base is the host the probe paths are appended to; url is the endpoint that answered, remembered
	// across collections so the probing happens once.
	base string
	url  string
}

// New builds a client for the instance's endpoint. The base URL follows the instance settings, so a
// configured `endpoint` (an application's own context path) is used as it is.
func New(inst *integrations.Instance, ep integrations.Endpoint, servicePorts ...int) (*Client, error) {
	base, err := httpx.BaseURL(inst, ep, DefaultPort, servicePorts...)
	if err != nil {
		return nil, err
	}
	client, err := httpx.Client(inst)
	if err != nil {
		return nil, err
	}
	c := &Client{inst: inst, http: client, base: strings.TrimSuffix(base, "/")}
	// An endpoint the operator wrote is taken at face value: they know where they mounted it.
	if raw := strings.TrimSpace(inst.Settings.Endpoint); strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		c.url = c.base
	}
	return c, nil
}

// Close releases the idle connections.
func (c *Client) Close() { c.http.CloseIdleConnections() }

// Version asks Jolokia what it is, which is the cheapest proof that the endpoint is a Jolokia bridge and
// not something else answering JSON.
func (c *Client) Version(ctx context.Context) (string, error) {
	url, err := c.endpoint(ctx)
	if err != nil {
		return "", err
	}
	body, err := httpx.Get(ctx, c.http, c.inst, url+"/version", MaxBody)
	if err != nil {
		return "", err
	}
	var v struct {
		Status int `json:"status"`
		Value  struct {
			Agent    string `json:"agent"`
			Protocol string `json:"protocol"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &v); err != nil || v.Status != http.StatusOK {
		return "", fmt.Errorf("not a Jolokia endpoint: %w", integrations.ErrTryNext)
	}
	return v.Value.Agent, nil
}

// endpoint returns the URL that answers, probing the usual paths once and remembering the one that did.
func (c *Client) endpoint(ctx context.Context) (string, error) {
	if c.url != "" {
		return c.url, nil
	}
	var lastErr error
	for _, p := range ProbePaths {
		url := c.base + p
		body, err := httpx.Get(ctx, c.http, c.inst, url+"/version", MaxBody)
		if err != nil {
			lastErr = err
			if integrations.IsUnreachable(err) {
				return "", err
			}
			continue
		}
		var v struct {
			Status int `json:"status"`
		}
		if err := json.Unmarshal(body, &v); err != nil || v.Status != http.StatusOK {
			lastErr = fmt.Errorf("%s is not a Jolokia endpoint: %w", url, integrations.ErrTryNext)
			continue
		}
		c.url = url
		c.inst.Log.Info("jolokia endpoint found", "url", url)
		return url, nil
	}
	if lastErr == nil {
		lastErr = integrations.NeedsConfiguration("no Jolokia endpoint found; add the Jolokia JVM agent and set the endpoint if it is not on the usual path", false)
	}
	return "", lastErr
}

// jolokiaRequest is one entry of the bulk POST body.
type jolokiaRequest struct {
	Type      string `json:"type"`
	MBean     string `json:"mbean"`
	Attribute string `json:"attribute,omitempty"`
	Path      string `json:"path,omitempty"`
}

// jolokiaResponse is one entry of the answer.
type jolokiaResponse struct {
	Status  int             `json:"status"`
	Value   json.RawMessage `json:"value"`
	Error   string          `json:"error"`
	Request jolokiaRequest  `json:"request"`
}

// ReadAll performs one bulk read. The answers come back in the order of the reads, but each carries the
// request it answers, so a caller may also match them by MBean.
func (c *Client) ReadAll(ctx context.Context, reads []Read) ([]Response, error) {
	if len(reads) == 0 {
		return nil, nil
	}
	if len(reads) > MaxReads {
		return nil, fmt.Errorf("a bulk read asks for at most %d attributes, got %d", MaxReads, len(reads))
	}
	url, err := c.endpoint(ctx)
	if err != nil {
		return nil, err
	}
	body := make([]jolokiaRequest, 0, len(reads))
	for _, r := range reads {
		body = append(body, jolokiaRequest{Type: "read", MBean: r.MBean, Attribute: r.Attribute, Path: r.Path})
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	raw, err := c.post(ctx, url, payload)
	if err != nil {
		return nil, err
	}
	// A single read answers with an object rather than an array; the bulk form is what we send, but a
	// proxy or an old agent may still collapse it.
	var list []jolokiaResponse
	if err := json.Unmarshal(raw, &list); err != nil {
		var one jolokiaResponse
		if err2 := json.Unmarshal(raw, &one); err2 != nil {
			return nil, fmt.Errorf("jolokia answer: %w: %v", integrations.ErrTryNext, err)
		}
		list = []jolokiaResponse{one}
	}
	out := make([]Response, 0, len(list))
	for _, item := range list {
		r := Response{Status: item.Status, Error: item.Error, MBean: item.Request.MBean, Attribute: item.Request.Attribute}
		if len(item.Value) > 0 {
			_ = json.Unmarshal(item.Value, &r.Value)
		}
		out = append(out, r)
	}
	return out, nil
}

// post sends the bulk request. Jolokia's POST form is used rather than the GET one because an MBean name
// carries characters (`:`, `=`, `,`, `*`) that a URL path turns into an escaping problem.
func (c *Client) post(ctx context.Context, url string, payload []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if u := c.inst.Settings.Username; u != "" || c.inst.Settings.Password != "" {
		pw, err := c.inst.Settings.Password.Resolve()
		if err != nil {
			return nil, integrations.NeedsConfiguration("password: "+err.Error(), false)
		}
		req.SetBasicAuth(u, pw)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", integrations.ErrUnreachable, err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxBody))
	switch resp.StatusCode {
	case http.StatusOK:
		if readErr != nil {
			return nil, readErr
		}
		return body, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, integrations.NeedsConfiguration("authentication failed (HTTP "+resp.Status+"): check the user and password", false)
	case http.StatusNotFound:
		return nil, fmt.Errorf("POST %s: HTTP %s: %w", url, resp.Status, integrations.ErrTryNext)
	}
	return nil, fmt.Errorf("POST %s: HTTP %s", url, resp.Status)
}

// Number reads a numeric value out of a Jolokia answer. JSON gives every number as a float64, and an
// attribute that is not a number at all (a string, a null for an unsupported platform counter) reports so
// rather than becoming 0.
func Number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// Field reads a numeric field out of a composite value (`{"used": 123, "max": 456}`).
func Field(v any, name string) (float64, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return 0, false
	}
	return Number(m[name])
}

// ErrNoEndpoint reports that no Jolokia bridge answered.
var ErrNoEndpoint = errors.New("no Jolokia endpoint")
