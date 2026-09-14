package main

// PHP agent installation through the infra agent (docs/contracts/php-agent.md §7.3, D-082): the unprivileged manager
// runs next to the update manager and hands requests to the privileged pre-start step.

import (
	"context"
	"crypto/ed25519"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"

	"github.com/onuragtas/openlog/agents/infra/internal/agent"
	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/exporter"
	"github.com/onuragtas/openlog/agents/infra/internal/phpagent"
	"github.com/onuragtas/openlog/agents/infra/internal/release"
	"github.com/onuragtas/openlog/agents/infra/internal/update"
)

func newPHPAgentManager(cfg *config.Config, ver string, install update.Install, mgr *update.Manager, a *agent.Agent,
	restart func(), log *slog.Logger) *phpagent.Manager {
	keys, err := release.TrustedKeys(cfg.Release.TrustedKeysFile)
	if err != nil {
		keys = nil
	}
	// Installing needs the privileged pre-start step of this start (it also runs with update.enabled: false).
	capable, reason := install.Apply != nil && install.VersionDir != "" && len(keys) > 0, ""
	switch {
	case install.Method == update.MethodContainer || install.Method == update.MethodDev:
		capable, reason = false, install.Reason
	case len(keys) == 0:
		reason = "no trusted release keys"
	case !capable:
		reason = "the privileged pre-start step (ExecStartPre=+… -apply) did not run for this start"
	}
	versionDir := filepath.Join(install.InstallRoot, "versions", install.VersionDir)
	return phpagent.NewManager(phpagent.Options{
		Config: cfg.PHPAgent, StateDir: cfg.StateDir, StatusDir: install.InstallRoot, AgentVersion: ver,
		Trusted: keys, Capable: capable, Reason: reason,
		OwnManifest: func() ([]byte, []byte, error) {
			m, err := update.ReadLimited(filepath.Join(versionDir, update.ManifestFile), 1<<20)
			if err != nil {
				return nil, nil, err
			}
			s, err := update.ReadLimited(filepath.Join(versionDir, update.SignatureFile), 64<<10)
			return m, s, err
		},
		DownloadHeader: func(download string) http.Header {
			h := http.Header{}
			h.Set("User-Agent", update.AgentName+"/"+ver)
			du, err1 := url.Parse(download)
			eu, err2 := url.Parse(cfg.Endpoint)
			if err1 == nil && err2 == nil && cfg.LicenseKey != "" && du.Scheme == eu.Scheme && du.Host == eu.Host {
				h.Set(exporter.LicenseHeader, cfg.LicenseKey)
			}
			return h
		},
		APMActive: a.PHPActive,
		CanRestart: func() bool {
			st := mgr.State()
			return !mgr.Restarting() && st.Staged == "" && (st.Candidate == "" || st.Confirmed) && st.Status != update.StateRestarting
		},
		Restart: restart,
		Log:     log,
	})
}

// runPHPAgentApply is the PHP agent part of "-apply" (after the agent's own update step).
func runPHPAgentApply(sys *update.Sys, cfg *config.Config, install update.Install, keys []ed25519.PublicKey, log *slog.Logger) {
	if install.VersionDir == "" || install.InstallRoot == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), applyBudget)
	defer cancel()
	phpagent.Apply(ctx, phpagent.ApplyOptions{Sys: sys, StateDir: cfg.StateDir, StatusDir: install.InstallRoot,
		Config: cfg.PHPAgent, Trusted: keys, Log: log})
}

// mergeKicks forwards every signal of the inputs to one channel (the syncer's Kick).
func mergeKicks(ctx context.Context, in ...<-chan struct{}) <-chan struct{} {
	out := make(chan struct{}, 1)
	for _, c := range in {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-c:
					select {
					case out <- struct{}{}:
					default:
					}
				}
			}
		}()
	}
	return out
}
