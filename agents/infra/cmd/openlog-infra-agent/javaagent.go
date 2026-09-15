package main

// Java agent jar management through the infra agent (docs/contracts/java-agent.md §2, D-123). Linux: the unprivileged
// manager hands requests to the privileged pre-start step like the PHP agent installation. macOS and Windows: the
// service process is privileged (D-104) and applies them itself, without a restart.

import (
	"context"
	"crypto/ed25519"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/exporter"
	"github.com/onuragtas/openlog/agents/infra/internal/javaagent"
	"github.com/onuragtas/openlog/agents/infra/internal/release"
	"github.com/onuragtas/openlog/agents/infra/internal/update"
)

func newJavaAgentManager(cfg *config.Config, ver string, install update.Install, mgr *update.Manager, restart func(), log *slog.Logger) *javaagent.Manager {
	keys, err := release.TrustedKeys(cfg.Release.TrustedKeysFile)
	if err != nil {
		keys = nil
	}
	inProcess := nativeServiceMode()
	capable, reason := install.VersionDir != "" && len(keys) > 0 && (inProcess || install.Apply != nil), ""
	switch {
	case install.Method == update.MethodContainer || install.Method == update.MethodDev:
		capable, reason = false, install.Reason
	case len(keys) == 0:
		reason = "no trusted release keys"
	case install.VersionDir == "":
		reason = "the infra agent does not run from its versions layout"
	case !capable:
		reason = "the privileged pre-start step (ExecStartPre=+… -apply) did not run for this start"
	}
	versionDir := filepath.Join(install.InstallRoot, "versions", install.VersionDir)
	o := javaagent.Options{
		Config: cfg.JavaAgent, StateDir: cfg.StateDir, StatusDir: install.InstallRoot, AgentVersion: ver,
		Trusted: keys, Capable: capable, Reason: reason, InProcess: inProcess,
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
		CanRestart: func() bool {
			st := mgr.State()
			return !mgr.Restarting() && st.Staged == "" && (st.Candidate == "" || st.Confirmed) && st.Status != update.StateRestarting
		},
		Restart: restart,
		Log:     log,
	}
	if inProcess {
		o.Sys = update.HostSys()
	}
	return javaagent.NewManager(o)
}

// runJavaAgentApply is the Java agent part of "-apply" (Linux, after the PHP agent step).
func runJavaAgentApply(sys *update.Sys, cfg *config.Config, install update.Install, keys []ed25519.PublicKey, log *slog.Logger) {
	if install.VersionDir == "" || install.InstallRoot == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), applyBudget)
	defer cancel()
	javaagent.Apply(ctx, javaagent.ApplyOptions{Sys: sys, StateDir: cfg.StateDir, StatusDir: install.InstallRoot,
		Config: cfg.JavaAgent, Trusted: keys, Log: log})
}
