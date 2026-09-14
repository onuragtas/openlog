package updater

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	lib "github.com/onuragtas/openlog/libs/release"

	"github.com/onuragtas/openlog/internal/updatemsg"
	"github.com/onuragtas/openlog/internal/version"
)

// Notice is an operator hint shown with the updater status (Settings → Version and updates), e.g. compose files
// older than the running version. Message is the English text of Code (internal/updatemsg).
type Notice struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Params  map[string]string `json:"params,omitempty"`
}

func newNotice(code string, params updatemsg.Params) Notice {
	return Notice{Code: code, Message: updatemsg.Format(code, params), Params: params}
}

// Noticer is implemented by engines that report installation notices; the runner refreshes Status.Notices with it
// on every run.
type Noticer interface {
	Notices(ctx context.Context, running string, st *Status) []Notice
}

// composeVersionRe finds the version marker of deploy/compose/docker-compose.yml (stamped by `make release-prepare`).
var composeVersionRe = regexp.MustCompile(`(?m)^x-openlog-compose-version:\s*["']?([0-9A-Za-z.+-]+)["']?\s*(?:#.*)?$`)

const composeConfigHashLabel = "com.docker.compose.config-hash"

// Notices implements Noticer: compose files older than the running version, and compose changes of an installed
// bundle that still wait for `docker compose up -d` or a restart. Nothing is changed on disk.
func (e *ComposeEngine) Notices(ctx context.Context, running string, st *Status) []Notice {
	var out []Notice
	if n, ok := e.outdatedNotice(running); ok {
		out = append(out, n)
	}
	if n, ok := e.pendingNotice(ctx, st); ok {
		out = append(out, n)
	}
	return out
}

func (e *ComposeEngine) outdatedNotice(running string) (Notice, bool) {
	if e.Cfg.ComposeDir == "" {
		return Notice{}, false
	}
	runV, err := lib.ParseVersion(running)
	if err != nil || strings.Contains(running, "dev") {
		return Notice{}, false
	}
	bundle, err := e.installedBundleVersion()
	if err != nil {
		return Notice{}, false
	}
	filesVersion, code := bundle, updatemsg.ComposeOutdatedBundle
	if bundle == "" {
		b, err := os.ReadFile(filepath.Join(e.Cfg.ComposeDir, "docker-compose.yml"))
		if err != nil {
			e.Log.Debug("compose version check skipped: no docker-compose.yml in the compose directory", "dir", e.Cfg.ComposeDir, "err", err)
			return Notice{}, false
		}
		code = updatemsg.ComposeOutdated
		if m := composeVersionRe.FindSubmatch(b); m != nil {
			filesVersion = string(m[1])
		}
	}
	params := updatemsg.Params{"running_version": running}
	if filesVersion == "" {
		// No marker: the files predate version markers, i.e. the release of this updater.
		self := e.SelfVersion
		if self == "" {
			self = version.Version
		}
		selfV, err := lib.ParseVersion(self)
		if err != nil || strings.Contains(self, "dev") || lib.Compare(runV, selfV) < 0 {
			return Notice{}, false
		}
		params["files_version"] = "< " + self
	} else {
		filesV, err := lib.ParseVersion(filesVersion)
		if err != nil || lib.Compare(filesV, runV) >= 0 {
			return Notice{}, false
		}
		params["files_version"] = filesVersion
	}
	n := newNotice(code, params)
	e.Log.Warn(n.Message, "compose_dir", e.Cfg.ComposeDir)
	return n, true
}

// recordPending stores the changes of an installed bundle that the updater did not apply.
func (e *ComposeEngine) recordPending(ctx context.Context, st *Status, b *composeBundle) {
	if len(b.Pending) == 0 {
		return
	}
	cc := &ComposeChanges{Version: b.Version, InstalledAt: e.now()}
	for _, p := range b.Pending {
		if p.Reason == "definition" {
			p.ConfigHash = e.configHash(ctx, p.Service)
		}
		cc.Services = append(cc.Services, p)
	}
	// Changes of an earlier bundle that were not applied yet stay pending.
	if prev := st.ComposeChanges; prev != nil {
		for _, p := range prev.Services {
			if !slices.ContainsFunc(cc.Services, func(q PendingService) bool { return q.Service == p.Service }) {
				cc.Services = append(cc.Services, p)
			}
		}
	}
	st.ComposeChanges = cc
	e.Log.Warn("compose changes are not applied by the updater: re-run install-server.sh (or docker compose up -d)",
		"version", b.Version, "services", pendingNames(cc.Services))
}

// pendingNotice reports the pending services and forgets the ones that were recreated (definition: a different
// compose config hash) or restarted (files) since the bundle was installed.
func (e *ComposeEngine) pendingNotice(ctx context.Context, st *Status) (Notice, bool) {
	cc := st.ComposeChanges
	if cc == nil {
		return Notice{}, false
	}
	var keep []PendingService
	for _, p := range cc.Services {
		if e.applied(ctx, p, cc.InstalledAt) {
			e.Log.Info("pending compose change applied", "service", p.Service, "reason", p.Reason)
			continue
		}
		keep = append(keep, p)
	}
	if len(keep) == 0 {
		st.ComposeChanges = nil
		return Notice{}, false
	}
	cc.Services = keep
	return newNotice(updatemsg.ComposeChangesPending, updatemsg.Params{"version": cc.Version, "services": strings.Join(pendingNames(keep), ", ")}), true
}

func pendingNames(ps []PendingService) []string {
	var out []string
	for _, p := range ps {
		if !slices.Contains(out, p.Service) {
			out = append(out, p.Service)
		}
	}
	return out
}

func (e *ComposeEngine) configHash(ctx context.Context, service string) string {
	cs, err := e.Docker.ListContainers(ctx, map[string]string{labelProject: e.Cfg.Project, labelService: service})
	if err != nil {
		return ""
	}
	for _, c := range cs {
		if c.Labels[labelOneoff] != "True" && !strings.HasSuffix(c.Name(), preUpdateSuffix) {
			return c.Labels[composeConfigHashLabel]
		}
	}
	return ""
}

// applied: every container of the service was started after installedAt and, for definition changes, carries a
// config hash other than the recorded one (docker compose recreated it from the new files).
func (e *ComposeEngine) applied(ctx context.Context, p PendingService, installedAt time.Time) bool {
	cs, err := e.Docker.ListContainers(ctx, map[string]string{labelProject: e.Cfg.Project, labelService: p.Service})
	if err != nil {
		return false
	}
	n := 0
	for _, c := range cs {
		if c.Labels[labelOneoff] == "True" || strings.HasSuffix(c.Name(), preUpdateSuffix) {
			continue
		}
		n++
		if p.Reason == "definition" {
			if p.ConfigHash == "" || c.Labels[composeConfigHashLabel] == p.ConfigHash {
				return false
			}
			continue
		}
		full, err := e.Docker.InspectContainer(ctx, c.ID)
		if err != nil {
			return false
		}
		started, err := time.Parse(time.RFC3339Nano, full.State.StartedAt)
		if err != nil || !started.After(installedAt) {
			return false
		}
	}
	return n > 0
}
