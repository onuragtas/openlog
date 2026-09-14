package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	lib "github.com/onuragtas/openlog/libs/release"
)

// Artifact naming convention inside a release directory (docs/operations/releasing.md):
//
//	openlog-infra-agent_<v>_<os>_<arch>.tar.gz|.deb|.rpm      component infra-agent (linux, darwin tar.gz)
//	openlog-infra-agent_<v>_windows_<arch>.zip|.msi            component infra-agent (windows)
//	openlog-infra-agent_<v>_darwin_<arch>.pkg                  component infra-agent (macOS installer, optional)
//	openlog-php-agent_<v>_<os>_<arch>.tar.gz|.deb|.rpm|.apk   component php-agent (agents/php/packaging/build-artifacts.sh)
//	openlog_<v>_<os>_<arch>.tar.gz                            component backend (all backend binaries)
//	openlog-<v>.tgz                                           Helm chart (manifest.helm_chart and helm_charts.openlog)
//	openlog-agent-<v>.tgz                                     Helm chart (manifest.helm_charts.openlog-agent)
//	openlog-javaagent-<v>.jar                                 component java-agent, os/arch "any", format jar
//	openlog-node-<v>.tgz                                      component node-agent, os/arch "any", format tgz (npm pack)
//	openlog_agent-<pep440 v>-py3-none-any.whl                 component python-agent, os/arch "any", format whl
//	OpenLog.Agent.<v>.nupkg                                   component dotnet-agent, os/arch "any", format nupkg
//
// The Python sdist (openlog_agent-<pep440 v>.tar.gz) and the .sha256 files are published with the GitHub release but
// are not manifest artifacts.
var componentPrefixes = map[string]string{
	"openlog-infra-agent": lib.ComponentInfraAgent,
	"openlog-php-agent":   lib.ComponentPHPAgent,
	"openlog":             "backend",
}

var artifactFormats = []string{lib.FormatTarGz, lib.FormatDeb, lib.FormatRPM, lib.FormatAPK, lib.FormatZip, lib.FormatMSI, lib.FormatPkg}

// prefixOnlyFormats restricts formats to the components that publish them (Windows and macOS infra agent).
var prefixOnlyFormats = map[string]string{
	lib.FormatZip: "openlog-infra-agent",
	lib.FormatMSI: "openlog-infra-agent",
	lib.FormatPkg: "openlog-infra-agent",
}

// helmCharts are the charts `make release-helm` packages (deploy/helm/<chart>) as <chart>-<v>.tgz.
var helmCharts = []string{"openlog", "openlog-agent"}

// helmChartName reports the chart of a packaged chart file name of this version.
func helmChartName(name, version string) (string, bool) {
	for _, chart := range helmCharts {
		if name == chart+"-"+version+".tgz" {
			return chart, true
		}
	}
	return "", false
}

// classify maps a file name of a release directory to an artifact. ok is false for files that are
// not release artifacts (manifest, signatures, install.sh, …).
func classify(name, version string) (a lib.Artifact, ok bool) {
	anyPlatform := func(component, format string) (lib.Artifact, bool) {
		return lib.Artifact{Component: component, OS: lib.PlatformAny, Arch: lib.PlatformAny, Format: format, Name: name}, true
	}
	switch name {
	case "openlog-javaagent-" + version + "." + lib.FormatJar:
		return anyPlatform(lib.ComponentJavaAgent, lib.FormatJar)
	case lib.NodeAgentPackageName(version):
		return anyPlatform(lib.ComponentNodeAgent, lib.FormatTgz)
	case lib.PythonAgentWheelName(version):
		return anyPlatform(lib.ComponentPythonAgent, lib.FormatWheel)
	case lib.DotnetAgentPackageName(version):
		return anyPlatform(lib.ComponentDotnetAgent, lib.FormatNupkg)
	}
	for _, format := range artifactFormats {
		base, found := strings.CutSuffix(name, "."+format)
		if !found {
			continue
		}
		for prefix, component := range componentPrefixes {
			if only, restricted := prefixOnlyFormats[format]; restricted && only != prefix {
				continue
			}
			rest, found := strings.CutPrefix(base, prefix+"_"+version+"_")
			if !found {
				continue
			}
			osName, arch, found := strings.Cut(rest, "_")
			if !found || osName == "" || arch == "" || strings.Contains(arch, "_") {
				continue
			}
			return lib.Artifact{Component: component, OS: osName, Arch: arch, Format: format, Name: name}, true
		}
	}
	return lib.Artifact{}, false
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

type manifestOptions struct {
	Version    string
	Channel    string
	Dist       string
	BaseURL    string
	NotesURL   string
	ReleasedAt time.Time
	Compat     []string
	Images     []string
	Migrations []string
}

func cmdBuildManifest(args []string, stdout io.Writer) error {
	fs := newFlagSet("build-manifest")
	var o manifestOptions
	var compat, images, migrations multiFlag
	fs.StringVar(&o.Version, "version", "", "release version (SemVer, leading v allowed)")
	fs.StringVar(&o.Channel, "channel", "", "stable or beta (default: beta for pre-releases, stable otherwise)")
	fs.StringVar(&o.Dist, "dist", "", "release directory containing the artifacts")
	fs.StringVar(&o.BaseURL, "base-url", "", "URL of the directory the artifacts are downloaded from, e.g. https://github.com/onuragtas/openlog/releases/download/v0.4.0")
	fs.StringVar(&o.NotesURL, "notes-url", "", "release notes URL")
	out := fs.String("out", "", "output file (default DIST/manifest.json)")
	releasedAt := fs.String("released-at", "", "RFC 3339 release time (default $SOURCE_DATE_EPOCH or now)")
	fs.Var(&compat, "compat", "compatibility bound key=version (min_backend_for_agent, oldest_supported_agent, min_upgrade_from, rollback_floor); repeatable, empty value clears a default")
	fs.Var(&images, "image", "container image name=ref@sha256:digest; repeatable")
	fs.Var(&migrations, "migrations", "database=directory of numbered *.sql migrations (e.g. postgres=migrations/postgres); repeatable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return usagef("unexpected arguments %v", fs.Args())
	}
	if o.Version == "" || o.Dist == "" || o.BaseURL == "" {
		return usagef("--version, --dist and --base-url are required")
	}
	o.Compat, o.Images, o.Migrations = compat, images, migrations
	t, err := releaseTime(*releasedAt)
	if err != nil {
		return err
	}
	o.ReleasedAt = t
	m, err := buildManifest(o)
	if err != nil {
		return err
	}
	data, err := marshalJSON(m)
	if err != nil {
		return err
	}
	if _, err := lib.ParseManifest(data); err != nil {
		return fmt.Errorf("generated manifest is invalid: %w", err)
	}
	if *out == "" {
		*out = filepath.Join(o.Dist, "manifest.json")
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s: %s (%s), %d artifacts\n", *out, m.Version, m.Channel, len(m.Artifacts))
	return nil
}

func releaseTime(s string) (time.Time, error) {
	if s != "" {
		return time.Parse(time.RFC3339, s)
	}
	if e := os.Getenv("SOURCE_DATE_EPOCH"); e != "" {
		n, err := strconv.ParseInt(e, 10, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("SOURCE_DATE_EPOCH: %w", err)
		}
		return time.Unix(n, 0).UTC(), nil
	}
	return time.Now().UTC().Truncate(time.Second), nil
}

func buildManifest(o manifestOptions) (*lib.Manifest, error) {
	v, err := lib.ParseVersion(o.Version)
	if err != nil {
		return nil, err
	}
	version := v.String()
	channel := o.Channel
	if channel == "" {
		channel = lib.ChannelStable
		if v.IsPrerelease() {
			channel = lib.ChannelBeta
		}
	}
	base := strings.TrimRight(o.BaseURL, "/")
	m := &lib.Manifest{
		Schema:        lib.SchemaVersion,
		Product:       lib.Product,
		Version:       version,
		Channel:       channel,
		ReleasedAt:    o.ReleasedAt.UTC(),
		NotesURL:      o.NotesURL,
		Compatibility: defaultCompatibility(v),
		Artifacts:     []lib.Artifact{},
	}
	if err := applyCompat(&m.Compatibility, o.Compat); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(o.Dist)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		path := filepath.Join(o.Dist, name)
		if chart, ok := helmChartName(name, version); ok {
			sum, _, err := hashFile(path)
			if err != nil {
				return nil, err
			}
			f := lib.File{Name: name, URL: base + "/" + name, SHA256: sum}
			if m.HelmCharts == nil {
				m.HelmCharts = map[string]lib.File{}
			}
			m.HelmCharts[chart] = f
			if chart == "openlog" {
				m.HelmChart = &f
			}
			continue
		}
		a, ok := classify(name, version)
		if !ok {
			continue
		}
		sum, size, err := hashFile(path)
		if err != nil {
			return nil, err
		}
		a.URL, a.SHA256, a.Size = base+"/"+name, sum, size
		m.Artifacts = append(m.Artifacts, a)
	}
	if len(m.Artifacts) == 0 {
		return nil, fmt.Errorf("no artifacts for version %s found in %s", version, o.Dist)
	}
	sort.Slice(m.Artifacts, func(i, j int) bool {
		a, b := m.Artifacts[i], m.Artifacts[j]
		return a.Component+a.OS+a.Arch+a.Format < b.Component+b.OS+b.Arch+b.Format
	})

	for _, img := range o.Images {
		name, ref, ok := strings.Cut(img, "=")
		if !ok || name == "" || !strings.Contains(ref, "@sha256:") {
			return nil, usagef("--image %q: want name=repository@sha256:digest", img)
		}
		if m.Images == nil {
			m.Images = map[string]string{}
		}
		m.Images[name] = ref
	}
	for _, spec := range o.Migrations {
		db, dir, ok := strings.Cut(spec, "=")
		if !ok || db == "" || dir == "" {
			return nil, usagef("--migrations %q: want database=directory", spec)
		}
		info, err := scanMigrations(dir)
		if err != nil {
			return nil, err
		}
		if m.Migrations == nil {
			m.Migrations = map[string]lib.MigrationInfo{}
		}
		m.Migrations[db] = info
	}
	return m, nil
}

// defaultCompatibility follows docs/plan/09-releases-updates.md §1: the backend supports agents of
// the last 3 minors, and a rollback may go back at most one minor.
func defaultCompatibility(v lib.Version) lib.Compatibility {
	minus := func(n uint64) string {
		minor := uint64(0)
		if v.Minor > n {
			minor = v.Minor - n
		}
		return fmt.Sprintf("%d.%d.0", v.Major, minor)
	}
	return lib.Compatibility{OldestSupportedAgent: minus(2), RollbackFloor: minus(1)}
}

func applyCompat(c *lib.Compatibility, kvs []string) error {
	fields := map[string]*string{
		"min_backend_for_agent":  &c.MinBackendForAgent,
		"oldest_supported_agent": &c.OldestSupportedAgent,
		"min_upgrade_from":       &c.MinUpgradeFrom,
		"rollback_floor":         &c.RollbackFloor,
	}
	for _, kv := range kvs {
		for _, item := range strings.Split(kv, ",") {
			if strings.TrimSpace(item) == "" {
				continue
			}
			k, val, ok := strings.Cut(item, "=")
			p, known := fields[strings.TrimSpace(k)]
			if !ok || !known {
				return usagef("--compat %q: want one of min_backend_for_agent, oldest_supported_agent, min_upgrade_from, rollback_floor = version", item)
			}
			val = strings.TrimSpace(val)
			if val != "" {
				pv, err := lib.ParseVersion(val)
				if err != nil {
					return fmt.Errorf("--compat %s: %w", k, err)
				}
				val = pv.String()
			}
			*p = val
		}
	}
	return nil
}

var migrationFile = regexp.MustCompile(`^(\d+)_.*\.sql$`)

// scanMigrations reports the highest migration number of a directory and the contract migrations
// among them (first line "-- openlog:phase contract", docs/contracts/releases-updates.md §6).
func scanMigrations(dir string) (lib.MigrationInfo, error) {
	info := lib.MigrationInfo{ContractPending: []int{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return info, err
	}
	for _, e := range entries {
		sm := migrationFile.FindStringSubmatch(e.Name())
		if sm == nil || !e.Type().IsRegular() {
			continue
		}
		n, _ := strconv.Atoi(sm[1])
		if n > info.Latest {
			info.Latest = n
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			return info, err
		}
		sc := bufio.NewScanner(f)
		if sc.Scan() && strings.Join(strings.Fields(sc.Text()), " ") == "-- openlog:phase contract" {
			info.ContractPending = append(info.ContractPending, n)
		}
		f.Close()
	}
	sort.Ints(info.ContractPending)
	return info, nil
}

func marshalJSON(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
