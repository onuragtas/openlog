package javaagent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// Config is java_agent of the configuration file only root can change (defaults otherwise).
	Config  config.JavaAgentConfig
	Trusted []ed25519.PublicKey
	// OS selects the link method: a symlink (linux, darwin) or a file copy replaced by rename (windows).
	OS  string
	Now func() time.Time
	Log *slog.Logger
}

var requestID = regexp.MustCompile(`^[0-9a-f]{16,64}$`)

// Apply is the Java agent part of the privileged step (root / LocalSystem). It handles the agent's request once:
// install (verify the staged jar again, copy it root-owned into versions/<v>, check it is a Java agent of that
// version, switch current and link_path atomically, keep the previous version, prune older unused ones), rollback
// (back to the recorded previous version), switch (retry link_path after a Windows lock) or uninstall. It never
// changes a jar in place, never touches an install root without the fleet marker and never replaces a link_path the
// infra agent did not create. It returns the written status, or nil when there was nothing to do.
func Apply(ctx context.Context, o ApplyOptions) *Status {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.OS == "" {
		o.OS = runtime.GOOS
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	log := o.Log.With("component", "java_agent_apply")
	if o.Sys == nil || !o.Sys.IsRoot() || o.StatusDir == "" || o.Config.InstallRoot == "" || o.Config.LinkPath == "" {
		return nil
	}
	req, err := loadRequest(o.StateDir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Warn("Java agent request unreadable; ignored", "error", err)
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
	a := &applier{o: o, log: log, root: filepath.Clean(o.Config.InstallRoot), link: filepath.Clean(o.Config.LinkPath), prev: prev}
	st := prev // carries the link state and the switch history
	st.RequestID, st.At, st.Action, st.Operation, st.Result, st.Error = req.ID, o.Now().UTC(), update.Truncate(req.Action, 32), "", "", ""
	st.Target = update.Truncate(req.Version, 64)
	st.Version = InstalledVersion(a.root)
	a.st = &st
	a.inUse = validVersions(req.InUse)
	a.handle(ctx, req)
	if err := update.WriteFileAtomic(statusPath, mustJSON(a.st), 0o644); err != nil {
		log.Error("Java agent status not saved", "error", err)
	}
	level := slog.LevelInfo
	if st.Result != ResultApplied && st.Result != ResultUninstalled {
		level = slog.LevelError
	}
	log.Log(ctx, level, "Java agent request handled", "action", st.Action, "result", st.Result, "target", st.Target,
		"version", st.Version, "previous", st.Previous, "link", st.LinkState, "error", st.Error)
	return a.st
}

type applier struct {
	o     ApplyOptions
	log   *slog.Logger
	root  string
	link  string
	prev  Status
	st    *Status
	inUse []string
}

func validVersions(vs []string) []string {
	var out []string
	for _, v := range vs {
		if validVersion(v) && !slices.Contains(out, v) && len(out) < MaxJVMs {
			out = append(out, v)
		}
	}
	return out
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

func (a *applier) fail(format string, args ...any) {
	a.st.Result, a.st.Error = ResultFailed, update.Truncate(fmt.Sprintf(format, args...), 1024)
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
	return true, a.o.Sys.TrustedRegular(filepath.Join(a.root, MarkerFile))
}

func (a *applier) handle(ctx context.Context, req Request) {
	exists, fleet := a.ownership()
	if exists && !fleet {
		a.reject("%s is not managed by the infra agent (no %s): left alone", a.root, MarkerFile)
		return
	}
	switch req.Action {
	case ActionInstall:
		a.st.Operation = OpInstall
		if a.st.Version != "" {
			a.st.Operation = OpUpgrade
		}
		if a.o.Config.Mode == config.JavaAgentModeOff && !a.o.Config.RemoteConfig {
			a.reject("java_agent.mode is off and remote_config is false in the configuration")
			return
		}
		a.install(req)
	case ActionRollback:
		a.st.Operation = OpRollback
		a.rollback(req)
	case ActionSwitch:
		a.st.Operation = OpSwitch
		if a.st.Version == "" || req.Version != a.st.Version || !a.trustedVersion(a.st.Version) {
			a.reject("switch of %s requested, but %q is installed", update.Truncate(req.Version, 64), a.st.Version)
			return
		}
		a.st.Result = ResultApplied
		a.switchLink(a.st.Version)
	case ActionUninstall:
		a.st.Operation = OpUninstall
		a.uninstall()
	default:
		a.reject("unknown action %q", update.Truncate(req.Action, 32))
	}
}

func (a *applier) versionsDir() string { return filepath.Join(a.root, "versions") }

func (a *applier) jarOf(v string) string { return filepath.Join(a.versionsDir(), v, JarName) }

func (a *applier) install(req Request) {
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
	v, err := Verify(manifest, sig, o.Trusted, ver, func() (*lib.Manifest, error) { return a.versionManifest(cur) })
	if err != nil {
		a.reject("%v", err)
		return
	}
	for _, d := range []string{a.root, a.versionsDir()} {
		if err := o.Sys.FixDir(d, 0o755); err != nil {
			a.reject("install root: %v", err)
			return
		}
	}
	marker := filepath.Join(a.root, MarkerFile)
	if err := update.WriteFileAtomic(marker, []byte("installed by openlog-infra-agent (java-agent.md §2)\n"), 0o644); err != nil {
		a.reject("marker: %v", err)
		return
	}
	if err := o.Sys.Lchown(marker, o.Sys.RootUID, o.Sys.RootGID); err != nil {
		a.reject("marker: %v", err)
		return
	}
	if !a.sameJar(ver, v.Artifact.SHA256) {
		if err := a.place(sr, rel, v, manifest, sig); err != nil {
			a.reject("%v", err)
			return
		}
	}
	now := o.Now().UTC()
	if ver != cur {
		if err := a.switchCurrent(ver); err != nil {
			a.fail("%v", err)
			return
		}
		a.st.Previous, a.st.Version = cur, ver
		a.st.Switches = pushSwitch(a.st.Switches, ver, now)
	}
	a.st.Result = ResultApplied
	a.switchLink(ver)
	keep := append([]string{ver, a.st.Previous}, a.inUse...)
	if a.st.LinkVersion != "" {
		keep = append(keep, a.st.LinkVersion) // a locked Windows copy still names it
	}
	if removed, err := update.Prune(a.root, keep...); len(removed) > 0 || err != nil {
		a.log.Info("pruned old Java agent versions", "removed", removed, "error", err)
	}
}

// sameJar reports whether versions/<v> is already a trusted directory whose jar has the signed digest.
func (a *applier) sameJar(v, sha string) bool {
	if !a.trustedVersion(v) {
		return false
	}
	sum, err := fileSHA256(a.jarOf(v))
	return err == nil && strings.EqualFold(sum, sha)
}

// place copies the staged jar into a root-owned directory (size and sha256 checked on the copy), checks that it is a
// Java agent of the release and renames the verified directory to versions/<v>.
func (a *applier) place(sr *os.Root, rel string, v *Verified, manifest, sig []byte) error {
	ver := v.Version.String()
	partial := filepath.Join(a.versionsDir(), "."+ver+".partial")
	if err := os.RemoveAll(partial); err != nil {
		return err
	}
	if err := a.o.Sys.FixDir(partial, 0o755); err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(partial)
		}
	}()
	jar := filepath.Join(partial, JarName)
	if err := update.CopyVerified(sr, path.Join(rel, JarName), jar, v.Artifact.Size, v.Artifact.SHA256); err != nil {
		return err
	}
	if err := os.Chmod(jar, 0o644); err != nil {
		return err
	}
	if err := ValidateJar(jar, ver); err != nil {
		return err
	}
	for name, data := range map[string][]byte{ManifestFile: manifest, SignatureFile: sig} {
		if err := update.WriteFileAtomic(filepath.Join(partial, name), data, 0o644); err != nil {
			return err
		}
	}
	if !a.o.Sys.TrustedTree(partial) {
		return fmt.Errorf("installed jar of %s is not a root-owned tree", ver)
	}
	final := filepath.Join(a.versionsDir(), ver)
	if err := os.RemoveAll(final); err != nil {
		return fmt.Errorf("replace %s: %w", final, err)
	}
	if err := os.Rename(partial, final); err != nil {
		return err
	}
	ok = true
	update.SyncDir(a.versionsDir())
	return nil
}

// switchCurrent atomically points <root>/current at versions/<v> (temp symlink + rename).
func (a *applier) switchCurrent(v string) error {
	if _, err := os.Stat(a.jarOf(v)); err != nil {
		return fmt.Errorf("switch to %s: %w", v, err)
	}
	tmp := filepath.Join(a.root, fmt.Sprintf(".current.%d.tmp", os.Getpid()))
	os.Remove(tmp)
	if err := os.Symlink(filepath.Join("versions", v), tmp); err != nil {
		return fmt.Errorf("switch to %s: %w", v, err)
	}
	if err := update.ReplaceLink(tmp, filepath.Join(a.root, "current")); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("switch to %s: %w", v, err)
	}
	update.SyncDir(a.root)
	return nil
}

// LinkStateOf classifies link_path: missing, managed (a symlink into install_root, or a Windows copy whose digest
// the privileged step recorded) or unmanaged.
func LinkStateOf(link, root, recordedSHA string) string {
	fi, err := os.Lstat(link)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return LinkMissing
	case err != nil:
		return LinkUnmanaged
	case fi.Mode()&os.ModeSymlink != 0:
		t, err := os.Readlink(link)
		if err != nil {
			return LinkUnmanaged
		}
		if !filepath.IsAbs(t) {
			t = filepath.Join(filepath.Dir(link), t)
		}
		t, root = filepath.Clean(t), filepath.Clean(root)
		if strings.HasPrefix(t, root+string(filepath.Separator)) {
			return LinkManaged
		}
		return LinkUnmanaged
	case fi.Mode().IsRegular() && recordedSHA != "":
		if sum, err := fileSHA256(link); err == nil && strings.EqualFold(sum, recordedSHA) {
			return LinkManaged
		}
	}
	return LinkUnmanaged
}

// switchLink points link_path at versions/<v>/openlog-javaagent.jar: a new symlink renamed over the old one
// (Linux, macOS), or a copy renamed over the old copy (Windows; a JVM that has the old copy open locks it, then the
// link stays pending and the agent retries). A link_path the infra agent did not create is never changed.
func (a *applier) switchLink(v string) {
	if a.st.LinkVersion == v && a.st.LinkState == LinkManaged && LinkStateOf(a.link, a.root, a.st.LinkSHA256) == LinkManaged {
		return
	}
	switch LinkStateOf(a.link, a.root, a.prev.LinkSHA256) {
	case LinkUnmanaged:
		a.st.LinkState = LinkUnmanaged
		return
	}
	parent := filepath.Dir(a.link)
	if _, err := os.Stat(parent); errors.Is(err, fs.ErrNotExist) {
		if err := a.o.Sys.FixDir(parent, 0o755); err != nil {
			a.linkError("create %s: %v", parent, err)
			return
		}
	}
	if err := a.o.Sys.TrustedDir(parent); err != nil {
		a.linkError("link_path not written: %v", err)
		return
	}
	target := a.jarOf(v)
	if a.o.OS == "windows" {
		a.copyLink(v, target)
		return
	}
	tmp := filepath.Join(parent, fmt.Sprintf(".%s.%d.tmp", filepath.Base(a.link), os.Getpid()))
	os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		a.linkError("link_path: %v", err)
		return
	}
	if err := os.Rename(tmp, a.link); err != nil {
		os.Remove(tmp)
		a.linkError("link_path: %v", err)
		return
	}
	update.SyncDir(parent)
	a.linkDone(v, "")
}

// copyLink replaces the Windows copy: copy to a temporary name, then MoveFileEx(REPLACE_EXISTING) through os.Rename.
func (a *applier) copyLink(v, target string) {
	tmp := a.link + ".openlog-new"
	os.Remove(tmp)
	if err := copyFile(target, tmp); err != nil {
		os.Remove(tmp)
		a.linkError("copy to link_path: %v", err)
		return
	}
	sum, err := fileSHA256(tmp)
	if err != nil {
		os.Remove(tmp)
		a.linkError("copy to link_path: %v", err)
		return
	}
	if err := os.Rename(tmp, a.link); err != nil {
		os.Remove(tmp)
		a.st.LinkState = LinkPending
		a.st.Error = update.Truncate(fmt.Sprintf("%s is in use by a running JVM (Windows locks open jars): it switches to %s once "+
			"the JVMs using it are stopped (the agent retries); %v", a.link, v, err), 1024)
		return
	}
	a.linkDone(v, sum)
}

func (a *applier) linkDone(v, sum string) {
	a.st.LinkState, a.st.LinkVersion, a.st.LinkSHA256 = LinkManaged, v, sum
	a.st.LinkSwitches = pushSwitch(a.st.LinkSwitches, v, a.o.Now().UTC())
}

func (a *applier) linkError(format string, args ...any) {
	a.st.LinkState = LinkError
	a.st.Error = update.Truncate(fmt.Sprintf(format, args...), 1024)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(in, MaxJarBytes+1)); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// rollback switches current and link_path back to the recorded previous version (a failed verification after the
// switch). Without a trusted previous version the installed one stays: removing the jar would stop JVMs from starting.
func (a *applier) rollback(req Request) {
	cur := a.st.Version
	if cur == "" || req.Version != cur {
		a.reject("rollback of %s requested, but %q is installed", update.Truncate(req.Version, 64), cur)
		return
	}
	p := a.prev.Previous
	if p == "" || p == cur || !a.trustedVersion(p) {
		a.fail("no previous Java agent version to roll back to: %s stays installed", cur)
		return
	}
	if err := a.switchCurrent(p); err != nil {
		a.fail("%v", err)
		return
	}
	now := a.o.Now().UTC()
	a.st.Switches = pushSwitch(a.st.Switches, p, now)
	a.st.Result, a.st.Version, a.st.Previous = ResultRolledBack, p, ""
	a.switchLink(p)
	if !slices.Contains(a.inUse, cur) && a.st.LinkVersion != cur {
		os.RemoveAll(filepath.Join(a.versionsDir(), cur))
	}
}

// uninstall removes a managed link_path and the install root. It is refused while JVMs use the jar.
func (a *applier) uninstall() {
	if len(a.inUse) > 0 {
		a.reject("the Java agent is in use by running JVMs (%s): not removed", strings.Join(a.inUse, ", "))
		return
	}
	if exists, _ := a.ownership(); !exists {
		a.st.Result, a.st.Version, a.st.Previous = ResultUninstalled, "", ""
		return
	}
	if LinkStateOf(a.link, a.root, a.prev.LinkSHA256) == LinkManaged {
		if err := os.Remove(a.link); err != nil {
			a.fail("remove %s: %v", a.link, err)
			return
		}
	}
	if err := os.RemoveAll(a.root); err != nil {
		a.fail("%v", err)
		return
	}
	a.st.Result, a.st.Version, a.st.Previous = ResultUninstalled, "", ""
	a.st.LinkState, a.st.LinkVersion, a.st.LinkSHA256, a.st.Switches, a.st.LinkSwitches = LinkMissing, "", "", nil, nil
}

// trustedVersion reports whether versions/<v> is a root-owned directory with its jar.
func (a *applier) trustedVersion(v string) bool {
	if !validVersion(v) {
		return false
	}
	dir := filepath.Join(a.versionsDir(), v)
	fi, err := os.Lstat(filepath.Join(dir, JarName))
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
	b, err := update.ReadLimited(filepath.Join(a.versionsDir(), v, ManifestFile), maxManifestBytes)
	if err != nil {
		return nil, err
	}
	return lib.ParseManifest(b)
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
