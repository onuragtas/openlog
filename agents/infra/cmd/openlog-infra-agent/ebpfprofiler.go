package main

// Whole-host CPU profiler management through the infra agent (docs/contracts/ebpf-profiler.md, D-149). The
// profiler is a separate component built for Linux only: the unprivileged manager stages a verified release and
// the privileged pre-start step installs it, like the PHP and Java agent installations.

import (
	"context"
	"crypto/ed25519"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/ebpfprofiler"
	"github.com/onuragtas/openlog/agents/infra/internal/exporter"
	"github.com/onuragtas/openlog/agents/infra/internal/release"
	"github.com/onuragtas/openlog/agents/infra/internal/update"
)

func newEBPFProfilerManager(cfg *config.Config, ver string, install update.Install, mgr *update.Manager, restart func(), log *slog.Logger) *ebpfprofiler.Manager {
	keys, err := release.TrustedKeys(cfg.Release.TrustedKeysFile)
	if err != nil {
		keys = nil
	}
	capable, reason := install.VersionDir != "" && len(keys) > 0 && install.Apply != nil, ""
	switch {
	case runtime.GOOS != "linux":
		capable, reason = false, "the profiler is built for Linux only"
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
	return ebpfprofiler.NewManager(ebpfprofiler.Options{
		Config: cfg.EBPFProfiler, StateDir: cfg.StateDir, StatusDir: install.InstallRoot, AgentVersion: ver,
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
		CanRestart: func() bool {
			st := mgr.State()
			return !mgr.Restarting() && st.Staged == "" && (st.Candidate == "" || st.Confirmed) && st.Status != update.StateRestarting
		},
		Restart: restart,
		Log:     log,
	})
}

// runEBPFProfilerApply is the profiler part of "-apply" (Linux, after the Java agent step).
func runEBPFProfilerApply(sys *update.Sys, cfg *config.Config, install update.Install, keys []ed25519.PublicKey, log *slog.Logger) {
	if runtime.GOOS != "linux" || install.VersionDir == "" || install.InstallRoot == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), applyBudget)
	defer cancel()
	ebpfprofiler.Apply(ctx, ebpfprofiler.ApplyOptions{Sys: sys, StateDir: cfg.StateDir, StatusDir: install.InstallRoot,
		Config: cfg.EBPFProfiler, Trusted: keys, Log: log})
}
