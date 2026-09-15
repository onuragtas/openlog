package updater

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	lib "github.com/onuragtas/openlog/libs/release"

	"github.com/onuragtas/openlog/internal/updatemsg"
	"github.com/onuragtas/openlog/internal/updatereq"
	"github.com/onuragtas/openlog/internal/version"
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
	// Requests is the UI request channel (update_requests); nil without PostgreSQL.
	Requests updatereq.Poller
	// SelfVersion is the updater's own version (default version.Version), reported as Status.UpdaterVersion and
	// compared with the running version for the updater_outdated notices.
	SelfVersion string
	// SelfUpdate lets Loop complete a self-update handover at start and, after a successful update, let an engine
	// implementing SelfUpdater replace the updater's own container (the long-running Compose updater, D-120).
	SelfUpdate bool

	lastHeartbeat time.Time
	// selfUpdateTarget is the release installed by the last successful update, waiting for the self-update.
	selfUpdateTarget *Target
}

// SelfUpdateHooks let an engine record a self-update in the status document and the audit log.
type SelfUpdateHooks struct {
	// Update loads the status, applies fn and saves it.
	Update func(fn func(st *Status))
	Audit  func(action string, details map[string]any)
}

// SelfUpdater is implemented by engines that can replace the updater's own container (Compose).
type SelfUpdater interface {
	// SelfUpdate replaces the running updater by one running t's image after the installation was updated to t (st:
	// the status after that update). handedOver means this process must not act any more.
	SelfUpdate(ctx context.Context, t *Target, st Status, h SelfUpdateHooks) (handedOver bool)
	// Handover completes or undoes a handover recorded by a previous process. proceed=false: this process must exit
	// without acting (err says why when it is a failure).
	Handover(ctx context.Context, h SelfUpdateHooks) (proceed bool, err error)
}

func (r *Runner) selfVersion() string {
	if r.SelfVersion != "" {
		return r.SelfVersion
	}
	return version.Version
}

func (r *Runner) selfUpdateHooks(ctx context.Context) SelfUpdateHooks {
	sctx := context.WithoutCancel(ctx)
	return SelfUpdateHooks{
		Update: func(fn func(st *Status)) {
			st, _, err := r.Store.Load(sctx)
			if err != nil {
				r.Log.Debug("cannot load updater status", "err", err)
			}
			fn(&st)
			st.UpdaterVersion = r.selfVersion()
			if err := r.Store.Save(sctx, st); err != nil {
				r.Log.Warn("cannot save updater status", "err", err)
			}
		},
		Audit: func(action string, details map[string]any) { r.audit(sctx, action, details) },
	}
}

// selfUpdate runs the pending self-update of the engine (nothing without one). stop: this process handed over.
func (r *Runner) selfUpdate(ctx context.Context) (stop bool) {
	t := r.selfUpdateTarget
	r.selfUpdateTarget = nil
	su, ok := r.Engine.(SelfUpdater)
	if t == nil || !ok || !r.SelfUpdate || ctx.Err() != nil {
		return false
	}
	st, _, err := r.Store.Load(ctx)
	if err != nil {
		r.Log.Debug("cannot load updater status", "err", err)
	}
	return su.SelfUpdate(ctx, t, st, r.selfUpdateHooks(ctx))
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
// every retryDelay) until ctx is done. With Requests set it also polls update requests every
// Cfg.RequestPoll; a handled request counts as a run. With SelfUpdate it first completes or undoes a self-update
// handover and returns (nil, or the error that made this process give up) when this process must not act.
func (r *Runner) Loop(ctx context.Context) error {
	if su, ok := r.Engine.(SelfUpdater); ok && r.SelfUpdate {
		proceed, err := su.Handover(ctx, r.selfUpdateHooks(ctx))
		if !proceed {
			if err != nil {
				r.Log.Error("self-update handover: this updater exits without acting", "err", err)
			}
			return err
		}
		if err != nil {
			r.Log.Warn("self-update handover", "err", err)
		}
	}
	if err := r.Engine.Recover(ctx); err != nil {
		r.Log.Error("recovering an interrupted update failed", "err", err)
	}
	if r.Requests != nil {
		if err := r.Requests.Abandon(ctx, updatemsg.Format(updatemsg.RequestInterrupted, nil)); err != nil && !updatereq.IsUndefinedTable(err) {
			r.Log.Debug("cannot mark interrupted update requests", "err", err)
		}
	}
	next := r.now() // first run immediately
	for {
		if !r.now().Before(next) {
			next = r.now().Add(r.afterRun(ctx, r.RunOnce(ctx)))
			if r.selfUpdate(ctx) {
				return nil
			}
		}
		sleep := next.Sub(r.now())
		if r.Requests != nil {
			if handled, err := r.pollRequest(ctx); handled {
				next = r.now().Add(r.afterRun(ctx, err))
				// The request result is stored before the updater may replace itself.
				if r.selfUpdate(ctx) {
					return nil
				}
				continue // look for the next request right away
			}
			sleep = min(sleep, r.requestPoll())
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(max(sleep, 0)):
		}
	}
}

// afterRun logs a failed run and returns the wait until the next regular run.
func (r *Runner) afterRun(ctx context.Context, err error) time.Duration {
	var rej *RejectedError
	if err != nil && ctx.Err() == nil && !errors.As(err, &rej) {
		wait := r.retryDelay()
		r.Log.Warn("update run failed", "err", err, "retry_in", wait)
		return wait
	}
	return r.Cfg.Interval
}

func (r *Runner) requestPoll() time.Duration {
	if r.Cfg.RequestPoll > 0 {
		return r.Cfg.RequestPoll
	}
	return 10 * time.Second
}

// pollRequest refreshes the heartbeat (at most every updatereq.HeartbeatEvery), claims one pending
// request and handles it. handled is false when there was none (or PostgreSQL is unavailable).
func (r *Runner) pollRequest(ctx context.Context) (handled bool, err error) {
	if now := r.now(); now.Sub(r.lastHeartbeat) >= updatereq.HeartbeatEvery {
		hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		herr := r.Requests.PutHeartbeat(hctx, updatereq.Heartbeat{Engine: r.Engine.Name(), Mode: r.Cfg.Mode,
			PollSeconds: int(r.requestPoll() / time.Second), PolledAt: now})
		cancel()
		if herr == nil {
			r.lastHeartbeat = now
		} else {
			r.Log.Debug("cannot store updater heartbeat", "err", herr)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	req, cerr := r.Requests.Claim(cctx)
	cancel()
	if cerr != nil {
		if !updatereq.IsUndefinedTable(cerr) && ctx.Err() == nil {
			r.Log.Debug("cannot poll update requests", "err", cerr)
		}
		return false, nil
	}
	if req == nil {
		return false, nil
	}
	return true, r.HandleRequest(ctx, req)
}

// RunRequests handles every pending request once (the Kubernetes CronJob calls it before RunOnce).
// It returns how many were handled.
func (r *Runner) RunRequests(ctx context.Context) (int, error) {
	if r.Requests == nil {
		return 0, nil
	}
	var errs []error
	n := 0
	for ; n < 20; n++ {
		req, err := r.Requests.Claim(ctx)
		if err != nil {
			if updatereq.IsUndefinedTable(err) {
				return n, nil
			}
			return n, err
		}
		if req == nil {
			break
		}
		var rej *RejectedError
		if err := r.HandleRequest(ctx, req); err != nil && !errors.As(err, &rej) {
			errs = append(errs, err)
		}
	}
	return n, errors.Join(errs...)
}

// HandleRequest runs a claimed request and records its result: check = one regular run; apply =
// install req.TargetVersion now, also in notify mode and (when confirmed) outside the maintenance
// window.
func (r *Runner) HandleRequest(ctx context.Context, req *updatereq.Request) error {
	r.Log.Info("handling update request", "id", req.ID, "action", req.Action, "target", req.TargetVersion,
		"ignore_maintenance_window", req.IgnoreMaintenanceWindow, "requested_by", req.RequestedByEmail)
	var st Status
	var err error
	switch req.Action {
	case updatereq.ActionCheck:
		st, err = r.run(ctx, nil)
	case updatereq.ActionApply:
		st, err = r.run(ctx, req)
	default:
		err = rejectf("unknown request action %q", req.Action)
	}
	state, msg := updatereq.StateDone, st.Message
	if msg == "" {
		msg = st.State
	}
	if err != nil {
		state, msg = updatereq.StateFailed, err.Error()
		if st.Message != "" && !errors.As(err, new(*RejectedError)) {
			msg = st.Message + ": " + err.Error()
		}
	}
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if ferr := r.Requests.Finish(fctx, req.ID, state, msg); ferr != nil {
		r.Log.Warn("cannot store update request result", "id", req.ID, "err", ferr)
	}
	return err
}

// RejectedError means an apply request was not carried out (nothing was changed).
type RejectedError struct{ Msg string }

func (e *RejectedError) Error() string { return e.Msg }

func rejectf(format string, args ...any) error {
	return &RejectedError{Msg: fmt.Sprintf(format, args...)}
}

// reject is a RejectedError with a fixed message (internal/updatemsg), so the request message can
// be translated.
func reject(code string, params updatemsg.Params) error {
	return &RejectedError{Msg: updatemsg.Format(code, params)}
}

// RunOnce performs one check and, depending on the mode and window, one update.
func (r *Runner) RunOnce(ctx context.Context) error {
	_, err := r.run(ctx, nil)
	return err
}

// run is RunOnce; req (an apply request, or nil) forces the installation of req.TargetVersion.
func (r *Runner) run(ctx context.Context, req *updatereq.Request) (Status, error) {
	st, _, err := r.Store.Load(ctx)
	if err != nil {
		r.Log.Debug("cannot load updater status", "err", err)
	}
	st.Engine, st.Mode, st.CheckedAt, st.UpdaterVersion = r.Engine.Name(), r.Cfg.Mode, r.now(), r.selfVersion()
	st.MaintenanceWindow = NewWindowStatus(r.Cfg.MaintenanceWindows, st.CheckedAt)
	save := func() {
		if err := r.Store.Save(context.WithoutCancel(ctx), st); err != nil {
			r.Log.Warn("cannot save updater status", "err", err)
		}
	}
	fail := func(err error) (Status, error) {
		st.State, st.Error = StateError, err.Error()
		st.clearMessage()
		save()
		return st, err
	}
	apply := req != nil && req.Action == updatereq.ActionApply
	if r.Cfg.Mode == ModeOff {
		st.State, st.Error = StateOff, ""
		st.setMessage(updatemsg.ModeOff, updatemsg.Params{})
		save()
		if apply {
			return st, reject(updatemsg.RequestModeOff, updatemsg.Params{})
		}
		return st, nil
	}

	cur, err := r.Engine.CurrentVersion(ctx)
	if err != nil {
		return fail(fmt.Errorf("current version: %w", err))
	}
	st.CurrentVersion = cur
	r.refreshNotices(ctx, &st, cur)
	curV, err := lib.ParseVersion(cur)
	if err != nil {
		return fail(fmt.Errorf("current version %q: %w", cur, err))
	}
	idx, err := r.Source.Index(ctx, r.Cfg.IndexURL)
	if err != nil {
		return fail(err)
	}
	failed := st.FailedVersions
	if apply {
		// An explicit request may retry a version that failed before.
		failed = slices.DeleteFunc(slices.Clone(failed), func(v string) bool { return v == req.TargetVersion })
	}
	target, reason, err := SelectTarget(ctx, idx, r.Cfg.Channel, curV, failed, r.Cfg.ImageRepository, r.Source.Manifest)
	if err != nil {
		return fail(err)
	}
	st.Error = ""
	if target == nil {
		st.State, st.TargetVersion, st.NotesURL = StateUpToDate, "", ""
		st.setText(reason)
		r.Log.Info("no update", "current", cur, "reason", reason)
		save()
		if apply {
			return st, reject(updatemsg.RequestNotInstallable, updatemsg.Params{"version": req.TargetVersion, "reason": reason})
		}
		return st, nil
	}
	st.TargetVersion, st.NotesURL = target.Version.String(), target.Manifest.NotesURL
	version := updatemsg.Params{"version": st.TargetVersion}
	if apply && st.TargetVersion != req.TargetVersion {
		// The index changed since the admin confirmed: never install a version nobody confirmed.
		if st.State != StateAvailable && st.State != StateWaiting {
			st.State = StateAvailable
		}
		st.setMessage(updatemsg.UpdateAvailable, version)
		save()
		return st, reject(updatemsg.RequestTargetChanged, updatemsg.Params{"version": st.TargetVersion, "requested": req.TargetVersion})
	}
	ignoreWindow := apply && req.IgnoreMaintenanceWindow
	switch {
	case !apply && r.Cfg.Mode == ModeNotify:
		if st.State != StateAvailable {
			r.audit(ctx, "updater.update_available", map[string]any{"from": cur, "to": st.TargetVersion})
		}
		st.State = StateAvailable
		st.setMessage(updatemsg.UpdateAvailableNotify, version)
		r.Log.Info("update available", "current", cur, "target", st.TargetVersion)
		save()
		return st, nil
	case !ignoreWindow && !InWindow(r.Cfg.MaintenanceWindows, r.now()):
		st.State = StateWaiting
		st.setMessage(updatemsg.WaitingForWindow, version)
		if apply && r.Cfg.Mode == ModeNotify {
			st.State = StateAvailable
			st.setMessage(updatemsg.UpdateAvailableNotify, version)
		}
		r.Log.Info("update waiting for maintenance window", "target", st.TargetVersion)
		save()
		if apply {
			return st, reject(updatemsg.RequestOutsideWindow, updatemsg.Params{})
		}
		return st, nil
	}

	// auto (or an apply request): an update in progress is not interrupted by shutdown signals
	// (every step has its own timeout, and a crash is recovered at the next start).
	actx := context.WithoutCancel(ctx)
	started := r.now()
	st.State, st.PreviousVersion = StateUpdating, cur
	st.clearMessage()
	st.StartedAt, st.FinishedAt, st.Steps, st.BackupFile = &started, nil, nil, ""
	details := map[string]any{"from": cur, "to": st.TargetVersion, "image": target.Image, "engine": st.Engine}
	if apply {
		details["request_id"], details["requested_by"], details["ignore_maintenance_window"] = req.ID, req.RequestedByEmail, req.IgnoreMaintenanceWindow
		st.FailedVersions = failed
	}
	r.Log.Info("starting update", "from", cur, "to", st.TargetVersion, "image", target.Image, "requested", apply)
	r.audit(actx, "updater.update_started", details)
	save()

	err = r.Engine.Apply(actx, target, &st, save)
	finished := r.now()
	st.FinishedAt = &finished
	h := HistoryEntry{From: cur, To: st.TargetVersion, At: finished}
	if err == nil {
		st.State, st.CurrentVersion = StateSucceeded, st.TargetVersion
		r.refreshNotices(actx, &st, st.TargetVersion)
		st.setMessage(updatemsg.Updated, updatemsg.Params{"from": cur, "to": st.TargetVersion})
		h.Result = StateSucceeded
		st.addHistory(h)
		r.Log.Info("update succeeded", "from", cur, "to", st.TargetVersion, "duration", finished.Sub(started).Round(time.Second))
		r.audit(actx, "updater.update_succeeded", map[string]any{"from": cur, "to": st.TargetVersion, "backup": st.BackupFile})
		save()
		if r.SelfUpdate {
			// Loop replaces the updater's container once this run (and its request) is recorded; never after a failure
			// or rollback.
			r.selfUpdateTarget = target
		}
		return st, nil
	}
	if st.State == StateUpdating {
		st.State = StateFailed
	}
	st.Error = err.Error()
	if !slices.Contains(st.FailedVersions, st.TargetVersion) {
		st.FailedVersions = append(st.FailedVersions, st.TargetVersion)
	}
	fromTo := updatemsg.Params{"to": st.TargetVersion, "from": cur}
	switch st.State {
	case StateRolledBack:
		st.setMessage(updatemsg.RolledBack, fromTo)
	case StateRollbackFailed:
		st.setMessage(updatemsg.RollbackFailed, fromTo)
	default:
		st.setMessage(updatemsg.FailedBeforeChange, updatemsg.Params{"to": st.TargetVersion})
	}
	h.Result, h.Error = st.State, st.Error
	st.addHistory(h)
	r.Log.Error("update failed", "from", cur, "to", st.TargetVersion, "state", st.State, "err", err)
	r.audit(actx, "updater.update_"+st.State, map[string]any{"from": cur, "to": st.TargetVersion, "error": st.Error})
	save()
	return st, err
}

// refreshNotices replaces st.Notices with the engine's notices for the running version (engines without notices:
// none) and the notice about the updater itself (updaternotice.go).
func (r *Runner) refreshNotices(ctx context.Context, st *Status, running string) {
	st.Notices = nil
	if n, ok := r.Engine.(Noticer); ok {
		st.Notices = n.Notices(ctx, running, st)
	}
	if n, ok := r.updaterNotice(st, running); ok {
		st.Notices = append(st.Notices, n)
		r.Log.Warn(n.Message, "engine", r.Engine.Name())
	}
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
