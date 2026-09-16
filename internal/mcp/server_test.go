package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolNames are the tools this server promises (docs/operations/mcp.md §3).
var toolNames = []string{
	"get_incident", "list_incidents", "list_services", "list_slos", "oql_schema",
	"run_oql", "search_logs", "service_summary", "slo_status",
}

// connect runs an MCP session against the service over in-memory transports, so the tools are
// exercised through the real protocol (schema validation included) rather than by calling handlers.
func connect(t *testing.T, s *Service, cr Creds) *mcpsdk.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	ctx := context.Background()
	go func() { _ = s.Server(cr).Run(ctx, serverTransport) }()
	session, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// Every tool must be listed, described, read-only and carry an input schema the SDK accepted.
func TestServerExposesReadOnlyTools(t *testing.T) {
	api := newFakeAPI(t)
	session := connect(t, newTestService(t, api), callerCreds)
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	got := map[string]*mcpsdk.Tool{}
	for _, tool := range res.Tools {
		got[tool.Name] = tool
	}
	if len(got) != len(toolNames) {
		t.Errorf("%d tools, want %d", len(got), len(toolNames))
	}
	for _, name := range toolNames {
		tool, ok := got[name]
		if !ok {
			t.Errorf("tool %s is missing", name)
			continue
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %s is not marked read-only", name)
		}
		// The description is what a model reads to choose the tool.
		if len(tool.Description) < 80 {
			t.Errorf("tool %s has a thin description: %q", name, tool.Description)
		}
		if tool.InputSchema == nil {
			t.Errorf("tool %s has no input schema", name)
		}
	}
}

func TestCallToolThroughProtocol(t *testing.T) {
	api := newFakeAPI(t)
	api.body = `{"slos": [], "status_truncated": false}`
	session := connect(t, newTestService(t, api), callerCreds)
	res, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "list_slos"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", contentText(res))
	}
	if res.StructuredContent == nil {
		t.Error("no structured content")
	}
	if !strings.Contains(contentText(res), "status_truncated") {
		t.Errorf("content %q", contentText(res))
	}
	// The call reached the API as the session's caller, not as the configured fallback key.
	if got := api.last().Header.Get("Authorization"); got != "Bearer ola_caller" {
		t.Errorf("Authorization %q", got)
	}
}

// The protocol must reject an argument the schema forbids before a handler (and the API) sees it.
func TestCallToolValidatesArguments(t *testing.T) {
	api := newFakeAPI(t)
	session := connect(t, newTestService(t, api), callerCreds)
	// run_oql requires "query"; sending a number for it must not reach the API.
	_, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: "run_oql", Arguments: map[string]any{"query": 42},
	})
	if err == nil {
		t.Error("no error for an argument of the wrong type")
	}
	if len(api.requests) != 0 {
		t.Errorf("%d requests sent for invalid arguments", len(api.requests))
	}
}

// An API error becomes a tool error the model can read, not a protocol failure.
func TestCallToolReportsAPIErrors(t *testing.T) {
	api := newFakeAPI(t)
	api.status = http.StatusForbidden
	api.body = `{"error":{"code":"permission_denied","message":"your role does not allow reading telemetry"}}`
	session := connect(t, newTestService(t, api), callerCreds)
	res, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "list_incidents"})
	if err != nil {
		t.Fatalf("CallTool returned a protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatal("result is not an error")
	}
	if !strings.Contains(contentText(res), "read-only") {
		t.Errorf("content %q", contentText(res))
	}
}

func contentText(res *mcpsdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// Without a key no MCP session starts: the http transport never talks to the API unauthenticated.
func TestHTTPHandlerRequiresAKey(t *testing.T) {
	api := newFakeAPI(t)
	cfg := testConfig(api.srv.URL)
	cfg.Transport, cfg.APIKey = TransportHTTP, "" // no fallback key configured
	s := NewService(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	srv := httptest.NewServer(s.HTTPHandler())
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", resp.StatusCode)
	}
	if got := resp.Header.Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
		t.Errorf("WWW-Authenticate %q", got)
	}
	var body struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if body.Error.Code != "unauthenticated" {
		t.Errorf("error code %q", body.Error.Code)
	}
	if len(api.requests) != 0 {
		t.Errorf("%d requests reached the API without a key", len(api.requests))
	}
}

// A request that brings a key gets past the gate (the fallback key is not needed).
func TestHTTPHandlerAcceptsARequestKey(t *testing.T) {
	api := newFakeAPI(t)
	cfg := testConfig(api.srv.URL)
	cfg.Transport, cfg.APIKey = TransportHTTP, ""
	s := NewService(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	srv := httptest.NewServer(s.HTTPHandler())
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer ola_caller")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Error("a request with a key was refused")
	}
}

func TestBearer(t *testing.T) {
	cases := map[string]string{
		"Bearer ola_key": "ola_key", "bearer ola_key": "ola_key", "BEARER  ola_key ": "ola_key",
		"": "", "ola_key": "", "Basic dXNlcjpwYXNz": "", "Bearer": "", "Bearer ": "",
	}
	for header, want := range cases {
		if got := bearer(header); got != want {
			t.Errorf("bearer(%q) = %q, want %q", header, got, want)
		}
	}
}

// A stdio session ends when the client closes the stream; that is a normal stop, not a failure, so
// the binary must exit 0. The SDK reports it as its "closing" wire error wrapping the read error.
func TestClosedByPeer(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"server closing", fmt.Errorf("%w: %v", &jsonrpc.Error{Code: codeServerClosing, Message: "server is closing"}, io.EOF), true},
		{"client closing", fmt.Errorf("%w: %v", &jsonrpc.Error{Code: codeClientClosing, Message: "client is closing"}, io.EOF), true},
		{"connection closed", fmt.Errorf("reading: %w", mcpsdk.ErrConnectionClosed), true},
		{"invalid params", fmt.Errorf("%w: %v", &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid params"}, io.EOF), false},
		{"unrelated", errors.New("connection refused"), false},
		{"none", nil, false},
	}
	for _, c := range cases {
		if got := closedByPeer(c.err); got != c.want {
			t.Errorf("%s: closedByPeer = %v, want %v", c.name, got, c.want)
		}
	}
}
