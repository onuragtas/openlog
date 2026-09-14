package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Every variable Load reads must be documented in docs/contracts/config.md (the source of truth), passed through by
// the Compose stack (docker-compose.yml and .env.example) and settable in the Helm chart (values.yaml or templates),
// unless it is listed below with the reason it is intentionally not exposed there.

// notInCompose: variables fixed by the Compose topology (ports, healthchecks, bundled plaintext dependencies).
var notInCompose = map[string]string{
	"OPENLOG_ADMIN_ADDR":          "port mapping and healthchecks use :9464",
	"OPENLOG_INGEST_HTTP_ADDR":    "port mapping uses :4318",
	"OPENLOG_INGEST_GRPC_ADDR":    "port mapping uses :4317",
	"OPENLOG_API_HTTP_ADDR":       "port mapping uses :8080",
	"OPENLOG_CLICKHOUSE_DATABASE": "fixed to openlog by contract (D-015)",
	"OPENLOG_MIGRATE_SKIP_KAFKA":  "the bundled broker has no topic operator; openlog creates the topics",
}

// notInComposePrefixes: the bundled Kafka, ClickHouse and PostgreSQL containers are reached over the Compose network
// without TLS or SASL; use the Helm chart (or your own Compose file with mounted certificates) for secured dependencies.
var notInComposePrefixes = []string{"OPENLOG_KAFKA_TLS_", "OPENLOG_KAFKA_SASL_", "OPENLOG_CLICKHOUSE_TLS_", "OPENLOG_POSTGRES_TLS_"}

// composeOwnService: variables read by internal/config that the Compose stack passes to another service than openlog.
var composeOwnService = map[string]string{
	"OPENLOG_BOOTSTRAP_": "bootstrap",        // openlog-admin bootstrap (first organization, owner, keys)
	"OPENLOG_RENDERER_":  "openlog-renderer", // renderer process settings; openlog gets URL/token/TLS through the anchor
	"OPENLOG_S3_":        "clickhouse",       // tiered storage credentials stay in ClickHouse; exports use OPENLOG_DATA_EXPORT_S3_*
}

// notInHelm: variables of the single-process profile only.
var notInHelm = map[string]string{
	"OPENLOG_ALERT_ENABLED":    "openlog-allinone only; the chart runs openlog-alert as its own Deployment (alert.enabled)",
	"OPENLOG_MIGRATE_ON_START": "openlog-allinone only; the chart runs openlog-migrate as a hook Job",
}

// composeServiceEnvKeys returns the environment keys of a Compose service (map syntax; anchors and merge keys resolved).
func composeServiceEnvKeys(t *testing.T, compose, service string) map[string]bool {
	t.Helper()
	var doc struct {
		Services map[string]struct {
			Environment map[string]any `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(compose), &doc); err != nil {
		t.Fatalf("docker-compose.yml: %v", err)
	}
	svc, ok := doc.Services[service]
	if !ok || len(svc.Environment) == 0 {
		t.Fatalf("docker-compose.yml: service %s has no environment map", service)
	}
	keys := map[string]bool{}
	for k := range svc.Environment {
		keys[k] = true
	}
	return keys
}

func loadedEnvNames(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	if _, err := Load(func(name string) string {
		seen[name] = true
		return ""
	}); err != nil {
		t.Fatalf("Load with an empty environment: %v", err)
	}
	var out []string
	for n := range seen {
		if strings.HasPrefix(n, "OPENLOG_") {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func readRepoTree(t *testing.T, rel string) string {
	t.Helper()
	var sb strings.Builder
	root := filepath.Join("..", "..", filepath.FromSlash(rel))
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch filepath.Ext(path) {
		case ".yaml", ".yml", ".tpl", ".txt":
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sb.Write(b)
			sb.WriteByte('\n')
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sb.String()
}

// mentions reports whether text names the variable as a whole word, or through a family prefix: a documented
// placeholder ("OPENLOG_STORAGE_WARM_AFTER_DAYS_<CLASS>") or a templated name ("OPENLOG_STORAGE_COLD_AFTER_DAYS_{{ upper $class }}").
func mentions(text, name string, prefixes []string) bool {
	if regexp.MustCompile(`(^|[^A-Z0-9_])` + name + `($|[^A-Z0-9_])`).MatchString(text) {
		return true
	}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

var (
	placeholderPrefix = regexp.MustCompile(`(OPENLOG_[A-Z0-9_]*_)<[A-Z_]+>`)
	templatedPrefix   = regexp.MustCompile(`(OPENLOG_[A-Z0-9_]*_)\{\{`)
)

func prefixes(re *regexp.Regexp, text string) []string {
	var out []string
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestEnvVarCoverage(t *testing.T) {
	names := loadedEnvNames(t)
	if len(names) < 150 {
		t.Fatalf("only %d variables recorded; Load changed?", len(names))
	}
	doc := readRepoFile(t, "docs/contracts/config.md")
	compose := readRepoFile(t, "deploy/compose/docker-compose.yml")
	envExample := readRepoFile(t, "deploy/compose/.env.example")
	helm := readRepoTree(t, "deploy/helm/openlog")
	docPrefixes, helmPrefixes := prefixes(placeholderPrefix, doc), prefixes(templatedPrefix, helm)

	var missing = map[string][]string{}
	for _, n := range names {
		if !mentions(doc, n, docPrefixes) {
			missing["docs/contracts/config.md"] = append(missing["docs/contracts/config.md"], n)
		}
		_, skipCompose := notInCompose[n]
		for _, p := range notInComposePrefixes {
			skipCompose = skipCompose || strings.HasPrefix(n, p)
		}
		if !skipCompose && !mentions(compose, n, nil) {
			missing["deploy/compose/docker-compose.yml"] = append(missing["deploy/compose/docker-compose.yml"], n)
		}
		// .env.example lists what a user can set: every variable the Compose file interpolates (${NAME...}).
		if !skipCompose && strings.Contains(compose, "${"+n+":") && !mentions(envExample, n, nil) {
			missing["deploy/compose/.env.example"] = append(missing["deploy/compose/.env.example"], n)
		}
		if _, skip := notInHelm[n]; !skip && !mentions(helm, n, helmPrefixes) {
			missing["deploy/helm/openlog"] = append(missing["deploy/helm/openlog"], n)
		}
	}
	for _, where := range []string{"docs/contracts/config.md", "deploy/compose/docker-compose.yml", "deploy/compose/.env.example", "deploy/helm/openlog"} {
		if len(missing[where]) > 0 {
			t.Errorf("%s does not mention %d variable(s) read by internal/config (add them, or allowlist them in envcoverage_test.go with a reason):\n  %s",
				where, len(missing[where]), strings.Join(missing[where], "\n  "))
		}
	}
	// A mention anywhere in the Compose file (a comment) does not reach a process: every variable must be an environment
	// key of `openlog` (the settings anchor or an explicit entry), or of the service it belongs to, so a setting in .env
	// is never silently ignored (D-111).
	var notPassed []string
	for _, n := range names {
		skip := false
		if _, ok := notInCompose[n]; ok {
			skip = true
		}
		for _, p := range notInComposePrefixes {
			skip = skip || strings.HasPrefix(n, p)
		}
		services := []string{"openlog"}
		for prefix, svc := range composeOwnService {
			if strings.HasPrefix(n, prefix) {
				services = append(services, svc)
			}
		}
		passed := false
		for _, svc := range services {
			passed = passed || composeServiceEnvKeys(t, compose, svc)[n]
		}
		if !skip && !passed {
			notPassed = append(notPassed, n+" ("+strings.Join(services, " or ")+")")
		}
	}
	if len(notPassed) > 0 {
		t.Errorf("deploy/compose/docker-compose.yml does not pass %d variable(s) to their service (openlog: add them to x-openlog-settings):\n  %s",
			len(notPassed), strings.Join(notPassed, "\n  "))
	}
	// Allowlist entries must still exist, so stale exceptions are removed.
	known := map[string]bool{}
	for _, n := range names {
		known[n] = true
	}
	for _, m := range []map[string]string{notInCompose, notInHelm} {
		for n := range m {
			if !known[n] {
				t.Errorf("allowlisted variable %s is no longer read by internal/config", n)
			}
		}
	}
}
