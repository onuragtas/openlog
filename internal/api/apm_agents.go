package api

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/fleet/catalog"
	"github.com/onuragtas/openlog/internal/version"
	lib "github.com/onuragtas/openlog/libs/release"
)

// GET /api/v1/apm/agents (docs/contracts/api.md "APM", D-124): the language agents that report each APM service,
// the versions they run and how they compare with the newest release of the organization's release channel. Language
// agents are application dependencies (lockfiles, CI builds), so openlog never updates them; the response carries the
// upgrade command instead. A separate endpoint rather than a field of GET /apm/services keeps the services list query
// and its refresh interval unchanged and lets the registry checks run only where upgrade commands are shown.

// Agent kinds.
const (
	agentKindGo         = "go"
	agentKindNode       = "node"
	agentKindPython     = "python"
	agentKindJava       = "java"
	agentKindDotnet     = "dotnet"
	agentKindPHP        = "php"
	agentKindUnknown    = "unknown"     // openlog distro with an unrecognised telemetry.sdk.language
	agentKindThirdParty = "third_party" // any SDK that is not an openlog distro (plain OpenTelemetry)
)

// Agent version statuses, worst first.
const (
	agentStatusUnsupported = "unsupported" // older than compatibility.oldest_supported_agent
	agentStatusOutdated    = "outdated"    // older than the newest release of the channel
	agentStatusOK          = "ok"
	agentStatusUnknown     = "unknown"     // no release catalog, or a version that is not SemVer (development builds)
	agentStatusThirdParty  = "third_party" // not compared
)

// Release catalog state of the response.
const (
	agentCatalogOK          = "ok"
	agentCatalogUnavailable = "unavailable" // configured but no verified release of the channel (offline, not fetched yet)
	agentCatalogDisabled    = "disabled"    // no release catalog (static auth mode)
)

const (
	// maxAgentRows bounds the aggregated rows read (service × agent × version).
	maxAgentRows = 10000
	// maxAgentVersions bounds the versions listed per agent (newest first).
	maxAgentVersions = 20
	goModulePath     = "github.com/onuragtas/openlog/agents/go"
	docsBase         = "https://github.com/onuragtas/openlog/blob/master/"
)

// SetAgentReleases enables the version comparison of GET /api/v1/apm/agents with the verified release catalog shared
// with fleet management (nil: every status is unknown). Must be called before Run.
func (s *Server) SetAgentReleases(snapshot func() *catalog.Snapshot) { s.agentReleases = snapshot }

type agentReleaseJSON struct {
	Catalog         string  `json:"catalog"`
	Channel         string  `json:"channel"`
	Latest          *string `json:"latest"`
	OldestSupported *string `json:"oldest_supported"`
	NotesURL        string  `json:"notes_url"`
}

type agentVersionJSON struct {
	Version   string `json:"version"`
	Status    string `json:"status"`
	Instances uint64 `json:"instances"`
	Spans     uint64 `json:"spans"`
	LastSeen  string `json:"last_seen"`
}

type agentUpgradeJSON struct {
	// Package is the registry package (openlog-node, openlog-agent, OpenLog.Agent, the Go module path,
	// openlog-javaagent, openlog-php-agent).
	Package string `json:"package"`
	// Version is the package version of the latest release (PEP 440 for Python).
	Version string `json:"version"`
	// Command upgrades the dependency; Lang is the snippet language (sh).
	Command string `json:"command"`
	Lang    string `json:"lang"`
	// Registry: available, missing or unknown for npm, PyPI and nuget.org; empty for other kinds. Command installs from
	// the registry only when available, else from ReleaseAssetURL.
	Registry        string `json:"registry"`
	RegistryURL     string `json:"registry_url"`
	ReleaseAssetURL string `json:"release_asset_url"`
	DocsURL         string `json:"docs_url"`
	// DocsSection names the documentation section (e.g. "Distribution and updates").
	DocsSection string `json:"docs_section"`
	// Notes are message codes the UI translates: go_modules, java_fleet_auto, php_fleet_auto, registry_fallback.
	Notes []string `json:"notes"`
}

type serviceAgentJSON struct {
	Kind        string             `json:"kind"`
	DistroName  string             `json:"distro_name"`
	SDKName     string             `json:"sdk_name"`
	SDKLanguage string             `json:"sdk_language"`
	Status      string             `json:"status"`
	Instances   uint64             `json:"instances"`
	LastSeen    string             `json:"last_seen"`
	Versions    []agentVersionJSON `json:"versions"`
	// VersionsTruncated: more than maxAgentVersions versions were seen.
	VersionsTruncated bool `json:"versions_truncated"`
	// InstrumentationModules are openlog Go instrumentation modules recognised from span scopes (gin, echo, grpc).
	InstrumentationModules []string          `json:"instrumentation_modules"`
	Upgrade                *agentUpgradeJSON `json:"upgrade"`
	last                   time.Time
}

type serviceAgentsJSON struct {
	serviceIdentity
	Status string              `json:"status"`
	Agents []*serviceAgentJSON `json:"agents"`
}

// agentRow is one aggregated row of apm_agent_versions_1h.
type agentRow struct {
	id                                                      serviceIdentity
	distroName, distroVersion, sdkName, sdkLang, sdkVersion string
	spans, instances                                        uint64
	lastSeen                                                time.Time
	goModules                                               []string
}

func (s *Server) apmAgents(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	from, to, err := s.timeRange(r)
	if err != nil {
		return err
	}
	qp := r.URL.Query()
	if v := qp.Get("service"); len(v) > maxServiceNameBytes {
		return badRequest("service name must be at most %d bytes", maxServiceNameBytes)
	}
	withUpgrade := qp.Get("upgrade") != "false"
	idCols := []string{"service_name", "service_namespace", "deployment_environment"}
	keyCols := append(append([]string{}, idCols...), "distro_name", "distro_version", "sdk_name", "sdk_language", "sdk_version")
	q := sc.From(query.ApmAgentVersions1h).Columns(cols(keyCols, []string{"sum(span_count) AS a_spans", "uniqMerge(instances) AS a_inst",
		"max(last_seen) AS a_last", "groupUniqArrayArrayMerge(16)(go_modules) AS a_mods"})...).
		Where("timestamp >= toStartOfHour(toDateTime({t_from:Int64}, 'UTC')) AND timestamp < toDateTime({t_to:Int64}, 'UTC')").
		Param("t_from", from.Unix()).Param("t_to", to.Unix()).
		Where("last_seen >= fromUnixTimestamp64Nano({t_seen:Int64})").Param("t_seen", from.UnixNano()).
		GroupBy(keyCols...).OrderBy(idCols...).Limit(min(maxAgentRows, max(s.cfg.MaxRows, 1)))
	applyScope(q, optionalParam(qp, "namespace"), optionalParam(qp, "environment"))
	if v := qp.Get("service"); v != "" {
		q.Where("service_name = {svc_name:String}").Param("svc_name", v)
	}
	rows, err := sc.Query(r.Context(), q)
	if err != nil {
		return err
	}
	var list []agentRow
	for rows.Next() {
		var a agentRow
		if err := rows.Scan(&a.id.ServiceName, &a.id.ServiceNamespace, &a.id.Environment, &a.distroName, &a.distroVersion, &a.sdkName,
			&a.sdkLang, &a.sdkVersion, &a.spans, &a.instances, &a.lastSeen, &a.goModules); err != nil {
			rows.Close()
			return err
		}
		list = append(list, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rel, latest, floor := s.agentRelease(r.Context())
	services := groupAgentRows(list, latest, floor)
	if withUpgrade && latest != nil {
		var checker *PackageRegistryChecker
		if s.onboarding != nil {
			checker = s.onboarding.PackageRegistries
		}
		addAgentUpgrades(r.Context(), services, *rel.Latest, checker)
	}
	writeJSON(w, http.StatusOK, map[string]any{"release": rel, "services": services})
	return nil
}

// agentRelease resolves the newest release of the organization's fleet channel (stable without fleet management) and
// the oldest supported agent of this backend's release manifest (else of that newest release).
func (s *Server) agentRelease(ctx context.Context) (agentReleaseJSON, *lib.Version, *lib.Version) {
	out := agentReleaseJSON{Catalog: agentCatalogDisabled, Channel: lib.ChannelStable}
	if s.fleet != nil {
		if p, ok := auth.PrincipalFrom(ctx); ok && p.OrgID != "" {
			if sp, err := s.fleet.Policy(ctx, p.OrgID); err == nil && sp.Channel != "" {
				out.Channel = sp.Channel
			}
		}
	}
	if s.agentReleases == nil {
		return out, nil, nil
	}
	return resolveAgentRelease(s.agentReleases(), out.Channel, version.Version)
}

func resolveAgentRelease(snap *catalog.Snapshot, channel, backendVersion string) (agentReleaseJSON, *lib.Version, *lib.Version) {
	out := agentReleaseJSON{Catalog: agentCatalogUnavailable, Channel: channel}
	rel, ok := snap.Latest(channel)
	if !ok {
		return out, nil, nil
	}
	out.Catalog = agentCatalogOK
	lv := rel.Version
	ls := rel.VersionString()
	out.Latest, out.NotesURL = &ls, rel.Manifest.NotesURL
	var floor *lib.Version
	manifest := rel.Manifest
	if own, ok := snap.Release(backendVersion); ok {
		manifest = own.Manifest
	}
	if f := manifest.Compatibility.OldestSupportedAgent; f != "" {
		if fv, err := lib.ParseVersion(f); err == nil {
			fs := fv.String()
			floor, out.OldestSupported = &fv, &fs
		}
	}
	return out, &lv, floor
}

// agentKind maps the resource identity to an agent kind.
func agentKind(distroName, sdkLanguage string) string {
	switch strings.ToLower(distroName) {
	case "openlog-php":
		return agentKindPHP
	case "openlog":
	default:
		return agentKindThirdParty
	}
	switch strings.ToLower(sdkLanguage) {
	case "go":
		return agentKindGo
	case "nodejs", "node", "javascript":
		return agentKindNode
	case "python":
		return agentKindPython
	case "java":
		return agentKindJava
	case "dotnet":
		return agentKindDotnet
	case "php":
		return agentKindPHP
	}
	return agentKindUnknown
}

var pep440Pre = regexp.MustCompile(`^(\d+\.\d+\.\d+)(a|b|rc)(\d+)$`)

// parseAgentVersion parses a reported agent version: SemVer with an optional "v" and build metadata, or the PEP 440
// pre-release form of the Python agent (X.Y.ZaN, bN, rcN). Development builds (0.0.0…) are not comparable.
func parseAgentVersion(v string) (lib.Version, bool) {
	v, _, _ = strings.Cut(strings.TrimPrefix(strings.TrimSpace(v), "v"), "+")
	if m := pep440Pre.FindStringSubmatch(v); m != nil {
		v = m[1] + "-" + map[string]string{"a": "alpha", "b": "beta", "rc": "rc"}[m[2]] + "." + m[3]
	}
	if v == "" || strings.HasPrefix(v, "0.0.0") {
		return lib.Version{}, false
	}
	pv, err := lib.ParseVersion(v)
	return pv, err == nil
}

// agentVersionStatus compares one reported version with the latest release and the oldest supported agent.
func agentVersionStatus(v string, latest, floor *lib.Version) string {
	if latest == nil {
		return agentStatusUnknown
	}
	pv, ok := parseAgentVersion(v)
	if !ok {
		return agentStatusUnknown
	}
	if floor != nil && pv.Less(*floor) {
		return agentStatusUnsupported
	}
	if pv.Less(*latest) {
		return agentStatusOutdated
	}
	return agentStatusOK
}

var agentStatusRank = map[string]int{agentStatusUnsupported: 4, agentStatusOutdated: 3, agentStatusOK: 2, agentStatusUnknown: 1, agentStatusThirdParty: 0}

func worseStatus(a, b string) string {
	if agentStatusRank[b] > agentStatusRank[a] {
		return b
	}
	return a
}

// groupAgentRows builds the per-service agents from aggregated rows (rows of one service are adjacent or not; order
// does not matter). Services are ordered by identity, agents by kind, versions newest first.
func groupAgentRows(rows []agentRow, latest, floor *lib.Version) []*serviceAgentsJSON {
	services := []*serviceAgentsJSON{}
	bySvc := map[serviceIdentity]*serviceAgentsJSON{}
	type agentKey struct {
		svc                            serviceIdentity
		kind, distro, sdkName, sdkLang string
	}
	byAgent := map[agentKey]*serviceAgentJSON{}
	for _, row := range rows {
		sv := bySvc[row.id]
		if sv == nil {
			sv = &serviceAgentsJSON{serviceIdentity: row.id, Agents: []*serviceAgentJSON{}}
			bySvc[row.id] = sv
			services = append(services, sv)
		}
		kind := agentKind(row.distroName, row.sdkLang)
		key := agentKey{svc: row.id, kind: kind}
		version := row.distroVersion
		if kind == agentKindThirdParty {
			key.distro, key.sdkName, key.sdkLang = row.distroName, row.sdkName, row.sdkLang
			version = row.sdkVersion
		}
		ag := byAgent[key]
		if ag == nil {
			ag = &serviceAgentJSON{Kind: kind, DistroName: row.distroName, SDKName: row.sdkName, SDKLanguage: row.sdkLang,
				Versions: []agentVersionJSON{}, InstrumentationModules: []string{}}
			byAgent[key] = ag
			sv.Agents = append(sv.Agents, ag)
		}
		if row.lastSeen.After(ag.last) {
			ag.last = row.lastSeen
		}
		ag.InstrumentationModules = mergeSorted(ag.InstrumentationModules, row.goModules)
		// Rows of the same agent version with different SDK versions are merged.
		found := false
		for i := range ag.Versions {
			if ag.Versions[i].Version == version {
				v := &ag.Versions[i]
				v.Spans += row.spans
				v.Instances = max(v.Instances, row.instances)
				if t := formatTime(row.lastSeen); t > v.LastSeen {
					v.LastSeen = t
				}
				found = true
				break
			}
		}
		if !found {
			ag.Versions = append(ag.Versions, agentVersionJSON{Version: version, Instances: row.instances, Spans: row.spans, LastSeen: formatTime(row.lastSeen)})
		}
	}
	for _, sv := range services {
		sort.SliceStable(sv.Agents, func(i, j int) bool { return sv.Agents[i].Kind < sv.Agents[j].Kind })
		sv.Status = ""
		for _, ag := range sv.Agents {
			finishAgent(ag, latest, floor)
			if sv.Status == "" {
				sv.Status = ag.Status
			} else {
				sv.Status = worseStatus(sv.Status, ag.Status)
			}
		}
	}
	sort.SliceStable(services, func(i, j int) bool {
		a, b := services[i], services[j]
		if a.ServiceName != b.ServiceName {
			return a.ServiceName < b.ServiceName
		}
		if a.ServiceNamespace != b.ServiceNamespace {
			return a.ServiceNamespace < b.ServiceNamespace
		}
		return a.Environment < b.Environment
	})
	return services
}

func finishAgent(ag *serviceAgentJSON, latest, floor *lib.Version) {
	sort.SliceStable(ag.Versions, func(i, j int) bool {
		a, aok := parseAgentVersion(ag.Versions[i].Version)
		b, bok := parseAgentVersion(ag.Versions[j].Version)
		if aok && bok {
			if c := lib.Compare(a, b); c != 0 {
				return c > 0
			}
		} else if aok != bok {
			return aok
		}
		return ag.Versions[i].Version > ag.Versions[j].Version
	})
	if len(ag.Versions) > maxAgentVersions {
		ag.Versions, ag.VersionsTruncated = ag.Versions[:maxAgentVersions], true
	}
	ag.LastSeen = formatTime(ag.last)
	ag.Status = ""
	for i := range ag.Versions {
		v := &ag.Versions[i]
		ag.Instances += v.Instances
		if ag.Kind == agentKindThirdParty {
			v.Status = agentStatusThirdParty
		} else {
			v.Status = agentVersionStatus(v.Version, latest, floor)
		}
		if ag.Status == "" {
			ag.Status = v.Status
		} else {
			ag.Status = worseStatus(ag.Status, v.Status)
		}
	}
	if ag.Status == "" {
		ag.Status = agentStatusUnknown
	}
	if ag.Kind != agentKindGo {
		ag.InstrumentationModules = []string{}
	}
}

func mergeSorted(a, b []string) []string {
	for _, v := range b {
		if v != "" && !slices.Contains(a, v) {
			a = append(a, v)
		}
	}
	sort.Strings(a)
	return a
}

// addAgentUpgrades sets the upgrade instructions of every openlog agent to latest. Registry availability of the Node.js,
// Python and .NET packages comes from the onboarding checks (cached; offline: release asset commands).
func addAgentUpgrades(ctx context.Context, services []*serviceAgentsJSON, latest string, checker *PackageRegistryChecker) {
	var pk *onboardingPackagesJSON
	for _, sv := range services {
		for _, ag := range sv.Agents {
			switch ag.Kind {
			case agentKindNode, agentKindPython, agentKindDotnet:
				if pk == nil {
					pk = agentPackages(ctx, checker, &latest)
				}
			}
			ag.Upgrade = agentUpgrade(ag.Kind, latest, pk, ag.InstrumentationModules)
		}
	}
}

// agentUpgrade returns the upgrade instructions of kind to release v (nil for kinds without instructions). pk holds the
// Node.js, Python and .NET package information of v (agentPackages).
func agentUpgrade(kind, v string, pk *onboardingPackagesJSON, goModules []string) *agentUpgradeJSON {
	u := &agentUpgradeJSON{Version: v, Lang: "sh", Notes: []string{}}
	fromRegistry := func(p onboardingPackageJSON, registryCmd, assetCmd string) {
		u.Package, u.Version, u.Registry, u.RegistryURL, u.ReleaseAssetURL = p.Name, p.Version, p.Registry, p.RegistryURL, p.ReleaseAssetURL
		if p.Registry == registryAvailable {
			u.Command = registryCmd
		} else {
			u.Command = assetCmd
			u.Notes = append(u.Notes, "registry_fallback")
		}
	}
	switch kind {
	case agentKindNode:
		if pk == nil {
			return nil
		}
		fromRegistry(pk.Node, "npm install openlog-node@"+v, "npm install "+pk.Node.ReleaseAssetURL)
		u.DocsURL = docsBase + "agents/node/README.md"
	case agentKindPython:
		if pk == nil {
			return nil
		}
		fromRegistry(pk.Python, `pip install -U "openlog-agent==`+pk.Python.Version+`"`, `pip install -U "`+pk.Python.ReleaseAssetURL+`"`)
		u.DocsURL = docsBase + "agents/python/README.md"
	case agentKindDotnet:
		if pk == nil {
			return nil
		}
		fromRegistry(pk.Dotnet, "dotnet add package OpenLog.Agent --version "+v,
			"curl -fsSLO "+pk.Dotnet.ReleaseAssetURL+"\ndotnet add package OpenLog.Agent --version "+v+" --source .")
		u.DocsURL = docsBase + "agents/dotnet/README.md"
	case agentKindGo:
		u.Package, u.RegistryURL = goModulePath, "https://pkg.go.dev/"+goModulePath+"@v"+v
		mods := []string{goModulePath + "@v" + v}
		for _, m := range goModules {
			mods = append(mods, goModulePath+"/instrumentation/"+m+"@v"+v)
		}
		u.Command = "go get " + strings.Join(mods, " ") + "\ngo mod tidy"
		u.DocsURL = docsBase + "agents/go/README.md"
		u.Notes = append(u.Notes, "go_modules")
	case agentKindJava:
		file := "openlog-javaagent-" + v + ".jar"
		u.Package, u.ReleaseAssetURL = "openlog-javaagent", releaseDownloadBase+"/v"+v+"/"+file
		u.Command = "curl -fsSLo openlog-javaagent.jar " + u.ReleaseAssetURL
		u.DocsURL, u.DocsSection = docsBase+"docs/contracts/java-agent.md#distribution-and-updates", "Distribution and updates"
		u.Notes = append(u.Notes, "java_fleet_auto")
	case agentKindPHP:
		file := "openlog-php-agent_" + v + "_linux_amd64.deb"
		u.Package, u.ReleaseAssetURL = "openlog-php-agent", releaseDownloadBase+"/v"+v+"/"+file
		u.Command = "curl -fsSLO " + u.ReleaseAssetURL + "\nsudo apt-get install ./" + file
		u.DocsURL = docsBase + "docs/contracts/php-agent.md"
		u.Notes = append(u.Notes, "php_fleet_auto")
	default:
		return nil
	}
	return u
}
