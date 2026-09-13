package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultsValid(t *testing.T) {
	if err := Default().Validate(false); err != nil {
		t.Fatal(err)
	}
	if err := Default().Validate(true); err == nil {
		t.Fatal("expected missing license/endpoint error")
	}
}

func TestLoadFileAndEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yml := `
license_key: "file-key"
endpoint: "https://ingest.example:4318"
interval: 30s
inventory_interval: 2h
host:
  root_path: /host
  extra_attributes: { env: prod, team: payments }
collectors:
  disk: false
discovery:
  rules_dir: /tmp/rules
buffer:
  max_bytes: 1048576
`
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENLOG_ENDPOINT", "http://env:4318")
	cfg, err := Load(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LicenseKey != "file-key" || cfg.Endpoint != "http://env:4318" {
		t.Errorf("license/endpoint = %q %q", cfg.LicenseKey, cfg.Endpoint)
	}
	if cfg.Interval.D() != 30*time.Second || cfg.InventoryInterval.D() != 2*time.Hour {
		t.Errorf("intervals = %v %v", cfg.Interval.D(), cfg.InventoryInterval.D())
	}
	if cfg.Host.RootPath != "/host" || cfg.Host.ExtraAttributes["team"] != "payments" {
		t.Errorf("host = %+v", cfg.Host)
	}
	if cfg.Collectors.Disk || !cfg.Collectors.CPU || !cfg.Collectors.Network {
		t.Errorf("collectors = %+v (defaults must survive partial override)", cfg.Collectors)
	}
	if !cfg.Discovery.Enabled || cfg.Discovery.RulesDir != "/tmp/rules" || cfg.Buffer.MaxBytes != 1048576 {
		t.Errorf("discovery/buffer = %+v %+v", cfg.Discovery, cfg.Buffer)
	}
	if err := cfg.Validate(true); err != nil {
		t.Errorf("validate: %v", err)
	}
}

func TestLoadMissing(t *testing.T) {
	if _, err := Load("/nonexistent/config.yaml", false); err == nil {
		t.Error("expected error for missing file")
	}
	if _, err := Load("/nonexistent/config.yaml", true); err != nil {
		t.Errorf("allowMissing: %v", err)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"unknown key":  "intervall: 10s\n",
		"bad duration": "interval: ten\n",
	}
	for name, yml := range cases {
		if err := Parse([]byte(yml), Default()); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestUpdateConfig(t *testing.T) {
	cfg := Default()
	if !cfg.Update.Enabled || cfg.Update.InstallRoot != "/opt/openlog/infra-agent" || cfg.Release.TrustedKeysFile != "" {
		t.Fatalf("defaults: %+v %+v", cfg.Update, cfg.Release)
	}
	err := Parse([]byte("update:\n  enabled: false\n  install_root: /srv/agent\nrelease:\n  trusted_keys_file: /etc/openlog-infra-agent/release-keys\n"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Update.Enabled || cfg.Update.InstallRoot != "/srv/agent" || cfg.Release.TrustedKeysFile != "/etc/openlog-infra-agent/release-keys" {
		t.Fatalf("parsed: %+v %+v", cfg.Update, cfg.Release)
	}
	if err := cfg.Validate(false); err != nil {
		t.Fatal(err)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"short interval", func(c *Config) { c.Interval = Duration(time.Millisecond) }, "interval must be"},
		{"inventory shorter", func(c *Config) { c.InventoryInterval = Duration(time.Second) }, "inventory_interval"},
		{"bad endpoint", func(c *Config) { c.Endpoint = "ftp://x" }, "http(s) URL"},
		{"relative root", func(c *Config) { c.Host.RootPath = "host" }, "root_path"},
		{"log level", func(c *Config) { c.LogLevel = "trace" }, "log_level"},
		{"negative buffer", func(c *Config) { c.Buffer.MaxBytes = -1 }, "max_bytes"},
		{"relative install root", func(c *Config) { c.Update.InstallRoot = "opt/openlog" }, "update.install_root"},
		{"relative keys file", func(c *Config) { c.Release.TrustedKeysFile = "keys" }, "release.trusted_keys_file"},
	}
	for _, c := range cases {
		cfg := Default()
		c.mutate(cfg)
		err := cfg.Validate(false)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want containing %q", c.name, err, c.want)
		}
	}
}

func TestLogsProcessContainerConfig(t *testing.T) {
	cfg := Default()
	if !cfg.Logs.Enabled || cfg.Logs.AutoFromDiscovery || cfg.Logs.MaskSecrets || cfg.Logs.StartAt != "end" ||
		cfg.ProcessMetrics.TopNCPU != 20 || cfg.ProcessMetrics.TopNMemory != 20 || cfg.Containers.DockerSocket != "/var/run/docker.sock" {
		t.Errorf("defaults = %+v %+v %+v", cfg.Logs, cfg.ProcessMetrics, cfg.Containers)
	}
	yml := `
logs:
  auto_from_discovery: true
  mask_secrets: true
  files:
    - path: /var/log/app/*.log
      exclude: ["*.gz"]
      multiline_start: '^\d{4}-'
      attributes: { app: billing }
  journald: { enabled: true, units: [nginx.service], priority: warning }
process_metrics: { top_n_cpu: 5, top_n_memory: 0 }
containers: { enabled: false }
`
	if err := Parse([]byte(yml), cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(false); err != nil {
		t.Fatal(err)
	}
	if cfg.Logs.RateLimitLines != 2000 || cfg.Logs.Files[0].Attributes["app"] != "billing" || cfg.Logs.Journald.JournalctlPath != "journalctl" ||
		cfg.ProcessMetrics.TopNCPU != 5 || !cfg.ProcessMetrics.Enabled || cfg.Containers.Enabled {
		t.Errorf("parsed = %+v %+v", cfg.Logs, cfg.ProcessMetrics)
	}
	bad := Default()
	bad.Logs.Files = []LogFile{{Path: "relative/*.log"}, {Path: "/x/[", MultilineStart: "("}}
	bad.Logs.StartAt = "middle"
	bad.Logs.Journald = JournaldInput{Enabled: true, JournalctlPath: "journalctl", Priority: "loud"}
	bad.ProcessMetrics.TopNCPU = -1
	err := bad.Validate(false)
	for _, want := range []string{"logs.files[0].path", "logs.files[1].path: invalid glob", "multiline_start", "start_at", "journald.priority", "top_n_cpu"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}
