package sampler

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ParanoidPath is the sysctl that decides who may open perf events.
const ParanoidPath = "/proc/sys/kernel/perf_event_paranoid"

// readParanoid returns the value of the sysctl, below root when the profiler runs with the host mounted.
func readParanoid(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, ParanoidPath))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// PerfHint explains a perf_event_open failure with the setting that most likely caused it, so the failure
// says what is blocking it instead of naming a sysctl and leaving the operator to look it up (contract §3).
// paranoid and readErr are what readParanoid returned.
func PerfHint(err error, paranoid string, readErr error) string {
	if !errors.Is(err, syscall.EPERM) && !errors.Is(err, syscall.EACCES) {
		return ""
	}
	const needs = "whole-host sampling (pid -1) needs CAP_PERFMON, which this unit grants: check that it reached " +
		"the process (systemctl show -p EffectiveCapabilities) and that the kernel is 5.8 or newer"
	if readErr != nil {
		return " [could not read " + ParanoidPath + ": " + readErr.Error() + "; " + needs + "]"
	}
	v, convErr := strconv.Atoi(paranoid)
	if convErr != nil {
		return " [" + ParanoidPath + " reads " + strconv.Quote(paranoid) + "; " + needs + "]"
	}
	if v >= 3 {
		return " [kernel.perf_event_paranoid=" + strconv.Itoa(v) + " refuses perf events to everything without " +
			"CAP_SYS_ADMIN (a Debian/Ubuntu hardening level above the mainline maximum of 2); lower it with: " +
			"sysctl -w kernel.perf_event_paranoid=2]"
	}
	return " [kernel.perf_event_paranoid=" + strconv.Itoa(v) + ", which permits this, so the setting is not the " +
		"reason: " + needs + ". Containers and LXC/OpenVZ guests often forbid perf events regardless " +
		"(systemd-detect-virt)]"
}
