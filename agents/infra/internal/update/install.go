package update

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/inventory"
	"github.com/onuragtas/openlog/agents/infra/internal/rpmdb"
)

// ContainerEnv forces container detection on ("1") or off ("0", e.g. for a tarball install
// inside a systemd-in-container test machine).
const ContainerEnv = "OPENLOG_AGENT_CONTAINER"

// Env is the environment install detection looks at.
type Env struct {
	// Root is prepended to system paths (/.dockerenv, /proc/self/cgroup, package databases).
	// "/" in production; a fixture directory in tests.
	Root string
	// Getenv reads environment variables.
	Getenv func(string) string
	// Executable is the path of the running binary (os.Executable).
	Executable string
	// InstallRoot is update.install_root.
	InstallRoot string
	// UpdatesEnabled is update.enabled.
	UpdatesEnabled bool
	// HaveTrustedKeys reports whether this build has release keys (compiled in or from a file).
	HaveTrustedKeys bool
	// InvocationID is systemd's $INVOCATION_ID of this start ("" outside systemd).
	InvocationID string
	// Privileged is set for "-apply"/"-reconcile": only the method and the layout are detected, and a
	// container is only assumed when OPENLOG_AGENT_CONTAINER=1 (installers run inside containers too).
	Privileged bool
}

// Install is the detected install method.
type Install struct {
	Method string
	// Capable is update_capable: tarball/deb/rpm, updates enabled, trusted keys, writable install root.
	Capable bool
	// Reason explains why Capable is false.
	Reason string
	// InstallRoot is the resolved install root (symlinks evaluated).
	InstallRoot string
	// VersionDir is the name of versions/<dir> the binary runs from ("" when not in the layout).
	VersionDir string
	// PackageVersion is the deb/rpm package version, when packaged.
	PackageVersion string
	// Mode is ModeStaged or ModeLegacy when Capable.
	Mode string
	// Notice is an operator action to report (NoticeUnitOutdated).
	Notice string
	// Apply is the privileged pre-start step's status for this start (nil: it did not run).
	Apply *ApplyStatus
	// Reconcile is the last reconcile status (nil: never ran).
	Reconcile *ReconcileStatus
}

// Detect determines the install method and whether this agent may update itself.
func Detect(env Env) Install {
	if env.Root == "" {
		env.Root = "/"
	}
	if env.Getenv == nil {
		env.Getenv = os.Getenv
	}
	in := Install{InstallRoot: env.InstallRoot}
	if r, err := filepath.EvalSymlinks(env.InstallRoot); err == nil {
		in.InstallRoot = r
	}

	switch {
	case isContainer(env):
		in.Method = MethodContainer
		in.Reason = "running in a container: update the image instead"
		return in
	}

	exe := env.Executable
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	in.VersionDir = versionDirOf(in.InstallRoot, exe)
	if in.VersionDir == "" {
		in.Method = MethodDev
		in.Reason = fmt.Sprintf("binary %s is not under %s", exe, filepath.Join(env.InstallRoot, "versions"))
		return in
	}

	switch {
	case debOwns(env, env.InstallRoot) || (in.InstallRoot != env.InstallRoot && debOwns(env, in.InstallRoot)):
		in.Method = MethodDeb
		in.PackageVersion = debVersion(env)
	default:
		if v, ok := rpmVersion(env); ok {
			in.Method, in.PackageVersion = MethodRPM, v
		} else {
			in.Method = MethodTarball
		}
	}

	if env.Privileged {
		return in
	}
	// Staged mode needs the privileged pre-start step to have run for this very start (same systemd
	// invocation); otherwise the unit predates it and only the legacy binary swap is possible.
	if st, err := LoadApplyStatus(in.InstallRoot); err == nil && env.InvocationID != "" && st.InvocationID == env.InvocationID {
		in.Apply = st
	} else {
		in.Notice = NoticeUnitOutdated
	}
	in.Reconcile, _ = LoadReconcileStatus(in.InstallRoot)

	switch {
	case !env.UpdatesEnabled:
		in.Reason = "updates disabled by configuration (update.enabled=false)"
	case !env.HaveTrustedKeys:
		in.Reason = "no trusted release keys"
	default:
		if fi, err := os.Lstat(filepath.Join(in.InstallRoot, "current")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			in.Reason = "install root has no current symlink"
		} else if in.Apply != nil {
			in.Mode, in.Capable = ModeStaged, true
		} else if err := checkWritable(in.InstallRoot); err != nil {
			in.Reason = "install root not writable: " + err.Error() + " (" + NoticeUnitOutdated + ")"
		} else if err := checkWritable(filepath.Join(in.InstallRoot, "versions")); err != nil {
			in.Reason = "versions directory not writable: " + err.Error() + " (" + NoticeUnitOutdated + ")"
		} else {
			in.Mode, in.Capable = ModeLegacy, true
		}
	}
	return in
}

// versionDirOf returns <dir> when exe is <root>/versions/<dir>/openlog-infra-agent.
func versionDirOf(root, exe string) string {
	rel, err := filepath.Rel(filepath.Join(root, "versions"), exe)
	if err != nil {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) != 2 || parts[1] != BinaryName || parts[0] == ".." || parts[0] == "" || strings.HasPrefix(parts[0], ".") {
		return ""
	}
	return parts[0]
}

func isContainer(env Env) bool {
	switch strings.ToLower(env.Getenv(ContainerEnv)) {
	case "1", "true", "yes":
		return true
	case "0", "false", "no":
		return false
	}
	if env.Privileged {
		return false
	}
	for _, p := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(filepath.Join(env.Root, p)); err == nil {
			return true
		}
	}
	b, err := os.ReadFile(filepath.Join(env.Root, "/proc/self/cgroup"))
	if err != nil {
		return false
	}
	s := string(b)
	for _, marker := range []string{"docker", "kubepods", "containerd", "libpod", "/lxc/"} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// debOwns reports whether dpkg's file list of the package contains the install root.
func debOwns(env Env, installRoot string) bool {
	f, err := os.Open(filepath.Join(env.Root, "/var/lib/dpkg/info", AgentName+".list"))
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line == installRoot || strings.HasPrefix(line, installRoot+"/") {
			return true
		}
	}
	return false
}

func debVersion(env Env) string {
	b, err := os.ReadFile(filepath.Join(env.Root, inventory.DpkgStatusPath))
	if err != nil {
		return ""
	}
	for _, p := range inventory.ParseDpkgStatus(b) {
		if p.Name == AgentName {
			return p.Version
		}
	}
	return ""
}

func rpmVersion(env Env) (string, bool) {
	for _, p := range inventory.RpmSQLitePaths {
		file := filepath.Join(env.Root, p)
		if _, err := os.Stat(file); err != nil {
			continue
		}
		pkgs, err := rpmdb.ReadSQLite(file)
		if err != nil {
			continue
		}
		for _, pkg := range pkgs {
			if pkg.Name == AgentName {
				return pkg.Version, true
			}
		}
		return "", false
	}
	return "", false
}

// packageUpstreamVersion maps a deb/rpm version to the SemVer directory name: the epoch and the
// package revision are dropped and "~" (package pre-release marker) becomes "-".
// "1:0.4.0-1" → "0.4.0", "0.5.0~beta.1-1" → "0.5.0-beta.1", "0.4.0-1.el9" → "0.4.0".
func packageUpstreamVersion(v string) string {
	if i := strings.IndexByte(v, ':'); i >= 0 {
		v = v[i+1:]
	}
	if i := strings.LastIndexByte(v, '-'); i >= 0 {
		v = v[:i]
	}
	return strings.ReplaceAll(v, "~", "-")
}

func checkWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".openlog-write-test-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}
