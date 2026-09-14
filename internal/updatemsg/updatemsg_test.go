package updatemsg

import (
	"maps"
	"testing"
)

func TestFormatParseRoundTrip(t *testing.T) {
	cases := []struct {
		code   string
		params Params
		text   string
	}{
		{ModeOff, Params{}, "OPENLOG_UPDATER_MODE=off"},
		{UpdateAvailable, Params{"version": "0.9.1"}, "openlog 0.9.1 is available"},
		{UpdateAvailableNotify, Params{"version": "0.9.1"}, "openlog 0.9.1 is available (OPENLOG_UPDATER_MODE=notify)"},
		{WaitingForWindow, Params{"version": "0.9.1"}, "openlog 0.9.1 will be installed in the next maintenance window"},
		{Updated, Params{"from": "0.0.0-dev+abc", "to": "0.9.1"}, "updated 0.0.0-dev+abc → 0.9.1"},
		{RolledBack, Params{"to": "0.9.1", "from": "0.9.0"}, "update to 0.9.1 failed and was rolled back to 0.9.0"},
		{RollbackFailed, Params{"to": "0.9.1", "from": "0.9.0"}, "update to 0.9.1 failed and the rollback to 0.9.0 failed: manual action required"},
		{FailedBeforeChange, Params{"to": "0.9.1"}, "update to 0.9.1 failed before services were changed"},
		{UpToDateNewest, Params{"version": "0.9.1", "channel": "stable"}, "0.9.1 is the newest release on channel stable"},
		{UpToDateNoEligible, Params{"version": "0.9.0", "details": "0.9.1: failed before on this installation; 0.9.2: requires upgrading from >= 0.9.1"},
			"no eligible release newer than 0.9.0: 0.9.1: failed before on this installation; 0.9.2: requires upgrading from >= 0.9.1"},
		{RequestModeOff, Params{}, "updates are disabled (OPENLOG_UPDATER_MODE=off)"},
		{RequestNotInstallable, Params{"version": "0.9.2", "reason": "0.9.1 is the newest release on channel stable"}, "0.9.2 is not installable: 0.9.1 is the newest release on channel stable"},
		{RequestTargetChanged, Params{"version": "0.9.1", "requested": "0.9.2"}, "the release to install is now 0.9.1, not the requested 0.9.2: check again and confirm the new version"},
		{RequestOutsideWindow, Params{}, "outside the maintenance window (OPENLOG_UPDATER_MAINTENANCE_WINDOW): confirm installing outside the window to update now"},
		{RequestInterrupted, Params{}, "interrupted: openlog-updater restarted while handling the request"},
		{RequestExpired, Params{}, "not picked up by openlog-updater within 15m (is an updater with request support running?)"},
		// A failed request: the status message with the error appended.
		{RolledBack, Params{"to": "0.9.1", "from": "0.9.0", ParamError: "health: container exited: code 1"},
			"update to 0.9.1 failed and was rolled back to 0.9.0: health: container exited: code 1"},
		{RollbackFailed, Params{"to": "0.9.1", "from": "0.9.0", ParamError: "recreate: x; rollback: y"},
			"update to 0.9.1 failed and the rollback to 0.9.0 failed: manual action required: recreate: x; rollback: y"},
	}
	for _, c := range cases {
		if got := Format(c.code, c.params); got != c.text {
			t.Errorf("Format(%s) = %q, want %q", c.code, got, c.text)
		}
		code, params, ok := Parse(c.text)
		if !ok || code != c.code || !maps.Equal(params, c.params) {
			t.Errorf("Parse(%q) = %s %v %v, want %s %v", c.text, code, params, ok, c.code, c.params)
		}
	}
}

func TestParseUnknown(t *testing.T) {
	for _, s := range []string{"", "current version: dial tcp: no such host", "unknown request action \"x\"", "openlog 0.9.1 is available soon"} {
		if code, _, ok := Parse(s); ok {
			t.Errorf("Parse(%q) = %s, want no match", s, code)
		}
	}
	if Format("nope", nil) != "" {
		t.Error("unknown code must format to empty text")
	}
}
