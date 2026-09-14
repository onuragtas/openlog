package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestAgentPackages(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.EscapedPath()]++
		mu.Unlock()
		switch r.URL.EscapedPath() {
		case "/@openlog%2fnode/1.2.0-beta.3":
			_, _ = w.Write([]byte(`{"name":"@openlog/node","version":"1.2.0-beta.3"}`))
		case "/pypi/openlog-agent/1.2.0b3/json":
			http.NotFound(w, r)
		default: // nuget: server error → unknown
			http.Error(w, "boom", http.StatusBadGateway)
		}
	}))
	defer registry.Close()

	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	c := NewPackageRegistryChecker(registry.Client())
	c.npm, c.pypi, c.nuget = registry.URL, registry.URL, registry.URL
	c.now = func() time.Time { return now }

	v := "1.2.0-beta.3"
	p := agentPackages(context.Background(), c, &v)
	if p == nil {
		t.Fatal("no packages")
	}
	if p.Node.Registry != registryAvailable || p.Python.Registry != registryMissing || p.Dotnet.Registry != registryUnknown {
		t.Errorf("registry = %s %s %s", p.Node.Registry, p.Python.Registry, p.Dotnet.Registry)
	}
	const base = "https://github.com/onuragtas/openlog/releases/download/v1.2.0-beta.3/"
	if p.Node.ReleaseAssetURL != base+"openlog-node-1.2.0-beta.3.tgz" || p.Node.ReleaseAssetSHA256URL != base+"openlog-node-1.2.0-beta.3.tgz.sha256" {
		t.Errorf("node assets = %+v", p.Node)
	}
	if p.Python.Version != "1.2.0b3" || p.Python.ReleaseAssetURL != base+"openlog_agent-1.2.0b3-py3-none-any.whl" {
		t.Errorf("python = %+v", p.Python)
	}
	if p.Dotnet.Name != "OpenLog.Agent" || p.Dotnet.ReleaseAssetURL != base+"OpenLog.Agent.1.2.0-beta.3.nupkg" {
		t.Errorf("dotnet = %+v", p.Dotnet)
	}
	if hits["/v3-flatcontainer/openlog.agent/1.2.0-beta.3/openlog.agent.nuspec"] != 1 {
		t.Errorf("nuget flat container not asked: %v", hits)
	}

	// Cached: answers for an hour, unknown answers for ten minutes.
	agentPackages(context.Background(), c, &v)
	now = now.Add(11 * time.Minute)
	agentPackages(context.Background(), c, &v)
	if hits["/@openlog%2fnode/1.2.0-beta.3"] != 1 || hits["/pypi/openlog-agent/1.2.0b3/json"] != 1 {
		t.Errorf("cache not used: %v", hits)
	}
	if hits["/v3-flatcontainer/openlog.agent/1.2.0-beta.3/openlog.agent.nuspec"] != 2 {
		t.Errorf("unknown answer not re-checked after ten minutes: %v", hits)
	}
	now = now.Add(time.Hour)
	agentPackages(context.Background(), c, &v)
	if hits["/@openlog%2fnode/1.2.0-beta.3"] != 2 {
		t.Errorf("expired cache entry not re-checked: %v", hits)
	}

	// Offline registries: unknown, within the check timeout.
	registry.Close()
	off := NewPackageRegistryChecker(&http.Client{Timeout: time.Second})
	off.npm, off.pypi, off.nuget = registry.URL, registry.URL, registry.URL
	p = agentPackages(context.Background(), off, &v)
	if p.Node.Registry != registryUnknown || p.Python.Registry != registryUnknown || p.Dotnet.Registry != registryUnknown {
		t.Errorf("offline = %s %s %s", p.Node.Registry, p.Python.Registry, p.Dotnet.Registry)
	}

	// No checker: unknown without network access; no version: no packages.
	if p = agentPackages(context.Background(), nil, &v); p.Node.Registry != registryUnknown || p.Node.ReleaseAssetURL == "" {
		t.Errorf("nil checker = %+v", p.Node)
	}
	if p = agentPackages(context.Background(), c, nil); p != nil {
		t.Errorf("dev build packages = %+v", p)
	}
}
