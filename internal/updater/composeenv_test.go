package updater

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestParseDotenv(t *testing.T) {
	d, err := parseDotenv([]byte(`# comment
OPENLOG_A=plain value # inline comment
export OPENLOG_B='single $NOT #kept'
OPENLOG_C="double\nline ${OPENLOG_A}"
OPENLOG_D=${OPENLOG_A:-x}-suffix
OPENLOG_E=${UNSET_IN_FILE}
OPENLOG_F=a#b
OPENLOG_G='multi
line'
OPENLOG_H=
OPENLOG_I=$$literal
OPENLOG_A=override
`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"OPENLOG_A": "override", "OPENLOG_B": "single $NOT #kept", "OPENLOG_C": "double\nline plain value",
		"OPENLOG_D": "plain value-suffix", "OPENLOG_F": "a#b", "OPENLOG_G": "multi\nline", "OPENLOG_H": "", "OPENLOG_I": "$literal",
	}
	for k, v := range want {
		if got, ok := d.lookup(k); !ok || got != v {
			t.Errorf("%s = %q %v, want %q", k, got, ok, v)
		}
	}
	if _, ok := d.lookup("OPENLOG_E"); ok {
		t.Error("a value referencing a variable the file does not set must be undetermined")
	}
	if !slices.Equal(d.order, []string{"OPENLOG_A", "OPENLOG_B", "OPENLOG_C", "OPENLOG_D", "OPENLOG_E", "OPENLOG_F", "OPENLOG_G", "OPENLOG_H", "OPENLOG_I"}) {
		t.Errorf("order %v", d.order)
	}
	for _, bad := range []string{"NOEQUALS\n", "A='open\n", `B="open` + "\n", "1BAD=x\n"} {
		if _, err := parseDotenv([]byte(bad)); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestInterpolate(t *testing.T) {
	vars := map[string]string{"SET": "v", "EMPTY": ""}
	lookup := func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
	for in, want := range map[string]string{
		"literal": "literal", "${SET}": "v", "$SET/x": "v/x", "${EMPTY:-d}": "d", "${EMPTY-d}": "", "${SET:-d}": "v",
		"${SET:+alt}": "alt", "${EMPTY:+alt}": "", "${EMPTY+alt}": "alt", "$$SET": "$SET", "${EMPTY:-${SET}}": "v", "a$": "a$",
	} {
		got, ok := interpolate(in, lookup)
		if !ok || got != want {
			t.Errorf("interpolate(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"${UNSET}", "${UNSET:-default}", "$UNSET", "${EMPTY:?required}", "${SET", "${1X}"} {
		if got, ok := interpolate(in, lookup); ok {
			t.Errorf("interpolate(%q) = %q, want undetermined", in, got)
		}
	}
}

const testCompose = `name: openlog
x-openlog-compose-version: "0.9.1"
x-settings: &settings
  OPENLOG_SAAS_MODE: ${OPENLOG_SAAS_MODE:-}
  OPENLOG_API_TRUSTED_PROXIES: ${OPENLOG_API_TRUSTED_PROXIES:-}
services:
  clickhouse:
    environment:
      OPENLOG_S3_ACCESS_KEY_ID: ${OPENLOG_S3_ACCESS_KEY_ID-minio}
  openlog:
    image: ${OPENLOG_IMAGE:-openlog:dev}
    ports: ["${OPENLOG_API_PORT:-8080}:8080"]
    environment:
      <<: *settings
      OPENLOG_LOG_LEVEL: ${OPENLOG_LOG_LEVEL:-info}
      OPENLOG_KAFKA_BROKERS: kafka:19092
      OPENLOG_SMTP_HOST: ${OPENLOG_SMTP_HOST:-}
  openlog-alert:
    environment:
      - OPENLOG_AUTH_MODE=postgres
      - OPENLOG_PASSTHROUGH
`

func envMap(env []string) map[string]string {
	out := map[string]string{}
	for _, kv := range env {
		k, v, _ := cutEq(kv)
		out[k] = v
	}
	return out
}

func cutEq(s string) (string, string, bool) {
	for i := range s {
		if s[i] == '=' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

func TestMergeServiceEnv(t *testing.T) {
	proj, err := parseComposeProject([][]byte{[]byte(testCompose)})
	if err != nil {
		t.Fatal(err)
	}
	dot, err := parseDotenv([]byte(`OPENLOG_SAAS_MODE=true
OPENLOG_LOG_LEVEL=debug
OPENLOG_KAFKA_BROKERS=elsewhere:9092
OPENLOG_NEW_SETTING=on
OPENLOG_S3_ACCESS_KEY_ID=secret
OPENLOG_API_PORT=127.0.0.1:8080
OPENLOG_IMAGE=ghcr.io/x:1
OPENLOG_API_HTTP_ADDR=:9999
OPENLOG_UPDATER_SERVICES=openlog
OPENLOG_AUTH_MODE=static
`))
	if err != nil {
		t.Fatal(err)
	}
	// The container of an older compose file: no OPENLOG_SAAS_MODE, a value from the shell for the SMTP host.
	current := []string{"OPENLOG_LOG_LEVEL=info", "OPENLOG_KAFKA_BROKERS=kafka:19092", "OPENLOG_SMTP_HOST=smtp.shell", "OPENLOG_OVERRIDE=from-override", "NOEQUALS"}

	m := mergeServiceEnv(current, "openlog", proj, dot, true)
	got := envMap(m.env)
	want := map[string]string{
		"OPENLOG_LOG_LEVEL":     "debug",       // rule 1: variable set in .env
		"OPENLOG_KAFKA_BROKERS": "kafka:19092", // rule 1: compose literal wins over .env
		"OPENLOG_SAAS_MODE":     "true",        // rule 1: new key of the compose files
		"OPENLOG_SMTP_HOST":     "smtp.shell",  // rule 2: not in .env, container value kept
		"OPENLOG_OVERRIDE":      "from-override",
		"OPENLOG_NEW_SETTING":   "on", // rule 3 (stale files): not referenced by the compose files
		"NOEQUALS":              "",
	}
	if !maps.Equal(got, want) {
		t.Errorf("env\n got %v\nwant %v", got, want)
	}
	if m.fallback || !slices.Equal(m.changed, []string{"OPENLOG_LOG_LEVEL", "OPENLOG_NEW_SETTING", "OPENLOG_SAAS_MODE"}) {
		t.Errorf("changed %v fallback %v", m.changed, m.fallback)
	}
	if m.env[0] != "OPENLOG_LOG_LEVEL=debug" || m.env[4] != "NOEQUALS" {
		t.Errorf("existing order not kept: %v", m.env)
	}

	// Current compose files: keys they do not pass are intentionally not passed.
	if got := envMap(mergeServiceEnv(current, "openlog", proj, dot, false).env); got["OPENLOG_NEW_SETTING"] != "" {
		t.Errorf("current files: unreferenced key added: %v", got)
	}
	// List syntax, literal and pass-through entries.
	alert := envMap(mergeServiceEnv([]string{"OPENLOG_PASSTHROUGH=shell"}, "openlog-alert", proj, dot, false).env)
	if alert["OPENLOG_AUTH_MODE"] != "postgres" || alert["OPENLOG_PASSTHROUGH"] != "shell" {
		t.Errorf("alert env %v", alert)
	}
	// Fallback (no usable compose files): only missing or empty OPENLOG_* settings, never images or topology.
	fb := mergeServiceEnv([]string{"OPENLOG_LOG_LEVEL=info", "OPENLOG_SAAS_MODE="}, "openlog", nil, dot, true)
	fbEnv := envMap(fb.env)
	if !fb.fallback || fbEnv["OPENLOG_LOG_LEVEL"] != "info" || fbEnv["OPENLOG_SAAS_MODE"] != "true" || fbEnv["OPENLOG_NEW_SETTING"] != "on" {
		t.Errorf("fallback env %v", fbEnv)
	}
	for _, k := range []string{"OPENLOG_IMAGE", "OPENLOG_API_HTTP_ADDR", "OPENLOG_UPDATER_SERVICES"} {
		if _, ok := fbEnv[k]; ok {
			t.Errorf("fallback added %s", k)
		}
	}
	// No .env: unchanged.
	if m := mergeServiceEnv(current, "openlog", proj, nil, true); !slices.Equal(m.env, current) || len(m.changed) != 0 {
		t.Errorf("without .env: %v", m)
	}
}

func TestComposeProjectOverrides(t *testing.T) {
	override := `services:
  openlog:
    environment:
      OPENLOG_DATA_EXPORT_LOCAL_PATH: /exports
    env_file: [.env]
  openlog-alert:
    environment: !reset {}
`
	proj, err := parseComposeProject([][]byte{[]byte(testCompose), []byte(override)})
	if err != nil {
		t.Fatal(err)
	}
	o := proj.services["openlog"]
	if o.env["OPENLOG_DATA_EXPORT_LOCAL_PATH"].expr != "/exports" || o.env["OPENLOG_SAAS_MODE"].expr != "${OPENLOG_SAAS_MODE:-}" || !o.dotEnvFile {
		t.Errorf("openlog %+v", o)
	}
	if len(proj.services["openlog-alert"].env) != 0 {
		t.Errorf("!reset not applied: %+v", proj.services["openlog-alert"].env)
	}
	if !proj.refs["OPENLOG_API_PORT"] || !proj.refs["OPENLOG_S3_ACCESS_KEY_ID"] || proj.refs["OPENLOG_NEW_SETTING"] {
		t.Errorf("refs %v", proj.refs)
	}
	dot, _ := parseDotenv([]byte("OPENLOG_DATA_EXPORT_LOCAL_PATH=/elsewhere\nOPENLOG_API_PORT=8080\n"))
	env := envMap(mergeServiceEnv(nil, "openlog", proj, dot, false).env)
	// env_file: .env passes every .env variable the environment does not define; the override's literal wins.
	if env["OPENLOG_DATA_EXPORT_LOCAL_PATH"] != "/exports" || env["OPENLOG_API_PORT"] != "8080" {
		t.Errorf("env %v", env)
	}
	if _, err := parseComposeProject([][]byte{[]byte("services: [")}); err == nil {
		t.Error("expected a YAML error")
	}
}

func TestComposeFilePaths(t *testing.T) {
	labels := map[string]any{
		"com.docker.compose.project.config_files": "/opt/app/docker-compose.yml,/opt/app/docker-compose.override.yml",
		"com.docker.compose.project.working_dir":  "/opt/app",
	}
	got, err := composeFilePaths(labels, "/compose", map[string]string{"docker-compose.yml": "/compose/.bundle-staging/docker-compose.yml"})
	if err != nil || !slices.Equal(got, []string{"/compose/.bundle-staging/docker-compose.yml", filepath.Join("/compose", "docker-compose.override.yml")}) {
		t.Fatalf("paths %v %v", got, err)
	}
	labels["com.docker.compose.project.config_files"] = "/opt/app/docker-compose.yml,/srv/other/override.yml"
	if _, err := composeFilePaths(labels, "/compose", nil); err == nil {
		t.Error("a file outside the project directory must not be mapped")
	}
	if _, err := composeFilePaths(map[string]any{}, "/compose", nil); err == nil {
		t.Error("missing labels must fail")
	}
	if _, err := loadComposeProject([]string{filepath.Join(t.TempDir(), "missing.yml")}); !os.IsNotExist(err) {
		t.Errorf("missing file: %v", err)
	}
}
