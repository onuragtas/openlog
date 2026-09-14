package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	lib "github.com/onuragtas/openlog/libs/release"
)

// Language agent packages of GET /api/v1/onboarding (docs/contracts/api.md "Onboarding"): whether npm, PyPI and
// nuget.org serve the release the install commands pin, and the GitHub release assets that install without them
// (docs/operations/releasing.md "Language agent packages"). The Add data page installs from the registry only when
// it reports "available".

// Registry availability of a package version.
const (
	registryAvailable = "available" // the registry answered 200
	registryMissing   = "missing"   // the registry answered 404
	registryUnknown   = "unknown"   // not checked, offline, timeout or any other answer
)

const (
	releaseDownloadBase = "https://github.com/onuragtas/openlog/releases/download"
	// registryCheckTimeout bounds the three checks of one onboarding request together.
	registryCheckTimeout = 3 * time.Second
	registryCacheTTL     = time.Hour
	// registryUnknownTTL is shorter so a registry that was unreachable is asked again soon, without a check on every
	// request of an offline installation.
	registryUnknownTTL = 10 * time.Minute
)

type onboardingPackageJSON struct {
	// Name is the registry package name (@openlog/node, openlog-agent, OpenLog.Agent).
	Name string `json:"name"`
	// Version is the package version: agent_version, as PEP 440 for Python.
	Version string `json:"version"`
	// Registry: available, missing or unknown.
	Registry    string `json:"registry"`
	RegistryURL string `json:"registry_url"`
	// ReleaseAssetURL is the package file attached to the GitHub release; ReleaseAssetSHA256URL its sha256sum file.
	ReleaseAssetURL       string `json:"release_asset_url"`
	ReleaseAssetSHA256URL string `json:"release_asset_sha256_url"`
}

type onboardingPackagesJSON struct {
	Node   onboardingPackageJSON `json:"node"`
	Python onboardingPackageJSON `json:"python"`
	Dotnet onboardingPackageJSON `json:"dotnet"`
}

// PackageRegistryChecker asks npm, PyPI and nuget.org whether a language agent version is published, caching every
// answer (an hour; unknown answers ten minutes). Safe for concurrent use.
type PackageRegistryChecker struct {
	client *http.Client
	// registry base URLs (tests point them at a local server)
	npm, pypi, nuget string
	now              func() time.Time

	mu    sync.Mutex
	cache map[string]registryCacheEntry
}

type registryCacheEntry struct {
	status  string
	expires time.Time
}

// NewPackageRegistryChecker checks the public registries with client (nil: a client with the check timeout).
func NewPackageRegistryChecker(client *http.Client) *PackageRegistryChecker {
	if client == nil {
		client = &http.Client{Timeout: registryCheckTimeout}
	}
	return &PackageRegistryChecker{
		client: client,
		npm:    "https://registry.npmjs.org",
		pypi:   "https://pypi.org",
		nuget:  "https://api.nuget.org",
		now:    time.Now,
		cache:  map[string]registryCacheEntry{},
	}
}

// status reports whether url answers 200 (available) or 404 (missing), from the cache when possible.
func (c *PackageRegistryChecker) status(ctx context.Context, url string) string {
	c.mu.Lock()
	if e, ok := c.cache[url]; ok && c.now().Before(e.expires) {
		c.mu.Unlock()
		return e.status
	}
	c.mu.Unlock()

	status := registryUnknown
	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil); err == nil {
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "openlog-api (onboarding package check)")
		if resp, err := c.client.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusOK:
				status = registryAvailable
			case http.StatusNotFound:
				status = registryMissing
			}
		}
	}
	ttl := registryCacheTTL
	if status == registryUnknown {
		ttl = registryUnknownTTL
	}
	c.mu.Lock()
	c.cache[url] = registryCacheEntry{status: status, expires: c.now().Add(ttl)}
	c.mu.Unlock()
	return status
}

// agentPackages describes the language agent packages of release version; nil without a version (dev builds).
// checker nil reports every registry as unknown without network access.
func agentPackages(ctx context.Context, checker *PackageRegistryChecker, version *string) *onboardingPackagesJSON {
	if version == nil || *version == "" {
		return nil
	}
	v := *version
	pyv := lib.PythonVersion(v)
	nugetV := strings.ToLower(v)
	asset := func(file string) (string, string) {
		u := releaseDownloadBase + "/v" + v + "/" + file
		return u, u + ".sha256"
	}
	p := &onboardingPackagesJSON{
		Node:   onboardingPackageJSON{Name: "@openlog/node", Version: v, RegistryURL: "https://www.npmjs.com/package/@openlog/node/v/" + v},
		Python: onboardingPackageJSON{Name: "openlog-agent", Version: pyv, RegistryURL: "https://pypi.org/project/openlog-agent/" + pyv + "/"},
		Dotnet: onboardingPackageJSON{Name: "OpenLog.Agent", Version: v, RegistryURL: "https://www.nuget.org/packages/OpenLog.Agent/" + v},
	}
	p.Node.ReleaseAssetURL, p.Node.ReleaseAssetSHA256URL = asset(lib.NodeAgentPackageName(v))
	p.Python.ReleaseAssetURL, p.Python.ReleaseAssetSHA256URL = asset(lib.PythonAgentWheelName(v))
	p.Dotnet.ReleaseAssetURL, p.Dotnet.ReleaseAssetSHA256URL = asset(lib.DotnetAgentPackageName(v))
	p.Node.Registry, p.Python.Registry, p.Dotnet.Registry = registryUnknown, registryUnknown, registryUnknown
	if checker == nil {
		return p
	}

	ctx, cancel := context.WithTimeout(ctx, registryCheckTimeout)
	defer cancel()
	checks := []struct {
		dst *string
		url string
	}{
		{&p.Node.Registry, checker.npm + "/@openlog%2fnode/" + v},
		{&p.Python.Registry, checker.pypi + "/pypi/openlog-agent/" + pyv + "/json"},
		{&p.Dotnet.Registry, checker.nuget + "/v3-flatcontainer/openlog.agent/" + nugetV + "/openlog.agent.nuspec"},
	}
	var wg sync.WaitGroup
	for _, ch := range checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			*ch.dst = checker.status(ctx, ch.url)
		}()
	}
	wg.Wait()
	return p
}
