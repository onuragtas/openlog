package updater

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	lib "github.com/onuragtas/openlog/libs/release"
)

// Engine performs updates on one kind of installation.
type Engine interface {
	// Name is "compose" or "kubernetes".
	Name() string
	// CurrentVersion is the version the installation runs now.
	CurrentVersion(ctx context.Context) (string, error)
	// Recover finishes or undoes an update interrupted by a crash (called once at start).
	Recover(ctx context.Context) error
	// Apply updates to t. Steps are recorded in st; save persists st. On error Apply sets
	// st.State to StateFailed (nothing changed), StateRolledBack or StateRollbackFailed.
	Apply(ctx context.Context, t *Target, st *Status, save func()) error
}

// Source reads the signed release index and manifests.
type Source interface {
	Index(ctx context.Context, url string) (*lib.Index, error)
	Manifest(ctx context.Context, url string) (*lib.Manifest, error)
}

// Runner is the updater state machine: check → (notify | wait for window | apply).
type Runner struct {
	Cfg    Config
	Engine Engine
	Source Source
	Store  StatusStore
	Audit  Auditor
	Log    *slog.Logger
	Now    func() time.Time
	// RetryDelay is the wait after a failed run [min(Cfg.Interval, DefaultRetryDelay)].
	RetryDelay time.Duration
}

// DefaultRetryDelay bounds the wait after a failed run. A failure is often transient, e.g. the
// updater starting before `openlog` resolves on a fresh `docker compose up`; waiting the whole
// interval (1h by default) would report state=error in the UI for that long. Failed update targets
// are not retried (FailedVersions), so a quick retry never repeats an update.
const DefaultRetryDelay = time.Minute

func (r *Runner) retryDelay() time.Duration {
	if r.RetryDelay > 0 {
		return r.RetryDelay
	}
	return min(r.Cfg.Interval, DefaultRetryDelay)
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *Runner) audit(ctx context.Context, action string, details map[string]any) {
	if r.Audit != nil {
		r.Audit.Audit(ctx, action, details)
	}
}

// Loop recovers interrupted updates, then runs RunOnce every Cfg.Interval (after a failed run:
// every retryDelay) until ctx is done.
func (r *Runner) Loop(ctx context.Context) {
	if err := r.Engine.Recover(ctx); err != nil {
		r.Log.Error("recovering an interrupted update failed", "err", err)
	}
	for {
		wait := r.Cfg.Interval
		if err := r.RunOnce(ctx); err != nil && ctx.Err() == nil {
			wait = r.retryDelay()
			r.Log.Warn("update run failed", "err", err, "retry_in", wait)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// RunOnce performs one check and, depending on the mode and window, one update.
func (r *Runner) RunOnce(ctx context.Context) error {
	st, _, err := r.Store.Load(ctx)
	if err != nil {
		r.Log.Debug("cannot load updater status", "err", err)
	}
	st.Engine, st.Mode, st.CheckedAt = r.Engine.Name(), r.Cfg.Mode, r.now()
	save := func() {
		if err := r.Store.Save(context.WithoutCancel(ctx), st); err != nil {
			r.Log.Warn("cannot save updater status", "err", err)
		}
	}
	fail := func(err error) error {
		st.State, st.Error, st.Message = StateError, err.Error(), ""
		save()
		return err
	}
	if r.Cfg.Mode == ModeOff {
		st.State, st.Message, st.Error = StateOff, "OPENLOG_UPDATER_MODE=off", ""
		save()
		return nil
	}

	cur, err := r.Engine.CurrentVersion(ctx)
	if err != nil {
		return fail(fmt.Errorf("current version: %w", err))
	}
	st.CurrentVersion = cur
	curV, err := lib.ParseVersion(cur)
	if err != nil {
		return fail(fmt.Errorf("current version %q: %w", cur, err))
	}
	idx, err := r.Source.Index(ctx, r.Cfg.IndexURL)
	if err != nil {
		return fail(err)
	}
	target, reason, err := SelectTarget(ctx, idx, r.Cfg.Channel, curV, st.FailedVersions, r.Cfg.ImageRepository, r.Source.Manifest)
	if err != nil {
		return fail(err)
	}
	st.Error = ""
	if target == nil {
		st.State, st.Message, st.TargetVersion, st.NotesURL = StateUpToDate, reason, "", ""
		r.Log.Info("no update", "current", cur, "reason", reason)
		save()
		return nil
	}
	st.TargetVersion, st.NotesURL = target.Version.String(), target.Manifest.NotesURL
	switch {
	case r.Cfg.Mode == ModeNotify:
		if st.State != StateAvailable {
			r.audit(ctx, "updater.update_available", map[string]any{"from": cur, "to": st.TargetVersion})
		}
		st.State, st.Message = StateAvailable, fmt.Sprintf("openlog %s is available (OPENLOG_UPDATER_MODE=notify)", st.TargetVersion)
		r.Log.Info("update available", "current", cur, "target", st.TargetVersion)
		save()
		return nil
	case !InWindow(r.Cfg.MaintenanceWindows, r.now()):
		st.State, st.Message = StateWaiting, fmt.Sprintf("openlog %s will be installed in the next maintenance window", st.TargetVersion)
		r.Log.Info("update waiting for maintenance window", "target", st.TargetVersion)
		save()
		return nil
	}

	// auto: an update in progress is not interrupted by shutdown signals (every step has its own
	// timeout, and a crash is recovered at the next start).
	actx := context.WithoutCancel(ctx)
	started := r.now()
	st.State, st.Message, st.PreviousVersion = StateUpdating, "", cur
	st.StartedAt, st.FinishedAt, st.Steps, st.BackupFile = &started, nil, nil, ""
	r.Log.Info("starting update", "from", cur, "to", st.TargetVersion, "image", target.Image)
	r.audit(actx, "updater.update_started", map[string]any{"from": cur, "to": st.TargetVersion, "image": target.Image, "engine": st.Engine})
	save()

	err = r.Engine.Apply(actx, target, &st, save)
	finished := r.now()
	st.FinishedAt = &finished
	h := HistoryEntry{From: cur, To: st.TargetVersion, At: finished}
	if err == nil {
		st.State, st.CurrentVersion, st.Message = StateSucceeded, st.TargetVersion, fmt.Sprintf("updated %s → %s", cur, st.TargetVersion)
		h.Result = StateSucceeded
		st.addHistory(h)
		r.Log.Info("update succeeded", "from", cur, "to", st.TargetVersion, "duration", finished.Sub(started).Round(time.Second))
		r.audit(actx, "updater.update_succeeded", map[string]any{"from": cur, "to": st.TargetVersion, "backup": st.BackupFile})
		save()
		return nil
	}
	if st.State == StateUpdating {
		st.State = StateFailed
	}
	st.Error = err.Error()
	if !slices.Contains(st.FailedVersions, st.TargetVersion) {
		st.FailedVersions = append(st.FailedVersions, st.TargetVersion)
	}
	switch st.State {
	case StateRolledBack:
		st.Message = fmt.Sprintf("update to %s failed and was rolled back to %s", st.TargetVersion, cur)
	case StateRollbackFailed:
		st.Message = fmt.Sprintf("update to %s failed and the rollback to %s failed: manual action required", st.TargetVersion, cur)
	default:
		st.Message = fmt.Sprintf("update to %s failed before services were changed", st.TargetVersion)
	}
	h.Result, h.Error = st.State, st.Error
	st.addHistory(h)
	r.Log.Error("update failed", "from", cur, "to", st.TargetVersion, "state", st.State, "err", err)
	r.audit(actx, "updater.update_"+st.State, map[string]any{"from": cur, "to": st.TargetVersion, "error": st.Error})
	save()
	return err
}

// errStep wraps a step failure.
func errStep(step string, err error) error {
	if err == nil {
		return nil
	}
	var se *stepError
	if errors.As(err, &se) {
		return err
	}
	return &stepError{step: step, err: err}
}

type stepError struct {
	step string
	err  error
}

func (e *stepError) Error() string { return e.step + ": " + e.err.Error() }
func (e *stepError) Unwrap() error { return e.err }
