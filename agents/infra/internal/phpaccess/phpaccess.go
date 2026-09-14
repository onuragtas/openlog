// Package phpaccess decides which local users may send spans to the php_forwarder socket (php-agent.md §1, D-103).
//
// The socket is 0660 with the dedicated group openlog-php. PHP-FPM pools usually run as per-site users (hosting
// panels: HestiaCP, cPanel, Plesk, ...), so the privileged reconcile step adds every pool user found in the PHP-FPM
// pool configuration (and the Apache/nginx user when a web server is installed) to that group. The unprivileged agent
// uses the same discovery to report pools whose user is not a member yet.
//
// openlog-php grants nothing but write access to php.sock: it is deliberately not the openlog-agent group, which can
// read /etc/openlog-infra-agent/config.yaml (license key).
//
// Every function takes root, the directory the host's file system is visible at ("/" on the host; a fixture in tests,
// host.root_path in a container).
package phpaccess

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

const (
	// Group is the socket group of php.sock.
	Group = "openlog-php"
	// OptOutFile in the configuration directory disables automatic grants (like no-docker-access).
	OptOutFile = "no-php-access"
	// AccessEnv=0 at install time records OptOutFile.
	AccessEnv = "OPENLOG_AGENT_PHP_ACCESS"

	// MaxPools bounds discovery (pool files and reported pools).
	MaxPools = 512
	// maxPoolFileBytes bounds one pool file.
	maxPoolFileBytes = 1 << 20
)

// Access values of a reported pool.
const (
	AccessOK       = "ok"
	AccessMissing  = "missing"
	AccessOptedOut = "opted_out"
	// AccessNoUser: the pool's user does not exist in /etc/passwd (e.g. a directory service account) or is root.
	AccessNoUser = "unsupported_user"
)

// Pool is one [pool] section of a PHP-FPM pool file.
type Pool struct {
	Name       string `json:"pool"`
	PHPVersion string `json:"php_version"`
	User       string `json:"user"`
	// Unit is the systemd service of the FPM master serving the pool ("" when unknown).
	Unit string `json:"unit"`
	// File is the pool file (host path).
	File string `json:"file"`
}

// poolDir is a pool directory layout and how its FPM service is named.
type poolDir struct {
	glob string
	// re matches the directory (host path) and captures the version component used by unit.
	re   *regexp.Regexp
	unit func(v string) string
	ver  func(v string) string
}

func dotted(v string) string { // "82" -> "8.2", "8.2" stays
	if strings.Contains(v, ".") || len(v) < 2 {
		return v
	}
	return v[:1] + "." + v[1:]
}

func undotted(v string) string { return strings.ReplaceAll(v, ".", "") }

var poolDirs = []poolDir{
	// Debian/Ubuntu (ondrej/php, HestiaCP, ISPConfig): php8.2-fpm.service.
	{"/etc/php/*/fpm/pool.d", regexp.MustCompile(`^/etc/php/([0-9]+\.[0-9]+)/fpm/pool\.d$`),
		func(v string) string { return "php" + v + "-fpm.service" }, dotted},
	// RHEL/Fedora distribution PHP: php-fpm.service.
	{"/etc/php-fpm.d", regexp.MustCompile(`^/etc/php-fpm\.d$()`), func(string) string { return "php-fpm.service" }, dotted},
	// Remi SCL: php82-php-fpm.service.
	{"/etc/opt/remi/php*/php-fpm.d", regexp.MustCompile(`^/etc/opt/remi/php([0-9]+)/php-fpm\.d$`),
		func(v string) string { return "php" + v + "-php-fpm.service" }, dotted},
	// cPanel EasyApache: ea-php82-php-fpm.service.
	{"/opt/cpanel/ea-php*/root/etc/php-fpm.d", regexp.MustCompile(`^/opt/cpanel/ea-php([0-9]+)/root/etc/php-fpm\.d$`),
		func(v string) string { return "ea-php" + v + "-php-fpm.service" }, dotted},
	// Plesk: plesk-php82-fpm.service.
	{"/opt/plesk/php/*/etc/php-fpm.d", regexp.MustCompile(`^/opt/plesk/php/([0-9]+\.[0-9]+)/etc/php-fpm\.d$`),
		func(v string) string { return "plesk-php" + undotted(v) + "-fpm.service" }, dotted},
	// Alpine: php-fpm82 (OpenRC usually; the systemd name is only used when such a unit is active).
	{"/etc/php*/php-fpm.d", regexp.MustCompile(`^/etc/php([0-9]+)/php-fpm\.d$`),
		func(v string) string { return "php-fpm" + v + ".service" }, dotted},
}

// UnitForPoolDir derives the FPM service name and PHP version from a pool directory (host path). ok is false for
// unknown layouts.
func UnitForPoolDir(dir string) (unit, phpVersion string, ok bool) {
	dir = filepath.Clean(dir)
	for _, d := range poolDirs {
		if m := d.re.FindStringSubmatch(dir); m != nil {
			return d.unit(m[1]), d.ver(m[1]), true
		}
	}
	return "", "", false
}

// DiscoverPools reads the pool files of every known layout below root. Unreadable or oversized files are skipped;
// include directives are not followed (the pool directories of every layout are scanned directly).
func DiscoverPools(root string) []Pool {
	var out []Pool
	seen := map[string]bool{}
	for _, d := range poolDirs {
		dirs, _ := filepath.Glob(filepath.Join(root, d.glob))
		sort.Strings(dirs)
		for _, local := range dirs {
			host := "/" + strings.TrimPrefix(filepath.ToSlash(mustRel(root, local)), "/")
			unit, ver, ok := UnitForPoolDir(host)
			if !ok || seen[host] {
				continue
			}
			seen[host] = true
			files, _ := filepath.Glob(filepath.Join(local, "*.conf"))
			sort.Strings(files)
			for _, f := range files {
				b, err := readRegular(f)
				if err != nil {
					continue
				}
				file := filepath.Join(host, filepath.Base(f))
				for _, p := range ParsePoolFile(b) {
					p.PHPVersion, p.Unit, p.File = ver, unit, file
					out = append(out, p)
					if len(out) >= MaxPools {
						return out
					}
				}
			}
		}
	}
	return out
}

func mustRel(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return r
}

// readRegular reads a regular file (symlinks to regular files are followed: package managers link pool files) of at
// most maxPoolFileBytes without blocking on a FIFO.
func readRegular(p string) ([]byte, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxPoolFileBytes {
		return nil, fmt.Errorf("%s: not a regular file of at most %d bytes", p, maxPoolFileBytes)
	}
	return io.ReadAll(io.LimitReader(f, maxPoolFileBytes))
}

// ParsePoolFile returns the pools of a PHP-FPM ini file: every [section] except [global] with its `user` value.
// `;` and `#` start comments, values may be quoted, `$pool` expands to the section name. Pools without a user (they
// run as the master's user) and values with other variables (${ENV}) are skipped.
func ParsePoolFile(b []byte) []Pool {
	var out []Pool
	section, user := "", ""
	flush := func() {
		if section != "" && !strings.EqualFold(section, "global") && user != "" {
			out = append(out, Pool{Name: section, User: user})
		}
		user = ""
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), maxPoolFileBytes)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == ';' || line[0] == '#' {
			continue
		}
		if line[0] == '[' {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				continue
			}
			flush()
			section = strings.TrimSpace(line[1:end])
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != "user" {
			continue
		}
		user = poolValue(v, section)
	}
	flush()
	return out
}

func poolValue(v, section string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
		if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
			v = v[1 : end+1]
		}
	} else if i := strings.IndexByte(v, ';'); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	v = strings.ReplaceAll(v, "$pool", section)
	if strings.Contains(v, "$") || !validName(v) {
		return ""
	}
	return v
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}$`)

// validName accepts POSIX-ish user names (also Hestia's capitalized ones); anything else is never passed to usermod.
func validName(s string) bool { return nameRE.MatchString(s) }

// Accounts is a parsed /etc/passwd and /etc/group.
type Accounts struct {
	users  map[string][2]int // name -> uid, gid
	groups map[string]group
}

type group struct {
	gid     int
	members []string
}

// ReadAccounts parses <root>/etc/passwd and <root>/etc/group (missing files yield empty maps).
func ReadAccounts(root string) *Accounts {
	a := &Accounts{users: map[string][2]int{}, groups: map[string]group{}}
	if b, err := os.ReadFile(filepath.Join(root, "etc/passwd")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Split(line, ":")
			if len(f) < 4 || f[0] == "" || strings.HasPrefix(f[0], "#") {
				continue
			}
			uid, e1 := strconv.Atoi(f[2])
			gid, e2 := strconv.Atoi(f[3])
			if e1 == nil && e2 == nil {
				if _, dup := a.users[f[0]]; !dup {
					a.users[f[0]] = [2]int{uid, gid}
				}
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, "etc/group")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Split(line, ":")
			if len(f) < 4 || f[0] == "" || strings.HasPrefix(f[0], "#") {
				continue
			}
			gid, err := strconv.Atoi(f[2])
			if err != nil {
				continue
			}
			if _, dup := a.groups[f[0]]; dup {
				continue
			}
			g := group{gid: gid}
			for _, m := range strings.Split(f[3], ",") {
				if m = strings.TrimSpace(m); m != "" {
					g.members = append(g.members, m)
				}
			}
			a.groups[f[0]] = g
		}
	}
	return a
}

// User returns uid and primary gid.
func (a *Accounts) User(name string) (uid, gid int, ok bool) {
	u, ok := a.users[name]
	return u[0], u[1], ok
}

// GroupExists reports whether the group exists.
func (a *Accounts) GroupExists(name string) bool {
	_, ok := a.groups[name]
	return ok
}

// InGroup reports whether user is a listed member of group or has it as primary group.
func (a *Accounts) InGroup(user, groupName string) bool {
	g, ok := a.groups[groupName]
	if !ok {
		return false
	}
	if slices.Contains(g.members, user) {
		return true
	}
	_, gid, ok := a.User(user)
	return ok && gid == g.gid
}

// Grantable reports whether reconcile may add user to the group: an existing, non-root local account.
func (a *Accounts) Grantable(user string) bool {
	uid, _, ok := a.User(user)
	return ok && uid != 0 && user != "root" && validName(user)
}

// WebServerUsers are the accounts Apache (mod_php) and nginx run as on the supported distributions.
var WebServerUsers = []string{"www-data", "apache", "nginx"}

// webServerUnits and webServerComms detect an installed or running Apache/nginx.
var (
	webServerUnits = []string{"apache2.service", "httpd.service", "nginx.service"}
	webServerComms = []string{"apache2", "httpd", "nginx"}
	unitDirs       = []string{"/etc/systemd/system", "/usr/lib/systemd/system", "/lib/systemd/system"}
)

// UnitInstalled reports whether a systemd unit file for unit exists below root.
func UnitInstalled(root, unit string) bool {
	for _, d := range unitDirs {
		if _, err := os.Stat(filepath.Join(root, d, unit)); err == nil {
			return true
		}
	}
	return false
}

// WebServerPresent reports whether an Apache or nginx unit file or process exists below root.
func WebServerPresent(root string) bool {
	for _, u := range webServerUnits {
		if UnitInstalled(root, u) {
			return true
		}
	}
	entries, _ := os.ReadDir(filepath.Join(root, "proc"))
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, "proc", e.Name(), "comm"))
		if err == nil && slices.Contains(webServerComms, strings.TrimSpace(string(b))) {
			return true
		}
	}
	return false
}

// Candidates returns the users reconcile grants access: the pool users plus the web server users when a web server is
// present. Order is stable (pool order, then WebServerUsers), duplicates removed. Accounts that are not Grantable and
// exclude (the agent user) are dropped.
func Candidates(pools []Pool, webServer bool, acc *Accounts, exclude ...string) []string {
	var out []string
	add := func(u string) {
		if u == "" || slices.Contains(out, u) || slices.Contains(exclude, u) || !acc.Grantable(u) {
			return
		}
		out = append(out, u)
	}
	for _, p := range pools {
		add(p.User)
	}
	if webServer {
		for _, u := range WebServerUsers {
			add(u)
		}
	}
	return out
}

// Report is php_access of the agent sync request (releases-updates.md §3, php-agent.md §1).
type Report struct {
	// SocketGroup is the group applied to php.sock ("" when the forwarder is not running or no group applied).
	SocketGroup string `json:"socket_group"`
	// Group is Group; GroupExists whether it exists; AgentMember whether the agent user is in it.
	Group       string `json:"group"`
	GroupExists bool   `json:"group_exists"`
	AgentMember bool   `json:"agent_member"`
	// Grants is "auto" or "opted_out" (config php_forwarder.grant_pool_users: false or the no-php-access file).
	Grants string       `json:"grants"`
	Pools  []PoolAccess `json:"pools"`
}

// PoolAccess is one pool in Report.
type PoolAccess struct {
	Pool       string `json:"pool"`
	PHPVersion string `json:"php_version"`
	User       string `json:"user"`
	Unit       string `json:"unit"`
	Access     string `json:"access"`
}

// Grant values of Report.
const (
	GrantsAuto     = "auto"
	GrantsOptedOut = "opted_out"
)

// ReportInput configures BuildReport.
type ReportInput struct {
	Root        string
	AgentUser   string
	SocketGroup string // applied group, "" if none
	SocketMode  os.FileMode
	OptedOut    bool
}

// BuildReport evaluates the pools of the host for the unprivileged agent. It returns nil when the host has no PHP-FPM
// pools (nothing to report).
func BuildReport(in ReportInput) *Report {
	pools := DiscoverPools(in.Root)
	if len(pools) == 0 {
		return nil
	}
	acc := ReadAccounts(in.Root)
	r := &Report{SocketGroup: in.SocketGroup, Group: Group, GroupExists: acc.GroupExists(Group),
		AgentMember: acc.InGroup(in.AgentUser, Group), Grants: GrantsAuto, Pools: []PoolAccess{}}
	if in.OptedOut {
		r.Grants = GrantsOptedOut
	}
	target := in.SocketGroup
	if target == "" {
		target = Group
	}
	for _, p := range pools {
		pa := PoolAccess{Pool: p.Name, PHPVersion: p.PHPVersion, User: p.User, Unit: p.Unit}
		switch {
		case in.SocketMode&0o002 != 0:
			pa.Access = AccessOK // 0666: any local user may send
		case p.User == target || acc.InGroup(p.User, target):
			pa.Access = AccessOK
		case !acc.Grantable(p.User):
			pa.Access = AccessNoUser
		case in.OptedOut:
			pa.Access = AccessOptedOut
		default:
			pa.Access = AccessMissing
		}
		r.Pools = append(r.Pools, pa)
	}
	return r
}

// Missing returns the pools of r whose access is AccessMissing.
func (r *Report) Missing() []PoolAccess {
	if r == nil {
		return nil
	}
	var out []PoolAccess
	for _, p := range r.Pools {
		if p.Access == AccessMissing {
			out = append(out, p)
		}
	}
	return out
}
