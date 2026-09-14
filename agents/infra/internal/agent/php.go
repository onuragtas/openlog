package agent

import (
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/discovery"
	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
	"github.com/onuragtas/openlog/agents/infra/internal/phpaccess"
	"github.com/onuragtas/openlog/agents/infra/internal/phpforwarder"
)

// phpModule switches the PHP forwarder on and off: an explicit php_forwarder.enabled wins, otherwise it runs while
// discovery finds a PHP runtime (re-evaluated with every inventory snapshot).
type phpModule struct {
	cfg config.PHPForwarder
	fwd *phpforwarder.Forwarder
	fs  *hostfs.FS
	log *slog.Logger

	mu       sync.Mutex
	armed    bool // between Run start and shutdown
	detected bool
	reason   string
	lastErr  string
	// lastMissing is the last logged set of pools without socket access.
	lastMissing string
}

func (p *phpModule) want() bool {
	if p.cfg.Enabled != nil {
		return *p.cfg.Enabled
	}
	return p.detected
}

// arm allows the forwarder to run (Agent.Run) and applies the current decision.
func (p *phpModule) arm() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.armed = true
	p.applyLocked()
}

// observe re-evaluates PHP runtime detection from a discovery result.
func (p *phpModule) observe(services []discovery.Service) {
	found, reason := phpforwarder.DetectRuntime(p.fs, services)
	p.mu.Lock()
	defer p.mu.Unlock()
	if found != p.detected && p.cfg.Enabled == nil {
		p.log.Info("php runtime detection changed", "php_detected", found, "reason", reason)
	}
	p.detected, p.reason = found, reason
	p.applyLocked()
}

func (p *phpModule) applyLocked() {
	if !p.armed {
		return
	}
	switch want := p.want(); {
	case want && !p.fwd.Running():
		if err := p.fwd.Start(); err != nil {
			if msg := err.Error(); msg != p.lastErr {
				p.lastErr = msg
				p.log.Error("php forwarder could not start; retrying with the next inventory evaluation", "error", err)
			}
			return
		}
		p.lastErr = ""
	case !want && p.fwd.Running():
		p.fwd.Stop()
	}
}

// shutdown stops the forwarder and flushes pending spans into the pipeline.
func (p *phpModule) shutdown() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.armed = false
	p.fwd.Stop()
}

// active reports whether spans arrived within phpforwarder.ActiveWindow.
func (p *phpModule) active(now time.Time) bool {
	if p == nil {
		return false
	}
	last := p.fwd.LastReceived()
	return !last.IsZero() && now.Sub(last) < phpforwarder.ActiveWindow
}

// PHPAccess reports which PHP-FPM pools can send to php.sock (php-agent.md §1). nil without PHP-FPM pools or without
// the forwarder module (-once). optOutFile is <config dir>/no-php-access. The agent cannot change groups itself: pools
// reported missing are granted by the privileged pre-start step at the next service start, so a change of the missing
// set is logged with that fix.
func (a *Agent) PHPAccess(agentUser, optOutFile string) *phpaccess.Report {
	p := a.php
	if p == nil {
		return nil
	}
	_, err := os.Stat(optOutFile)
	r := phpaccess.BuildReport(phpaccess.ReportInput{
		Root: a.fs.Root(), AgentUser: agentUser, SocketGroup: p.fwd.SocketGroup(), SocketMode: p.cfg.Mode(),
		OptedOut: err == nil || (p.cfg.GrantPoolUsers != nil && !*p.cfg.GrantPoolUsers),
	})
	var missing []string
	for _, m := range r.Missing() {
		missing = append(missing, m.Pool+" (user "+m.User+")")
	}
	key := strings.Join(missing, ", ")
	p.mu.Lock()
	changed := key != p.lastMissing
	p.lastMissing = key
	p.mu.Unlock()
	if changed && key != "" {
		p.log.Warn("PHP-FPM pools cannot send spans to the php forwarder socket: their users are not in the socket group",
			"pools", key, "group", phpaccess.Group,
			"fix", "systemctl restart openlog-infra-agent (the privileged pre-start step adds pool users and reloads PHP-FPM)")
	}
	return r
}

// annotateAPMHints sets apm_hint.status on services instrumented by the PHP extension. Hints are copied: the rule's
// hint is shared by every service of the rule.
func annotateAPMHints(services []discovery.Service, active bool) {
	status := phpforwarder.APMStatusNotInstalled
	if active {
		status = phpforwarder.APMStatusActive
	}
	for i := range services {
		if h := services[i].APMHint; h != nil && h.Agent == phpforwarder.APMAgent {
			c := *h
			c.Status = status
			services[i].APMHint = &c
		}
	}
}
