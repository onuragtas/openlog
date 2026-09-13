package agent

import (
	"log/slog"
	"sync"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/discovery"
	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
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
