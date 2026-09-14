//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/svc"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/update"
)

// exitRestart is the service specific exit code of a start that ended without a stop request (self-update switch,
// rollback, restart for a new service definition): a non-crash failure, so the recovery actions restart the service.
const exitRestart = 3

// Service log file (the SCM discards stdout/stderr): rotated at start above maxServiceLog.
const maxServiceLog = 10 << 20

// runWindowsService runs the agent under the Service Control Manager when started by it.
func runWindowsService() (int, bool) {
	ok, err := svc.IsWindowsService()
	if err != nil || !ok {
		return 0, false
	}
	runningAsWindowsService = true
	redirectServiceOutput()
	h := &agentService{}
	if err := svc.Run(update.ServiceName, h); err != nil {
		fmt.Fprintln(os.Stderr, "service:", err)
		return 1, true
	}
	return h.code, true
}

// redirectServiceOutput sends stdout/stderr (the JSON log) to %ProgramData%\openlog\infra-agent\logs\openlog-infra-agent.log.
func redirectServiceOutput() {
	dir := filepath.Join(filepath.Dir(config.DefaultPath), "logs")
	if err := update.SecureDir(dir); err != nil {
		return
	}
	file := filepath.Join(dir, "openlog-infra-agent.log")
	if fi, err := os.Stat(file); err == nil && fi.Size() > maxServiceLog {
		_ = os.Remove(file + ".2")
		_ = os.Rename(file+".1", file+".2")
		_ = os.Rename(file, file+".1")
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	os.Stdout, os.Stderr = f, f
}

type agentService struct{ code int }

func (s *agentService) Execute(_ []string, reqs <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svcCtx = ctx
	done := make(chan int, 1)
	go func() { done <- run() }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	stopping := false
	for {
		select {
		case c := <-reqs:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				if !stopping {
					stopping = true
					status <- svc.Status{State: svc.StopPending, WaitHint: 20000}
					cancel()
				}
			}
		case code := <-done:
			s.code = code
			if stopping && code == 0 {
				return false, 0
			}
			if code == 0 {
				code = exitRestart
			}
			return true, uint32(code)
		}
	}
}
