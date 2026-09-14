package phpagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Runner executes a command and returns its standard output (tests substitute a fake).
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner runs commands with a 90 s limit; the error carries the end of stderr.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 512 {
			msg = "…" + msg[len(msg)-512:]
		}
		return out, fmt.Errorf("%s %s: %w: %s", filepath.Base(name), strings.Join(args, " "), err, msg)
	}
	return out, nil
}

// candidateGlobs are the PHP binaries of openlog-php-install's discovery (php-agent.md §7.2).
var candidateGlobs = []string{
	"/usr/bin/php", "/usr/bin/php[0-9]*", "/usr/bin/php-cgi", "/usr/bin/php-cgi[0-9]*", "/usr/sbin/php-fpm", "/usr/sbin/php-fpm[0-9]*",
	"/usr/local/bin/php", "/usr/local/bin/php-cgi", "/usr/local/sbin/php-fpm",
	"/opt/remi/php*/root/usr/bin/php", "/opt/remi/php*/root/usr/bin/php-cgi", "/opt/remi/php*/root/usr/sbin/php-fpm",
	"/opt/php*/bin/php", "/opt/php*/bin/php-cgi", "/opt/php*/sbin/php-fpm",
}

// binName accepts php, php8.2, php82, php-cgi8.2, php-fpm8.2 (not phpize, php-config, phpdbg).
var binName = regexp.MustCompile(`^php(-cgi|-fpm)?[0-9.]*$`)

// SupportedAPIs maps ZEND_MODULE_API_NO of the PHP versions a release builds modules for (PHP 7.1–8.4).
var SupportedAPIs = map[string]string{
	"20160303": "7.1", "20170718": "7.2", "20180731": "7.3", "20190902": "7.4",
	"20200930": "8.0", "20210902": "8.1", "20220829": "8.2", "20230831": "8.3", "20240924": "8.4",
}

const iniMarker = "; managed by openlog-php-install"

var (
	htmlCell = regexp.MustCompile(`</td><td class="v">`)
	htmlTag  = regexp.MustCompile(`<[^>]*>`)
)

// ParsePHPInfo reduces `php -i` output (text for CLI/FPM, an HTML table for CGI) to key → value.
func ParsePHPInfo(out []byte) map[string]string {
	kv := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := htmlCell.ReplaceAllString(sc.Text(), "=> ")
		line = strings.ReplaceAll(htmlTag.ReplaceAllString(line, ""), "&quot;", `"`)
		k, v, ok := strings.Cut(line, "=>")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if i := strings.Index(v, " =>"); i >= 0 { // "local => master" columns of ini settings
			v = strings.TrimSpace(v[:i])
		}
		if _, dup := kv[k]; !dup && k != "" {
			kv[k] = v
		}
	}
	return kv
}

// Libc is the host's C library (musl on Alpine).
func Libc() string {
	if _, err := os.Stat("/etc/alpine-release"); err == nil {
		return "musl"
	}
	if m, _ := filepath.Glob("/lib/ld-musl-*"); len(m) > 0 {
		return "musl"
	}
	return "glibc"
}

// Candidates returns the existing PHP binaries of the installer's locations plus extra (e.g. discovered php-fpm
// executables), de-duplicated by real path and sorted.
func Candidates(extra []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if !binName.MatchString(filepath.Base(p)) {
			return
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			return
		}
		fi, err := os.Stat(real)
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 || seen[real] {
			return
		}
		seen[real] = true
		out = append(out, real)
	}
	for _, g := range candidateGlobs {
		matches, _ := filepath.Glob(g)
		for _, m := range matches {
			add(m)
		}
	}
	for _, p := range extra {
		if filepath.IsAbs(p) {
			add(p)
		}
	}
	sort.Strings(out)
	return out
}

// DetectRuntimes inspects PHP binaries itself (used before the PHP agent is installed): `<bin> -i` for the ABI and
// scan directory, `<bin> -m` for loaded, the managed ini file for enabled. supported means a module is built for the
// ABI (PHP 7.1–8.4, no debug build).
func DetectRuntimes(ctx context.Context, run Runner, bins []string) []Runtime {
	libc := Libc()
	out := []Runtime{}
	for _, bin := range bins {
		info, err := run(ctx, bin, "-i")
		if err != nil && len(info) == 0 {
			continue
		}
		kv := ParsePHPInfo(info)
		api := kv["PHP Extension"]
		if api == "" {
			continue
		}
		r := Runtime{Bin: bin, Version: kv["PHP Version"], API: api, ZTS: kv["Thread Safety"] == "enabled",
			Debug: kv["Debug Build"] == "yes", Libc: libc, ScanDir: kv["Scan this dir for additional .ini files"]}
		if r.ScanDir == "(none)" {
			r.ScanDir = ""
		}
		zts := "nts"
		if r.ZTS {
			zts = "zts"
		}
		r.Module = api + "-" + zts + "-" + libc
		_, known := SupportedAPIs[api]
		r.Supported = known && !r.Debug
		r.Enabled = managedIni(iniPath(r.ScanDir))
		if mods, err := run(ctx, bin, "-m"); err == nil {
			r.Loaded = hasLine(mods, "openlog")
		}
		out = append(out, r)
	}
	return out
}

// iniPath is the managed ini file of a scan directory (Debian: mods-available/openlog.ini).
func iniPath(scan string) string {
	if scan == "" {
		return ""
	}
	if rest, ok := strings.CutPrefix(scan, "/etc/php/"); ok {
		if v, _, ok := strings.Cut(rest, "/"); ok && strings.HasSuffix(scan, "/conf.d") {
			if fi, err := os.Stat("/etc/php/" + v + "/mods-available"); err == nil && fi.IsDir() {
				return "/etc/php/" + v + "/mods-available/openlog.ini"
			}
		}
	}
	return filepath.Join(scan, "90-openlog.ini")
}

func managedIni(path string) bool {
	if path == "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	line, _ := bufio.NewReader(f).ReadString('\n')
	return strings.Contains(line, iniMarker)
}

func hasLine(out []byte, want string) bool {
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == want {
			return true
		}
	}
	return false
}

// InstallerStatus runs `<installer> status --json` and parses one runtime per line.
func InstallerStatus(ctx context.Context, run Runner, installer string, extraArgs ...string) ([]Runtime, error) {
	out, err := run(ctx, installer, append([]string{"status", "--json"}, extraArgs...)...)
	if err != nil {
		return nil, err
	}
	rts := []Runtime{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var r Runtime
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("installer status: %w", err)
		}
		rts = append(rts, r)
	}
	return rts, sc.Err()
}

// MarkExcluded sets Excluded on runtimes matching one of the globs.
func MarkExcluded(rts []Runtime, globs []string) {
	for i := range rts {
		rts[i].Excluded = false
		for _, g := range globs {
			if ok, _ := filepath.Match(g, rts[i].Bin); ok {
				rts[i].Excluded = true
				break
			}
		}
	}
}

// InstalledVersion is the version `current` of the install root points at ("" when not installed).
func InstalledVersion(root string) string {
	t, err := os.Readlink(filepath.Join(root, "current"))
	if err != nil {
		return ""
	}
	v := filepath.Base(filepath.Clean(t))
	if !validVersion(v) {
		return ""
	}
	return v
}

// ManagedBy tells who owns the install root: the fleet (marker file), a deb/rpm/apk package, something else
// (manual) or nobody (none).
func ManagedBy(ctx context.Context, run Runner, root string) string {
	fi, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return ManagedNone
	}
	if err == nil && fi.IsDir() {
		if mfi, err := os.Lstat(filepath.Join(root, MarkerFile)); err == nil && mfi.Mode().IsRegular() {
			return ManagedFleet
		}
		entries, _ := os.ReadDir(root)
		if len(entries) == 0 {
			return ManagedNone
		}
	}
	for _, q := range [][]string{{"dpkg-query", "-S", root}, {"dpkg", "-S", root}, {"rpm", "-qf", root}, {"apk", "info", "-W", root}} {
		if _, err := exec.LookPath(q[0]); err != nil {
			continue
		}
		if out, err := run(ctx, q[0], q[1:]...); err == nil && len(bytes.TrimSpace(out)) > 0 && !bytes.Contains(out, []byte("not owned")) {
			return ManagedPackage
		}
	}
	return ManagedManual
}
