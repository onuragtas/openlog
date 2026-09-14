package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	lib "github.com/onuragtas/openlog/libs/release"

	"github.com/onuragtas/openlog/internal/version"
)

// Compose labels and naming.
const (
	labelProject = "com.docker.compose.project"
	labelService = "com.docker.compose.service"
	labelOneoff  = "com.docker.compose.oneoff"
	labelImage   = "com.docker.compose.image"
	labelUpdater = "dev.openlog.updater"
	// preUpdateSuffix marks the previous container while its replacement is being verified. It is
	// removed only after the new container is healthy, so its presence means "not verified".
	preUpdateSuffix = "-pre-update"
)

// HealthFunc probes one URL and returns the reported version and whether it is ready.
type HealthFunc func(ctx context.Context, url string) (ver string, ready bool, err error)

// ComposeEngine updates a Docker Compose installation through the Docker Engine API.
//
// Containers are recreated from their own inspect data (config, env, labels, mounts, ports,
// networks and aliases) with only the image replaced, so no compose file or CLI is needed and the
// previous container is kept, stopped and renamed, as the rollback target.
type ComposeEngine struct {
	Cfg    Config
	Docker Docker
	Log    *slog.Logger
	Health HealthFunc
	Now    func() time.Time
	// Poll is the health polling interval.
	Poll time.Duration
	// HTTP downloads the compose bundle (default: 2 min timeout).
	HTTP *http.Client
	// SelfVersion is the updater's own version (default version.Version); compose files without a version marker
	// predate it.
	SelfVersion string
}

// Name implements Engine.
func (e *ComposeEngine) Name() string { return "compose" }

func (e *ComposeEngine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

func (e *ComposeEngine) poll() time.Duration {
	if e.Poll > 0 {
		return e.Poll
	}
	return 2 * time.Second
}

// Init detects the compose project from the updater's own container when not configured.
func (e *ComposeEngine) Init(ctx context.Context) error {
	if e.Health == nil {
		e.Health = HTTPHealth(&http.Client{Timeout: 5 * time.Second})
	}
	if e.Cfg.Project != "" {
		return nil
	}
	host, _ := os.Hostname()
	c, err := e.Docker.InspectContainer(ctx, host)
	if err != nil {
		return fmt.Errorf("detect compose project (set OPENLOG_UPDATER_COMPOSE_PROJECT): %w", err)
	}
	labels, _ := c.Config["Labels"].(map[string]any)
	p, _ := labels[labelProject].(string)
	if p == "" {
		return errors.New("detect compose project: the updater container has no compose project label (set OPENLOG_UPDATER_COMPOSE_PROJECT)")
	}
	e.Cfg.Project = p
	return nil
}

// CurrentVersion implements Engine: the version reported by the first health URL.
func (e *ComposeEngine) CurrentVersion(ctx context.Context) (string, error) {
	v, _, err := e.Health(ctx, e.Cfg.VersionURL)
	if err != nil {
		return "", err
	}
	if v == "" {
		return "", fmt.Errorf("%s does not report a version", e.Cfg.VersionURL)
	}
	return v, nil
}

// HTTPHealth probes a URL: ready = HTTP 200; version from X-Openlog-Version or a JSON "version".
func HTTPHealth(client *http.Client) HealthFunc {
	return func(ctx context.Context, url string) (string, bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", false, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", false, err
		}
		defer resp.Body.Close()
		v := resp.Header.Get(version.Header)
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if v == "" {
			var body struct {
				Version string `json:"version"`
			}
			if json.Unmarshal(b, &body) == nil {
				v = body.Version
			}
		}
		return v, resp.StatusCode == http.StatusOK, nil
	}
}

// targets returns the containers of the configured services, excluding one-off and pre-update
// containers.
func (e *ComposeEngine) targets(ctx context.Context) ([]ContainerSummary, error) {
	var out []ContainerSummary
	for _, svc := range e.Cfg.Services {
		cs, err := e.Docker.ListContainers(ctx, map[string]string{labelProject: e.Cfg.Project, labelService: svc})
		if err != nil {
			return nil, err
		}
		for _, c := range cs {
			if c.Labels[labelOneoff] == "True" || strings.HasSuffix(c.Name(), preUpdateSuffix) {
				continue
			}
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no containers for services %v in compose project %q", e.Cfg.Services, e.Cfg.Project)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

type recreated struct {
	name, service, oldID, newID string
	// envChanged are the variables refreshed from .env and the compose files (names only).
	envChanged  []string
	envFallback bool
}

// envFunc refreshes the environment of a recreated container of service (composeenv.go).
type envFunc func(service string, env []string) envMerge

// envPlanner reads .env (plus the settings a staged bundle adds) and the compose files the template container was
// created from (the staged docker-compose.yml replacing the current one). It never fails an update: without a usable
// .env the environment is left unchanged, and with unusable compose files only missing OPENLOG_* variables are added.
func (e *ComposeEngine) envPlanner(template *Container, b *composeBundle, target lib.Version) (envFunc, string) {
	if e.Cfg.EnvFile == "" {
		return nil, "environment unchanged (OPENLOG_UPDATER_ENV_FILE is not set)"
	}
	data, err := os.ReadFile(e.Cfg.EnvFile)
	if err != nil {
		e.Log.Warn("cannot read .env: the container environment is not refreshed", "file", e.Cfg.EnvFile, "err", err)
		return nil, "environment unchanged: " + err.Error()
	}
	if b != nil && len(b.EnvAdditions) > 0 {
		if len(data) > 0 && data[len(data)-1] != '\n' {
			data = append(data, '\n')
		}
		data = append(data, strings.Join(b.EnvAdditions, "\n")+"\n"...)
	}
	dot, err := parseDotenv(data)
	if err != nil {
		e.Log.Warn("cannot parse .env: the container environment is not refreshed", "file", e.Cfg.EnvFile, "err", err)
		return nil, fmt.Sprintf("environment unchanged: %s: %v", e.Cfg.EnvFile, err)
	}
	replace := map[string]string{}
	if b != nil {
		replace["docker-compose.yml"] = filepath.Join(b.Dir, "docker-compose.yml")
	}
	labels, _ := template.Config["Labels"].(map[string]any)
	var proj *composeProject
	paths, err := composeFilePaths(labels, e.Cfg.ComposeDir, replace)
	if err == nil {
		proj, err = loadComposeProject(paths)
	}
	if err != nil {
		e.Log.Warn("compose files not usable for the container environment: only OPENLOG_* variables that the containers lack are added from .env", "err", err)
	}
	// The files the containers are recreated from: the staged bundle (the target's own files) or the files on disk.
	stale := true
	if b != nil {
		stale = false
	} else if fv := e.composeFilesVersion(); fv != "" {
		if v, err := lib.ParseVersion(fv); err == nil && lib.Compare(v, target) >= 0 {
			stale = false
		}
	}
	return func(service string, env []string) envMerge { return mergeServiceEnv(env, service, proj, dot, stale) }, ""
}

// composeFilesVersion is the version of the compose files on disk: .bundle-version, else the
// x-openlog-compose-version marker of docker-compose.yml ("" when unknown).
func (e *ComposeEngine) composeFilesVersion() string {
	if v, err := e.installedBundleVersion(); err == nil && v != "" {
		return v
	}
	if e.Cfg.ComposeDir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(e.Cfg.ComposeDir, "docker-compose.yml"))
	if err != nil {
		return ""
	}
	if m := composeVersionRe.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

// presentServices returns the services of the project that have containers.
func (e *ComposeEngine) presentServices(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	cs, err := e.Docker.ListContainers(ctx, map[string]string{labelProject: e.Cfg.Project})
	if err != nil {
		return out
	}
	for _, c := range cs {
		if s := c.Labels[labelService]; s != "" && c.Labels[labelOneoff] != "True" {
			out[s] = true
		}
	}
	return out
}

// Apply implements Engine.
func (e *ComposeEngine) Apply(ctx context.Context, t *Target, st *Status, save func()) error {
	step := func(name string, fn func() (string, error)) error {
		st.beginStep(name, e.now())
		save()
		detail, err := fn()
		st.endStep(err, detail, e.now())
		save()
		return errStep(name, err)
	}
	targets, err := e.targets(ctx)
	if err != nil {
		st.State = StateFailed
		return err
	}
	template, err := e.Docker.InspectContainer(ctx, targets[0].ID)
	if err != nil {
		st.State = StateFailed
		return err
	}
	from := st.CurrentVersion

	if err := step("backup", func() (string, error) {
		f, err := e.backup(ctx, from)
		st.BackupFile = f
		return f, err
	}); err != nil {
		st.State = StateFailed
		return err
	}
	if err := step("pull", func() (string, error) { return t.Image, e.Docker.PullImage(ctx, t.Image) }); err != nil {
		st.State = StateFailed
		return err
	}
	// install-server.sh installations (.bundle-version): the compose bundle of the target is verified and staged now;
	// the files are replaced only after the new version is healthy.
	var bundle *composeBundle
	installed, verr := e.installedBundleVersion()
	if verr != nil {
		e.Log.Warn("cannot read the compose bundle version", "err", verr)
	}
	if installed != "" {
		if err := step("compose-bundle", func() (string, error) {
			b, detail, err := e.prepareBundle(ctx, t, installed, e.presentServices(ctx))
			bundle = b
			return detail, err
		}); err != nil {
			st.State = StateFailed
			return err
		}
	}
	discardBundle := func() {
		if bundle != nil {
			_ = os.RemoveAll(bundle.Dir)
		}
	}
	envFor, envNote := e.envPlanner(template, bundle, t.Version)
	if err := step("migrate", func() (string, error) { return e.runMigrate(ctx, template, t.Image, "expand", envFor) }); err != nil {
		// Migrations are expand-only for running releases: the current containers are untouched
		// and keep working with whatever part of the migration was applied.
		discardBundle()
		st.State = StateFailed
		return err
	}

	var done []recreated
	applyErr := step("recreate", func() (string, error) {
		changed := map[string]bool{}
		fallback := false
		for _, c := range targets {
			r, err := e.recreate(ctx, c.ID, t.Image, envFor)
			if r.oldID != "" {
				done = append(done, r)
			}
			for _, k := range r.envChanged {
				changed[k] = true
			}
			fallback = fallback || r.envFallback
			if err != nil {
				return "", fmt.Errorf("%s: %w", c.Name(), err)
			}
		}
		detail := fmt.Sprintf("%d container(s)", len(done))
		switch {
		case envNote != "":
			detail += "; " + envNote
		case len(changed) > 0 && fallback:
			detail += "; added from .env (compose files not usable): " + strings.Join(sortedKeys(changed), ", ")
		case len(changed) > 0:
			detail += "; environment from .env and the compose files: " + strings.Join(sortedKeys(changed), ", ")
		}
		return detail, nil
	})
	if applyErr == nil {
		applyErr = step("health", func() (string, error) {
			return "", e.waitHealthy(ctx, t.Version.String(), done)
		})
	}
	if applyErr != nil {
		// The compose files were not touched; the staged bundle is dropped.
		discardBundle()
		rbErr := step("rollback", func() (string, error) {
			if err := e.rollback(ctx, done); err != nil {
				return "", err
			}
			return "", e.waitHealthy(ctx, from, nil)
		})
		st.State = StateRolledBack
		if rbErr != nil {
			st.State = StateRollbackFailed
			return fmt.Errorf("%w; %w", applyErr, rbErr)
		}
		return applyErr
	}

	_ = step("cleanup", func() (string, error) {
		var errs []error
		var details []string
		for _, r := range done {
			if err := e.Docker.RemoveContainer(ctx, r.oldID); err != nil {
				errs = append(errs, err)
			}
		}
		if bundle != nil {
			if err := e.installBundle(bundle, installed); err != nil {
				e.Log.Error("installing the compose bundle failed; the compose files stay at the previous version (re-run install-server.sh)",
					"version", bundle.Version, "previous", installed, "err", err)
				errs = append(errs, fmt.Errorf("compose files %s: %w", bundle.Version, err))
			} else {
				details = append(details, fmt.Sprintf("compose files %s → %s (previous in %s)", installed, bundle.Version, bundlePreviousDir))
				if len(bundle.EnvAdditions) > 0 {
					details = append(details, fmt.Sprintf("%d setting(s) added to .env", len(bundle.EnvAdditions)))
				}
				e.recordPending(ctx, st, bundle)
			}
		}
		if e.Cfg.EnvFile != "" {
			if err := rewriteEnvFile(e.Cfg.EnvFile, "OPENLOG_IMAGE", t.Image); err != nil {
				errs = append(errs, fmt.Errorf("update %s: %w", e.Cfg.EnvFile, err))
			}
		}
		return strings.Join(details, "; "), errors.Join(errs...)
	})
	// Contract migrations need every old instance gone; the old containers are stopped now.
	// Failure is not an update failure: the next openlog-migrate run retries.
	_ = step("contract-migrate", func() (string, error) {
		cur, err := e.Docker.InspectContainer(ctx, done[0].newID)
		if err != nil {
			return "", err
		}
		return e.runMigrate(ctx, cur, t.Image, "contract", nil)
	})
	return nil
}

// backup runs pg_dump in the PostgreSQL container and keeps the newest BackupKeep dumps.
func (e *ComposeEngine) backup(ctx context.Context, from string) (string, error) {
	cs, err := e.Docker.ListContainers(ctx, map[string]string{labelProject: e.Cfg.Project, labelService: e.Cfg.PostgresService})
	if err != nil {
		return "", err
	}
	var pg *ContainerSummary
	for i := range cs {
		if cs[i].State == "running" {
			pg = &cs[i]
			break
		}
	}
	if pg == nil {
		return "", fmt.Errorf("no running %q container in project %q", e.Cfg.PostgresService, e.Cfg.Project)
	}
	if err := os.MkdirAll(e.Cfg.BackupDir, 0o750); err != nil {
		return "", err
	}
	name := fmt.Sprintf("openlog-%s-%s.dump", e.now().Format("20060102T150405Z"), safeName(from))
	path := filepath.Join(e.Cfg.BackupDir, name)
	f, err := os.OpenFile(path+".partial", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return "", err
	}
	bctx, cancel := context.WithTimeout(ctx, time.Hour)
	defer cancel()
	code, stderr, err := e.Docker.Exec(bctx, pg.ID, []string{"pg_dump", "-U", e.Cfg.PGDumpUser, "-d", e.Cfg.PGDumpDatabase, "-Fc"}, f)
	closeErr := f.Close()
	if err == nil && code != 0 {
		err = fmt.Errorf("pg_dump exited %d: %s", code, strings.TrimSpace(stderr))
	}
	if err == nil {
		err = closeErr
	}
	if err == nil {
		if fi, serr := os.Stat(path + ".partial"); serr != nil || fi.Size() == 0 {
			err = errors.New("pg_dump produced an empty backup")
		}
	}
	if err != nil {
		_ = os.Remove(path + ".partial")
		return "", err
	}
	if err := os.Rename(path+".partial", path); err != nil {
		return "", err
	}
	e.pruneBackups()
	return path, nil
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safeName(s string) string { return unsafeChars.ReplaceAllString(s, "_") }

func (e *ComposeEngine) pruneBackups() {
	matches, _ := filepath.Glob(filepath.Join(e.Cfg.BackupDir, "openlog-*.dump"))
	sort.Strings(matches) // names start with a UTC timestamp
	for len(matches) > e.Cfg.BackupKeep {
		if err := os.Remove(matches[0]); err != nil {
			e.Log.Warn("cannot remove old backup", "file", matches[0], "err", err)
		}
		matches = matches[1:]
	}
}

// runMigrate runs openlog-migrate from image as a one-off container with the environment and
// networks of template (a running openlog container), refreshed by envFor when set.
func (e *ComposeEngine) runMigrate(ctx context.Context, template *Container, image, label string, envFor envFunc) (string, error) {
	oldImg, _ := e.Docker.InspectImage(ctx, template.Image)
	env := stripImageDefaults(toStrings(template.Config["Env"]), oldImg, "Env")
	if envFor != nil {
		labels, _ := template.Config["Labels"].(map[string]any)
		svc, _ := labels[labelService].(string)
		env = envFor(svc, env).env
	}
	nets, order := endpointSettings(template, false)
	spec := ContainerSpec{
		Name: fmt.Sprintf("%s-updater-migrate-%d", e.Cfg.Project, e.now().UnixNano()),
		Config: map[string]any{
			"Image": image, "Entrypoint": e.Cfg.MigrateCommand, "Cmd": []string{}, "Env": env,
			"Labels": map[string]string{labelUpdater: "migrate"},
		},
		HostConfig:   map[string]any{"NetworkMode": template.HostConfig["NetworkMode"]},
		Networks:     nets,
		NetworkOrder: order,
	}
	id, err := e.Docker.CreateContainer(ctx, spec)
	if err != nil {
		return "", err
	}
	defer func() { _ = e.Docker.RemoveContainer(context.WithoutCancel(ctx), id) }()
	if err := e.Docker.StartContainer(ctx, id); err != nil {
		return "", err
	}
	mctx, cancel := context.WithTimeout(ctx, e.Cfg.MigrateTimeout)
	defer cancel()
	code, err := e.Docker.WaitContainer(mctx, id)
	logs, _ := e.Docker.ContainerLogs(context.WithoutCancel(ctx), id, 30)
	if err != nil {
		return "", fmt.Errorf("openlog-migrate (%s): %w", label, err)
	}
	if code != 0 {
		return "", fmt.Errorf("openlog-migrate (%s) exited %d: %s", label, code, lastLines(logs, 10))
	}
	return label + " migrations applied", nil
}

// recreate replaces container id by one running image, with the environment refreshed by envFor (nil: unchanged).
// The old container is stopped and renamed "<name>-pre-update"; on any error it is restored before returning.
func (e *ComposeEngine) recreate(ctx context.Context, id, image string, envFor envFunc) (recreated, error) {
	old, err := e.Docker.InspectContainer(ctx, id)
	if err != nil {
		return recreated{}, err
	}
	name := strings.TrimPrefix(old.Name, "/")
	oldImg, err := e.Docker.InspectImage(ctx, old.Image)
	if err != nil && !IsNotFound(err) {
		return recreated{}, err
	}
	newImg, err := e.Docker.InspectImage(ctx, image)
	if err != nil {
		return recreated{}, err
	}
	spec := buildSpec(old, name, image, newImg.ID, oldImg)
	labels, _ := old.Config["Labels"].(map[string]any)
	service, _ := labels[labelService].(string)
	var merged envMerge
	if envFor != nil {
		merged = envFor(service, toStrings(spec.Config["Env"]))
		spec.Config["Env"] = merged.env
		if len(merged.changed) > 0 {
			e.Log.Info("container environment refreshed from .env and the compose files", "name", name,
				"variables", merged.changed, "compose_files_usable", !merged.fallback)
		}
	}

	if err := e.Docker.StopContainer(ctx, old.ID, stopTimeout(old)); err != nil {
		return recreated{}, fmt.Errorf("stop: %w", err)
	}
	r := recreated{name: name, service: service, oldID: old.ID, envChanged: merged.changed, envFallback: merged.fallback}
	if err := e.Docker.RenameContainer(ctx, old.ID, name+preUpdateSuffix); err != nil {
		_ = e.Docker.StartContainer(context.WithoutCancel(ctx), old.ID)
		return recreated{}, fmt.Errorf("rename: %w", err)
	}
	newID, err := e.Docker.CreateContainer(ctx, spec)
	if err != nil {
		return r, fmt.Errorf("create: %w", err)
	}
	r.newID = newID
	if err := e.Docker.StartContainer(ctx, newID); err != nil {
		return r, fmt.Errorf("start: %w", err)
	}
	e.Log.Info("container recreated", "name", name, "image", image)
	return r, nil
}

// rollback removes the new containers and restores the previous ones (in reverse order).
func (e *ComposeEngine) rollback(ctx context.Context, done []recreated) error {
	var errs []error
	for i := len(done) - 1; i >= 0; i-- {
		r := done[i]
		if r.newID != "" {
			if logs, err := e.Docker.ContainerLogs(ctx, r.newID, 20); err == nil && logs != "" {
				e.Log.Warn("logs of the failed container", "name", r.name, "tail", lastLines(logs, 20))
			}
			if err := e.Docker.RemoveContainer(ctx, r.newID); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if err := e.Docker.RenameContainer(ctx, r.oldID, r.name); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := e.Docker.StartContainer(ctx, r.oldID); err != nil {
			errs = append(errs, err)
		}
		e.Log.Info("container restored", "name", r.name)
	}
	return errors.Join(errs...)
}

// Recover implements Engine: a "<name>-pre-update" container means an update was interrupted
// before it was verified, so the replacement (if any) is removed and the previous container is
// restored.
func (e *ComposeEngine) Recover(ctx context.Context) error {
	cs, err := e.Docker.ListContainers(ctx, map[string]string{labelProject: e.Cfg.Project})
	if err != nil {
		return err
	}
	byName := map[string]ContainerSummary{}
	for _, c := range cs {
		byName[c.Name()] = c
	}
	var done []recreated
	for _, c := range cs {
		if !strings.HasSuffix(c.Name(), preUpdateSuffix) {
			continue
		}
		name := strings.TrimSuffix(c.Name(), preUpdateSuffix)
		r := recreated{name: name, oldID: c.ID}
		if n, ok := byName[name]; ok {
			r.newID = n.ID
		}
		e.Log.Warn("found an unverified update; restoring the previous container", "name", name)
		done = append(done, r)
	}
	err = e.rollback(ctx, done)
	if e.Cfg.ComposeDir != "" {
		if _, jerr := os.Stat(filepath.Join(e.Cfg.ComposeDir, bundleJournalFile)); jerr == nil {
			e.Log.Warn("found an interrupted compose bundle replacement; restoring the previous compose files", "dir", e.Cfg.ComposeDir)
			if rerr := e.restoreBundle(); rerr != nil {
				err = errors.Join(err, fmt.Errorf("restore compose files: %w", rerr))
			}
		} else {
			_ = os.RemoveAll(filepath.Join(e.Cfg.ComposeDir, bundleStagingDir))
		}
	}
	return err
}

// waitHealthy waits until every health URL is ready and reports want, failing early when a
// recreated container exits.
func (e *ComposeEngine) waitHealthy(ctx context.Context, want string, containers []recreated) error {
	hctx, cancel := context.WithTimeout(ctx, e.Cfg.HealthTimeout)
	defer cancel()
	var last error
	for {
		last = e.checkOnce(hctx, want, containers)
		if last == nil {
			return nil
		}
		var fatal *fatalError
		if errors.As(last, &fatal) {
			return fatal.err
		}
		select {
		case <-hctx.Done():
			return fmt.Errorf("not healthy within %s: %w", e.Cfg.HealthTimeout, last)
		case <-time.After(e.poll()):
		}
	}
}

type fatalError struct{ err error }

func (f *fatalError) Error() string { return f.err.Error() }

func (e *ComposeEngine) checkOnce(ctx context.Context, want string, containers []recreated) error {
	for _, r := range containers {
		c, err := e.Docker.InspectContainer(ctx, r.newID)
		if err != nil {
			return err
		}
		if c.State.Status == "exited" || c.State.Status == "dead" {
			policy, _ := c.HostConfig["RestartPolicy"].(map[string]any)
			if name, _ := policy["Name"].(string); name == "" || name == "no" {
				return &fatalError{fmt.Errorf("container %s exited with code %d", r.name, c.State.ExitCode)}
			}
		}
	}
	for _, u := range e.Cfg.HealthURLs {
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		v, ready, err := e.Health(rctx, u)
		cancel()
		switch {
		case err != nil:
			return fmt.Errorf("%s: %w", u, err)
		case !ready:
			return fmt.Errorf("%s: not ready (version %q)", u, v)
		case !sameVersion(v, want):
			return fmt.Errorf("%s: reports version %q, want %s", u, v, want)
		}
	}
	return nil
}

// buildSpec derives the new container from the old one. Settings that came from the old image
// (not from the compose file) are dropped so the new image's defaults apply.
func buildSpec(old *Container, name, image, imageID string, oldImg *Image) ContainerSpec {
	cfg := deepCopy(old.Config)
	cfg["Image"] = image
	if h, _ := cfg["Hostname"].(string); len(old.ID) >= 12 && h == old.ID[:12] {
		delete(cfg, "Hostname") // Docker's default hostname is the container id
	}
	cfg["Env"] = stripImageDefaults(toStrings(cfg["Env"]), oldImg, "Env")
	if oldImg != nil {
		for _, k := range []string{"Entrypoint", "Cmd", "WorkingDir", "User", "StopSignal"} {
			if v, ok := cfg[k]; ok && reflect.DeepEqual(v, oldImg.Config[k]) {
				delete(cfg, k)
			}
		}
	}
	if labels, ok := cfg["Labels"].(map[string]any); ok {
		if oldImg != nil {
			imgLabels, _ := oldImg.Config["Labels"].(map[string]any)
			for k, v := range imgLabels {
				if reflect.DeepEqual(labels[k], v) {
					delete(labels, k)
				}
			}
		}
		if _, ok := labels[labelImage]; ok {
			labels[labelImage] = imageID
		}
		labels[labelUpdater+".version-image"] = image
	}
	nets, order := endpointSettings(old, true)
	return ContainerSpec{Name: name, Config: cfg, HostConfig: deepCopy(old.HostConfig), Networks: nets, NetworkOrder: order}
}

// endpointSettings copies the user-defined endpoint settings (aliases, static IPs, links) of
// every network; runtime fields (IP, MAC, ids) are dropped.
func endpointSettings(c *Container, withAliases bool) (map[string]map[string]any, []string) {
	out := map[string]map[string]any{}
	var order []string
	short := ""
	if len(c.ID) >= 12 {
		short = c.ID[:12]
	}
	for name, ep := range c.NetworkSettings.Networks {
		n := map[string]any{}
		if withAliases {
			var aliases []string
			for _, a := range toStrings(ep["Aliases"]) {
				if a != short && a != c.ID {
					aliases = append(aliases, a)
				}
			}
			if len(aliases) > 0 {
				n["Aliases"] = aliases
			}
		}
		for _, k := range []string{"IPAMConfig", "Links", "DriverOpts"} {
			if v, ok := ep[k]; ok && v != nil {
				n[k] = v
			}
		}
		out[name] = n
		order = append(order, name)
	}
	sort.Strings(order)
	if nm, _ := c.HostConfig["NetworkMode"].(string); nm != "" {
		for i, n := range order {
			if n == nm && i > 0 {
				order[0], order[i] = order[i], order[0]
			}
		}
	}
	return out, order
}

func stripImageDefaults(env []string, img *Image, key string) []string {
	if img == nil {
		return env
	}
	defaults := map[string]bool{}
	for _, s := range toStrings(img.Config[key]) {
		defaults[s] = true
	}
	out := []string{}
	for _, s := range env {
		if !defaults[s] {
			out = append(out, s)
		}
	}
	return out
}

func stopTimeout(c *Container) time.Duration {
	if v, ok := c.Config["StopTimeout"].(float64); ok && v > 0 {
		return time.Duration(v) * time.Second
	}
	return 40 * time.Second
}

func toStrings(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func deepCopy(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	b, _ := json.Marshal(m)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return out
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// rewriteEnvFile sets key=value in a compose .env file (appending when missing), so a later
// `docker compose up -d` keeps the updated image.
func rewriteEnvFile(path, key, value string) error {
	return editEnvFile(path, map[string]string{key: value}, nil)
}

// editEnvFile sets variables (appending the missing ones after "# set by openlog-updater") and appends lines. The
// file's mode and owner are kept (.env holds secrets: install-server.sh creates it with mode 0600).
func editEnvFile(path string, set map[string]string, appendLines []string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	found := map[string]bool{}
	for i, l := range lines {
		for key, value := range set {
			if strings.HasPrefix(strings.TrimSpace(l), key+"=") {
				lines[i], found[key] = key+"="+value, true
			}
		}
	}
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	lines = append(lines, appendLines...)
	keys := make([]string, 0, len(set))
	for k := range set {
		if !found[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		lines = append(lines, "# set by openlog-updater", k+"="+set[k])
	}
	out := []byte(strings.Join(append(lines, ""), "\n"))
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(path, out, fi.Mode().Perm()); err == nil {
		return nil
	}
	// A single bind-mounted file cannot be replaced by rename: write in place (the mode is kept).
	return os.WriteFile(path, out, fi.Mode().Perm())
}
