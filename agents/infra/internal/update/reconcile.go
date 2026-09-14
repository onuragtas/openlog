package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Reconcile contexts: who runs "-reconcile".
const (
	ReconcileApply   = "apply"   // "-apply" (unit pre-start step), before the agent starts
	ReconcilePackage = "package" // deb/rpm postinstall
	ReconcileInstall = "install" // install.sh
	ReconcileManual  = "manual"
)

// Docker access (same rules as before in postinstall.sh/install.sh, docs D-040).
const (
	DockerAccessEnv  = "OPENLOG_AGENT_DOCKER_ACCESS"
	DockerOptOutFile = "no-docker-access"
	DockerGroup      = "docker"
)

// Docker results (ReconcileStatus.Docker).
const (
	DockerAdded   = "added"
	DockerMember  = "member"
	DockerOptOut  = "opt_out"
	DockerNoGroup = "no_group"
	DockerFailed  = "error"
)

// ReconcileStatus is <install_root>/reconcile-status.json, written by every reconcile.
type ReconcileStatus struct {
	InvocationID string    `json:"invocation_id,omitempty"`
	At           time.Time `json:"at"`
	Version      string    `json:"version"`
	Context      string    `json:"context"`
	UnitPath     string    `json:"unit_path,omitempty"`
	UnitSHA256   string    `json:"unit_sha256,omitempty"`
	UnitChanged  bool      `json:"unit_changed"`
	// RestartRequired: the running service must restart to use the new unit (or new groups when the
	// agent already runs). With context apply the agent exits once for it.
	RestartRequired bool   `json:"restart_required"`
	Docker          string `json:"docker"`
	// PHPAccess is the openlog-php group step (reconcile_php.go).
	PHPAccess *PHPAccessStatus `json:"php_access,omitempty"`
	Notes     []string         `json:"notes,omitempty"`
	Errors    []string         `json:"errors,omitempty"`
}

// ReconcileOptions configures Reconcile.
type ReconcileOptions struct {
	Sys *Sys
	// Install is the detection result of the binary running "-reconcile".
	Install      Install
	StateDir     string
	ConfigPath   string
	AgentUser    string
	Version      string
	Unit         []byte
	Context      string
	InvocationID string
	// PHPGrantsDisabled is php_forwarder.grant_pool_users: false of the root-owned configuration.
	PHPGrantsDisabled bool
	Log               *slog.Logger
	Now               func() time.Time
}

// Reconcile makes the installation match this release, like a fresh package install or install.sh
// run would: the service account, root-owned install root and versions (legacy layouts migrated),
// state directory (agent-owned 0750), configuration (root:openlog-agent 0640, never rewritten), the
// systemd unit embedded in this binary, docker group membership, the openlog-php group for the PHP agent socket
// (reconcile_php.go) and the /usr/bin symlink (tarball).
//
// Unit path: deb/rpm manage the packaged /usr/lib/systemd/system unit (a package upgrade replaces it
// and its postinstall reconciles again with the current, possibly newer, binary); a full override in
// /etc/systemd/system is left alone; tarball installs manage /etc/systemd/system like install.sh.
// Drop-ins (systemctl edit) are never touched. It is idempotent and a no-op outside the versions layout.
func Reconcile(ctx context.Context, o ReconcileOptions) (*ReconcileStatus, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.AgentUser == "" {
		o.AgentUser = AgentUser
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	r := &reconciler{o: o, log: o.Log.With("component", "reconcile"), st: &ReconcileStatus{
		InvocationID: o.InvocationID, At: o.Now().UTC(), Version: o.Version, Context: o.Context,
	}}
	root := o.Install.InstallRoot
	if o.Install.VersionDir == "" || root == "" {
		return nil, fmt.Errorf("binary is not in the versions layout: %s", o.Install.Reason)
	}
	if !o.Sys.IsRoot() {
		return nil, errors.New("-reconcile needs root")
	}
	var prev ReconcileStatus
	if err := o.Sys.loadTrustedJSON(filepath.Join(root, ReconcileStatusFile), &prev); err != nil {
		prev = ReconcileStatus{}
	}

	uid, gid, err := r.ensureUser(ctx)
	if err != nil {
		r.fail("account", err)
	}
	notes, err := o.Sys.secureLayout(root, r.log)
	r.st.Notes = append(r.st.Notes, notes...)
	if err != nil {
		r.fail("install root", err)
	}
	if uid >= 0 {
		r.dirs(uid, gid)
	}
	r.unit(ctx, &prev)
	if uid >= 0 {
		r.docker(ctx)
		r.phpAccess(ctx)
	}
	r.binLink()

	if err := saveStatus(filepath.Join(root, ReconcileStatusFile), r.st); err != nil {
		r.fail("status", err)
	}
	if len(r.st.Errors) > 0 {
		return r.st, errors.New(strings.Join(r.st.Errors, "; "))
	}
	return r.st, nil
}

type reconciler struct {
	o   ReconcileOptions
	log *slog.Logger
	st  *ReconcileStatus
}

func (r *reconciler) fail(step string, err error) {
	r.st.Errors = append(r.st.Errors, step+": "+err.Error())
	r.log.Error("reconcile step failed", "step", step, "error", err)
}

func (r *reconciler) note(msg string, args ...any) {
	r.st.Notes = append(r.st.Notes, msg)
	r.log.Info(msg, args...)
}

func (r *reconciler) exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// ensureUser creates the service account when missing (like preinstall.sh and install.sh).
func (r *reconciler) ensureUser(ctx context.Context) (uid, gid int, err error) {
	s, name := r.o.Sys, r.o.AgentUser
	if uid, gid, err := s.lookupUser(name); err == nil {
		return uid, gid, nil
	}
	nologin := "/bin/false"
	for _, p := range []string{"/usr/sbin/nologin", "/sbin/nologin"} {
		if r.exists(s.path(p)) {
			nologin = p
			break
		}
	}
	home := r.o.StateDir
	if _, ok := s.groupMembers(name); !ok {
		if _, err := s.LookPath("groupadd"); err == nil {
			err = s.Run(ctx, "groupadd", "--system", name)
		} else {
			err = s.Run(ctx, "addgroup", "-S", name)
		}
		if err != nil {
			return -1, -1, err
		}
	}
	if _, err := s.LookPath("useradd"); err == nil {
		err = s.Run(ctx, "useradd", "--system", "--gid", name, "--home-dir", home, "--no-create-home",
			"--shell", nologin, "--comment", "openlog infrastructure agent", name)
		if err != nil {
			return -1, -1, err
		}
	} else if err := s.Run(ctx, "adduser", "-S", "-D", "-H", "-h", home, "-s", nologin, "-G", name, name); err != nil {
		return -1, -1, err
	}
	r.note("created the service account " + name)
	return s.lookupUser(name)
}

// dirs fixes the state directory and the configuration's ownership and modes.
func (r *reconciler) dirs(uid, gid int) {
	s := r.o.Sys
	if r.o.StateDir != "" {
		if err := s.fixDir(r.o.StateDir, uid, gid, 0o750); err != nil {
			r.fail("state dir", err)
		}
	}
	if r.o.ConfigPath == "" {
		return
	}
	dir := filepath.Dir(r.o.ConfigPath)
	if fi, err := os.Lstat(dir); err == nil && fi.IsDir() {
		// Keep the operator's mode but never writable by others: the directory steers a root process.
		if err := s.fixDir(dir, s.RootUID, s.RootGID, fi.Mode().Perm()&^0o022); err != nil {
			r.fail("config dir", err)
		}
	}
	fi, err := os.Lstat(r.o.ConfigPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return
	case err != nil:
		r.fail("config", err)
		return
	case !fi.Mode().IsRegular():
		r.note("configuration " + r.o.ConfigPath + " is not a regular file; ownership not changed")
		return
	}
	// Contains the license key: readable by the agent group, writable by root only (never rewritten).
	if u, g := owner(fi); u != s.RootUID || g != gid {
		if err := s.Lchown(r.o.ConfigPath, s.RootUID, gid); err != nil {
			r.fail("config", err)
		}
	}
	if fi.Mode().Perm() != 0o640 {
		if err := os.Chmod(r.o.ConfigPath, 0o640); err != nil {
			r.fail("config", err)
		}
	}
}

// unit installs the embedded systemd unit and reloads systemd when its content changed.
func (r *reconciler) unit(ctx context.Context, prev *ReconcileStatus) {
	if len(r.o.Unit) == 0 {
		return
	}
	s := r.o.Sys
	etc := "/etc/systemd/system/" + UnitName
	usr := "/usr/lib/systemd/system/" + UnitName
	tarball := r.o.Install.Method == MethodTarball
	target := ""
	switch {
	case tarball && r.exists(s.path(etc)):
		target = etc
	case r.exists(s.path(usr)) && r.exists(s.path(etc)):
		r.note(etc + " overrides the packaged unit; not modified (use drop-ins: systemctl edit " + UnitName + ")")
		return
	case r.exists(s.path(usr)):
		target = usr
	case tarball && r.exists(s.path("/etc/systemd/system")):
		target = etc
	default:
		r.note("no systemd unit directory; unit not installed")
		return
	}
	sum := sha256.Sum256(r.o.Unit)
	r.st.UnitPath, r.st.UnitSHA256 = target, hex.EncodeToString(sum[:])
	cur, err := os.ReadFile(s.path(target))
	if err == nil && bytes.Equal(cur, r.o.Unit) {
		return
	}
	if err := writeFileAtomic(s.path(target), r.o.Unit, 0o644); err != nil {
		r.fail("unit", err)
		return
	}
	if err := s.lchownRoot(s.path(target)); err != nil {
		r.fail("unit", err)
	}
	r.st.UnitChanged = true
	r.log.Info("systemd unit updated", "path", target)
	if !r.exists(s.path("/run/systemd/system")) {
		return
	}
	if err := s.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		r.fail("daemon-reload", err)
		return
	}
	// Restart-loop guard: if something rewrites the unit on every start, restart for it only once.
	if prev.RestartRequired && prev.UnitSHA256 == r.st.UnitSHA256 && prev.Context == ReconcileApply && r.o.Context == ReconcileApply {
		r.note("unit was rewritten again with the same content (changed by something else?); not restarting again")
		return
	}
	r.st.RestartRequired = true
}

// docker adds the agent user to an existing docker group unless opted out (D-040).
func (r *reconciler) docker(ctx context.Context) {
	s, user := r.o.Sys, r.o.AgentUser
	confDir := "/etc/openlog-infra-agent"
	if r.o.ConfigPath != "" {
		confDir = filepath.Dir(r.o.ConfigPath)
	}
	optOut := filepath.Join(confDir, DockerOptOutFile)
	switch strings.ToLower(s.Getenv(DockerAccessEnv)) {
	case "0", "false", "no", "off":
		r.st.Docker = DockerOptOut
		if err := os.MkdirAll(confDir, 0o755); err != nil {
			r.fail("docker opt-out", err)
			return
		}
		if f, err := os.OpenFile(optOut, os.O_CREATE|os.O_WRONLY, 0o644); err != nil {
			r.fail("docker opt-out", err)
		} else {
			f.Close()
		}
		r.log.Info("not adding the agent user to the docker group", "env", DockerAccessEnv+"=0", "recorded_in", optOut)
		return
	}
	if r.exists(optOut) {
		r.st.Docker = DockerOptOut
		return
	}
	members, ok := s.groupMembers(DockerGroup)
	switch {
	case !ok:
		r.st.Docker = DockerNoGroup
		return
	case slices.Contains(members, user):
		r.st.Docker = DockerMember
		return
	}
	for _, cmd := range [][]string{
		{"usermod", "-aG", DockerGroup, user},
		{"gpasswd", "-a", user, DockerGroup},
		{"adduser", user, DockerGroup},
	} {
		if _, err := s.LookPath(cmd[0]); err != nil {
			continue
		}
		if err := s.Run(ctx, cmd[0], cmd[1:]...); err != nil {
			r.st.Docker = DockerFailed
			r.fail("docker group", err)
			return
		}
		r.st.Docker = DockerAdded
		if r.o.Context != ReconcileApply {
			r.st.RestartRequired = true // supplementary groups apply at process start
		}
		r.log.Warn("added the agent user to the docker group for container metadata and discovery; docker group membership is "+
			"root-equivalent: whoever controls the agent user controls Docker and thus the host",
			"user", user, "revert", "gpasswd -d "+user+" docker && touch "+optOut+" && systemctl restart openlog-infra-agent")
		return
	}
	r.st.Docker = DockerFailed
	r.note("cannot add " + user + " to the docker group (no usermod, gpasswd or adduser)")
}

// binLink keeps /usr/bin/openlog-infra-agent -> current for tarball installs (packages ship it).
func (r *reconciler) binLink() {
	if r.o.Install.Method != MethodTarball || !r.exists(r.o.Sys.path("/usr/bin")) {
		return
	}
	link := r.o.Sys.path("/usr/bin/" + BinaryName)
	want := filepath.Join(r.o.Install.InstallRoot, "current", BinaryName)
	if t, err := os.Readlink(link); err == nil && t == want {
		return
	}
	if r.exists(link) {
		if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			return // a real file: not ours
		}
	}
	tmp := link + ".openlog-tmp"
	os.Remove(tmp)
	if err := os.Symlink(want, tmp); err != nil {
		r.fail("bin link", err)
		return
	}
	if err := os.Rename(tmp, link); err != nil {
		os.Remove(tmp)
		r.fail("bin link", err)
	}
}

// Report summarizes the status for sync requests.
func (st *ReconcileStatus) Report() *ReconcileReport {
	if st == nil {
		return nil
	}
	return &ReconcileReport{
		Version: st.Version, At: st.At.UTC().Format(time.RFC3339), UnitChanged: st.UnitChanged,
		Docker: st.Docker, PHPAccess: st.PHPAccess.summary(), Error: truncate(strings.Join(st.Errors, "; "), 1024),
	}
}
