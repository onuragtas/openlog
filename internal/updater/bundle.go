package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	lib "github.com/onuragtas/openlog/libs/release"

	"github.com/onuragtas/openlog/internal/version"
)

// Compose bundle sync (docs/operations/upgrading.md "Compose files", D-111). An install-server.sh installation has
// ComposeDir/.bundle-version. When the updater installs version V it downloads openlog-compose-V.tar.gz from the
// signed manifest (sha256 and size checked), stages it in .bundle-staging, uses its docker-compose.yml for the
// environment of the recreated containers and replaces the files only after the new version is healthy. The replaced
// files are kept in .bundle-previous; a journal (.bundle-swap.json) lets an interrupted replacement be undone.
const (
	bundleVersionFile = ".bundle-version"
	bundleStagingDir  = ".bundle-staging"
	bundlePreviousDir = ".bundle-previous"
	bundleJournalFile = ".bundle-swap.json"
	maxBundleBytes    = 16 << 20
	maxBundleFiles    = 256
)

// composeBundle is a verified, extracted compose bundle waiting to be installed.
type composeBundle struct {
	Version string
	// Dir is the staging directory holding Files.
	Dir string
	// Files are relative slash paths, sorted.
	Files []string
	// EnvAdditions are lines of the new .env.example for variables .env does not set (install-server.sh rule).
	EnvAdditions []string
	// Pending are the services whose changes the updater cannot apply (volumes, ports, mounted files).
	Pending []PendingService
}

// installedBundleVersion returns the content of ComposeDir/.bundle-version ("" without the file).
func (e *ComposeEngine) installedBundleVersion() (string, error) {
	if e.Cfg.ComposeDir == "" {
		return "", nil
	}
	b, err := os.ReadFile(filepath.Join(e.Cfg.ComposeDir, bundleVersionFile))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return strings.TrimSpace(string(b)), err
}

// prepareBundle downloads, verifies and stages the compose bundle of t. A nil bundle with a detail means the compose
// files are not changed by this update (not an install-server.sh installation, sync off, no bundle in the manifest).
func (e *ComposeEngine) prepareBundle(ctx context.Context, t *Target, installed string, present map[string]bool) (*composeBundle, string, error) {
	if e.Cfg.ComposeSync == ComposeSyncOff {
		return nil, fmt.Sprintf("OPENLOG_UPDATER_COMPOSE_SYNC=off: compose files stay at %s", installed), nil
	}
	v := t.Version.String()
	a, ok := t.Manifest.Artifact(lib.ComponentCompose, lib.PlatformAny, lib.PlatformAny, lib.FormatTarGz)
	if !ok || a.Name != lib.ComposeBundleName(v) {
		return nil, fmt.Sprintf("the manifest of %s has no compose bundle: compose files stay at %s (re-run install-server.sh)", v, installed), nil
	}
	data, err := e.download(ctx, a)
	if err != nil {
		return nil, "", err
	}
	dir := filepath.Join(e.Cfg.ComposeDir, bundleStagingDir)
	if err := os.RemoveAll(dir); err != nil {
		return nil, "", err
	}
	files, err := extractBundle(data, v, dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, "", fmt.Errorf("%s: %w", a.Name, err)
	}
	b := &composeBundle{Version: v, Dir: dir, Files: files}
	fail := func(err error) (*composeBundle, string, error) {
		_ = os.RemoveAll(dir)
		return nil, "", fmt.Errorf("%s: %w", a.Name, err)
	}
	newCompose, err := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	if err != nil {
		return fail(err)
	}
	proj, err := parseComposeProject([][]byte{newCompose})
	if err != nil {
		return fail(err)
	}
	if proj.services[ImageComponent] == nil {
		return fail(errors.New("docker-compose.yml has no openlog service"))
	}
	example, err := os.ReadFile(filepath.Join(dir, ".env.example"))
	if err != nil {
		return fail(err)
	}
	if e.Cfg.EnvFile != "" {
		cur, err := os.ReadFile(e.Cfg.EnvFile)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fail(err)
		}
		b.EnvAdditions = envAdditions(cur, example)
	}
	oldCompose, _ := os.ReadFile(filepath.Join(e.Cfg.ComposeDir, "docker-compose.yml"))
	b.Pending, err = pendingChanges(oldCompose, newCompose, e.Cfg.ComposeDir, dir, files, present)
	if err != nil {
		return fail(err)
	}
	detail := fmt.Sprintf("%s verified (sha256 %s), %d files", a.Name, a.SHA256[:12], len(files))
	return b, detail, nil
}

func (e *ComposeEngine) download(ctx context.Context, a lib.Artifact) ([]byte, error) {
	if a.Size <= 0 || a.Size > maxBundleBytes {
		return nil, fmt.Errorf("%s: size %d is not within 1..%d bytes", a.Name, a.Size, maxBundleBytes)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "openlog-updater/"+version.String())
	client := e.HTTP
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", a.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", a.URL, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, a.Size+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", a.Name, err)
	}
	if int64(len(b)) != a.Size {
		return nil, fmt.Errorf("%s: size %d, manifest says %d", a.Name, len(b), a.Size)
	}
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); got != a.SHA256 {
		return nil, fmt.Errorf("%s: sha256 %s does not match the signed manifest (%s)", a.Name, got, a.SHA256)
	}
	return b, nil
}

// extractBundle unpacks openlog-compose-<v>/ into dest: regular files and directories only, no path outside the
// directory, no .env, .bundle-*, backups/ or releases/ entries.
func extractBundle(data []byte, v, dest string) ([]string, error) {
	prefix := "openlog-compose-" + v + "/"
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var files []string
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if hdr.Name == prefix || hdr.Name+"/" == prefix {
			continue
		}
		rel, ok := strings.CutPrefix(hdr.Name, prefix)
		rel = strings.TrimSuffix(rel, "/")
		if !ok || rel == "" || strings.Contains(rel, `\`) || !filepath.IsLocal(filepath.FromSlash(rel)) {
			return nil, fmt.Errorf("entry %q is outside %s", hdr.Name, prefix)
		}
		top, _, _ := strings.Cut(rel, "/")
		if top == ".env" || strings.HasPrefix(top, ".bundle") || top == "backups" || top == "releases" {
			return nil, fmt.Errorf("entry %q is not allowed in a compose bundle", hdr.Name)
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return nil, err
			}
		case tar.TypeReg:
			total += hdr.Size
			if len(files) >= maxBundleFiles || total > 4*maxBundleBytes {
				return nil, errors.New("too many or too large files")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return nil, err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
			if err != nil {
				return nil, err
			}
			_, err = io.Copy(f, io.LimitReader(tr, hdr.Size))
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return nil, err
			}
			files = append(files, rel)
		default:
			return nil, fmt.Errorf("entry %q: only regular files and directories are allowed", hdr.Name)
		}
	}
	sort.Strings(files)
	for _, want := range []string{"docker-compose.yml", ".env.example"} {
		if _, found := sort.Find(len(files), func(i int) int { return strings.Compare(want, files[i]) }); !found {
			return nil, fmt.Errorf("%s is missing", want)
		}
	}
	return files, nil
}

var (
	envAssignRe = regexp.MustCompile(`^([A-Z][A-Za-z0-9_]*)=`)
	// Credentials are never taken from the example: a missing value means the compose default has been in use since
	// the volumes were created (same rule as install-server.sh).
	envAdditionSkip = regexp.MustCompile(`^(OPENLOG_POSTGRES_PASSWORD|OPENLOG_CLICKHOUSE_.*|OPENLOG_SECRETS_KEY|OPENLOG_BOOTSTRAP_.*|OPENLOG_LICENSE_KEYS|OPENLOG_LOADGEN_KEY|OPENLOG_IMAGE)$`)
)

// envAdditions returns the active lines of example whose variable env does not set.
func envAdditions(env, example []byte) []string {
	have := map[string]bool{}
	for _, l := range strings.Split(string(env), "\n") {
		if m := envAssignRe.FindStringSubmatch(strings.TrimRight(l, "\r")); m != nil {
			have[m[1]] = true
		}
	}
	var out []string
	for _, l := range strings.Split(string(example), "\n") {
		l = strings.TrimRight(l, "\r")
		m := envAssignRe.FindStringSubmatch(l)
		if m == nil || have[m[1]] || envAdditionSkip.MatchString(m[1]) {
			continue
		}
		have[m[1]] = true
		out = append(out, l)
	}
	return out
}

// PendingService is a service whose new compose definition or mounted files are not applied by the updater.
type PendingService struct {
	Service string `json:"service"`
	// Reason: "definition" (volumes, ports, … changed: `docker compose up -d` recreates it) or "files" (a mounted
	// file changed: a restart applies it).
	Reason string `json:"reason"`
	// ConfigHash is the com.docker.compose.config-hash of the service's containers when the bundle was installed.
	ConfigHash string `json:"config_hash,omitempty"`
}

// ComposeChanges records compose changes of an installed bundle that still wait for `docker compose up -d`
// (install-server.sh) or a restart.
type ComposeChanges struct {
	Version     string           `json:"version"`
	InstalledAt time.Time        `json:"installed_at"`
	Services    []PendingService `json:"services"`
}

// pendingChanges compares the current and the new compose file (services with containers, and new services without
// profiles) beyond environment and env_file, and finds services mounting a file whose content changes.
func pendingChanges(oldCompose, newCompose []byte, curDir, newDir string, files []string, present map[string]bool) ([]PendingService, error) {
	var o, n map[string]any
	if err := yaml.Unmarshal(newCompose, &n); err != nil {
		return nil, err
	}
	if len(oldCompose) > 0 {
		if err := yaml.Unmarshal(oldCompose, &o); err != nil {
			o = nil // an unreadable old file: every present service counts as changed
		}
	}
	services := func(m map[string]any) map[string]any {
		s, _ := m["services"].(map[string]any)
		return s
	}
	strip := func(v any) any {
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		c := map[string]any{}
		for k, x := range m {
			if k != "environment" && k != "env_file" {
				c[k] = x
			}
		}
		return c
	}
	os_, ns := services(o), services(n)
	names := map[string]bool{}
	for k := range os_ {
		names[k] = true
	}
	for k := range ns {
		names[k] = true
	}
	var out []PendingService
	seen := map[string]bool{}
	add := func(svc, reason string) {
		if !seen[svc] {
			seen[svc] = true
			out = append(out, PendingService{Service: svc, Reason: reason})
		}
	}
	for _, name := range sortedKeys(names) {
		ov, inOld := os_[name]
		nv, inNew := ns[name]
		_, hasProfiles := asMap(nv)["profiles"]
		switch {
		case present[name] && !reflect.DeepEqual(strip(ov), strip(nv)):
			add(name, "definition")
		case !present[name] && inNew && !inOld && !hasProfiles:
			add(name, "definition") // a new default service: `docker compose up -d` starts it
		}
	}
	// Mounted files whose content changes (a replaced file reaches a container at its next start).
	var changed []string
	for _, f := range files {
		if f == "docker-compose.yml" || f == ".env.example" || strings.HasSuffix(f, ".md") {
			continue
		}
		cur, err := os.ReadFile(filepath.Join(curDir, filepath.FromSlash(f)))
		if err == nil {
			nb, _ := os.ReadFile(filepath.Join(newDir, filepath.FromSlash(f)))
			if bytes.Equal(cur, nb) {
				continue
			}
		}
		changed = append(changed, f)
	}
	for _, name := range sortedKeys(names) {
		if !present[name] || seen[name] {
			continue
		}
		vols, _ := asMap(ns[name])["volumes"].([]any)
		for _, v := range vols {
			entry := fmt.Sprint(v)
			if m, ok := v.(map[string]any); ok {
				entry = fmt.Sprint(m["source"])
			}
			for _, f := range changed {
				if strings.Contains(entry, f) {
					add(name, "files")
				}
			}
		}
	}
	return out, nil
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type bundleJournal struct {
	Version  string   `json:"version"`
	Previous string   `json:"previous"`
	Files    []string `json:"files"`
	// Existed are the files that existed before (restored from .bundle-previous); the others are removed on restore.
	Existed []string `json:"existed"`
}

// installBundle replaces the compose files by the staged bundle: the current files go to .bundle-previous, every file
// is renamed into place, the new .env settings are appended, .bundle-version is written last. On error the previous
// files are restored.
func (e *ComposeEngine) installBundle(b *composeBundle, previous string) error {
	dir := e.Cfg.ComposeDir
	prev := filepath.Join(dir, bundlePreviousDir)
	prevTmp := prev + ".tmp"
	if err := os.RemoveAll(prevTmp); err != nil {
		return err
	}
	j := bundleJournal{Version: b.Version, Previous: previous, Files: b.Files}
	for _, rel := range append(slicesClone(b.Files), bundleVersionFile) {
		src := filepath.Join(dir, filepath.FromSlash(rel))
		fi, err := os.Lstat(src)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", src)
		}
		if err := copyFile(src, filepath.Join(prevTmp, filepath.FromSlash(rel))); err != nil {
			return fmt.Errorf("keep previous %s: %w", rel, err)
		}
		if rel != bundleVersionFile {
			j.Existed = append(j.Existed, rel)
		}
	}
	if err := os.RemoveAll(prev); err != nil {
		return err
	}
	if err := os.Rename(prevTmp, prev); err != nil {
		return err
	}
	jb, _ := json.MarshalIndent(j, "", "  ")
	if err := writeFileAtomic(filepath.Join(dir, bundleJournalFile), jb, 0o644); err != nil {
		return err
	}
	err := func() error {
		for _, rel := range b.Files {
			dst := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			if err := os.Rename(filepath.Join(b.Dir, filepath.FromSlash(rel)), dst); err != nil {
				return err
			}
		}
		if len(b.EnvAdditions) > 0 && e.Cfg.EnvFile != "" {
			if err := editEnvFile(e.Cfg.EnvFile, nil, append([]string{"# added by openlog-updater from .env.example " + b.Version}, b.EnvAdditions...)); err != nil {
				return fmt.Errorf("add settings to %s: %w", e.Cfg.EnvFile, err)
			}
		}
		return writeFileAtomic(filepath.Join(dir, bundleVersionFile), []byte(b.Version+"\n"), 0o644)
	}()
	if err != nil {
		if rerr := e.restoreBundle(); rerr != nil {
			return fmt.Errorf("%w; restoring the previous compose files failed: %w", err, rerr)
		}
		return fmt.Errorf("%w (previous compose files %s restored)", err, previous)
	}
	_ = os.Remove(filepath.Join(dir, bundleJournalFile))
	_ = os.RemoveAll(b.Dir)
	return nil
}

// restoreBundle undoes an interrupted or failed installBundle from its journal (no journal: nothing to do).
func (e *ComposeEngine) restoreBundle() error {
	dir := e.Cfg.ComposeDir
	jb, err := os.ReadFile(filepath.Join(dir, bundleJournalFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var j bundleJournal
	if err := json.Unmarshal(jb, &j); err != nil {
		return fmt.Errorf("%s: %w", bundleJournalFile, err)
	}
	prev := filepath.Join(dir, bundlePreviousDir)
	existed := map[string]bool{}
	for _, f := range j.Existed {
		existed[f] = true
	}
	var errs []error
	for _, rel := range j.Files {
		if !filepath.IsLocal(filepath.FromSlash(rel)) {
			errs = append(errs, fmt.Errorf("journal entry %q", rel))
			continue
		}
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if existed[rel] {
			if err := copyFile(filepath.Join(prev, filepath.FromSlash(rel)), dst); err != nil {
				errs = append(errs, err)
			}
		} else if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	pv := filepath.Join(prev, bundleVersionFile)
	if _, err := os.Stat(pv); err == nil {
		if err := copyFile(pv, filepath.Join(dir, bundleVersionFile)); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	_ = os.RemoveAll(filepath.Join(dir, bundleStagingDir))
	return os.Remove(filepath.Join(dir, bundleJournalFile))
}

func slicesClone(s []string) []string { return append([]string(nil), s...) }

// copyFile copies src to dst atomically (temporary file + rename), keeping the mode.
func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(dst, b, fi.Mode().Perm())
}

// writeFileAtomic writes a temporary file next to path and renames it over path. The mode (and, for an existing
// file, its owner) is kept.
func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	var uid, gid = -1, -1
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			uid, gid = int(st.Uid), int(st.Gid)
		}
	}
	tmp := path + ".updater-tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if uid >= 0 {
		_ = os.Lchown(tmp, uid, gid)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
