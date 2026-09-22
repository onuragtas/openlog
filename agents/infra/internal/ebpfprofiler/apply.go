package ebpfprofiler

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
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

var requestID = regexp.MustCompile(`^[0-9a-f]{16,64}$`)

// ApplyOptions configure the privileged step ("-apply" of the next start).
type ApplyOptions struct {
	Sys *update.Sys
	// StateDir is state_dir: everything below it was written by the agent user and is untrusted here.
	StateDir string
	// StatusDir is the infra agent's install root, where the root-owned status file goes.
	StatusDir string
	Config    config.EBPFProfilerConfig
	Trusted   []ed25519.PublicKey
	OS, Arch  string
	Now       func() time.Time
	Log       *slog.Logger
}

// Apply handles one request left by the agent and returns the status it wrote, or nil when there was
// nothing to do.
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
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	log := o.Log.With("component", "ebpf_profiler_apply")
	if o.Sys == nil || !o.Sys.IsRoot() || o.StatusDir == "" || o.Config.InstallRoot == "" {
		return nil
	}
	req, err := loadRequest(o.StateDir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Warn("profiler request unreadable; ignored", "error", err)
		}
		return nil
	}
	statusPath := filepath.Join(o.StatusDir, StatusFile)
	var prev Status
	if err := o.Sys.LoadTrustedJSON(statusPath, &prev); err != nil {
		prev = Status{}
	}
	// A request is handled once: the agent writes a new id for every decision.
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
		log.Error("profiler status not saved", "error", err)
	}
	level := slog.LevelInfo
	if st.Result != ResultApplied && st.Result != ResultUninstalled {
		level = slog.LevelError
	}
	log.Log(ctx, level, "profiler request handled", "action", st.Action, "result", st.Result,
		"target", st.Target, "version", st.Version, "previous", st.Previous, "error", st.Error)
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
// A root without the marker belongs to the package manager or to a person and is never changed.
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
	exists, fleet := a.ownership()
	switch req.Action {
	case ActionInstall:
		if exists && !fleet {
			a.reject("%s exists and was not installed by this agent; leaving it alone", a.root)
			return
		}
		a.install(ctx, req)
	case ActionUninstall:
		a.st.Operation = OpUninstall
		if !exists {
			a.st.Result, a.st.Version = ResultUninstalled, ""
			return
		}
		if !fleet {
			a.reject("%s was not installed by this agent; leaving it alone", a.root)
			return
		}
		a.uninstall(ctx)
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
	a.st.Operation = OpInstall
	if cur != "" {
		a.st.Operation = OpUpgrade
		if cv, err := lib.ParseVersion(cur); err == nil {
			if tv, err := lib.ParseVersion(ver); err == nil && lib.Compare(tv, cv) < 0 {
				a.st.Operation = OpRollback
			}
		}
	}
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
	if err := update.WriteFileAtomic(marker, []byte("installed by openlog-infra-agent (ebpf-profiler.md)\n"), 0o644); err != nil {
		a.reject("marker: %v", err)
		return
	}
	if o.Sys.Lchown != nil {
		if err := o.Sys.Lchown(marker, o.Sys.RootUID, o.Sys.RootGID); err != nil {
			a.reject("marker: %v", err)
			return
		}
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
	ok, detail := a.enable(ctx, ver)
	if ok {
		if removed := prune(versions, ver, a.st.Previous); len(removed) > 0 {
			a.log.Info("pruned old profiler versions", "removed", removed)
		}
		a.st.Result = ResultApplied
		return
	}
	// The new version does not run: go back to the one that did, and say why.
	a.st.Error = update.Truncate(detail, 1024)
	if p := a.st.Previous; p != "" && p != ver && a.trustedVersion(p) {
		if err := switchCurrent(a.root, p); err == nil {
			if ok, d := a.enable(ctx, p); !ok {
				a.st.Error = update.Truncate(detail+"; previous version "+p+": "+d, 1024)
			}
			os.RemoveAll(filepath.Join(versions, ver))
			a.st.Result, a.st.Version, a.st.Previous = ResultRolledBack, p, a.prev.Previous
			return
		}
	}
	a.disable(ctx)
	os.RemoveAll(a.root)
	a.st.Result, a.st.Version, a.st.Previous = ResultFailed, "", ""
}

// extract copies the staged archive into a root-owned file (size and sha256 checked on the copy), extracts it
// and renames the verified tree to versions/<v>.
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
	bin := filepath.Join(tree, BinaryRel)
	if fi, err := os.Lstat(bin); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("release %s has no regular file %s", ver, BinaryRel)
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		return err
	}
	if fi, err := os.Lstat(filepath.Join(tree, UnitRel)); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("release %s has no regular file %s", ver, UnitRel)
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

// enable installs the unit of this version and starts it, then checks it is actually running.
func (a *applier) enable(ctx context.Context, ver string) (bool, string) {
	if err := a.installUnit(ctx, ver); err != nil {
		return false, err.Error()
	}
	if !a.systemdRunning() {
		// No systemd here: the files are in place and nothing else can be checked.
		return true, ""
	}
	if err := a.run(ctx, "systemctl", "enable", "--now", UnitName); err != nil {
		return false, "systemctl enable --now " + UnitName + ": " + err.Error()
	}
	if err := a.run(ctx, "systemctl", "is-active", "--quiet", UnitName); err != nil {
		return false, UnitName + " is not running after start: " + err.Error()
	}
	a.st.Unit = UnitName
	return true, ""
}

// installUnit writes the version's unit file to /etc/systemd/system, where install.sh and every other tarball
// installation keeps its units (reconcile.go: "tarball installs manage /etc/systemd/system"). A packaged unit in
// /usr/lib belongs to dpkg/rpm and stops the installation instead.
func (a *applier) installUnit(ctx context.Context, ver string) error {
	src := filepath.Join(a.root, "versions", ver, UnitRel)
	data, err := update.ReadLimited(src, 1<<20)
	if err != nil {
		return fmt.Errorf("unit of %s: %w", ver, err)
	}
	etc := "/etc/systemd/system/" + UnitName
	usr := "/usr/lib/systemd/system/" + UnitName
	target := ""
	switch {
	case a.exists(usr):
		// The .deb/.rpm owns that unit. This agent installs into its own root and never fights the package
		// manager; a host with both is left to the operator (drop-ins: systemctl edit).
		return fmt.Errorf("%s belongs to the package manager; not modified", usr)
	case a.exists("/etc/systemd/system"):
		target = etc
	default:
		return errors.New("no systemd unit directory")
	}
	p := a.sysPath(target)
	if cur, err := os.ReadFile(p); err == nil && string(cur) == string(data) {
		return nil
	}
	if err := update.WriteFileAtomic(p, data, 0o644); err != nil {
		return fmt.Errorf("unit: %w", err)
	}
	if a.o.Sys.Lchown != nil {
		if err := a.o.Sys.Lchown(p, a.o.Sys.RootUID, a.o.Sys.RootGID); err != nil {
			return fmt.Errorf("unit: %w", err)
		}
	}
	sum := sha256.Sum256(data)
	a.log.Info("profiler unit installed", "path", target, "sha256", hex.EncodeToString(sum[:]))
	if a.systemdRunning() {
		if err := a.run(ctx, "systemctl", "daemon-reload"); err != nil {
			return fmt.Errorf("daemon-reload: %w", err)
		}
	}
	return nil
}

func (a *applier) uninstall(ctx context.Context) {
	a.disable(ctx)
	if err := os.RemoveAll(a.root); err != nil {
		a.st.Result, a.st.Error = ResultFailed, update.Truncate(err.Error(), 1024)
		return
	}
	a.st.Result, a.st.Version, a.st.Previous = ResultUninstalled, "", ""
}

func (a *applier) disable(ctx context.Context) {
	if !a.systemdRunning() {
		return
	}
	_ = a.run(ctx, "systemctl", "disable", "--now", UnitName)
}

// versionManifest is the signed manifest kept next to an installed version ("" = nothing installed).
func (a *applier) versionManifest(v string) (*lib.Manifest, error) {
	if v == "" {
		return nil, nil
	}
	b, err := update.ReadLimited(filepath.Join(a.root, "versions", v, ManifestFile), maxManifestBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return lib.ParseManifest(b)
}

// trustedVersion reports whether a version directory is still a root-owned tree we may switch back to.
func (a *applier) trustedVersion(v string) bool {
	dir := filepath.Join(a.root, "versions", v)
	fi, err := os.Lstat(filepath.Join(dir, BinaryRel))
	return err == nil && fi.Mode().IsRegular() && a.o.Sys.TrustedTree(dir)
}

func (a *applier) sysPath(p string) string { return filepath.Join(a.o.Sys.Root, p) }

func (a *applier) exists(p string) bool {
	_, err := os.Lstat(a.sysPath(p))
	return err == nil
}

func (a *applier) systemdRunning() bool { return a.exists("/run/systemd/system") }

func (a *applier) run(ctx context.Context, name string, args ...string) error {
	if a.o.Sys.Run == nil {
		return errors.New(name + ": no command runner")
	}
	return a.o.Sys.Run(ctx, name, args...)
}

func switchCurrent(root, v string) error {
	if _, err := os.Stat(filepath.Join(root, "versions", v, BinaryRel)); err != nil {
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
		if slices.Contains(keep, e.Name()) || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if os.RemoveAll(filepath.Join(versions, e.Name())) == nil {
			removed = append(removed, e.Name())
		}
	}
	return removed
}

// InstalledVersion is the version current points at ("" when nothing is installed).
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

// ManagedBy tells who owns the install root: this agent (marker file), something else, or nobody.
//
// sys may be nil for the unprivileged side, which cannot verify ownership: the marker's existence is then the
// honest answer, and the privileged step checks that it is really root-owned before it changes anything.
func ManagedBy(sys *update.Sys, root string) string {
	fi, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return ManagedNone
	}
	if err != nil || !fi.IsDir() {
		return ManagedManual
	}
	marker := filepath.Join(root, MarkerFile)
	if sys == nil {
		if fi, err := os.Lstat(marker); err == nil && fi.Mode().IsRegular() {
			return ManagedFleet
		}
		return ManagedPackage
	}
	if sys.TrustedFile(marker) == nil {
		return ManagedFleet
	}
	return ManagedPackage
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
