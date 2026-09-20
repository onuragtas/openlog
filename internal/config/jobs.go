package config

import (
	"fmt"
	"time"
)

// Jobs configures cron and heartbeat monitoring (openlog-api, D-141).
type Jobs struct {
	// Enabled offers /api/v1/jobs/* and runs the overdue sweeper on the api leader
	// (OPENLOG_JOBS_ENABLED; postgres auth mode only).
	Enabled bool
	// SweepInterval is how often overdue monitors are looked for (OPENLOG_JOBS_SWEEP_INTERVAL). It bounds
	// how late a missed-run alert can be beyond the monitor's own grace period.
	SweepInterval time.Duration
	// MaxPerSweep bounds the monitors concluded in one pass (OPENLOG_JOBS_MAX_PER_SWEEP); a pass that fills
	// its limit is followed immediately by another, so this is a batch size, not a ceiling.
	MaxPerSweep int
}

func loadJobs(p *parser) Jobs {
	return Jobs{
		Enabled:       p.bool("OPENLOG_JOBS_ENABLED", true),
		SweepInterval: p.duration("OPENLOG_JOBS_SWEEP_INTERVAL", 30*time.Second),
		MaxPerSweep:   int(p.int64("OPENLOG_JOBS_MAX_PER_SWEEP", 200)),
	}
}

func (c Config) validateJobs() []error {
	var errs []error
	j := c.Jobs
	if j.SweepInterval < 5*time.Second || j.SweepInterval > 10*time.Minute {
		errs = append(errs, fmt.Errorf("OPENLOG_JOBS_SWEEP_INTERVAL: must be between 5s and 10m, got %s", j.SweepInterval))
	}
	if j.MaxPerSweep < 1 || j.MaxPerSweep > 10000 {
		errs = append(errs, fmt.Errorf("OPENLOG_JOBS_MAX_PER_SWEEP: must be between 1 and 10000, got %d", j.MaxPerSweep))
	}
	return errs
}
