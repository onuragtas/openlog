package config

import (
	"testing"
	"time"
)

func base() map[string]string { return map[string]string{"OPENLOG_LICENSE_KEY": "olk_test"} }

// A profiler that starts without a key would sample a whole machine and throw every profile away.
func TestLicenseKeyIsRequired(t *testing.T) {
	if _, err := Load(Env(map[string]string{})); err == nil {
		t.Error("a missing license key was accepted")
	}
}

func TestDefaults(t *testing.T) {
	c, err := Load(Env(base()))
	if err != nil {
		t.Fatal(err)
	}
	if c.Endpoint != DefaultEndpoint || c.Interval != DefaultInterval || c.Frequency != DefaultFrequency || !c.Gzip {
		t.Errorf("defaults = %+v", c)
	}
}

// 99 Hz, not 100, so sampling does not fall into lockstep with timer-driven work; and a count only becomes
// nanoseconds through this period.
func TestPeriodFollowsTheFrequency(t *testing.T) {
	c, _ := Load(Env(base()))
	if got := c.PeriodNanos(); got != int64(time.Second)/99 {
		t.Errorf("period = %d", got)
	}
	m := base()
	m["OPENLOG_SAMPLE_FREQUENCY"] = "100"
	c2, _ := Load(Env(m))
	if got := c2.PeriodNanos(); got != 10_000_000 {
		t.Errorf("period at 100 Hz = %d, want 10ms", got)
	}
}

// The frequency is the one setting that can turn a profiler into the load it is measuring.
func TestFrequencyIsBounded(t *testing.T) {
	for _, v := range []string{"0", "-1", "100000", "abc"} {
		m := base()
		m["OPENLOG_SAMPLE_FREQUENCY"] = v
		if _, err := Load(Env(m)); err == nil {
			t.Errorf("frequency %q was accepted", v)
		}
	}
}

func TestIntervalFloor(t *testing.T) {
	m := base()
	m["OPENLOG_PROFILE_INTERVAL"] = "100ms"
	if _, err := Load(Env(m)); err == nil {
		t.Error("an interval below the floor was accepted")
	}
	m["OPENLOG_PROFILE_INTERVAL"] = "30s"
	c, err := Load(Env(m))
	if err != nil || c.Interval != 30*time.Second {
		t.Errorf("interval = %v, err = %v", c.Interval, err)
	}
}

func TestCompressionIsGzipOrNone(t *testing.T) {
	m := base()
	m["OPENLOG_COMPRESSION"] = "none"
	if c, err := Load(Env(m)); err != nil || c.Gzip {
		t.Errorf("none: gzip = %v, err = %v", c.Gzip, err)
	}
	m["OPENLOG_COMPRESSION"] = "zstd"
	if _, err := Load(Env(m)); err == nil {
		t.Error("an unsupported compression was accepted")
	}
}
