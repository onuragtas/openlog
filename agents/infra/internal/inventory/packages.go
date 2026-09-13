package inventory

import (
	"context"
	"os/exec"
	"sort"
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
	"github.com/onuragtas/openlog/agents/infra/internal/rpmdb"
)

// Package is the body of a "package" item.
type Package struct {
	Manager string `json:"manager"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Arch    string `json:"arch"`
}

// Key returns <manager>:<name>.
func (p Package) Key() string { return p.Manager + ":" + p.Name }

// Package database locations, also used for change fingerprints.
const (
	DpkgStatusPath   = "/var/lib/dpkg/status"
	ApkInstalledPath = "/lib/apk/db/installed"
	RpmDBDir         = "/var/lib/rpm"
)

// RpmSQLitePaths are the rpmdb.sqlite locations in lookup order (Fedora 36+
// moved the database to /usr/lib/sysimage/rpm and keeps /var/lib/rpm as a link).
var RpmSQLitePaths = []string{"/var/lib/rpm/rpmdb.sqlite", "/usr/lib/sysimage/rpm/rpmdb.sqlite"}

// RpmFingerprintPaths are stamped for change detection.
var RpmFingerprintPaths = []string{
	RpmDBDir, "/var/lib/rpm/rpmdb.sqlite", "/var/lib/rpm/rpmdb.sqlite-wal", "/var/lib/rpm/Packages",
	"/usr/lib/sysimage/rpm/rpmdb.sqlite", "/usr/lib/sysimage/rpm/rpmdb.sqlite-wal", "/usr/lib/sysimage/rpm/Packages.db",
}

// ParseDpkgStatus parses /var/lib/dpkg/status and returns installed packages.
func ParseDpkgStatus(data []byte) []Package {
	var out []Package
	for _, para := range paragraphs(string(data)) {
		fields := map[string]string{}
		for line := range strings.Lines(para) {
			if line == "" || line[0] == ' ' || line[0] == '\t' {
				continue // continuation line
			}
			k, v, ok := strings.Cut(line, ":")
			if ok {
				fields[k] = strings.TrimSpace(v)
			}
		}
		st := strings.Fields(fields["Status"])
		if fields["Package"] == "" || len(st) < 3 || st[2] != "installed" {
			continue
		}
		out = append(out, Package{Manager: "dpkg", Name: fields["Package"], Version: fields["Version"], Arch: fields["Architecture"]})
	}
	return out
}

// ParseApkInstalled parses the Alpine /lib/apk/db/installed database.
func ParseApkInstalled(data []byte) []Package {
	var out []Package
	for _, para := range paragraphs(string(data)) {
		p := Package{Manager: "apk"}
		for line := range strings.Lines(para) {
			line = strings.TrimRight(line, "\n")
			if len(line) < 2 || line[1] != ':' {
				continue
			}
			switch line[0] {
			case 'P':
				p.Name = line[2:]
			case 'V':
				p.Version = line[2:]
			case 'A':
				p.Arch = line[2:]
			}
		}
		if p.Name != "" {
			out = append(out, p)
		}
	}
	return out
}

// collectRPM reads rpmdb.sqlite directly (RHEL/Rocky/Alma 9+, Fedora 33+).
// Older Berkeley DB (RHEL 7/8) and ndb (SUSE) databases are read by running
// "rpm -qa" when an rpm binary is available to the agent (with --root when
// host.root_path is not "/"); otherwise they are skipped.
func collectRPM(fs *hostfs.FS) []Package {
	var pkgs []rpmdb.Package
	found := false
	for _, p := range RpmSQLitePaths {
		if _, err := fs.Stat(p); err != nil {
			continue
		}
		found = true
		var err error
		if pkgs, err = rpmdb.ReadSQLite(fs.Path(p)); err == nil {
			break
		}
		pkgs = nil
	}
	if len(pkgs) == 0 {
		legacy := false
		for _, p := range []string{"/var/lib/rpm/Packages", "/usr/lib/sysimage/rpm/Packages.db"} {
			if _, err := fs.Stat(p); err == nil {
				legacy = true
			}
		}
		if legacy || found {
			if rpmPath, err := exec.LookPath("rpm"); err == nil {
				pkgs, _ = rpmdb.ReadCLI(context.Background(), rpmPath, fs.Root())
			}
		}
	}
	out := make([]Package, 0, len(pkgs))
	for _, p := range pkgs {
		out = append(out, Package{Manager: "rpm", Name: p.Name, Version: p.Version, Arch: p.Arch})
	}
	return out
}

func collectPackages(fs *hostfs.FS) []Package {
	var out []Package
	if b, err := fs.ReadFile(DpkgStatusPath); err == nil {
		out = append(out, ParseDpkgStatus(b)...)
	}
	if b, err := fs.ReadFile(ApkInstalledPath); err == nil {
		out = append(out, ParseApkInstalled(b)...)
	}
	out = append(out, collectRPM(fs)...)
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

func paragraphs(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Split(s, "\n\n")
}
