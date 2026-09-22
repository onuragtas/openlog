// Package config resolves the profiler's settings from the environment (docs/contracts/ebpf-profiler.md).
//
// Environment only, no configuration file. This is one daemon with a handful of settings, and the names are
// the ones the language agents already use, so an operator who has configured any openlog agent has
// configured this one.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultEndpoint  = "http://localhost:4318"
	DefaultInterval  = 60 * time.Second
	DefaultFrequency = 99
	DefaultTimeout   = 10 * time.Second
)

// Config is the resolved configuration.
type Config struct {
	Endpoint   string
	LicenseKey string
	Interval   time.Duration
	// Frequency is the sampling frequency per CPU, in hertz.
	Frequency int
	Gzip      bool
	Timeout   time.Duration
	LogLevel  string
	// HostRoot prefixes host files when the profiler runs in a container with the host mounted.
	HostRoot string
	// RuntimeDir is where the infra agent publishes host-id and the service map.
	RuntimeDir string
}

// PeriodNanos is the CPU time one sample stands for. The sampler fires at a fixed frequency, so a count
// becomes nanoseconds by multiplying by this.
func (c Config) PeriodNanos() int64 { return int64(time.Second) / int64(c.Frequency) }

// Load reads the environment and validates it. It refuses rather than guessing: a profiler that starts
// without a license key would sample a whole machine and throw every profile away.
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		Endpoint:   DefaultEndpoint,
		Interval:   DefaultInterval,
		Frequency:  DefaultFrequency,
		Gzip:       true,
		Timeout:    DefaultTimeout,
		LogLevel:   "info",
		RuntimeDir: "/run/openlog-infra-agent",
	}
	if v := strings.TrimSpace(getenv("OPENLOG_ENDPOINT")); v != "" {
		c.Endpoint = v
	}
	c.LicenseKey = strings.TrimSpace(getenv("OPENLOG_LICENSE_KEY"))
	if c.LicenseKey == "" {
		return c, fmt.Errorf("OPENLOG_LICENSE_KEY is required")
	}
	if v := strings.TrimSpace(getenv("OPENLOG_PROFILE_INTERVAL")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return c, fmt.Errorf("OPENLOG_PROFILE_INTERVAL: %w", err)
		}
		if d < time.Second {
			return c, fmt.Errorf("OPENLOG_PROFILE_INTERVAL: %s is below the 1s minimum", d)
		}
		c.Interval = d
	}
	if v := strings.TrimSpace(getenv("OPENLOG_SAMPLE_FREQUENCY")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return c, fmt.Errorf("OPENLOG_SAMPLE_FREQUENCY: %w", err)
		}
		// An unbounded frequency is the one setting that can turn a profiler into the load it is measuring.
		if n < 1 || n > 1000 {
			return c, fmt.Errorf("OPENLOG_SAMPLE_FREQUENCY: %d is outside 1..1000", n)
		}
		c.Frequency = n
	}
	switch v := strings.ToLower(strings.TrimSpace(getenv("OPENLOG_COMPRESSION"))); v {
	case "", "gzip":
		c.Gzip = true
	case "none":
		c.Gzip = false
	default:
		return c, fmt.Errorf("OPENLOG_COMPRESSION: %q is not gzip or none", v)
	}
	if v := strings.TrimSpace(getenv("OPENLOG_EXPORT_TIMEOUT")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return c, fmt.Errorf("OPENLOG_EXPORT_TIMEOUT: %w", err)
		}
		c.Timeout = d
	}
	if v := strings.TrimSpace(getenv("OPENLOG_LOG_LEVEL")); v != "" {
		c.LogLevel = strings.ToLower(v)
	}
	if v := strings.TrimSpace(getenv("OPENLOG_HOST_ROOT")); v != "" {
		c.HostRoot = v
	}
	if v := strings.TrimSpace(getenv("OPENLOG_INFRA_RUNTIME_DIR")); v != "" {
		c.RuntimeDir = v
	}
	return c, nil
}

// Env builds a getenv from a map, for tests and for -once.
func Env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

var _ = os.Getenv
