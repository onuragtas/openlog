// Package config loads and validates the agent configuration file
// (/etc/openlog-infra-agent/config.yaml), see docs/plan/05-infra-agent.md §8.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultPath is the configuration file used when -config is not given.
const DefaultPath = "/etc/openlog-infra-agent/config.yaml"

// DefaultInstallRoot is the default update.install_root.
const DefaultInstallRoot = "/opt/openlog/infra-agent"

// Duration is a time.Duration that unmarshals from Go duration strings.
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: invalid duration %q", n.Line, s)
	}
	*d = Duration(v)
	return nil
}

// D returns the value as time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// Config is the full agent configuration.
type Config struct {
	LicenseKey        string             `yaml:"license_key"`
	Endpoint          string             `yaml:"endpoint"`
	Interval          Duration           `yaml:"interval"`
	InventoryInterval Duration           `yaml:"inventory_interval"`
	StateDir          string             `yaml:"state_dir"`
	LogLevel          string             `yaml:"log_level"`
	Host              HostConfig         `yaml:"host"`
	Collectors        Collectors         `yaml:"collectors"`
	Inventory         InventoryConfig    `yaml:"inventory"`
	Discovery         DiscoveryConfig    `yaml:"discovery"`
	Buffer            BufferConfig       `yaml:"buffer"`
	Export            ExportConfig       `yaml:"export"`
	ProcessMetrics    ProcessMetrics     `yaml:"process_metrics"`
	Containers        Containers         `yaml:"containers"`
	Logs              LogsConfig         `yaml:"logs"`
	Integrations      IntegrationsConfig `yaml:"integrations"`
	Update            UpdateConfig       `yaml:"update"`
	Release           ReleaseConfig      `yaml:"release"`
	PHPForwarder      PHPForwarder       `yaml:"php_forwarder"`
	PHPAgent          PHPAgentConfig     `yaml:"php_agent"`  // php_agent.go
	Kubernetes        KubernetesConfig   `yaml:"kubernetes"` // kubernetes.go
}

// PHPForwarder configures the php_forwarder module (docs/contracts/php-agent.md §6).
type PHPForwarder struct {
	// Enabled: nil (unset) = enabled while discovery finds a PHP runtime; an explicit value wins.
	Enabled *bool `yaml:"enabled"`
	// Socket is the unix datagram socket path; empty disables the unix listener (UDP only).
	Socket string `yaml:"socket"`
	// SocketGroup: "" or "auto" = first existing of www-data, nginx, apache, php-fpm; or a group name / numeric gid.
	SocketGroup string `yaml:"socket_group"`
	// SocketMode is "0660" (default) or "0666" (any local user may send).
	SocketMode        string   `yaml:"socket_mode"`
	UDPListen         string   `yaml:"udp_listen"`
	MaxPendingTraces  int      `yaml:"max_pending_traces"`
	ReassemblyTimeout Duration `yaml:"reassembly_timeout"`
}

// DefaultPHPSocket is the default php_forwarder.socket.
const DefaultPHPSocket = "/run/openlog-infra-agent/php.sock"

// Mode returns the socket file mode.
func (p PHPForwarder) Mode() os.FileMode {
	if p.SocketMode == "0666" {
		return 0o666
	}
	return 0o660
}

func (p *PHPForwarder) validate() []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if p.Socket != "" {
		if !filepath.IsAbs(p.Socket) {
			add("php_forwarder.socket must be an absolute path")
		}
		if len(p.Socket) > 107 {
			add("php_forwarder.socket must be at most 107 bytes (unix socket path limit)")
		}
	}
	if p.Socket == "" && p.UDPListen == "" {
		add("php_forwarder: socket or udp_listen must be set")
	}
	if p.SocketMode != "0660" && p.SocketMode != "0666" {
		add("php_forwarder.socket_mode must be \"0660\" or \"0666\"")
	}
	if p.UDPListen != "" {
		if _, port, err := net.SplitHostPort(p.UDPListen); err != nil || port == "" || port == "0" {
			add("php_forwarder.udp_listen must be host:port (e.g. 127.0.0.1:18127)")
		}
	}
	if p.MaxPendingTraces < 1 || p.MaxPendingTraces > 1_000_000 {
		add("php_forwarder.max_pending_traces must be between 1 and 1000000")
	}
	if d := p.ReassemblyTimeout.D(); d < 100*time.Millisecond || d > 5*time.Minute {
		add("php_forwarder.reassembly_timeout must be between 100ms and 5m")
	}
	return errs
}

// UpdateConfig configures agent self-update (docs/contracts/releases-updates.md §3).
type UpdateConfig struct {
	// Enabled allows applying updates ordered by the backend. Sync (version reporting) runs regardless.
	Enabled bool `yaml:"enabled"`
	// InstallRoot holds versions/<v>/ and the current symlink.
	InstallRoot string `yaml:"install_root"`
}

// ReleaseConfig configures release verification.
type ReleaseConfig struct {
	// TrustedKeysFile adds base64 Ed25519 public keys (one per line) to the compiled-in keys.
	TrustedKeysFile string `yaml:"trusted_keys_file"`
}

// ProcessMetrics configures per-process metrics for the top processes.
type ProcessMetrics struct {
	Enabled    bool `yaml:"enabled"`
	TopNCPU    int  `yaml:"top_n_cpu"`
	TopNMemory int  `yaml:"top_n_memory"`
}

// Containers configures container inventory (Docker Engine API, CRI) and cgroup v2 metrics.
type Containers struct {
	Enabled bool `yaml:"enabled"`
	// DockerSocket is a host path, resolved under host.root_path.
	DockerSocket string `yaml:"docker_socket"`
	// CRISockets are host paths of CRI runtime sockets (containerd, CRI-O); empty disables CRI listing.
	CRISockets []string `yaml:"cri_sockets"`
}

// LogsConfig configures log collection from files and journald.
type LogsConfig struct {
	Enabled           bool          `yaml:"enabled"`
	AutoFromDiscovery bool          `yaml:"auto_from_discovery"`
	MaskSecrets       bool          `yaml:"mask_secrets"`
	ParseSeverity     bool          `yaml:"parse_severity"`
	PollInterval      Duration      `yaml:"poll_interval"`
	StartAt           string        `yaml:"start_at"`
	MaxLineBytes      int           `yaml:"max_line_bytes"`
	RateLimitLines    int           `yaml:"rate_limit_lines"`
	Files             []LogFile     `yaml:"files"`
	Journald          JournaldInput `yaml:"journald"`
	Containers        ContainerLogs `yaml:"containers"`
}

// Container log sources (logs.containers.source).
const (
	ContainerLogSourceAuto = "auto" // json-file log when readable, else the Docker Engine API
	ContainerLogSourceFile = "file"
	ContainerLogSourceAPI  = "api"
)

// ContainerLogs configures automatic collection of container stdout/stderr (Docker).
type ContainerLogs struct {
	Enabled bool `yaml:"enabled"`
	// Source: auto, file (json-file logs only) or api (GET /containers/{id}/logs).
	Source         string           `yaml:"source"`
	MaxContainers  int              `yaml:"max_containers"`
	RateLimitLines int              `yaml:"rate_limit_lines"`
	Include        []ContainerMatch `yaml:"include"`
	Exclude        []ContainerMatch `yaml:"exclude"`
	// DockerDir is the host path of Docker's containers directory, used to find json-file
	// logs when the Docker Engine API is not accessible.
	DockerDir string `yaml:"docker_containers_dir"`
}

// ContainerMatch selects containers; every non-empty field must match (globs, filepath.Match syntax).
type ContainerMatch struct {
	Name           string `yaml:"name"`
	Image          string `yaml:"image"`
	ComposeProject string `yaml:"compose_project"`
	ComposeService string `yaml:"compose_service"`
	// Label is "key" (label present) or "key=value" (value is a glob).
	Label string `yaml:"label"`
	// MultilineStart (include items only) is the regex of a record's first line for matching containers;
	// the container label openlog.logs.multiline takes precedence.
	MultilineStart string `yaml:"multiline_start"`
}

func (m ContainerMatch) empty() bool {
	return m.Name == "" && m.Image == "" && m.ComposeProject == "" && m.ComposeService == "" && m.Label == ""
}

// LogFile is one file tailing input.
type LogFile struct {
	Path           string            `yaml:"path"`
	Exclude        []string          `yaml:"exclude"`
	MultilineStart string            `yaml:"multiline_start"`
	Attributes     map[string]string `yaml:"attributes"`
}

// JournaldInput configures reading the systemd journal through journalctl.
type JournaldInput struct {
	Enabled        bool     `yaml:"enabled"`
	JournalctlPath string   `yaml:"journalctl_path"`
	Units          []string `yaml:"units"`
	Priority       string   `yaml:"priority"`
}

// HostConfig describes how the host is seen and labelled.
type HostConfig struct {
	RootPath        string            `yaml:"root_path"`
	ExtraAttributes map[string]string `yaml:"extra_attributes"`
}

// Collectors toggles metric collectors.
type Collectors struct {
	CPU        bool `yaml:"cpu"`
	Memory     bool `yaml:"memory"`
	Load       bool `yaml:"load"`
	Filesystem bool `yaml:"filesystem"`
	Disk       bool `yaml:"disk"`
	Network    bool `yaml:"network"`
	Uptime     bool `yaml:"uptime"`
	Processes  bool `yaml:"processes"`
}

// InventoryConfig toggles inventory collection.
type InventoryConfig struct {
	Enabled bool `yaml:"enabled"`
}

// DiscoveryConfig configures the discovery rule engine.
type DiscoveryConfig struct {
	Enabled  bool   `yaml:"enabled"`
	RulesDir string `yaml:"rules_dir"`
}

// BufferConfig configures the on-disk retry buffer.
type BufferConfig struct {
	Dir      string `yaml:"dir"`
	MaxBytes int64  `yaml:"max_bytes"`
}

// ExportConfig tunes the OTLP/HTTP exporter.
type ExportConfig struct {
	Timeout         Duration `yaml:"timeout"`
	MaxRequestBytes int      `yaml:"max_request_bytes"`
}

// Default returns a configuration with all defaults applied.
func Default() *Config {
	return &Config{
		Interval:          Duration(10 * time.Second),
		InventoryInterval: Duration(time.Hour),
		StateDir:          "/var/lib/openlog-infra-agent",
		LogLevel:          "info",
		Host:              HostConfig{RootPath: "/"},
		Collectors: Collectors{
			CPU: true, Memory: true, Load: true, Filesystem: true,
			Disk: true, Network: true, Uptime: true, Processes: true,
		},
		Inventory: InventoryConfig{Enabled: true},
		Discovery: DiscoveryConfig{Enabled: true, RulesDir: "/etc/openlog-infra-agent/discovery.d"},
		Buffer:    BufferConfig{Dir: "/var/lib/openlog-infra-agent/buffer", MaxBytes: 256 << 20},
		Export:    ExportConfig{Timeout: Duration(15 * time.Second), MaxRequestBytes: 4 << 20},

		ProcessMetrics: ProcessMetrics{Enabled: true, TopNCPU: 20, TopNMemory: 20},
		Containers: Containers{Enabled: true, DockerSocket: "/var/run/docker.sock",
			CRISockets: []string{"/run/containerd/containerd.sock", "/run/k3s/containerd/containerd.sock", "/var/run/crio/crio.sock"}},
		Logs: LogsConfig{
			Enabled: true, ParseSeverity: true, PollInterval: Duration(time.Second), StartAt: "end",
			MaxLineBytes: 64 << 10, RateLimitLines: 2000,
			Journald: JournaldInput{JournalctlPath: "journalctl"},
			Containers: ContainerLogs{
				Enabled: true, Source: ContainerLogSourceAuto, MaxContainers: 100, RateLimitLines: 1000,
				DockerDir: "/var/lib/docker/containers",
			},
		},
		Integrations: defaultIntegrations(),
		Update:       UpdateConfig{Enabled: true, InstallRoot: DefaultInstallRoot},
		PHPForwarder: PHPForwarder{
			Socket: DefaultPHPSocket, SocketGroup: "auto", SocketMode: "0660",
			MaxPendingTraces: 10000, ReassemblyTimeout: Duration(5 * time.Second),
		},
		PHPAgent:   defaultPHPAgent(),
		Kubernetes: defaultKubernetes(),
	}
}

// Load reads path (if it exists or !allowMissing), applies environment
// overrides and returns the configuration. It does not validate.
func Load(path string, allowMissing bool) (*Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := Parse(data, cfg); err != nil {
			return nil, fmt.Errorf("config %s: %w", path, err)
		}
	case allowMissing && errors.Is(err, fs.ErrNotExist):
	default:
		return nil, fmt.Errorf("config: %w", err)
	}
	cfg.ApplyEnv(os.Getenv)
	return cfg, nil
}

// Parse decodes YAML into cfg, rejecting unknown keys.
func Parse(data []byte, cfg *Config) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// ApplyEnv applies OPENLOG_LICENSE_KEY and OPENLOG_ENDPOINT overrides.
func (c *Config) ApplyEnv(getenv func(string) string) {
	if v := getenv("OPENLOG_LICENSE_KEY"); v != "" {
		c.LicenseKey = v
	}
	if v := getenv("OPENLOG_ENDPOINT"); v != "" {
		c.Endpoint = v
	}
	c.Kubernetes.applyEnv(getenv)
}

// Validate checks the configuration. When requireExport is false (e.g. -once),
// the endpoint and license key may be empty.
func (c *Config) Validate(requireExport bool) error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if requireExport {
		if c.LicenseKey == "" {
			add("license_key is required (or set OPENLOG_LICENSE_KEY)")
		}
		if c.Endpoint == "" {
			add("endpoint is required (or set OPENLOG_ENDPOINT)")
		}
	}
	if c.Endpoint != "" {
		u, err := url.Parse(c.Endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			add("endpoint %q must be an http(s) URL", c.Endpoint)
		}
	}
	if c.Interval.D() < time.Second {
		add("interval must be at least 1s (got %s)", c.Interval.D())
	}
	if c.InventoryInterval.D() < c.Interval.D() {
		add("inventory_interval (%s) must not be shorter than interval (%s)", c.InventoryInterval.D(), c.Interval.D())
	}
	if c.Host.RootPath == "" || !strings.HasPrefix(c.Host.RootPath, "/") {
		add("host.root_path must be an absolute path")
	}
	if c.StateDir == "" {
		add("state_dir must not be empty")
	}
	if c.Buffer.MaxBytes < 0 {
		add("buffer.max_bytes must be >= 0")
	}
	if c.Buffer.MaxBytes > 0 && c.Buffer.Dir == "" {
		add("buffer.dir must be set when buffer.max_bytes > 0")
	}
	if c.Export.Timeout.D() <= 0 {
		add("export.timeout must be positive")
	}
	if c.Export.MaxRequestBytes < 64<<10 {
		add("export.max_request_bytes must be at least 65536")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		add("log_level must be one of debug, info, warn, error")
	}
	if c.ProcessMetrics.TopNCPU < 0 || c.ProcessMetrics.TopNMemory < 0 {
		add("process_metrics.top_n_cpu and top_n_memory must be >= 0")
	}
	if c.Containers.Enabled && !strings.HasPrefix(c.Containers.DockerSocket, "/") {
		add("containers.docker_socket must be an absolute path")
	}
	for i, s := range c.Containers.CRISockets {
		if c.Containers.Enabled && !strings.HasPrefix(s, "/") {
			add("containers.cri_sockets[%d] must be an absolute path", i)
		}
	}
	if !filepath.IsAbs(c.Update.InstallRoot) {
		add("update.install_root must be an absolute path")
	}
	if c.Release.TrustedKeysFile != "" && !filepath.IsAbs(c.Release.TrustedKeysFile) {
		add("release.trusted_keys_file must be an absolute path")
	}
	errs = append(errs, c.Logs.validate()...)
	errs = append(errs, c.Integrations.validate()...)
	errs = append(errs, c.PHPForwarder.validate()...)
	errs = append(errs, c.PHPAgent.validate()...)
	errs = append(errs, c.Kubernetes.validate(os.Getenv)...)
	for k := range c.Host.ExtraAttributes {
		if k == "" {
			add("host.extra_attributes contains an empty key")
		}
	}
	return errors.Join(errs...)
}

var journalPriorities = map[string]bool{
	"emerg": true, "alert": true, "crit": true, "err": true, "warning": true, "notice": true, "info": true, "debug": true,
	"0": true, "1": true, "2": true, "3": true, "4": true, "5": true, "6": true, "7": true,
}

func (l *LogsConfig) validate() []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if l.PollInterval.D() < 100*time.Millisecond || l.PollInterval.D() > time.Minute {
		add("logs.poll_interval must be between 100ms and 1m")
	}
	if l.StartAt != "end" && l.StartAt != "beginning" {
		add("logs.start_at must be end or beginning")
	}
	if l.MaxLineBytes < 256 || l.MaxLineBytes > 4<<20 {
		add("logs.max_line_bytes must be between 256 and 4194304")
	}
	if l.RateLimitLines < 0 {
		add("logs.rate_limit_lines must be >= 0 (0 = unlimited)")
	}
	for i, f := range l.Files {
		if !strings.HasPrefix(f.Path, "/") {
			add("logs.files[%d].path must be an absolute path or glob", i)
		} else if _, err := filepath.Match(f.Path, ""); err != nil {
			add("logs.files[%d].path: invalid glob %q", i, f.Path)
		}
		for _, e := range f.Exclude {
			if _, err := filepath.Match(e, ""); err != nil {
				add("logs.files[%d].exclude: invalid glob %q", i, e)
			}
		}
		if f.MultilineStart != "" {
			if _, err := regexp.Compile(f.MultilineStart); err != nil {
				add("logs.files[%d].multiline_start: %v", i, err)
			}
		}
	}
	if c := l.Containers; c.Enabled {
		switch c.Source {
		case ContainerLogSourceAuto, ContainerLogSourceFile, ContainerLogSourceAPI:
		default:
			add("logs.containers.source must be auto, file or api")
		}
		if c.MaxContainers < 1 || c.MaxContainers > 1000 {
			add("logs.containers.max_containers must be between 1 and 1000")
		}
		if c.RateLimitLines < 0 {
			add("logs.containers.rate_limit_lines must be >= 0 (0 = unlimited)")
		}
		if !strings.HasPrefix(c.DockerDir, "/") {
			add("logs.containers.docker_containers_dir must be an absolute path")
		}
		for name, list := range map[string][]ContainerMatch{"include": c.Include, "exclude": c.Exclude} {
			for i, m := range list {
				if m.empty() {
					add("logs.containers.%s[%d] must set name, image, compose_project, compose_service or label", name, i)
				}
				for _, g := range []string{m.Name, m.Image, m.ComposeProject, m.ComposeService, m.Label} {
					if _, err := filepath.Match(g, ""); err != nil {
						add("logs.containers.%s[%d]: invalid glob %q", name, i, g)
					}
				}
				if m.MultilineStart != "" {
					if name == "exclude" {
						add("logs.containers.exclude[%d]: multiline_start is only allowed in include", i)
					} else if _, err := regexp.Compile(m.MultilineStart); err != nil {
						add("logs.containers.include[%d].multiline_start: %v", i, err)
					}
				}
			}
		}
	}
	if l.Journald.Enabled {
		if l.Journald.JournalctlPath == "" {
			add("logs.journald.journalctl_path must not be empty")
		}
		if p := l.Journald.Priority; p != "" && !journalPriorities[p] {
			add("logs.journald.priority must be a syslog level name (emerg..debug) or 0..7")
		}
	}
	return errs
}
