// Command e2e is the helper of the self-update end-to-end scenario (../run.sh, ../scenario.sh). It
// is not shipped. Subcommands:
//
//	e2e keygen  -out DIR                              test signing key (DIR/key.seed, DIR/key.pub)
//	e2e release -seed F -version V -arch A -binary B -out DIR -base-url U [-floor V] [-min-upgrade-from V] [-corrupt]
//	e2e serve   -listen ADDR -releases DIR -arch A   fake ingest: OTLP sink, release files, scripted sync
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/update"
	lib "github.com/onuragtas/openlog/libs/release"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: e2e keygen|release|serve ...")
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2:])
	case "release":
		err = release(os.Args[2:])
	case "serve":
		err = serve(os.Args[2:])
	default:
		err = fmt.Errorf("unknown subcommand %q", os.Args[1])
	}
	if err != nil {
		log.Fatal(err)
	}
}

func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", "", "output directory")
	fs.Parse(args)
	pub, seed, err := lib.GenerateKey()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "key.seed"), []byte(seed+"\n"), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*out, "key.pub"), []byte(pub+"\n"), 0o644)
}

func release(args []string) error {
	fs := flag.NewFlagSet("release", flag.ExitOnError)
	seedFile := fs.String("seed", "", "signing seed file")
	version := fs.String("version", "", "release version")
	arch := fs.String("arch", "", "linux architecture")
	binary := fs.String("binary", "", "agent binary")
	out := fs.String("out", "", "releases directory")
	baseURL := fs.String("base-url", "", "URL the archives are served under")
	floor := fs.String("floor", "", "compatibility.rollback_floor")
	minFrom := fs.String("min-upgrade-from", "", "compatibility.min_upgrade_from")
	corrupt := fs.Bool("corrupt", false, "flip a byte of the archive after signing (sha256 mismatch, same size)")
	fs.Parse(args)

	seed, err := os.ReadFile(*seedFile)
	if err != nil {
		return err
	}
	priv, err := lib.PrivateKeyFromSeed(string(seed))
	if err != nil {
		return err
	}
	bin, err := os.ReadFile(*binary)
	if err != nil {
		return err
	}
	top := update.TopDir(*version, "linux", *arch)
	archive, err := tarball(top, map[string]fileEntry{
		update.BinaryName:                {bin, 0o755},
		"LICENSE":                        {[]byte("Apache License 2.0\n"), 0o644},
		"README.md":                      {[]byte("# openlog-infra-agent " + *version + "\n"), 0o644},
		"packaging/config.example.yaml":  {[]byte("license_key: \"\"\n"), 0o644},
		"packaging/systemd/example.unit": {[]byte("[Service]\n"), 0o644},
	})
	if err != nil {
		return err
	}
	sum := sha256.Sum256(archive)
	name := top + ".tar.gz"
	m := lib.Manifest{
		Schema: 1, Product: lib.Product, Version: *version, Channel: lib.ChannelStable,
		ReleasedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		NotesURL:   "https://github.com/onuragtas/openlog/releases/tag/v" + *version,
		Compatibility: lib.Compatibility{
			MinBackendForAgent: "0.1.0", OldestSupportedAgent: "0.1.0",
			MinUpgradeFrom: *minFrom, RollbackFloor: *floor,
		},
		Artifacts: []lib.Artifact{{
			Component: lib.ComponentInfraAgent, OS: "linux", Arch: *arch, Format: lib.FormatTarGz,
			Name: name, URL: strings.TrimRight(*baseURL, "/") + "/" + name,
			SHA256: hex.EncodeToString(sum[:]), Size: int64(len(archive)),
		}},
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if _, err := lib.ParseManifest(data); err != nil {
		return err
	}
	if *corrupt {
		archive[len(archive)/2] ^= 0xff
	}
	dir := filepath.Join(*out, *version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, name), archive, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, update.ManifestFile), data, 0o644); err != nil {
		return err
	}
	sig := lib.SignatureLine(data, priv) + "\n"
	if err := os.WriteFile(filepath.Join(dir, update.SignatureFile), []byte(sig), 0o644); err != nil {
		return err
	}
	fmt.Printf("release %s: %s (%d bytes, sha256 %s, corrupt=%v)\n", *version, name, len(archive), m.Artifacts[0].SHA256, *corrupt)
	return nil
}

type fileEntry struct {
	data []byte
	mode int64
}

func tarball(top string, files map[string]fileEntry) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	mtime := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	dirs := map[string]bool{}
	addDir := func(d string) error {
		if dirs[d] {
			return nil
		}
		dirs[d] = true
		return tw.WriteHeader(&tar.Header{Name: d + "/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: mtime})
	}
	if err := addDir(top); err != nil {
		return nil, err
	}
	for _, name := range []string{update.BinaryName, "LICENSE", "README.md", "packaging/config.example.yaml", "packaging/systemd/example.unit"} {
		f := files[name]
		parts := strings.Split(name, "/")
		for i := 1; i < len(parts); i++ {
			if err := addDir(top + "/" + strings.Join(parts[:i], "/")); err != nil {
				return nil, err
			}
		}
		if err := tw.WriteHeader(&tar.Header{Name: top + "/" + name, Typeflag: tar.TypeReg, Mode: f.mode, Size: int64(len(f.data)), ModTime: mtime}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(f.data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// step is one stage of the scripted scenario. Steps without an action send no instruction (the
// host-side script acts); a gated step sends its instruction once <gates>/<gate> exists.
type step struct {
	name    string
	action  string
	version string
	tamper  bool
	gate    string
	done    func(r update.SyncRequest) bool
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:8080", "listen address")
	releases := fs.String("releases", "", "releases directory")
	arch := fs.String("arch", "", "linux architecture")
	gates := fs.String("gates", "", "directory of gate files created by scenario.sh")
	fs.Parse(args)

	logf := func(format string, a ...any) {
		fmt.Printf("%s server: %s\n", time.Now().UTC().Format("15:04:05.000"), fmt.Sprintf(format, a...))
	}
	staged := func(r update.SyncRequest) bool {
		return r.Agent.UpdateMode == update.ModeStaged && r.Agent.UpdateNotice == "" && r.Agent.UpdateCapable
	}
	failedWith := func(running, version, text string) func(update.SyncRequest) bool {
		return func(r update.SyncRequest) bool {
			return r.Agent.Version == running && r.Update.State == update.StateFailed && r.Update.ToVersion == version && strings.Contains(r.Update.Error, text)
		}
	}
	steps := []step{
		{name: "legacy unit: binary-only update 0.9.0 -> 0.9.1, reported as unit outdated", action: update.ActionUpgrade, version: "0.9.1",
			done: func(r update.SyncRequest) bool {
				return r.Agent.Version == "0.9.1" && r.Update.State == update.StateSucceeded && r.Update.ToVersion == "0.9.1" &&
					r.Agent.UpdateMode == update.ModeLegacy && strings.Contains(r.Agent.UpdateNotice, "unit outdated")
			}},
		{name: "after the one-time bootstrap the privileged pre-start step runs (staged mode)",
			done: func(r update.SyncRequest) bool {
				return r.Agent.Version == "0.9.1" && staged(r) && r.Reconcile != nil && r.Reconcile.Docker == update.DockerNoGroup
			}},
		{name: "staged update 0.9.1 -> 0.9.2 with a new unit and docker group, confirmed", action: update.ActionUpgrade, version: "0.9.2", gate: "docker",
			done: func(r update.SyncRequest) bool {
				return r.Agent.Version == "0.9.2" && r.Update.State == update.StateSucceeded && r.Update.ToVersion == "0.9.2" && staged(r) &&
					r.Reconcile != nil && (r.Reconcile.Docker == update.DockerAdded || r.Reconcile.Docker == update.DockerMember)
			}},
		{name: "broken 0.9.3 installed by -apply, fails to start, rolled back to 0.9.2 by -apply", action: update.ActionUpgrade, version: "0.9.3",
			done: func(r update.SyncRequest) bool {
				return r.Agent.Version == "0.9.2" && r.Update.State == update.StateRolledBack && r.Update.ToVersion == "0.9.3" && staged(r)
			}},
		{name: "tampered manifest (0.9.4) rejected", action: update.ActionUpgrade, version: "0.9.4", tamper: true,
			done: failedWith("0.9.2", "0.9.4", "signature")},
		{name: "archive with sha256 mismatch (0.9.5) rejected", action: update.ActionUpgrade, version: "0.9.5",
			done: failedWith("0.9.2", "0.9.5", "sha256 mismatch")},
		{name: "downgrade to 0.9.0 without rollback action rejected", action: update.ActionUpgrade, version: "0.9.0",
			done: failedWith("0.9.2", "0.9.0", "not newer")},
		{name: "rollback to 0.8.0 below rollback_floor 0.9.1 rejected", action: update.ActionRollback, version: "0.8.0",
			done: failedWith("0.9.2", "0.8.0", "rollback_floor")},
		{name: "update staged by the agent user with a forged manifest rejected by -apply",
			done: failedWith("0.9.2", "0.9.9", "signature")},
		{name: "signed update staged with the archive symlinked out of the state dir rejected by -apply",
			done: failedWith("0.9.2", "0.9.3", "staged archive")},
		{name: "rollback action to 0.9.1 applied by -apply and confirmed", action: update.ActionRollback, version: "0.9.1", gate: "rollback",
			done: func(r update.SyncRequest) bool {
				return r.Agent.Version == "0.9.1" && r.Update.State == update.StateSucceeded && r.Update.ToVersion == "0.9.1" && staged(r)
			}},
	}
	gateOpen := func(s step) bool {
		if s.gate == "" {
			return true
		}
		_, err := os.Stat(filepath.Join(*gates, s.gate))
		return err == nil
	}

	var mu sync.Mutex
	cur := 0
	otlp := map[string]int{}
	logf("scenario step 1/%d: %s", len(steps), steps[0].name)

	mux := http.NewServeMux()
	mux.Handle("/releases/", http.StripPrefix("/releases/", http.FileServer(http.Dir(*releases))))
	for _, p := range []string{"/v1/metrics", "/v1/logs"} {
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			mu.Lock()
			otlp[p]++
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		})
	}
	mux.HandleFunc("/v1/openlog/agent/sync", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("openlog-license-key") != "e2e-license" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req update.SyncRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		rec := "none"
		if req.Reconcile != nil {
			rec = fmt.Sprintf("%s unit_changed=%v docker=%s error=%q", req.Reconcile.Version, req.Reconcile.UnitChanged, req.Reconcile.Docker, req.Reconcile.Error)
		}
		logf("sync  agent=%s method=%s capable=%v mode=%s notice=%q state=%s from=%s to=%s error=%q reconcile=[%s] (otlp metrics=%d logs=%d)",
			req.Agent.Version, req.Agent.InstallMethod, req.Agent.UpdateCapable, req.Agent.UpdateMode, req.Agent.UpdateNotice, req.Update.State,
			req.Update.FromVersion, req.Update.ToVersion, req.Update.Error, rec, otlp["/v1/metrics"], otlp["/v1/logs"])
		if cur < len(steps) && steps[cur].done(req) {
			logf("STEP %d PASSED: %s", cur+1, steps[cur].name)
			cur++
			if cur == len(steps) {
				logf("SCENARIO PASSED")
			} else {
				logf("scenario step %d/%d: %s", cur+1, len(steps), steps[cur].name)
			}
		}
		resp := update.SyncResponse{PollIntervalSeconds: 60, ServerVersion: "0.9.9"}
		if cur < len(steps) && steps[cur].action != "" && gateOpen(steps[cur]) {
			ins, err := instructionFor(steps[cur], cur, *releases, *arch, "http://"+*listen+"/releases")
			if err != nil {
				logf("SCENARIO FAILED: %v", err)
				http.Error(w, err.Error(), 500)
				return
			}
			resp.Update = ins
		}
		json.NewEncoder(w).Encode(resp)
	})
	logf("listening on %s", *listen)
	return http.ListenAndServe(*listen, mux)
}

func instructionFor(s step, idx int, releases, arch, baseURL string) (*update.Instruction, error) {
	dir := filepath.Join(releases, s.version)
	m, err := os.ReadFile(filepath.Join(dir, update.ManifestFile))
	if err != nil {
		return nil, err
	}
	sig, err := os.ReadFile(filepath.Join(dir, update.SignatureFile))
	if err != nil {
		return nil, err
	}
	if s.tamper {
		// A compromised server redirects the artifact to its own host: the signature no longer matches.
		t := bytes.Replace(m, []byte("127.0.0.1:8080"), []byte("203.0.113.66:8080"), 1)
		if bytes.Equal(t, m) {
			return nil, fmt.Errorf("tampering did not change the manifest")
		}
		m = t
	}
	return &update.Instruction{
		Action: s.action, TargetVersion: s.version,
		Manifest: base64.StdEncoding.EncodeToString(m), Signature: string(sig),
		DownloadURL: baseURL + "/" + update.TopDir(s.version, "linux", arch) + ".tar.gz",
		RolloutID:   fmt.Sprintf("e2e-step-%d", idx+1),
	}, nil
}
