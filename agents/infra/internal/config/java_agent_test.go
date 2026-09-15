package config

import (
	"strings"
	"testing"
	"time"
)

func TestJavaAgentValidate(t *testing.T) {
	def := defaultJavaAgent()
	if errs := def.validate(); len(errs) != 0 {
		t.Fatalf("defaults: %v", errs)
	}
	win := JavaAgentConfig{Mode: "auto", Version: "1.2.3", HealthCheckAfter: Duration(time.Minute), InstallRoot: WindowsJavaAgentRoot, LinkPath: WindowsJavaAgentLink}
	if errs := win.validate(); len(errs) != 0 {
		t.Fatalf("windows defaults: %v", errs)
	}
	for _, tc := range []struct {
		mut  func(*JavaAgentConfig)
		want string
	}{
		{func(c *JavaAgentConfig) { c.Mode = "sometimes" }, "java_agent.mode"},
		{func(c *JavaAgentConfig) { c.Version = "latest" }, "java_agent.version"},
		{func(c *JavaAgentConfig) { c.HealthCheckAfter = Duration(time.Second) }, "health_check_after"},
		{func(c *JavaAgentConfig) { c.InstallRoot = "/" }, "install_root"},
		{func(c *JavaAgentConfig) { c.LinkPath = "relative.jar" }, "link_path"},
		{func(c *JavaAgentConfig) { c.LinkPath = "/opt/openlog/agent" }, "link_path"},
		{func(c *JavaAgentConfig) { c.LinkPath = "/opt/openlog/java-agent/x.jar" }, "outside"},
	} {
		c := defaultJavaAgent()
		tc.mut(&c)
		errs := c.validate()
		if len(errs) == 0 || !strings.Contains(errs[0].Error(), tc.want) {
			t.Errorf("want %q, got %v", tc.want, errs)
		}
	}
}
