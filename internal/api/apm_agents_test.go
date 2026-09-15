package api

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/fleet/testutil"
	lib "github.com/onuragtas/openlog/libs/release"
)

func TestAgentKind(t *testing.T) {
	for _, tc := range []struct{ distro, lang, want string }{
		{"openlog", "nodejs", agentKindNode},
		{"openlog", "python", agentKindPython},
		{"openlog", "java", agentKindJava},
		{"openlog", "dotnet", agentKindDotnet},
		{"openlog", "go", agentKindGo},
		{"openlog-php", "", agentKindPHP},
		{"openlog", "rust", agentKindUnknown},
		{"", "nodejs", agentKindThirdParty},
		{"opentelemetry-java-instrumentation", "java", agentKindThirdParty},
	} {
		if got := agentKind(tc.distro, tc.lang); got != tc.want {
			t.Errorf("agentKind(%q, %q) = %q, want %q", tc.distro, tc.lang, got, tc.want)
		}
	}
}

func TestAgentVersionStatus(t *testing.T) {
	latest, floor := lib.MustParseVersion("0.1.31"), lib.MustParseVersion("0.1.20")
	for _, tc := range []struct {
		v             string
		latest, floor *lib.Version
		want          string
	}{
		{"0.1.31", &latest, &floor, agentStatusOK},
		{"v0.1.32", &latest, &floor, agentStatusOK},
		{"0.2.0-beta.1", &latest, &floor, agentStatusOK},
		{"0.1.30", &latest, &floor, agentStatusOutdated},
		{"0.1.31-rc.1", &latest, &floor, agentStatusOutdated},
		{"0.1.31rc1", &latest, &floor, agentStatusOutdated}, // PEP 440 (Python agent)
		{"0.1.30+abc", &latest, &floor, agentStatusOutdated},
		{"0.1.9", &latest, &floor, agentStatusUnsupported},
		{"0.1.9", &latest, nil, agentStatusOutdated},
		{"0.1.9", nil, nil, agentStatusUnknown},
		{"0.0.0-dev", &latest, &floor, agentStatusUnknown},
		{"garbage", &latest, &floor, agentStatusUnknown},
	} {
		if got := agentVersionStatus(tc.v, tc.latest, tc.floor); got != tc.want {
			t.Errorf("agentVersionStatus(%q) = %q, want %q", tc.v, got, tc.want)
		}
	}
}

func TestGroupAgentRows(t *testing.T) {
	latest, floor := lib.MustParseVersion("0.1.31"), lib.MustParseVersion("0.1.20")
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	web := serviceIdentity{ServiceName: "web", Environment: "prod"}
	api := serviceIdentity{ServiceName: "api", Environment: "prod"}
	rows := []agentRow{
		{id: web, distroName: "openlog", distroVersion: "0.1.9", sdkLang: "nodejs", sdkName: "opentelemetry", sdkVersion: "1.30.0", spans: 10, instances: 2, lastSeen: now.Add(-time.Hour)},
		{id: web, distroName: "openlog", distroVersion: "0.1.31", sdkLang: "nodejs", sdkName: "opentelemetry", sdkVersion: "1.30.0", spans: 5, instances: 1, lastSeen: now},
		{id: web, distroName: "openlog", distroVersion: "0.1.31", sdkLang: "nodejs", sdkName: "opentelemetry", sdkVersion: "1.31.0", spans: 1, instances: 1, lastSeen: now},
		{id: api, distroName: "openlog", distroVersion: "0.1.31", sdkLang: "go", spans: 3, instances: 1, lastSeen: now, goModules: []string{"gin"}},
		{id: api, distroName: "openlog", distroVersion: "0.1.30", sdkLang: "go", spans: 3, instances: 1, lastSeen: now, goModules: []string{"grpc", "gin"}},
		{id: api, sdkName: "opentelemetry", sdkLang: "python", sdkVersion: "1.27.0", spans: 1, instances: 1, lastSeen: now},
	}
	got := groupAgentRows(rows, &latest, &floor)
	if len(got) != 2 || got[0].ServiceName != "api" || got[1].ServiceName != "web" {
		t.Fatalf("services = %+v", got)
	}
	a := got[0]
	if a.Status != agentStatusOutdated || len(a.Agents) != 2 || a.Agents[0].Kind != agentKindGo || a.Agents[1].Kind != agentKindThirdParty {
		t.Fatalf("api = %+v", a)
	}
	if g := a.Agents[0]; strings.Join(g.InstrumentationModules, ",") != "gin,grpc" || g.Versions[0].Version != "0.1.31" || g.Versions[1].Status != agentStatusOutdated {
		t.Errorf("go agent = %+v", g)
	}
	if tp := a.Agents[1]; tp.Status != agentStatusThirdParty || tp.Versions[0].Version != "1.27.0" {
		t.Errorf("third party = %+v", tp)
	}
	w := got[1]
	if w.Status != agentStatusUnsupported || len(w.Agents) != 1 {
		t.Fatalf("web = %+v", w)
	}
	n := w.Agents[0]
	if len(n.Versions) != 2 || n.Versions[0].Version != "0.1.31" || n.Versions[0].Spans != 6 || n.Versions[0].Status != agentStatusOK ||
		n.Versions[1].Status != agentStatusUnsupported || n.Instances != 3 || len(n.InstrumentationModules) != 0 {
		t.Errorf("node agent = %+v", n)
	}

	// Without a release catalog every openlog agent is unknown.
	if got := groupAgentRows(rows, nil, nil); got[1].Status != agentStatusUnknown || got[0].Status != agentStatusUnknown {
		t.Errorf("no catalog: %q %q", got[0].Status, got[1].Status)
	}
	// Only third-party SDKs.
	if got := groupAgentRows(rows[5:], &latest, &floor); got[0].Status != agentStatusThirdParty {
		t.Errorf("third party only: %q", got[0].Status)
	}
}

func TestAgentVersionsTruncated(t *testing.T) {
	var rows []agentRow
	for i := range maxAgentVersions + 5 {
		rows = append(rows, agentRow{id: serviceIdentity{ServiceName: "s"}, distroName: "openlog", sdkLang: "java", distroVersion: "0.1." + strconv.Itoa(i), instances: 1})
	}
	ag := groupAgentRows(rows, nil, nil)[0].Agents[0]
	if len(ag.Versions) != maxAgentVersions || !ag.VersionsTruncated || ag.Versions[0].Version != "0.1.24" {
		t.Errorf("versions = %d truncated=%v first=%s", len(ag.Versions), ag.VersionsTruncated, ag.Versions[0].Version)
	}
}

func TestResolveAgentRelease(t *testing.T) {
	signer, err := testutil.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	snap, err := testutil.Snapshot(signer,
		testutil.ReleaseSpec{Version: "0.1.30", OldestSupportedAgent: "0.1.10"},
		testutil.ReleaseSpec{Version: "0.1.31", OldestSupportedAgent: "0.1.20"},
		testutil.ReleaseSpec{Version: "0.2.0-beta.1", OldestSupportedAgent: "0.1.25"},
	)
	if err != nil {
		t.Fatal(err)
	}
	rel, latest, floor := resolveAgentRelease(snap, lib.ChannelStable, "0.0.0-dev")
	if rel.Catalog != agentCatalogOK || *rel.Latest != "0.1.31" || latest.String() != "0.1.31" || *rel.OldestSupported != "0.1.20" || floor.String() != "0.1.20" {
		t.Errorf("stable: %+v", rel)
	}
	// The manifest of this backend's own release sets the floor.
	if rel, _, _ := resolveAgentRelease(snap, lib.ChannelStable, "0.1.30"); *rel.OldestSupported != "0.1.10" {
		t.Errorf("own manifest floor = %v", *rel.OldestSupported)
	}
	if rel, _, _ := resolveAgentRelease(snap, lib.ChannelBeta, "0.0.0-dev"); *rel.Latest != "0.2.0-beta.1" {
		t.Errorf("beta latest = %v", *rel.Latest)
	}
	if rel, latest, _ := resolveAgentRelease(nil, lib.ChannelStable, "0.0.0-dev"); rel.Catalog != agentCatalogUnavailable || latest != nil || rel.Latest != nil {
		t.Errorf("empty catalog: %+v", rel)
	}
}

func TestAgentUpgrade(t *testing.T) {
	rc := "0.2.0-rc.1"
	pk := agentPackages(context.Background(), nil, &rc) // registries unknown
	if u := agentUpgrade(agentKindNode, "0.2.0-rc.1", pk, nil); u.Command != "npm install https://github.com/onuragtas/openlog/releases/download/v0.2.0-rc.1/openlog-node-0.2.0-rc.1.tgz" ||
		u.Registry != registryUnknown || u.Notes[0] != "registry_fallback" {
		t.Errorf("node fallback = %+v", u)
	}
	pk.Node.Registry, pk.Python.Registry, pk.Dotnet.Registry = registryAvailable, registryAvailable, registryAvailable
	for kind, want := range map[string]string{
		agentKindNode:   "npm install openlog-node@0.2.0-rc.1",
		agentKindPython: `pip install -U "openlog-agent==0.2.0rc1"`,
		agentKindDotnet: "dotnet add package OpenLog.Agent --version 0.2.0-rc.1",
		agentKindGo:     "go get github.com/onuragtas/openlog/agents/go@v0.2.0-rc.1 github.com/onuragtas/openlog/agents/go/instrumentation/gin@v0.2.0-rc.1\ngo mod tidy",
		agentKindJava:   "curl -fsSLo openlog-javaagent.jar https://github.com/onuragtas/openlog/releases/download/v0.2.0-rc.1/openlog-javaagent-0.2.0-rc.1.jar",
	} {
		u := agentUpgrade(kind, "0.2.0-rc.1", pk, []string{"gin"})
		if u == nil || u.Command != want {
			t.Errorf("%s: %+v", kind, u)
		}
	}
	if u := agentUpgrade(agentKindPython, "0.2.0-rc.1", pk, nil); u.Version != "0.2.0rc1" || u.Package != "openlog-agent" {
		t.Errorf("python = %+v", u)
	}
	if u := agentUpgrade(agentKindJava, "1.0.0", nil, nil); u.DocsSection != "Distribution and updates" || u.Notes[0] != "java_fleet_auto" {
		t.Errorf("java = %+v", u)
	}
	if u := agentUpgrade(agentKindPHP, "1.0.0", nil, nil); !strings.Contains(u.Command, "openlog-php-agent_1.0.0_linux_amd64.deb") || u.Notes[0] != "php_fleet_auto" {
		t.Errorf("php = %+v", u)
	}
	if agentUpgrade(agentKindThirdParty, "1.0.0", pk, nil) != nil || agentUpgrade(agentKindNode, "1.0.0", nil, nil) != nil {
		t.Error("unexpected upgrade")
	}
}
