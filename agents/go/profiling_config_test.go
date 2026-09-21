package openlog

import "testing"

// Profiling is on by default, and the default is pinned here because it changes the behaviour of every
// process that imports this SDK: Start profiles it without anyone asking.
func TestProfilingDefaultsOn(t *testing.T) {
	c, _, err := loadConfig(env(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Profiling {
		t.Error("profiling is off by default")
	}
	if c.ProfileInterval != defaultProfileInterval {
		t.Errorf("interval = %s, want %s", c.ProfileInterval, defaultProfileInterval)
	}
}

// The off switch is not decoration: Go allows one CPU profile per process, so a service that needs
// net/http/pprof has to be able to stop ours. Both ways of doing it are pinned — an environment variable for
// a running deployment, an option for a build that never wants it.
func TestProfilingCanBeTurnedOff(t *testing.T) {
	c, _, err := loadConfig(env(map[string]string{"OPENLOG_PROFILING": "false"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Profiling {
		t.Error("OPENLOG_PROFILING=false did not turn profiling off")
	}

	c, _, err = loadConfig(env(nil), []Option{WithProfiling(false)})
	if err != nil {
		t.Fatal(err)
	}
	if c.Profiling {
		t.Error("WithProfiling(false) did not turn profiling off")
	}
}
