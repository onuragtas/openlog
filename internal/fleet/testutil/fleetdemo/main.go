// Command fleetdemo drives a local fleet update demo with test keys (never for real releases):
//
//	fleetdemo release -dir DIR -keys FILE [-seed FILE] [-extra 0.4.1]
//	    writes a signed release mirror (0.3.0, 0.4.0 and -extra versions) and the trusted keys file
//	fleetdemo agents -ingest http://127.0.0.1:24318 -key dev-license-key -n 50 -keys FILE [-fail-after 5 -fail-count 4]
//	    simulates agents that sync, verify the signed manifest, download the artifact, check its
//	    sha256 and report success or failure (updates number fail-after+1 … fail-after+fail-count fail)
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
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
	"os/signal"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/onuragtas/openlog/internal/fleet"
	"github.com/onuragtas/openlog/internal/fleet/testutil"
	"github.com/onuragtas/openlog/internal/release"
	lib "github.com/onuragtas/openlog/libs/release"
)

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: fleetdemo release|agents [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "release":
		releaseCmd(os.Args[2:])
	case "agents":
		agentsCmd(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, "usage: fleetdemo release|agents [flags]")
		os.Exit(2)
	}
}

func releaseCmd(args []string) {
	fs := flag.NewFlagSet("release", flag.ExitOnError)
	dir := fs.String("dir", "", "mirror directory to write")
	keys := fs.String("keys", "", "trusted keys file to write")
	seedFile := fs.String("seed", "", "signing seed file (created when missing, reused otherwise)")
	extra := fs.String("extra", "", "comma-separated additional versions (min_upgrade_from 0.3.0)")
	_ = fs.Parse(args)
	if *dir == "" || *keys == "" {
		log.Fatal("-dir and -keys are required")
	}
	seed := make([]byte, ed25519.SeedSize)
	if *seedFile != "" {
		if b, err := os.ReadFile(*seedFile); err == nil && len(b) == ed25519.SeedSize {
			seed = b
		} else {
			_, _ = rand.Read(seed)
			if err := os.WriteFile(*seedFile, seed, 0o600); err != nil {
				log.Fatal(err)
			}
		}
	} else {
		_, _ = rand.Read(seed)
	}
	s := testutil.SignerFromSeed(seed)
	specs := []testutil.ReleaseSpec{
		{Version: "0.3.0", MinUpgradeFrom: "0.2.0", RollbackFloor: "0.2.0", OldestSupportedAgent: "0.2.0", ReleasedAt: time.Now().Add(-30 * 24 * time.Hour)},
		{Version: "0.4.0", MinUpgradeFrom: "0.3.0", RollbackFloor: "0.3.0", OldestSupportedAgent: "0.3.0", ReleasedAt: time.Now().Add(-time.Hour)},
	}
	for _, v := range strings.Split(*extra, ",") {
		if v = strings.TrimSpace(v); v != "" {
			specs = append(specs, testutil.ReleaseSpec{Version: v, MinUpgradeFrom: "0.3.0", RollbackFloor: "0.3.0", OldestSupportedAgent: "0.3.0", ReleasedAt: time.Now()})
		}
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		log.Fatal(err)
	}
	if err := testutil.WriteMirror(*dir, s, specs, time.Now()); err != nil {
		log.Fatal(err)
	}
	if err := testutil.WriteKeysFile(*keys, s); err != nil {
		log.Fatal(err)
	}
	var vs []string
	for _, sp := range specs {
		vs = append(vs, sp.Version)
	}
	log.Printf("signed mirror in %s (releases %s, key id %s)", *dir, strings.Join(vs, ", "), lib.KeyID(s.PublicKey()))
}

type agent struct {
	id, name string
	rep      fleet.HostReport
	pending  *fleet.UpdateJSON
}

type syncReq struct {
	HostID   string `json:"host_id"`
	HostName string `json:"host_name"`
	Agent    struct {
		Name          string `json:"name"`
		Version       string `json:"version"`
		Commit        string `json:"commit"`
		OS            string `json:"os"`
		Arch          string `json:"arch"`
		InstallMethod string `json:"install_method"`
		UpdateCapable bool   `json:"update_capable"`
	} `json:"agent"`
	Update struct {
		State       string `json:"state"`
		FromVersion string `json:"from_version"`
		ToVersion   string `json:"to_version"`
		Error       string `json:"error"`
		ChangedAt   string `json:"changed_at"`
	} `json:"update"`
}

func agentsCmd(args []string) {
	fs := flag.NewFlagSet("agents", flag.ExitOnError)
	ingest := fs.String("ingest", "http://127.0.0.1:24318", "ingest OTLP/HTTP base URL")
	key := fs.String("key", "dev-license-key", "license key")
	n := fs.Int("n", 50, "agents")
	version := fs.String("version", "0.3.0", "initial agent version")
	interval := fs.Duration("interval", 3*time.Second, "sync interval (demo override of poll_interval_seconds)")
	keysFile := fs.String("keys", "", "trusted release keys file")
	failAfter := fs.Int64("fail-after", -1, "updates that succeed before failures start (-1: never fail)")
	failCount := fs.Int64("fail-count", 0, "number of failing updates after -fail-after")
	failFile := fs.String("fail-file", "", "while this file exists every update fails")
	prefix := fs.String("prefix", "demo", "host name prefix")
	_ = fs.Parse(args)
	keys, err := release.TrustedKeys(*keysFile)
	if err != nil || len(keys) == 0 {
		log.Fatalf("trusted keys: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var completed atomic.Int64
	agents := make([]*agent, *n)
	for i := range agents {
		name := fmt.Sprintf("%s-%02d", *prefix, i+1)
		agents[i] = &agent{id: fmt.Sprintf("%s-%02d-%08x", *prefix, i+1, i*2654435761), name: name,
			rep: fleet.HostReport{Version: *version, UpdateState: fleet.StateIdle}}
	}
	client := &http.Client{Timeout: 15 * time.Second}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i, a := range agents {
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(time.Duration(i) * *interval / time.Duration(*n))
			t := time.NewTicker(*interval)
			defer t.Stop()
			for {
				mu.Lock()
				req := a.request()
				mu.Unlock()
				resp, err := doSync(ctx, client, *ingest, *key, req)
				if err != nil {
					if ctx.Err() == nil {
						log.Printf("%s: sync failed: %v", a.name, err)
					}
				} else {
					mu.Lock()
					a.handle(ctx, client, *key, keys, resp, func() bool {
						if *failFile != "" {
							if _, err := os.Stat(*failFile); err == nil {
								return true
							}
						}
						c := completed.Add(1)
						return *failAfter >= 0 && c > *failAfter && c <= *failAfter+*failCount
					})
					mu.Unlock()
				}
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		start := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			mu.Lock()
			versions, states := map[string]int{}, map[string]int{}
			for _, a := range agents {
				versions[a.rep.Version]++
				states[a.rep.UpdateState]++
			}
			mu.Unlock()
			log.Printf("[%3.0fs] versions %s | states %s", time.Since(start).Seconds(), fmtCounts(versions), fmtCounts(states))
		}
	}()
	wg.Wait()
}

func fmtCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}

func (a *agent) request() syncReq {
	var r syncReq
	r.HostID, r.HostName = a.id, a.name
	r.Agent.Name, r.Agent.Version, r.Agent.Commit = "openlog-infra-agent", a.rep.Version, "demo"
	r.Agent.OS, r.Agent.Arch, r.Agent.InstallMethod, r.Agent.UpdateCapable = "linux", "amd64", "tarball", true
	r.Update.State, r.Update.FromVersion, r.Update.ToVersion, r.Update.Error = a.rep.UpdateState, a.rep.UpdateFrom, a.rep.UpdateTo, a.rep.UpdateError
	if !a.rep.UpdateChangedAt.IsZero() {
		r.Update.ChangedAt = a.rep.UpdateChangedAt.UTC().Format(time.RFC3339Nano)
	}
	return r
}

func doSync(ctx context.Context, c *http.Client, base, key string, req syncReq) (*fleet.SyncResponse, error) {
	body, _ := json.Marshal(req)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+fleet.SyncPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("openlog-license-key", key)
	resp, err := c.Do(hr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("%s: %s", resp.Status, b)
	}
	var out fleet.SyncResponse
	return &out, json.NewDecoder(resp.Body).Decode(&out)
}

// handle applies the agent verification rules of contract §3 (simplified: no extraction/self-test).
func (a *agent) handle(ctx context.Context, c *http.Client, key string, keys []ed25519.PublicKey, resp *fleet.SyncResponse, shouldFail func() bool) {
	now := time.Now()
	if a.pending != nil {
		u := a.pending
		a.pending = nil
		err := verifyAndDownload(ctx, c, key, keys, a.rep.Version, u)
		if err == nil && shouldFail() {
			err = fmt.Errorf("self-test failed: exit status 1")
		}
		a.rep.UpdateChangedAt = now
		if err != nil {
			a.rep.UpdateState, a.rep.UpdateError = fleet.StateFailed, err.Error()
			log.Printf("%s: %s %s → %s FAILED: %v", a.name, u.Action, a.rep.Version, u.TargetVersion, err)
			return
		}
		log.Printf("%s: %s %s → %s succeeded", a.name, u.Action, a.rep.Version, u.TargetVersion)
		a.rep.UpdateState, a.rep.UpdateError, a.rep.Version = fleet.StateSucceeded, "", u.TargetVersion
		return
	}
	u := resp.Update
	if u == nil {
		return
	}
	if (a.rep.UpdateState == fleet.StateFailed) && a.rep.UpdateTo == u.TargetVersion {
		return // agents do not retry a failed version in a loop
	}
	a.pending = u
	a.rep.UpdateState, a.rep.UpdateFrom, a.rep.UpdateTo, a.rep.UpdateError, a.rep.UpdateChangedAt = fleet.StateDownloading, a.rep.Version, u.TargetVersion, "", now
	log.Printf("%s: offered %s to %s (rollout %.8s, %s)", a.name, u.Action, u.TargetVersion, u.RolloutID, u.DownloadURL)
}

func verifyAndDownload(ctx context.Context, c *http.Client, key string, keys []ed25519.PublicKey, current string, u *fleet.UpdateJSON) error {
	raw, err := base64.StdEncoding.DecodeString(u.Manifest)
	if err != nil {
		return err
	}
	m, _, err := lib.VerifyManifest(raw, []byte(u.Signature), keys)
	if err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	if m.Version != u.TargetVersion {
		return fmt.Errorf("manifest version %s != target %s", m.Version, u.TargetVersion)
	}
	cur, _ := lib.ParseVersion(current)
	if u.Action == fleet.ActionUpgrade {
		if min := m.Compatibility.MinUpgradeFrom; min != "" && lib.Compare(cur, lib.MustParseVersion(min)) < 0 {
			return fmt.Errorf("%s is below min_upgrade_from %s", current, min)
		}
	}
	art, ok := m.Artifact(lib.ComponentInfraAgent, "linux", "amd64", lib.FormatTarGz)
	if !ok {
		return fmt.Errorf("no linux/amd64 artifact")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.DownloadURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("openlog-license-key", key)
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: %s", resp.Status)
	}
	h := sha256.New()
	nBytes, err := io.Copy(h, io.LimitReader(resp.Body, art.Size+1))
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if nBytes != art.Size || hex.EncodeToString(h.Sum(nil)) != art.SHA256 {
		return fmt.Errorf("artifact size/sha256 mismatch")
	}
	return nil
}
