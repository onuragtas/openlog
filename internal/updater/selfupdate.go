package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	lib "github.com/onuragtas/openlog/libs/release"

	"github.com/onuragtas/openlog/internal/updatemsg"
	"github.com/onuragtas/openlog/internal/version"
)

// Updater self-update (Compose, D-120, docs/operations/upgrading.md "Updater self-update").
//
// After the stack was updated to V (health check passed, compose bundle installed) an updater older than V replaces its
// own container with one running V's image, which the update already pulled and verified. The canonical container
// name (e.g. openlog-openlog-updater-1) is the ownership token: Docker renames are atomic and names unique, so exactly
// one container can hold it, and only the holder acts on the stack.
//
//  1. The old updater (A) writes the marker updater-handover.json into OPENLOG_UPDATER_BACKUP_DIR (shared by both
//     containers), rewrites OPENLOG_UPDATER_IMAGE in .env when .env pins it, and creates the new container (B) from
//     its own inspect data (labels incl. com.docker.compose.*, mounts, networks, environment refreshed like any
//     recreated container) under "<name>-self-update-new" with restart policy "no".
//  2. B starts, finds the marker with its own id, runs a self-test (version, Docker access, status file, compose
//     directory) and writes updater-handover-ready.json. It does not act.
//  3. A waits for the ready file (OPENLOG_UPDATER_HEALTH_TIMEOUT is not used: HandoverTimeout, 2 min). Failure (B
//     exited, self-test failed, timeout): A removes B, restores .env, deletes the marker and records
//     updater_self_update_failed; A keeps the name and keeps running. No retry for V.
//  4. Success: A renames itself "<name>-self-update-old" (release) and stops acting for good.
//  5. B renames itself to the canonical name, sets A's restart policy on itself (commit), stops and removes A, deletes
//     the marker and records the result. Then it starts its normal loop.
//
// Crash safety: until the commit B has restart policy "no", A keeps its restart policy until removed. A released A
// reclaims the name only when B is not running and not committed (a stopped container with restart policy "no" can
// never act again); B holding the name with a restart policy is never touched. On start every updater reads the marker
// first (Handover): A still holding the name undoes the handover; a released A waits (or reclaims); B continues the
// takeover; a container that holds the name without being part of the handover (recreated by docker compose) removes
// both handover containers. B exits when the handover misses its deadline, so A can reclaim.
const (
	handoverFile      = "updater-handover.json"
	handoverReadyFile = "updater-handover-ready.json"
	handoverNewSuffix = "-self-update-new"
	handoverOldSuffix = "-self-update-old"
	// DefaultHandoverTimeout bounds the self-test of the new updater container.
	DefaultHandoverTimeout = 2 * time.Minute
	// handoverGrace is added to the timeout for the new container's own deadline (release and rename).
	handoverGrace = time.Minute
)

type handoverMarker struct {
	TargetVersion string `json:"target_version"`
	FromVersion   string `json:"from_version"`
	Image         string `json:"image"`
	// Name is the canonical container name; OldID holds it until the release.
	Name    string `json:"name"`
	OldID   string `json:"old_id"`
	NewName string `json:"new_name"`
	NewID   string `json:"new_id,omitempty"`
	// RestartPolicy is the old container's policy, set on the new one when it commits.
	RestartPolicy map[string]any `json:"restart_policy"`
	// EnvKey/EnvPrevious: the .env variable rewritten to Image and its previous raw value (restored on failure).
	EnvKey         string    `json:"env_key,omitempty"`
	EnvPrevious    string    `json:"env_previous,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	TimeoutSeconds int       `json:"timeout_seconds"`
}

// deadline is when the new updater gives up (exits) unless it holds the canonical name.
func (m *handoverMarker) deadline() time.Time {
	return m.CreatedAt.Add(time.Duration(m.TimeoutSeconds)*time.Second + handoverGrace)
}

type handoverReady struct {
	NewID   string    `json:"new_id"`
	Version string    `json:"version"`
	OK      bool      `json:"ok"`
	Error   string    `json:"error,omitempty"`
	At      time.Time `json:"at"`
}

func (e *ComposeEngine) selfVersion() string {
	if e.SelfVersion != "" {
		return e.SelfVersion
	}
	return version.Version
}

func (e *ComposeEngine) handoverTimeout() time.Duration {
	if e.HandoverTimeout > 0 {
		return e.HandoverTimeout
	}
	return DefaultHandoverTimeout
}

func (e *ComposeEngine) handoverPath(name string) string { return filepath.Join(e.Cfg.BackupDir, name) }

func (e *ComposeEngine) readMarker() (*handoverMarker, error) {
	b, err := os.ReadFile(e.handoverPath(handoverFile))
	if err != nil {
		return nil, err
	}
	var m handoverMarker
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", handoverFile, err)
	}
	if m.Name == "" || m.OldID == "" || m.NewName == "" {
		return nil, fmt.Errorf("%s: incomplete marker", handoverFile)
	}
	return &m, nil
}

func (e *ComposeEngine) writeHandoverFile(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(e.Cfg.BackupDir, 0o750); err != nil {
		return err
	}
	return writeFileAtomic(e.handoverPath(name), b, 0o640)
}

// removeHandoverFiles deletes the ready file, then the marker (its absence means "no handover").
func (e *ComposeEngine) removeHandoverFiles() {
	for _, n := range []string{handoverReadyFile, handoverFile} {
		if err := os.Remove(e.handoverPath(n)); err != nil && !errors.Is(err, os.ErrNotExist) {
			e.Log.Error("cannot remove a self-update handover file", "file", e.handoverPath(n), "err", err)
		}
	}
}

func (e *ComposeEngine) ownContainer(ctx context.Context) (*Container, error) {
	host := e.Hostname
	if host == "" {
		h, err := os.Hostname()
		if err != nil {
			return nil, err
		}
		host = h
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c, err := e.Docker.InspectContainer(cctx, host)
	if err != nil {
		return nil, fmt.Errorf("inspect own container %q: %w", host, err)
	}
	return c, nil
}

func containerName(c *Container) string { return strings.TrimPrefix(c.Name, "/") }

func restartPolicyName(policy any) string {
	m, _ := policy.(map[string]any)
	name, _ := m["Name"].(string)
	if name == "" {
		return "no"
	}
	return name
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// selfUpdatePlan returns the updater's own container when it should replace itself with t's image; otherwise reason
// says why not ("" when the updater is not older than t).
func (e *ComposeEngine) selfUpdatePlan(ctx context.Context, t *Target, st Status) (*Container, string) {
	v := t.Version.String()
	self := e.selfVersion()
	selfV, err := lib.ParseVersion(self)
	if err != nil || strings.Contains(self, "dev") {
		return nil, "development build " + self
	}
	if !selfV.Less(t.Version) {
		return nil, ""
	}
	switch e.Cfg.SelfUpdate {
	case SelfUpdateOff:
		return nil, "OPENLOG_UPDATER_SELF_UPDATE=off"
	case SelfUpdateAuto:
		if b, _ := e.installedBundleVersion(); b == "" {
			return nil, "not an install-server.sh installation (OPENLOG_UPDATER_SELF_UPDATE=on enables it)"
		}
	}
	if rec := st.SelfUpdate; rec != nil && rec.TargetVersion == v {
		return nil, fmt.Sprintf("already attempted for %s (%s); it is not retried", v, rec.State)
	}
	if e.Cfg.BackupDir == "" {
		return nil, "no state directory (OPENLOG_UPDATER_BACKUP_DIR)"
	}
	if _, err := os.Stat(e.handoverPath(handoverFile)); !errors.Is(err, os.ErrNotExist) {
		return nil, "a previous self-update is not finished (" + e.handoverPath(handoverFile) + ")"
	}
	if _, err := e.Docker.InspectImage(ctx, t.Image); err != nil {
		return nil, "the image " + t.Image + " is not available: " + err.Error()
	}
	c, err := e.ownContainer(ctx)
	if err != nil {
		return nil, "cannot identify its own container: " + err.Error()
	}
	if name := containerName(c); strings.HasSuffix(name, handoverNewSuffix) || strings.HasSuffix(name, handoverOldSuffix) {
		return nil, "its container " + name + " belongs to an unfinished self-update"
	}
	return c, ""
}

// SelfUpdate implements SelfUpdater (steps 1, 3 and 4 of the handover). It returns true only when this process must
// stop acting: it released the canonical name and the new updater took over (or this process is being stopped).
func (e *ComposeEngine) SelfUpdate(ctx context.Context, t *Target, st Status, h SelfUpdateHooks) bool {
	v := t.Version.String()
	self, reason := e.selfUpdatePlan(ctx, t, st)
	if self == nil {
		if reason != "" {
			e.Log.Info("openlog-updater does not replace its own container", "version", e.selfVersion(), "target", v, "reason", reason)
		}
		return false
	}
	name := containerName(self)
	policy, _ := self.HostConfig["RestartPolicy"].(map[string]any)
	if policy == nil {
		policy = map[string]any{"Name": "no"}
	}
	m := &handoverMarker{TargetVersion: v, FromVersion: e.selfVersion(), Image: t.Image, Name: name, OldID: self.ID,
		NewName: name + handoverNewSuffix, RestartPolicy: policy, CreatedAt: time.Now().UTC(), TimeoutSeconds: int(e.handoverTimeout() / time.Second)}
	started := e.now()
	h.Update(func(s *Status) {
		s.SelfUpdate = &SelfUpdateRecord{TargetVersion: v, FromVersion: m.FromVersion, State: SelfUpdateRunning, StartedAt: started}
		s.beginStep(updatemsg.StepSelfUpdate, started)
	})
	h.Audit("updater.self_update_started", map[string]any{"from": m.FromVersion, "to": v, "image": t.Image, "container": name})
	e.Log.Info("openlog-updater replaces its own container with the installed release", "from", m.FromVersion, "to", v,
		"container", name, "image", t.Image)

	fail := func(stage string, err error) bool {
		err = fmt.Errorf("%s: %w", stage, err)
		e.Log.Error("self-update failed; this updater keeps running", "target", v, "err", err)
		e.abortHandover(context.WithoutCancel(ctx), m)
		e.finishSelfUpdate(h, m, err, "")
		return false
	}
	spec, err := e.selfSpec(ctx, self, t, m.NewName)
	if err != nil {
		return fail("prepare", err)
	}
	m.EnvKey, m.EnvPrevious = e.updaterImageEnv(t.Image)
	// The marker (with the .env value to restore) is written before anything changes.
	if err := e.writeHandoverFile(handoverFile, m); err != nil {
		return fail("write "+handoverFile, err)
	}
	if m.EnvKey != "" {
		if err := rewriteEnvFile(e.Cfg.EnvFile, m.EnvKey, t.Image); err != nil {
			return fail("update "+m.EnvKey+" in .env", err)
		}
	}
	id, err := e.Docker.CreateContainer(ctx, spec)
	if err != nil {
		return fail("create "+m.NewName, err)
	}
	m.NewID = id
	if err := e.writeHandoverFile(handoverFile, m); err != nil {
		return fail("write "+handoverFile, err)
	}
	if err := e.Docker.StartContainer(ctx, id); err != nil {
		return fail("start "+m.NewName, err)
	}
	if err := e.waitReady(ctx, m); err != nil {
		return fail("self-test", err)
	}
	if err := e.Docker.RenameContainer(ctx, self.ID, name+handoverOldSuffix); err != nil {
		// Only a rename that certainly did not happen may be undone (the new updater takes the name once it is free).
		cur, ierr := e.Docker.InspectContainer(context.WithoutCancel(ctx), self.ID)
		if ierr != nil || containerName(cur) == name {
			return fail("release the container name", err)
		}
	}
	e.Log.Info("handed over to the new updater container; this updater no longer acts", "new_container", m.NewName, "id", shortID(id))
	return e.awaitTakeover(ctx, m, h)
}

// selfSpec is the new updater container: the running one with the image replaced, the environment refreshed from .env
// and the compose files (as for recreated services) and restart policy "no" until it owns the canonical name.
func (e *ComposeEngine) selfSpec(ctx context.Context, self *Container, t *Target, name string) (ContainerSpec, error) {
	oldImg, err := e.Docker.InspectImage(ctx, self.Image)
	if err != nil && !IsNotFound(err) {
		return ContainerSpec{}, err
	}
	newImg, err := e.Docker.InspectImage(ctx, t.Image)
	if err != nil {
		return ContainerSpec{}, err
	}
	spec := buildSpec(self, name, t.Image, newImg.ID, oldImg)
	labels, _ := self.Config["Labels"].(map[string]any)
	service, _ := labels[labelService].(string)
	envFor, note := e.envPlanner(self, nil, t.Version)
	switch {
	case envFor != nil && service != "":
		merged := envFor(service, toStrings(spec.Config["Env"]))
		spec.Config["Env"] = merged.env
		if len(merged.changed) > 0 {
			e.Log.Info("updater container environment refreshed from .env and the compose files", "variables", merged.changed,
				"compose_files_usable", !merged.fallback)
		}
	case note != "":
		e.Log.Info("updater container environment unchanged", "reason", note)
	}
	spec.HostConfig["RestartPolicy"] = map[string]any{"Name": "no"}
	return spec, nil
}

// updaterImageEnv returns OPENLOG_UPDATER_IMAGE and its raw value when .env pins it to an image other than image
// (docker-compose.yml: image: ${OPENLOG_UPDATER_IMAGE:-${OPENLOG_IMAGE:-…}}); "" when the updater follows OPENLOG_IMAGE,
// which the update already rewrote, or refers to another variable.
func (e *ComposeEngine) updaterImageEnv(image string) (key, previous string) {
	const k = "OPENLOG_UPDATER_IMAGE"
	if e.Cfg.EnvFile == "" {
		return "", ""
	}
	b, err := os.ReadFile(e.Cfg.EnvFile)
	if err != nil {
		return "", ""
	}
	found := false
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), k+"="); ok {
			previous, found = v, true
		}
	}
	if !found || strings.TrimSpace(previous) == "" || strings.Contains(previous, "$") || strings.Trim(previous, `"'`) == image {
		return "", ""
	}
	return k, previous
}

func (e *ComposeEngine) restoreUpdaterImageEnv(m *handoverMarker) {
	if m.EnvKey == "" || e.Cfg.EnvFile == "" {
		return
	}
	if err := rewriteEnvFile(e.Cfg.EnvFile, m.EnvKey, m.EnvPrevious); err != nil {
		e.Log.Error("cannot restore .env after the failed self-update: set it back by hand", "file", e.Cfg.EnvFile,
			"variable", m.EnvKey, "value", m.EnvPrevious, "err", err)
		return
	}
	e.Log.Info("restored .env", "variable", m.EnvKey, "value", m.EnvPrevious)
}

// waitReady waits for the self-test result of the new container.
func (e *ComposeEngine) waitReady(ctx context.Context, m *handoverMarker) error {
	timeout := time.Duration(m.TimeoutSeconds) * time.Second
	deadline := time.Now().Add(timeout)
	for {
		var r handoverReady
		if b, err := os.ReadFile(e.handoverPath(handoverReadyFile)); err == nil && json.Unmarshal(b, &r) == nil && r.NewID == m.NewID {
			if !r.OK {
				return fmt.Errorf("the new updater (%s) failed its self-test: %s", r.Version, r.Error)
			}
			e.Log.Info("the new updater passed its self-test", "version", r.Version)
			return nil
		}
		c, err := e.Docker.InspectContainer(ctx, m.NewID)
		if err != nil {
			return err
		}
		if !c.State.Running {
			logs, _ := e.Docker.ContainerLogs(context.WithoutCancel(ctx), m.NewID, 20)
			return fmt.Errorf("the new updater container exited with code %d: %s", c.State.ExitCode, lastLines(logs, 10))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the new updater did not pass its self-test within %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(e.poll()):
		}
	}
}

// handoverNew inspects the new container of the marker (nil, nil when it does not exist).
func (e *ComposeEngine) handoverNew(ctx context.Context, m *handoverMarker) (*Container, error) {
	ref := m.NewID
	if ref == "" {
		ref = m.NewName
	}
	c, err := e.Docker.InspectContainer(ctx, ref)
	if IsNotFound(err) {
		return nil, nil
	}
	return c, err
}

// abortHandover undoes a handover while this (old) updater still holds the canonical name: the new container is
// removed (it never acted: it waits for the name), .env is restored and the handover files are deleted.
func (e *ComposeEngine) abortHandover(ctx context.Context, m *handoverMarker) {
	if c, err := e.handoverNew(ctx, m); err != nil {
		e.Log.Error("cannot inspect the new updater container; remove it by hand if it exists", "container", m.NewName, "err", err)
	} else if c != nil {
		if logs, err := e.Docker.ContainerLogs(ctx, c.ID, 20); err == nil && strings.TrimSpace(logs) != "" {
			e.Log.Warn("logs of the new updater container", "container", m.NewName, "tail", lastLines(logs, 20))
		}
		if err := e.Docker.RemoveContainer(ctx, c.ID); err != nil {
			e.Log.Error("cannot remove the new updater container; remove it by hand (docker rm -f)", "container", m.NewName, "err", err)
		} else {
			e.Log.Info("removed the new updater container", "container", m.NewName)
		}
	}
	e.restoreUpdaterImageEnv(m)
	e.removeHandoverFiles()
}

// awaitTakeover runs in the old updater after it released the canonical name. It never acts on the stack: it waits
// until the new updater removes this container, and reclaims the name only when the new container is gone or stopped
// without having committed (restart policy still "no"). It returns true when this process must stop (removed or being
// stopped) and false after a reclaim (the self-update failed and this updater goes on).
func (e *ComposeEngine) awaitTakeover(ctx context.Context, m *handoverMarker, h SelfUpdateHooks) bool {
	var lastLog time.Time
	logEvery := func(msg string, args ...any) {
		if time.Since(lastLog) >= time.Minute {
			lastLog = time.Now()
			e.Log.Warn(msg, args...)
		}
	}
	for {
		if ctx.Err() != nil {
			return true
		}
		me, err := e.Docker.InspectContainer(ctx, m.OldID)
		switch {
		case IsNotFound(err):
			return true
		case err != nil:
			logEvery("self-update: cannot inspect this container while waiting for the new updater", "err", err)
		default:
			b, berr := e.handoverNew(ctx, m)
			switch {
			case berr != nil:
				logEvery("self-update: cannot inspect the new updater container", "container", m.NewName, "err", berr)
			case b == nil || (!b.State.Running && restartPolicyName(b.HostConfig["RestartPolicy"]) == "no"):
				if err := e.reclaim(ctx, m, me, b, h); err != nil {
					logEvery("self-update: cannot take back the updater after a failed handover; retrying", "err", err)
				} else {
					return false
				}
			default:
				logEvery("self-update: waiting for the new updater to take over", "new_container", containerName(b),
					"running", b.State.Running, "restart_policy", restartPolicyName(b.HostConfig["RestartPolicy"]))
			}
		}
		select {
		case <-ctx.Done():
			return true
		case <-time.After(e.poll()):
		}
	}
}

// reclaim restores the old updater after the new one stopped before committing: remove the new container, take the
// canonical name back, restore .env and record the failure.
func (e *ComposeEngine) reclaim(ctx context.Context, m *handoverMarker, me, b *Container, h SelfUpdateHooks) error {
	reason := "the new updater container disappeared before it took over"
	if b != nil {
		reason = fmt.Sprintf("the new updater container stopped (exit code %d) before it took over", b.State.ExitCode)
		if logs, err := e.Docker.ContainerLogs(ctx, b.ID, 20); err == nil && strings.TrimSpace(logs) != "" {
			e.Log.Warn("logs of the new updater container", "container", containerName(b), "tail", lastLines(logs, 20))
		}
		if err := e.Docker.RemoveContainer(ctx, b.ID); err != nil {
			return fmt.Errorf("remove %s: %w", containerName(b), err)
		}
	}
	if containerName(me) != m.Name {
		if err := e.Docker.RenameContainer(ctx, me.ID, m.Name); err != nil {
			return fmt.Errorf("rename to %s: %w", m.Name, err)
		}
	}
	e.Log.Warn("self-update failed: this updater took its container name back and keeps running", "name", m.Name, "reason", reason)
	e.restoreUpdaterImageEnv(m)
	e.removeHandoverFiles()
	e.finishSelfUpdate(h, m, errors.New(reason), "")
	return nil
}

// Handover implements SelfUpdater: it runs at start, before the updater acts, and resolves a recorded handover.
func (e *ComposeEngine) Handover(ctx context.Context, h SelfUpdateHooks) (bool, error) {
	if e.Cfg.BackupDir == "" {
		return true, nil
	}
	m, err := e.readMarker()
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		bad := e.handoverPath(handoverFile) + ".invalid"
		e.Log.Error("unreadable self-update handover marker moved aside", "file", bad, "err", err)
		_ = os.Rename(e.handoverPath(handoverFile), bad)
		return true, nil
	}
	var self *Container
	for i := 0; ; i++ {
		if self, err = e.ownContainer(ctx); err == nil {
			break
		}
		if i == 4 {
			return false, fmt.Errorf("a self-update handover is recorded in %s but this updater cannot identify its own container: %w",
				e.handoverPath(handoverFile), err)
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(e.poll()):
		}
	}
	name := containerName(self)
	switch {
	case self.ID == m.NewID:
		return e.takeOver(ctx, m, self, h)
	case self.ID == m.OldID && name == m.Name:
		e.Log.Warn("found a self-update interrupted before the new updater took over; undoing it", "target", m.TargetVersion)
		e.abortHandover(ctx, m)
		e.finishSelfUpdate(h, m, errors.New("interrupted: openlog-updater restarted before the new updater took over"), "")
		return true, nil
	case self.ID == m.OldID:
		e.Log.Warn("this updater handed over to a new updater container; waiting for it to take over", "new_container", m.NewName)
		return !e.awaitTakeover(ctx, m, h), nil
	case name == m.Name:
		// Created by docker compose (or by hand) while the handover was going on: this container owns the name, the
		// handover containers can neither hold it nor act.
		e.Log.Warn("the updater container was recreated during a self-update; removing the containers of the handover", "target", m.TargetVersion)
		for _, ref := range []string{m.OldID, m.NewID, m.NewName} {
			if ref == "" || ref == self.ID {
				continue
			}
			if c, err := e.Docker.InspectContainer(ctx, ref); err == nil && c.ID != self.ID {
				if err := e.Docker.RemoveContainer(ctx, c.ID); err != nil {
					e.Log.Error("cannot remove a self-update container; remove it by hand (docker rm -f)", "container", containerName(c), "err", err)
				}
			}
		}
		e.removeHandoverFiles()
		e.finishSelfUpdate(h, m, errors.New("interrupted: the updater container was recreated during the handover"), "")
		return true, nil
	default:
		e.Log.Warn("a self-update handover marker belongs to other containers; ignoring it", "container", name, "marker", e.handoverPath(handoverFile))
		return true, nil
	}
}

// takeOver runs in the new updater (steps 2 and 5).
func (e *ComposeEngine) takeOver(ctx context.Context, m *handoverMarker, self *Container, h SelfUpdateHooks) (bool, error) {
	name := containerName(self)
	if name != m.Name {
		if name != m.NewName {
			return false, fmt.Errorf("self-update: unexpected container name %s (want %s)", name, m.NewName)
		}
		// Until it holds the canonical name this container exits at the deadline, so the old updater can reclaim.
		disarm := e.armWatchdog(m.deadline())
		report := handoverReady{NewID: self.ID, Version: e.selfVersion(), At: time.Now().UTC()}
		if err := e.selfTest(ctx, m); err != nil {
			report.Error = err.Error()
			_ = e.writeHandoverFile(handoverReadyFile, report)
			disarm()
			return false, fmt.Errorf("self-update: self-test failed: %w", err)
		}
		report.OK = true
		if err := e.writeHandoverFile(handoverReadyFile, report); err != nil {
			disarm()
			return false, fmt.Errorf("self-update: report the self-test: %w", err)
		}
		e.Log.Info("self-update: self-test passed; waiting for the previous updater to hand over", "version", report.Version, "previous", m.FromVersion)
		if err := e.waitRelease(ctx, m); err != nil {
			disarm()
			return false, fmt.Errorf("self-update: %w", err)
		}
		err := e.Docker.RenameContainer(ctx, self.ID, m.Name)
		disarm()
		if err != nil {
			return false, fmt.Errorf("self-update: take the container name %s: %w", m.Name, err)
		}
		e.Log.Info("self-update: took over the container name", "name", m.Name)
	}
	if restartPolicyName(self.HostConfig["RestartPolicy"]) != restartPolicyName(m.RestartPolicy) {
		if err := e.Docker.UpdateRestartPolicy(ctx, self.ID, m.RestartPolicy); err != nil {
			// Not committed: give the name back and exit, the previous updater reclaims it.
			if rerr := e.Docker.RenameContainer(context.WithoutCancel(ctx), self.ID, m.NewName); rerr != nil {
				e.Log.Error("self-update: cannot give the container name back", "err", rerr)
			}
			return false, fmt.Errorf("self-update: set the restart policy: %w", err)
		}
	}
	// Committed: this container is the updater.
	if err := e.removeOld(ctx, m); err != nil {
		// The marker stays, so the old container (restart policy kept) only waits when it restarts; the next start of
		// this updater retries the removal.
		old := m.Name + handoverOldSuffix
		e.Log.Error("self-update: the previous updater container could not be removed; it stays idle — remove it with docker rm -f "+old, "err", err)
		e.finishSelfUpdate(h, m, nil, "previous container "+old+" not removed: "+err.Error())
		return true, nil
	}
	e.removeHandoverFiles()
	e.finishSelfUpdate(h, m, nil, "")
	return true, nil
}

func (e *ComposeEngine) armWatchdog(deadline time.Time) (disarm func()) {
	exit := e.Exit
	if exit == nil {
		exit = os.Exit
	}
	t := time.AfterFunc(time.Until(deadline), func() {
		e.Log.Error("self-update: the handover missed its deadline; exiting so the previous updater keeps running", "deadline", deadline)
		exit(3)
	})
	return func() { t.Stop() }
}

// waitRelease waits until the old updater gave up the canonical name.
func (e *ComposeEngine) waitRelease(ctx context.Context, m *handoverMarker) error {
	deadline := m.deadline()
	for {
		if _, err := os.Stat(e.handoverPath(handoverFile)); errors.Is(err, os.ErrNotExist) {
			return errors.New("the previous updater cancelled the handover")
		}
		old, err := e.Docker.InspectContainer(ctx, m.OldID)
		if IsNotFound(err) || (err == nil && containerName(old) != m.Name) {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("the previous updater did not hand over before the deadline")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(e.poll()):
		}
	}
}

// selfTest checks what the new updater needs before it may act: the expected version, Docker access, the status file
// and the compose directory.
func (e *ComposeEngine) selfTest(ctx context.Context, m *handoverMarker) error {
	if v := e.selfVersion(); !sameVersion(v, m.TargetVersion) {
		return fmt.Errorf("this updater reports version %s, the handover expects %s", v, m.TargetVersion)
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := e.Docker.ListContainers(cctx, map[string]string{labelProject: e.Cfg.Project}); err != nil {
		return fmt.Errorf("docker: %w", err)
	}
	if _, _, err := (FileStore{Path: e.handoverPath(StatusFileName)}).Load(ctx); err != nil {
		return fmt.Errorf("status file: %w", err)
	}
	if e.Cfg.ComposeDir != "" {
		if fi, err := os.Stat(e.Cfg.ComposeDir); err != nil || !fi.IsDir() {
			return fmt.Errorf("compose directory %s is not readable: %v", e.Cfg.ComposeDir, err)
		}
	}
	if e.Cfg.EnvFile != "" {
		if _, err := os.ReadFile(e.Cfg.EnvFile); err != nil {
			return fmt.Errorf("env file: %w", err)
		}
	}
	return nil
}

// removeOld stops and removes the old updater container (it does nothing any more).
func (e *ComposeEngine) removeOld(ctx context.Context, m *handoverMarker) error {
	var err error
	for i := 0; i < 3; i++ {
		var old *Container
		old, err = e.Docker.InspectContainer(ctx, m.OldID)
		if IsNotFound(err) {
			return nil
		}
		if err == nil {
			if serr := e.Docker.StopContainer(ctx, old.ID, 15*time.Second); serr != nil {
				e.Log.Debug("self-update: stop the previous updater container", "err", serr)
			}
			if err = e.Docker.RemoveContainer(ctx, old.ID); err == nil {
				e.Log.Info("self-update: removed the previous updater container", "container", containerName(old))
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(e.poll()):
		}
	}
	return err
}

// finishSelfUpdate records the result in the status (self_update, the self-update step, notices) and the audit log.
func (e *ComposeEngine) finishSelfUpdate(h SelfUpdateHooks, m *handoverMarker, err error, note string) {
	now := e.now()
	detail := fmt.Sprintf("openlog-updater %s → %s", m.FromVersion, m.TargetVersion)
	if note != "" {
		detail += "; " + note
	}
	h.Update(func(st *Status) {
		rec := st.SelfUpdate
		if rec == nil || rec.TargetVersion != m.TargetVersion {
			rec = &SelfUpdateRecord{TargetVersion: m.TargetVersion, FromVersion: m.FromVersion, StartedAt: m.CreatedAt}
			st.SelfUpdate = rec
		}
		rec.FinishedAt = &now
		idx := -1
		for i := len(st.Steps) - 1; i >= 0; i-- {
			if st.Steps[i].Name == updatemsg.StepSelfUpdate {
				idx = i
				break
			}
		}
		if idx < 0 {
			st.Steps = append(st.Steps, StepRecord{Name: updatemsg.StepSelfUpdate, StartedAt: m.CreatedAt})
			idx = len(st.Steps) - 1
		}
		step := &st.Steps[idx]
		step.FinishedAt = &now
		var keep []Notice
		for _, n := range st.Notices {
			if !isUpdaterNotice(n.Code) {
				keep = append(keep, n)
			}
		}
		st.Notices = keep
		if err == nil {
			rec.State, rec.Error = SelfUpdateSucceeded, ""
			step.Status, step.Detail = StepOK, detail
			return
		}
		rec.State, rec.Error = SelfUpdateFailed, err.Error()
		step.Status, step.Detail = StepFailed, err.Error()
		st.Notices = append(st.Notices, newNotice(updatemsg.UpdaterSelfUpdateFailed, updatemsg.Params{"version": m.TargetVersion, "reason": err.Error()}))
	})
	details := map[string]any{"from": m.FromVersion, "to": m.TargetVersion, "image": m.Image}
	if err != nil {
		details["error"] = err.Error()
		h.Audit("updater.self_update_failed", details)
		return
	}
	if note != "" {
		details["note"] = note
	}
	h.Audit("updater.self_update_succeeded", details)
	e.Log.Info("openlog-updater replaced its own container", "from", m.FromVersion, "to", m.TargetVersion, "container", m.Name)
}
