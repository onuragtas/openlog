package phpagent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/update"
	lib "github.com/onuragtas/openlog/libs/release"
)

// ApplyOptions configures Apply.
type ApplyOptions struct {
	Sys *update.Sys
	// StateDir is the agent's state_dir (untrusted input).
	StateDir string
	// StatusDir is the infra agent's root-owned install root (holds StatusFile).
	StatusDir string
	// Config is php_agent of the configuration file only root can change (defaults otherwise).
	Config  config.PHPAgentConfig
	Trusted []ed25519.PublicKey
	OS      string
	Arch    string
	Run     Runner
	Now     func() time.Time
	Log     *slog.Logger
}

var requestID = regexp.MustCompile(`^[0-9a-f]{16,64}$`)

// Apply is the PHP agent part of the privileged pre-start step (root, D-082). It handles the agent's request once:
// install (verify the staged release again, copy and check it root-owned, extract into versions/<v>, switch current,
// openlog-php-install install, back to the previous version or uninstall when openlog.so does not load), rollback
// (after a failed health check: previous version or uninstall) or uninstall. It never touches an install root
// without the fleet marker. It returns the written status, or nil when there was nothing to do.
func Apply(ctx context.Context, o ApplyOptions) *Status {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.OS == "" {
		o.OS = runtime.GOOS
	}
	if o.Arch == "" {
		o.Arch = runtime.GOARCH
	}
	if o.Run == nil {
		o.Run = ExecRunner
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	log := o.Log.With("component", "php_agent_apply")
	if o.Sys == nil || !o.Sys.IsRoot() || o.StatusDir == "" || o.Config.InstallRoot == "" {
		return nil
	}
	req, err := loadRequest(o.StateDir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Warn("PHP agent request unreadable; ignored", "error", err)
		}
		return nil
	}
	statusPath := filepath.Join(o.StatusDir, StatusFile)
	var prev Status
	if err := o.Sys.LoadTrustedJSON(statusPath, &prev); err != nil {
		prev = Status{}
	}
	if !requestID.MatchString(req.ID) || req.ID == prev.RequestID {
		return nil
	}
	a := &applier{o: o, log: log, root: filepath.Clean(o.Config.InstallRoot), prev: prev}
	st := &Status{RequestID: req.ID, At: o.Now().UTC(), Action: update.Truncate(req.Action, 32),
		Target: update.Truncate(req.Version, 64), Previous: prev.Previous}
	a.st = st
	st.Version = InstalledVersion(a.root)
	a.handle(ctx, req)
	if err := update.WriteFileAtomic(statusPath, mustJSON(st), 0o644); err != nil {
		log.Error("PHP agent status not saved", "error", err)
	}
	level := slog.LevelInfo
	if st.Result != ResultApplied && st.Result != ResultUninstalled {
		level = slog.LevelError
	}
	log.Log(ctx, level, "PHP agent request handled", "action", st.Action, "result", st.Result, "target", st.Target,
		"version", st.Version, "previous", st.Previous, "error", st.Error)
	return st
}

type applier struct {
	o    ApplyOptions
	log  *slog.Logger
	root string
	prev Status
	st   *Status
}

func loadRequest(stateDir string) (Request, error) {
	var req Request
	r, err := os.OpenRoot(stateDir)
	if err != nil {
		return req, err
	}
	defer r.Close()
	b, err := update.ReadRootFile(r, path.Join(StateSubdir, RequestFile), maxRequestBytes)
	if err != nil {
		return req, err
	}
	if err := json.Unmarshal(b, &req); err != nil {
		return req, fmt.Errorf("request: %w", err)
	}
	return req, nil
}

func mustJSON(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return append(b, '\n')
}

func (a *applier) reject(format string, args ...any) {
	a.st.Result, a.st.Error = ResultRejected, update.Truncate(fmt.Sprintf(format, args...), 1024)
}

// ownership: exists reports whether the install root exists; fleet whether it carries the root-owned marker.
func (a *applier) ownership() (exists, fleet bool) {
	fi, err := os.Lstat(a.root)
	if errors.Is(err, fs.ErrNotExist) {
		return false, false
	}
	if err != nil || !fi.IsDir() {
		return true, false
	}
	return true, a.o.Sys.TrustedFile(filepath.Join(a.root, MarkerFile)) == nil
}

func (a *applier) handle(ctx context.Context, req Request) {
	if req.Reload != "" && req.Reload != config.PHPAgentReloadNone && req.Reload != config.PHPAgentReloadGraceful {
		a.reject("invalid reload %q", update.Truncate(req.Reload, 32))
		return
	}
	if err := config.ValidatePHPExcludeBins(req.ExcludeBins); err != nil {
		a.reject("exclude_bins: %v", err)
		return
	}
	exists, fleet := a.ownership()
	if exists && !fleet {
		a.reject("%s is not managed by the infra agent (package or manual installation): left alone", a.root)
		return
	}
	switch req.Action {
	case ActionInstall:
		a.st.Operation = OpInstall
		if a.st.Version != "" {
			a.st.Operation = OpUpgrade
		}
		if a.o.Config.Mode == config.PHPAgentModeOff && !a.o.Config.RemoteConfig {
			a.reject("php_agent.mode is off and remote_config is false in the configuration")
			return
		}
		a.install(ctx, req)
	case ActionRollback:
		a.st.Operation = OpRollback
		if !exists {
			a.reject("nothing to roll back: the PHP agent is not installed")
			return
		}
		a.rollback(ctx, req)
	case ActionUninstall:
		a.st.Operation = OpUninstall
		if !exists {
			a.st.Result, a.st.Version = ResultUninstalled, ""
			return
		}
		a.disable(ctx, a.st.Version, req)
		if err := os.RemoveAll(a.root); err != nil {
			a.st.Result, a.st.Error = ResultFailed, update.Truncate(err.Error(), 1024)
			return
		}
		a.st.Result, a.st.Version, a.st.Previous = ResultUninstalled, "", ""
	default:
		a.reject("unknown action %q", update.Truncate(req.Action, 32))
	}
}

func (a *applier) install(ctx context.Context, req Request) {
	o := a.o
	ver := req.Version
	if !validVersion(ver) {
		a.reject("invalid version %q", update.Truncate(ver, 64))
		return
	}
	sr, err := os.OpenRoot(o.StateDir)
	if err != nil {
		a.reject("staged release: %v", err)
		return
	}
	defer sr.Close()
	rel := path.Join(StateSubdir, StagedDir, ver)
	manifest, err := update.ReadRootFile(sr, path.Join(rel, ManifestFile), maxManifestBytes)
	if err != nil {
		a.reject("staged manifest: %v", err)
		return
	}
	sig, err := update.ReadRootFile(sr, path.Join(rel, SignatureFile), maxSignatureBytes)
	if err != nil {
		a.reject("staged signature: %v", err)
		return
	}
	cur := a.st.Version
	v, err := Verify(manifest, sig, o.Trusted, ver, o.OS, o.Arch, func() (*lib.Manifest, error) { return a.versionManifest(cur) })
	if err != nil {
		a.reject("%v", err)
		return
	}

	versions := filepath.Join(a.root, "versions")
	for _, d := range []string{a.root, versions} {
		if err := o.Sys.FixDir(d, 0o755); err != nil {
			a.reject("install root: %v", err)
			return
		}
	}
	marker := filepath.Join(a.root, MarkerFile)
	if err := update.WriteFileAtomic(marker, []byte("installed by openlog-infra-agent (php-agent.md §7.3)\n"), 0o644); err != nil {
		a.reject("marker: %v", err)
		return
	}
	if err := o.Sys.Lchown(marker, o.Sys.RootUID, o.Sys.RootGID); err != nil {
		a.reject("marker: %v", err)
		return
	}
	if ver != cur {
		if err := a.extract(sr, rel, v, manifest, sig); err != nil {
			a.reject("%v", err)
			return
		}
		a.st.Previous = cur
	}
	if err := switchCurrent(a.root, ver); err != nil {
		a.st.Result, a.st.Error = ResultFailed, update.Truncate(err.Error(), 1024)
		return
	}
	a.st.Version = ver
	ok, detail := a.enable(ctx, ver, req)
	if ok {
		if removed := prune(versions, ver, a.st.Previous); len(removed) > 0 {
			a.log.Info("pruned old PHP agent versions", "removed", removed)
		}
		a.st.Result = ResultApplied
		return
	}
	a.st.Error = update.Truncate(detail, 1024)
	if p := a.st.Previous; p != "" && p != ver && a.trustedVersion(p) {
		if err := switchCurrent(a.root, p); err == nil {
			if ok, d := a.enable(ctx, p, req); !ok {
				a.st.Error = update.Truncate(detail+"; previous version "+p+": "+d, 1024)
			}
			os.RemoveAll(filepath.Join(versions, ver))
			a.st.Result, a.st.Version, a.st.Previous = ResultRolledBack, p, a.prev.Previous
			return
		}
	}
	a.disable(ctx, ver, req)
	os.RemoveAll(a.root)
	a.st.Result, a.st.Version, a.st.Previous = ResultFailed, "", ""
}

// extract copies the staged archive into a root-owned file (size and sha256 checked on the copy), extracts it and
// renames the verified tree to versions/<v>.
func (a *applier) extract(sr *os.Root, rel string, v *Verified, manifest, sig []byte) error {
	ver := v.Version.String()
	versions := filepath.Join(a.root, "versions")
	partial := filepath.Join(versions, "."+ver+".partial")
	tree := filepath.Join(versions, "."+ver+".extract")
	os.Remove(partial)
	defer os.Remove(partial)
	os.RemoveAll(tree)
	if err := update.CopyVerified(sr, path.Join(rel, ArchiveFile), partial, v.Artifact.Size, v.Artifact.SHA256); err != nil {
		return err
	}
	if err := update.ExtractTarGz(partial, tree, TopDir(ver, a.o.OS, a.o.Arch), update.MaxExtractBytes); err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(tree)
		}
	}()
	inst := filepath.Join(tree, InstallerRel)
	if fi, err := os.Lstat(inst); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("release %s has no regular file %s", ver, InstallerRel)
	}
	if err := os.Chmod(inst, 0o755); err != nil {
		return err
	}
	for name, data := range map[string][]byte{ManifestFile: manifest, SignatureFile: sig} {
		if err := update.WriteFileAtomic(filepath.Join(tree, name), data, 0o644); err != nil {
			return err
		}
	}
	if !a.o.Sys.TrustedTree(tree) {
		return fmt.Errorf("extracted release %s is not a root-owned tree", ver)
	}
	final := filepath.Join(versions, ver)
	if err := os.RemoveAll(final); err != nil {
		return err
	}
	if err := os.Rename(tree, final); err != nil {
		return err
	}
	ok = true
	update.SyncDir(versions)
	return nil
}

func (a *applier) rollback(ctx context.Context, req Request) {
	cur := a.st.Version
	if req.Version != cur {
		a.reject("rollback of %s requested, but %s is installed", update.Truncate(req.Version, 64), cur)
		return
	}
	versions := filepath.Join(a.root, "versions")
	if p := a.prev.Previous; p != "" && p != cur && a.trustedVersion(p) {
		if err := switchCurrent(a.root, p); err == nil {
			if ok, d := a.enable(ctx, p, req); !ok {
				a.st.Error = update.Truncate("previous version "+p+": "+d, 1024)
			}
			os.RemoveAll(filepath.Join(versions, cur))
			a.st.Result, a.st.Version, a.st.Previous = ResultRolledBack, p, ""
			return
		}
	}
	a.disable(ctx, cur, req)
	if err := os.RemoveAll(a.root); err != nil {
		a.st.Result, a.st.Error = ResultFailed, update.Truncate(err.Error(), 1024)
		return
	}
	a.st.Result, a.st.Version, a.st.Previous = ResultRolledBack, "", ""
}

// trustedVersion reports whether versions/<v> is a root-owned release with its installer.
func (a *applier) trustedVersion(v string) bool {
	if !validVersion(v) {
		return false
	}
	dir := filepath.Join(a.root, "versions", v)
	fi, err := os.Lstat(filepath.Join(dir, InstallerRel))
	return err == nil && fi.Mode().IsRegular() && a.o.Sys.TrustedTree(dir)
}

// versionManifest reads the manifest kept in a trusted version directory (nil when v is "").
func (a *applier) versionManifest(v string) (*lib.Manifest, error) {
	if v == "" {
		return nil, nil
	}
	if !a.trustedVersion(v) {
		return nil, fmt.Errorf("installed version %s is not a root-owned release", v)
	}
	b, err := update.ReadLimited(filepath.Join(a.root, "versions", v, ManifestFile), maxManifestBytes)
	if err != nil {
		return nil, err
	}
	return lib.ParseManifest(b)
}

// enable runs `openlog-php-install install` of version v and checks the result with `status --json`: every enabled
// runtime must load openlog.so and at least one must (the installer removes the ini file of a runtime it cannot load).
func (a *applier) enable(ctx context.Context, v string, req Request) (bool, string) {
	inst := filepath.Join(a.root, "versions", v, InstallerRel)
	args := []string{"install"}
	if len(req.ExcludeBins) > 0 {
		rts, err := InstallerStatus(ctx, a.o.Run, inst)
		if err != nil {
			return false, err.Error()
		}
		MarkExcluded(rts, req.ExcludeBins)
		for _, r := range rts {
			if !r.Excluded && r.Supported && a.o.Sys.TrustedFile(r.Bin) == nil {
				args = append(args, "--php", r.Bin)
			}
		}
		if len(args) == 1 {
			return false, "no supported PHP runtime left after exclude_bins"
		}
	}
	if req.Reload == config.PHPAgentReloadGraceful {
		a.st.Units = runningUnits(ctx, a.o.Run)
		args = append(args, "--reload")
	}
	out, installErr := a.o.Run(ctx, inst, args...)
	rts, err := InstallerStatus(ctx, a.o.Run, inst)
	if err != nil {
		return false, err.Error()
	}
	MarkExcluded(rts, req.ExcludeBins)
	var bad []string
	a.st.Runtimes = nil
	for _, r := range rts {
		switch {
		case r.Excluded:
		case r.Enabled && !r.Loaded:
			bad = append(bad, r.Bin)
		case r.Enabled && r.Loaded:
			a.st.Runtimes = append(a.st.Runtimes, r.Bin)
		}
	}
	switch {
	case len(bad) > 0:
		return false, "openlog.so enabled but not loaded by " + strings.Join(bad, ", ")
	case len(a.st.Runtimes) == 0:
		msg := "openlog.so loads into no PHP runtime"
		if installErr != nil {
			msg += ": " + installErr.Error()
		} else if tail := lastLines(out, 3); tail != "" {
			msg += ": " + tail
		}
		return false, msg
	}
	return true, ""
}

func (a *applier) disable(ctx context.Context, v string, req Request) {
	if v == "" {
		return
	}
	inst := filepath.Join(a.root, "versions", v, InstallerRel)
	if fi, err := os.Lstat(inst); err != nil || !fi.Mode().IsRegular() {
		return
	}
	args := []string{"uninstall"}
	if req.Reload == config.PHPAgentReloadGraceful {
		args = append(args, "--reload")
	}
	if _, err := a.o.Run(ctx, inst, args...); err != nil {
		a.log.Warn("openlog-php-install uninstall failed", "error", err)
	}
}

// runningUnits lists the running PHP-FPM and Apache units the installer reloads.
func runningUnits(ctx context.Context, run Runner) []string {
	out, err := run(ctx, "systemctl", "list-units", "--type=service", "--state=running", "--no-legend", "--plain", "php*fpm*", "httpd*", "apache2*")
	if err != nil {
		return nil
	}
	var units []string
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) > 0 && strings.HasSuffix(f[0], ".service") {
			units = append(units, f[0])
		}
	}
	return units
}

func lastLines(b []byte, n int) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}

// switchCurrent atomically points <root>/current at versions/<v> (temp symlink + rename).
func switchCurrent(root, v string) error {
	if _, err := os.Stat(filepath.Join(root, "versions", v, InstallerRel)); err != nil {
		return fmt.Errorf("switch to %s: %w", v, err)
	}
	tmp := filepath.Join(root, fmt.Sprintf(".current.%d.tmp", os.Getpid()))
	os.Remove(tmp)
	if err := os.Symlink(filepath.Join("versions", v), tmp); err != nil {
		return fmt.Errorf("switch to %s: %w", v, err)
	}
	if err := os.Rename(tmp, filepath.Join(root, "current")); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("switch to %s: %w", v, err)
	}
	update.SyncDir(root)
	return nil
}

// prune removes version directories except keep.
func prune(versions string, keep ...string) []string {
	entries, err := os.ReadDir(versions)
	if err != nil {
		return nil
	}
	var removed []string
	for _, e := range entries {
		if slices.Contains(keep, e.Name()) {
			continue
		}
		if os.RemoveAll(filepath.Join(versions, e.Name())) == nil {
			removed = append(removed, e.Name())
		}
	}
	return removed
}

// LoadStatus reads the privileged step's status for the unprivileged agent.
func LoadStatus(statusDir string) (*Status, error) {
	b, err := update.ReadLimited(filepath.Join(statusDir, StatusFile), maxStatusBytes)
	if err != nil {
		return nil, err
	}
	var st Status
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}
