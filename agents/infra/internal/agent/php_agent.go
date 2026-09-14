package agent

import (
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/phpagent"
)

// PHPAgentVersionAttribute is the host resource attribute with the installed PHP agent version (php-agent.md §7.3).
const PHPAgentVersionAttribute = "openlog.php_agent.version"

// PHPActive reports whether PHP spans arrived within the last 10 minutes (apm_hint.status active).
func (a *Agent) PHPActive() bool { return a.php.active(time.Now()) }

// hostAttributes are host.extra_attributes plus openlog.php_agent.version. Every change of the PHP agent made by the
// infra agent restarts it, so the version read at startup stays current (a package upgrade shows after a restart).
func hostAttributes(cfg *config.Config) map[string]string {
	v := phpagent.InstalledVersion(cfg.PHPAgent.InstallRoot)
	if v == "" {
		return cfg.Host.ExtraAttributes
	}
	out := make(map[string]string, len(cfg.Host.ExtraAttributes)+1)
	for k, val := range cfg.Host.ExtraAttributes {
		out[k] = val
	}
	out[PHPAgentVersionAttribute] = v
	return out
}
