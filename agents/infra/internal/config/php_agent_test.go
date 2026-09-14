package config

import (
	"strings"
	"testing"
	"time"
)

func TestPHPAgentDefaultsAndParse(t *testing.T) {
	c := Default()
	if p := c.PHPAgent; p.Mode != PHPAgentModeManual || p.Version != PHPAgentVersionAgent || p.Reload != PHPAgentReloadNone ||
		!p.RemoteConfig || p.HealthCheckAfter.D() != 5*time.Minute || p.InstallRoot != DefaultPHPAgentRoot {
		t.Fatalf("defaults = %+v", p)
	}
	if err := Parse([]byte("php_agent:\n  mode: auto\n  version: 0.9.1\n  reload: graceful\n  exclude_bins: [/usr/bin/php7*]\n  health_check_after: 30s\n"), c); err != nil {
		t.Fatal(err)
	}
	if errs := c.PHPAgent.validate(); len(errs) != 0 {
		t.Fatalf("valid config: %v", errs)
	}
	if !c.PHPAgent.RemoteConfig || c.PHPAgent.InstallRoot != DefaultPHPAgentRoot {
		t.Errorf("unset fields lost their defaults: %+v", c.PHPAgent)
	}
}

func TestPHPAgentValidate(t *testing.T) {
	for name, c := range map[string]struct {
		mutate func(*PHPAgentConfig)
		want   string
	}{
		"mode":         {func(p *PHPAgentConfig) { p.Mode = "on" }, "php_agent.mode"},
		"version":      {func(p *PHPAgentConfig) { p.Version = "latest" }, "php_agent.version"},
		"reload":       {func(p *PHPAgentConfig) { p.Reload = "restart" }, "php_agent.reload"},
		"glob":         {func(p *PHPAgentConfig) { p.ExcludeBins = []string{"/usr/bin/php["} }, "invalid glob"},
		"health":       {func(p *PHPAgentConfig) { p.HealthCheckAfter = Duration(time.Second) }, "health_check_after"},
		"install root": {func(p *PHPAgentConfig) { p.InstallRoot = "/" }, "install_root"},
	} {
		p := defaultPHPAgent()
		c.mutate(&p)
		errs := p.validate()
		if len(errs) != 1 || !strings.Contains(errs[0].Error(), c.want) {
			t.Errorf("%s: errors = %v, want %q", name, errs, c.want)
		}
	}
}
