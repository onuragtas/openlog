package phpforwarder

import (
	"path"
	"regexp"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/discovery"
	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
)

// APMAgent is the apm_hint.agent value of rules that the PHP extension instruments.
const APMAgent = "openlog-agent-php"

// APM hint status values (semantic-conventions §3.4).
const (
	APMStatusActive       = "active"
	APMStatusNotInstalled = "not_installed"
)

// ActiveWindow is how recently spans must have arrived for apm_hint.status "active" (php-agent.md §6).
const ActiveWindow = 10 * time.Minute

var (
	phpBinary = regexp.MustCompile(`^php([0-9]+(\.[0-9]+)?)?$`)
	modPHP    = regexp.MustCompile(`^(libphp[0-9.]*\.so|php[0-9.]*\.load|[0-9]*-?php[0-9.]*\.conf)$`)
)

var (
	binDirs    = []string{"/usr/bin", "/usr/local/bin", "/bin"}
	modPHPDirs = []string{
		"/etc/apache2/mods-enabled", "/usr/lib/apache2/modules", // Debian/Ubuntu
		"/etc/httpd/conf.modules.d", "/usr/lib64/httpd/modules", "/usr/lib/httpd/modules", // RHEL family
	}
)

// DetectRuntime reports whether the host runs PHP (php-agent.md §6): a discovered php-fpm service, Apache with
// mod_php, or a php CLI binary. reason names what was found.
func DetectRuntime(fs *hostfs.FS, services []discovery.Service) (found bool, reason string) {
	apache := false
	for _, s := range services {
		switch s.RuleID {
		case "php-fpm":
			return true, "php-fpm"
		case "apache-httpd":
			apache = true
		}
	}
	if apache && anyMatch(fs, modPHPDirs, modPHP) != "" {
		return true, "apache mod_php"
	}
	if p := anyMatch(fs, binDirs, phpBinary); p != "" {
		return true, "php cli " + p
	}
	return false, ""
}

func anyMatch(fs *hostfs.FS, dirs []string, re *regexp.Regexp) string {
	for _, dir := range dirs {
		entries, err := fs.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && re.MatchString(e.Name()) {
				return path.Join(dir, e.Name())
			}
		}
	}
	return ""
}
