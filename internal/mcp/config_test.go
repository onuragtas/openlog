package mcp

import (
	"strings"
	"testing"
	"time"
)

// env builds a getenv function from a map.
func env(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func TestLoadConfigDefaults(t *testing.T) {
	c, err := LoadConfig(env(map[string]string{"OPENLOG_MCP_API_KEY": "ola_key"}))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.Transport != TransportStdio || c.APIURL != DefaultAPIURL || c.HTTPAddr != ":8092" {
		t.Errorf("defaults: %+v", c)
	}
	if c.Timeout != 60*time.Second || c.MaxRows != defaultMaxRows {
		t.Errorf("timeout %v, max rows %d", c.Timeout, c.MaxRows)
	}
}

func TestLoadConfigValues(t *testing.T) {
	c, err := LoadConfig(env(map[string]string{
		"OPENLOG_MCP_TRANSPORT": "HTTP", // case-insensitive
		"OPENLOG_MCP_API_URL":   "https://openlog.example.com/",
		"OPENLOG_MCP_ORG_ID":    "org-1",
		"OPENLOG_MCP_TIMEOUT":   "5s",
		"OPENLOG_MCP_MAX_ROWS":  "7",
		"OPENLOG_ADMIN_ADDR":    ":9999",
	}))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	// The trailing slash is trimmed so paths are joined without doubling it.
	if c.Transport != TransportHTTP || c.APIURL != "https://openlog.example.com" {
		t.Errorf("transport %q, url %q", c.Transport, c.APIURL)
	}
	if c.OrgID != "org-1" || c.Timeout != 5*time.Second || c.MaxRows != 7 || c.AdminAddr != ":9999" {
		t.Errorf("config %+v", c)
	}
}

// The http transport takes the key from each request, so it must load without one.
func TestLoadConfigHTTPWithoutKey(t *testing.T) {
	if _, err := LoadConfig(env(map[string]string{"OPENLOG_MCP_TRANSPORT": "http"})); err != nil {
		t.Errorf("http without a key: %v", err)
	}
}

func TestLoadConfigErrors(t *testing.T) {
	cases := []struct {
		name string
		vars map[string]string
		want string
	}{
		{"stdio without a key", map[string]string{}, "OPENLOG_MCP_API_KEY"},
		{"unknown transport", map[string]string{"OPENLOG_MCP_API_KEY": "k", "OPENLOG_MCP_TRANSPORT": "sse"}, "OPENLOG_MCP_TRANSPORT"},
		{"url without a scheme", map[string]string{"OPENLOG_MCP_API_KEY": "k", "OPENLOG_MCP_API_URL": "openlog.example.com"}, "OPENLOG_MCP_API_URL"},
		{"url with credentials", map[string]string{"OPENLOG_MCP_API_KEY": "k", "OPENLOG_MCP_API_URL": "https://u:p@example.com"}, "credentials"},
		{"url with a query", map[string]string{"OPENLOG_MCP_API_KEY": "k", "OPENLOG_MCP_API_URL": "https://example.com?a=1"}, "credentials, a query or a fragment"},
		{"invalid timeout", map[string]string{"OPENLOG_MCP_API_KEY": "k", "OPENLOG_MCP_TIMEOUT": "soon"}, "OPENLOG_MCP_TIMEOUT"},
		{"negative timeout", map[string]string{"OPENLOG_MCP_API_KEY": "k", "OPENLOG_MCP_TIMEOUT": "-1s"}, "OPENLOG_MCP_TIMEOUT"},
		{"max rows out of range", map[string]string{"OPENLOG_MCP_API_KEY": "k", "OPENLOG_MCP_MAX_ROWS": "5000"}, "OPENLOG_MCP_MAX_ROWS"},
		{"max rows not a number", map[string]string{"OPENLOG_MCP_API_KEY": "k", "OPENLOG_MCP_MAX_ROWS": "many"}, "OPENLOG_MCP_MAX_ROWS"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadConfig(env(c.vars))
			if err == nil {
				t.Fatal("no error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not mention %q", err, c.want)
			}
		})
	}
}
