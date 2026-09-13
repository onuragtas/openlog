package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIntegrationsConfig(t *testing.T) {
	yml := `
integrations:
  interval: 60s
  redis:
    password: env:REDIS_PW
    tls: { enabled: true, insecure_skip_verify: true }
    instances:
      - match: { port: 6380 }
        password: file:/etc/openlog-infra-agent/redis2.password
  mysql:
    username: openlog
    password: literal-secret
    top_n_tables: 10
  postgresql:
    enabled: false
    databases: [app]
  nginx:
    instances:
      - match: { unit: nginx.service }
        endpoint: http://127.0.0.1:8080/nginx_status
`
	cfg := Default()
	if err := Parse([]byte(yml), cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(false); err != nil {
		t.Fatal(err)
	}
	ic := cfg.Integrations
	if ic.EffectiveInterval("redis") != time.Minute || ic.Timeout.D() != 10*time.Second || !ic.Redis.Enabled || ic.PostgreSQL.Enabled {
		t.Errorf("integrations = %+v", ic)
	}
	merged := ic.Redis.InstanceSettings.Merge(ic.Redis.Instances[0].InstanceSettings)
	if merged.Password.Source() != "file:/etc/openlog-infra-agent/redis2.password" || merged.TLS == nil || !merged.TLS.Enabled {
		t.Errorf("merged = %+v", merged)
	}
	w := cfg.Warnings()
	if len(w) != 1 || !strings.Contains(w[0], "integrations.mysql.password is a literal") || strings.Contains(w[0], "literal-secret") {
		t.Errorf("warnings = %v", w)
	}
}

func TestIntegrationsValidation(t *testing.T) {
	yml := `
integrations:
  timeout: 1m
  nginx:
    password: env:X
    endpoint: 127.0.0.1:80
  redis:
    password: "file:relative"
    instances:
      - endpoint: "unix:relative.sock"
  mysql:
    endpoint: "no-port"
`
	cfg := Default()
	if err := Parse([]byte(yml), cfg); err != nil {
		t.Fatal(err)
	}
	err := cfg.Validate(false)
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{
		"integrations.timeout must be positive and not longer than integrations.interval",
		"integrations.nginx.password is not supported by the nginx integration",
		"integrations.nginx.endpoint must be the http(s) URL",
		"integrations.redis.password: file: needs an absolute path",
		"integrations.redis.instances[0].match: at least one of",
		"integrations.redis.instances[0].endpoint unix:<path> needs an absolute path",
		"integrations.mysql.endpoint must be host:port",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
	if Parse([]byte("integrations:\n  redis:\n    passwd: x\n"), Default()) == nil {
		t.Error("unknown integration keys must be rejected")
	}
}

func TestSecretResolveAndNeverRevealed(t *testing.T) {
	t.Setenv("OPENLOG_TEST_PW", "from-env")
	dir := t.TempDir()
	f := filepath.Join(dir, "pw")
	if err := os.WriteFile(f, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for s, want := range map[Secret]string{"env:OPENLOG_TEST_PW": "from-env", Secret("file:" + f): "from-file", "plain": "plain", "": ""} {
		got, err := s.Resolve()
		if err != nil || got != want {
			t.Errorf("Resolve(%s) = %q, %v", s.Source(), got, err)
		}
	}
	if _, err := Secret("env:OPENLOG_TEST_MISSING").Resolve(); !errors.Is(err, ErrSecretUnset) {
		t.Errorf("missing env: %v", err)
	}
	if _, err := Secret("file:" + filepath.Join(dir, "none")).Resolve(); !errors.Is(err, ErrSecretUnset) {
		t.Errorf("missing file: %v", err)
	}

	s := InstanceSettings{Username: "u", Password: "super-secret"}
	outs := []string{fmt.Sprintf("%v", s), fmt.Sprintf("%+v", s), fmt.Sprintf("%#v", s), fmt.Sprint(s.Password)}
	b, _ := json.Marshal(s)
	outs = append(outs, string(b))
	for _, o := range outs {
		if strings.Contains(o, "super-secret") {
			t.Errorf("secret revealed: %s", o)
		}
	}
}
