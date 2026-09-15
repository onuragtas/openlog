package updater

import (
	"strings"

	lib "github.com/onuragtas/openlog/libs/release"

	"github.com/onuragtas/openlog/internal/updatemsg"
)

// The updater does not update its own container as part of an update (Compose: it runs the image .env selected when
// its container was created; Kubernetes: the CronJob runs the chart's image.tag), so improvements of the updater reach an
// installation only when that container is recreated — or, on Compose, by the self-update (selfupdate.go, D-120).
// These notices tell the operator when the updater is older than the version it keeps running, with the fix for the
// kind of installation.

// updaterOutdatedCoder is implemented by engines to pick the notice code (and so the fix) for their installation.
type updaterOutdatedCoder interface {
	updaterOutdatedCode() string
}

func (e *ComposeEngine) updaterOutdatedCode() string {
	if b, err := e.installedBundleVersion(); err == nil && b != "" {
		return updatemsg.UpdaterOutdatedBundle
	}
	return updatemsg.UpdaterOutdated
}

func (e *K8sEngine) updaterOutdatedCode() string { return updatemsg.UpdaterOutdatedKubernetes }

// isUpdaterNotice reports the notices about the updater itself.
func isUpdaterNotice(code string) bool { return strings.HasPrefix(code, "updater_") }

// updaterNotice returns updater_self_update_failed while the updater is still older than the release its failed
// self-update targeted, else updater_outdated[_bundle|_kubernetes] when it is older than running. Development builds
// never report.
func (r *Runner) updaterNotice(st *Status, running string) (Notice, bool) {
	self := r.selfVersion()
	selfV, err := lib.ParseVersion(self)
	if err != nil || strings.Contains(self, "dev") {
		return Notice{}, false
	}
	if rec := st.SelfUpdate; rec != nil && rec.State == SelfUpdateFailed {
		if tv, err := lib.ParseVersion(rec.TargetVersion); err == nil && selfV.Less(tv) {
			return newNotice(updatemsg.UpdaterSelfUpdateFailed, updatemsg.Params{"version": rec.TargetVersion, "reason": rec.Error}), true
		}
	}
	runV, err := lib.ParseVersion(running)
	if err != nil || strings.Contains(running, "dev") || !selfV.Less(runV) {
		return Notice{}, false
	}
	code := updatemsg.UpdaterOutdated
	if c, ok := r.Engine.(updaterOutdatedCoder); ok {
		code = c.updaterOutdatedCode()
	}
	return newNotice(code, updatemsg.Params{"updater_version": self, "running_version": running}), true
}
