package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/osutil"
	lib "github.com/onuragtas/openlog/libs/release"
)

// Status files written by the privileged steps into the install root (root-owned, 0644). The agent
// reads them; it cannot write them.
const (
	ApplyStatusFile     = "apply-status.json"
	ReconcileStatusFile = "reconcile-status.json"
)

// Apply results (ApplyStatus.Result) of one start.
const (
	ApplySwitched       = "switched"
	ApplyRejected       = "rejected"
	ApplyRolledBack     = "rolled_back"
	ApplyRollbackFailed = "rollback_failed"
)

const (
	maxManifestBytes  = 1 << 20
	maxSignatureBytes = 64 << 10
)

// RootCandidate is an unconfirmed version switched to by "-apply". Only root writes it, so a
// rollback always goes to the version that was current before the switch.
type RootCandidate struct {
	Version    string    `json:"version"`
	Previous   string    `json:"previous"`
	SwitchedAt time.Time `json:"switched_at"`
	Attempts   int       `json:"attempts"`
}

// ApplyStatus is <install_root>/apply-status.json, rewritten by every "-apply".
type ApplyStatus struct {
	// InvocationID is systemd's $INVOCATION_ID of the start: the agent compares it with its own to
	// know that the privileged step ran for this start (staged mode).
	InvocationID  string         `json:"invocation_id"`
	At            time.Time      `json:"at"`
	BinaryVersion string         `json:"binary_version"`
	Current       string         `json:"current"`
	Candidate     *RootCandidate `json:"candidate,omitempty"`
	// HandledStagedAt is the staged_at of the last staged update processed (never processed twice).
	HandledStagedAt time.Time `json:"handled_staged_at,omitzero"`
	// Result of this start: "", ApplySwitched, ApplyRejected, ApplyRolledBack or ApplyRollbackFailed.
	Result      string   `json:"result,omitempty"`
	FromVersion string   `json:"from_version,omitempty"`
	ToVersion   string   `json:"to_version,omitempty"`
	Error       string   `json:"error,omitempty"`
	Notes       []string `json:"notes,omitempty"`
	// Carried marks a result taken over from the previous start (DeferAttemptCount); it is carried only once.
	Carried bool `json:"carried,omitempty"`
}

// ApplyOptions configures Apply.
type ApplyOptions struct {
	Sys *Sys
	// Install is the detection result of the binary running "-apply" (the trusted current version).
	Install        Install
	StateDir       string
	Version        string
	Trusted        []ed25519.PublicKey
	UpdatesEnabled bool
	AgentUser      string
	InvocationID   string
	Log            *slog.Logger
	OS, Arch       string
	Now            func() time.Time
	ConfirmWindow  time.Duration
	// SelfTest runs "<binary> -self-test" as uid:gid.
	SelfTest func(ctx context.Context, binary string, uid, gid int) error
	// DeferAttemptCount is set by the in-process apply of macOS and Windows services: the process that switched
	// exits and the service manager starts the new binary, whose own Apply counts the first attempt; the result
	// of the switching start is carried over to that start once.
	DeferAttemptCount bool
	// Reconcile runs the reconcile step of versions/<dir>/openlog-infra-agent after a switch, or of
	// this binary when dir is "".
	Reconcile func(ctx context.Context, dir string) error
}

// Apply is "-apply", the unit's privileged pre-start step (ExecStartPre=+, root, no sandbox). In order:
//
//  1. secure the layout (root-owned install root, versions/ and version trees; migrates legacy installs);
//  2. an unconfirmed candidate recorded by an earlier Apply: count the start, and switch back to the
//     recorded previous version after MaxStartAttempts starts, ConfirmWindow, or when the candidate asked for it;
//  3. otherwise a release the agent staged in <state_dir>/updates/<v>/: re-verify the signed manifest with
//     the keys of this binary (rules 1-5), copy the archive into the install root while checking size and
//     sha256 (rule 6), extract it root-owned, run the self-test as the agent user (rule 7) and switch current;
//  4. reconcile the installation with the version that starts next (its own code after a switch).
//
// Everything under state_dir is untrusted input. Apply never returns an error: the service must start
// the current version whatever happens. It is a no-op outside the versions layout.
func Apply(ctx context.Context, o ApplyOptions) *ApplyStatus {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.OS == "" {
		o.OS = runtime.GOOS
	}
	if o.Arch == "" {
		o.Arch = runtime.GOARCH
	}
	if o.ConfirmWindow == 0 {
		o.ConfirmWindow = ConfirmWindow
	}
	if o.AgentUser == "" {
		o.AgentUser = AgentUser
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	log := o.Log.With("component", "apply")
	root := o.Install.InstallRoot
	if o.Install.VersionDir == "" || root == "" {
		log.Info("binary is not in the versions layout; nothing to apply", "install_method", o.Install.Method, "reason", o.Install.Reason)
		return nil
	}
	if !o.Sys.IsRoot() {
		log.Warn("-apply needs root (the unit runs it with ExecStartPre=+); skipped")
		return nil
	}

	statusPath := filepath.Join(root, ApplyStatusFile)
	var prev ApplyStatus
	if err := o.Sys.loadTrustedJSON(statusPath, &prev); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Warn("previous apply status ignored", "error", err)
		prev = ApplyStatus{}
	}
	var prevRec ReconcileStatus
	if err := o.Sys.loadTrustedJSON(filepath.Join(root, ReconcileStatusFile), &prevRec); err != nil {
		prevRec = ReconcileStatus{}
	}
	a := &applier{o: o, log: log, root: root, st: &ApplyStatus{
		InvocationID: o.InvocationID, At: o.Now().UTC(), BinaryVersion: o.Version,
		Candidate: prev.Candidate, HandledStagedAt: prev.HandledStagedAt,
	}}

	notes, err := o.Sys.secureLayout(root, log)
	a.st.Notes = notes
	if err != nil {
		log.Error("securing the install root failed", "error", err)
		a.st.Notes = append(a.st.Notes, "securing the install root: "+err.Error())
	}

	agent, err := loadAgentState(o.StateDir)
	if err != nil {
		log.Warn("agent update state unreadable; ignored", "error", err)
	}

	next := ""
	if a.st.Candidate != nil {
		next = a.checkCandidate(agent, prev, prevRec)
	}
	if next == "" && a.st.Candidate == nil && agent.Staged != "" && !agent.StagedAt.Equal(a.st.HandledStagedAt) {
		a.st.HandledStagedAt = agent.StagedAt
		v, err := a.applyStaged(ctx, agent)
		if err != nil {
			a.st.Result, a.st.FromVersion, a.st.ToVersion = ApplyRejected, o.Version, truncate(agent.Staged, 64)
			a.st.Error = truncate(err.Error(), 1024)
			log.Error("staged update rejected", "version", a.st.ToVersion, "error", err)
		} else {
			next = v
			a.st.Result, a.st.FromVersion, a.st.ToVersion = ApplySwitched, o.Version, v
			log.Info("staged update installed; starting it", "from", o.Version, "to", v)
		}
	}
	if o.DeferAttemptCount && a.st.Result == "" && !prev.Carried && prev.InvocationID != o.InvocationID {
		switch prev.Result {
		case ApplySwitched, ApplyRolledBack, ApplyRollbackFailed:
			// The start that switched exited; this start runs the version it switched to and reports its result.
			if cur, _ := CurrentDir(root); cur == prev.Current {
				a.st.Result, a.st.FromVersion, a.st.ToVersion, a.st.Error = prev.Result, prev.FromVersion, prev.ToVersion, prev.Error
				a.st.Carried = true
			}
		}
	}
	a.save()

	if o.Reconcile != nil {
		if err := o.Reconcile(ctx, next); err != nil {
			log.Error("reconcile failed; starting anyway", "error", err, "version", next)
		}
	}
	return a.st
}

type applier struct {
	o    ApplyOptions
	log  *slog.Logger
	root string
	st   *ApplyStatus
}

func (a *applier) save() {
	a.st.Current, _ = CurrentDir(a.root)
	if err := saveStatus(filepath.Join(a.root, ApplyStatusFile), a.st); err != nil {
		a.log.Error("apply status not saved", "error", err)
	}
}

// loadAgentState reads the agent's state file as untrusted input: no symlink escape out of state_dir,
// no FIFO, bounded size.
func loadAgentState(stateDir string) (State, error) {
	r, err := os.OpenRoot(stateDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return State{Status: StateIdle}, nil
		}
		return State{Status: StateIdle}, err
	}
	defer r.Close()
	b, err := readRootFile(r, StateFile, maxStatusBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return State{Status: StateIdle}, nil
	}
	if err != nil {
		return State{Status: StateIdle}, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return State{Status: StateIdle}, err
	}
	return st, nil
}

func readRootFile(r *os.Root, name string, limit int64) ([]byte, error) {
	f, err := r.OpenFile(name, os.O_RDONLY|osutil.ONonblock, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readOpened(f, name, limit)
}

// checkCandidate handles an unconfirmed candidate. It returns the version switched to (rollback) or "".
func (a *applier) checkCandidate(agent State, prev ApplyStatus, prevRec ReconcileStatus) string {
	c := a.st.Candidate
	cur, _ := CurrentDir(a.root)
	if cur != c.Version {
		a.log.Info("candidate is no longer current (replaced by a package or installer); forgetting it", "candidate", c.Version, "current", cur)
		a.st.Candidate = nil
		return ""
	}
	if agent.ConfirmedVersion == c.Version && agent.ConfirmedAt.After(c.SwitchedAt) {
		a.st.Candidate = nil
		keep := []string{c.Version, c.Previous}
		if a.o.Install.PackageVersion != "" {
			keep = append(keep, packageUpstreamVersion(a.o.Install.PackageVersion)) // owned by dpkg/rpm
		}
		removed, err := Prune(a.root, keep...)
		if err != nil {
			a.log.Warn("pruning old versions failed", "error", err)
		}
		a.log.Info("candidate confirmed", "version", c.Version, "pruned", removed, "kept", keep)
		return ""
	}
	// The start right after a reconcile that changed the unit is a restart for the unit, not a failed start.
	if !(prevRec.RestartRequired && prevRec.InvocationID != "" && prevRec.InvocationID == prev.InvocationID) {
		c.Attempts++
	}
	age := a.o.Now().Sub(c.SwitchedAt)
	reason := ""
	switch {
	case agent.RollbackRequest != "" && agent.RollbackRequest == c.Version:
		reason = "version " + c.Version + " requested a rollback: " + truncate(agent.RollbackReason, 512)
	case c.Attempts > MaxStartAttempts || age > a.o.ConfirmWindow:
		reason = fmt.Sprintf("version %s did not confirm: start attempt %d, %s since the switch (limits: %d attempts, %s)",
			c.Version, c.Attempts, age.Round(time.Second), MaxStartAttempts, a.o.ConfirmWindow)
	default:
		a.log.Info("unconfirmed candidate starting", "version", c.Version, "previous", c.Previous, "attempt", c.Attempts)
		return ""
	}
	a.st.Candidate = nil
	a.st.FromVersion, a.st.ToVersion = c.Version, c.Previous
	err := a.switchBack(c.Previous)
	if err != nil {
		a.st.Result, a.st.Error = ApplyRollbackFailed, reason+"; rollback impossible: "+err.Error()
		a.log.Error("update failed and rollback is impossible; staying on this version", "candidate", c.Version, "reason", reason, "error", err)
		return ""
	}
	a.st.Result, a.st.Error = ApplyRolledBack, reason
	a.log.Error("update failed; rolled back", "candidate", c.Version, "previous", c.Previous, "reason", reason)
	return c.Previous
}

func (a *applier) switchBack(prev string) error {
	if !validVersionDir(prev) {
		return fmt.Errorf("no valid previous version recorded (%q)", truncate(prev, 64))
	}
	dir := filepath.Join(a.root, "versions", prev)
	if fi, err := os.Lstat(filepath.Join(dir, BinaryName)); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("previous version %s has no binary", prev)
	}
	if !a.o.Sys.trustedTree(dir) {
		return fmt.Errorf("previous version %s is not a root-owned directory", prev)
	}
	return SwitchCurrent(a.root, prev)
}

// validVersionDir reports whether name is a SemVer version usable as a versions/ directory name.
func validVersionDir(name string) bool {
	v, err := lib.ParseVersion(name)
	return err == nil && v.String() == name
}

// applyStaged installs the release staged in <state_dir>/updates/<v>/ and switches current to it.
func (a *applier) applyStaged(ctx context.Context, agent State) (string, error) {
	o := a.o
	ver := agent.Staged
	switch {
	case !o.UpdatesEnabled:
		return "", errors.New("updates disabled by configuration (update.enabled=false)")
	case len(o.Trusted) == 0:
		return "", ruleErr(1, ErrNoTrustedKeys)
	case !validVersionDir(ver):
		return "", ruleErr(2, "invalid staged version %q", truncate(ver, 64))
	case agent.Action != ActionUpgrade && agent.Action != ActionRollback:
		return "", ruleErr(0, "unknown staged update action %q", truncate(agent.Action, 32))
	}
	sr, err := os.OpenRoot(o.StateDir)
	if err != nil {
		return "", fmt.Errorf("staged update: %w", err)
	}
	defer sr.Close()
	rel := path.Join(UpdatesDir, ver)
	manifest, err := readRootFile(sr, path.Join(rel, ManifestFile), maxManifestBytes)
	if err != nil {
		return "", fmt.Errorf("staged manifest: %w", err)
	}
	sig, err := readRootFile(sr, path.Join(rel, SignatureFile), maxSignatureBytes)
	if err != nil {
		return "", fmt.Errorf("staged signature: %w", err)
	}
	v, err := VerifyInstruction(VerifyInput{
		Instruction: &Instruction{
			Action: agent.Action, TargetVersion: ver,
			Manifest: base64.StdEncoding.EncodeToString(manifest), Signature: string(sig),
		},
		Trusted: o.Trusted, Running: o.Version, OS: o.OS, Arch: o.Arch,
		RunningManifest: func() (*lib.Manifest, error) {
			b, err := readLimited(filepath.Join(a.root, "versions", o.Install.VersionDir, ManifestFile), maxManifestBytes)
			if err != nil {
				return nil, err
			}
			return lib.ParseManifest(b)
		},
	})
	if err != nil {
		return "", err
	}

	versions := filepath.Join(a.root, "versions")
	partial := filepath.Join(versions, "."+ver+".partial")
	extract := filepath.Join(versions, "."+ver+".extract")
	os.Remove(partial)
	defer os.Remove(partial)
	os.RemoveAll(extract)
	if err := copyVerified(sr, path.Join(rel, ArchiveFile), partial, v.Artifact.Size, v.Artifact.SHA256); err != nil {
		return "", err
	}
	if err := ExtractArchive(ArtifactFormat(o.OS), partial, extract, TopDir(ver, o.OS, o.Arch), MaxExtractBytes); err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(extract)
		}
	}()
	bin := filepath.Join(extract, BinaryName)
	if fi, err := os.Lstat(bin); err != nil || !fi.Mode().IsRegular() {
		return "", ruleErr(6, "archive has no regular file %s", BinaryName)
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		return "", err
	}
	if err := writeFileAtomic(filepath.Join(extract, ManifestFile), v.ManifestBytes, 0o644); err != nil {
		return "", err
	}
	if err := writeFileAtomic(filepath.Join(extract, SignatureFile), v.Signature, 0o644); err != nil {
		return "", err
	}
	if !o.Sys.trustedTree(extract) {
		return "", fmt.Errorf("extracted release %s is not a root-owned tree", ver)
	}
	uid, gid := -1, -1
	if !o.Sys.SelfTestAsCurrent {
		var lerr error
		if uid, gid, lerr = o.Sys.lookupUser(o.AgentUser); lerr != nil {
			return "", fmt.Errorf("self-test user: %w", lerr)
		}
	}
	if o.SelfTest != nil {
		if err := o.SelfTest(ctx, bin, uid, gid); err != nil {
			return "", err
		}
	}

	cur, err := CurrentDir(a.root)
	if err != nil {
		return "", err
	}
	if ver == cur {
		return "", fmt.Errorf("version %s is already current", ver)
	}
	final := filepath.Join(versions, ver)
	if err := os.RemoveAll(final); err != nil {
		return "", err
	}
	if err := os.Rename(extract, final); err != nil {
		return "", err
	}
	ok = true
	syncDir(versions)

	// Recorded before the switch: a crash in between leaves a candidate that is not current, which is forgotten.
	attempts := 1
	if o.DeferAttemptCount {
		attempts = 0 // the next start (of the new binary) is its first attempt
	}
	a.st.Candidate = &RootCandidate{Version: ver, Previous: cur, SwitchedAt: o.Now().UTC(), Attempts: attempts}
	a.save()
	if err := SwitchCurrent(a.root, ver); err != nil {
		a.st.Candidate = nil
		return "", err
	}
	return ver, nil
}

// copyVerified copies a staged archive into dest (a new root-owned file) and checks size and sha256
// against the signed manifest on the copy, so the staged file cannot change after verification.
func copyVerified(r *os.Root, name, dest string, size int64, sha string) (err error) {
	if size <= 0 || size > MaxArchiveBytes {
		return ruleErr(6, "archive size %d outside (0, %d]", size, MaxArchiveBytes)
	}
	in, err := r.OpenFile(name, os.O_RDONLY|osutil.ONonblock, 0)
	if err != nil {
		return fmt.Errorf("staged archive: %w", err)
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return ruleErr(6, "staged archive is not a regular file")
	}
	if fi.Size() != size {
		return ruleErr(6, "staged archive size mismatch: %d bytes, manifest says %d", fi.Size(), size)
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(dest)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, size+1))
	if err != nil {
		return fmt.Errorf("staged archive: %w", err)
	}
	if n != size {
		return ruleErr(6, "staged archive size mismatch: read %d bytes, manifest says %d", n, size)
	}
	// The digest of what was read is not reported: the path might name a file the agent user cannot read.
	if hex.EncodeToString(h.Sum(nil)) != sha {
		return ruleErr(6, "staged archive sha256 mismatch")
	}
	return out.Sync()
}
