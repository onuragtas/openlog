// Package updatemsg defines the fixed messages of openlog-updater as stable codes with string
// parameters, their English text (kept in the status document and in logs) and the reverse
// mapping from English text to code (for update_requests.message, which stores text only). The web
// UI translates by code (update.messages.<code>) and falls back to the English text.
package updatemsg

import (
	"regexp"
	"strings"
)

// Message codes. Parameters are listed per code.
const (
	// ModeOff: updates are disabled. No parameters.
	ModeOff = "mode_off"
	// UpdateAvailable: {version}.
	UpdateAvailable = "update_available"
	// UpdateAvailableNotify: {version} (notify mode).
	UpdateAvailableNotify = "update_available_notify"
	// WaitingForWindow: {version}.
	WaitingForWindow = "waiting_for_window"
	// Updated: {from, to}.
	Updated = "updated"
	// RolledBack: {to, from}.
	RolledBack = "rolled_back"
	// RollbackFailed: {to, from}.
	RollbackFailed = "rollback_failed"
	// FailedBeforeChange: {to}.
	FailedBeforeChange = "failed_before_change"
	// UpToDateNewest: {version, channel}.
	UpToDateNewest = "up_to_date_newest"
	// UpToDateNoEligible: {version, details} (details: English list of skipped releases).
	UpToDateNoEligible = "up_to_date_no_eligible"

	// RequestModeOff: an apply request while updates are disabled. No parameters.
	RequestModeOff = "request_mode_off"
	// RequestNotInstallable: {version, reason} (reason: English).
	RequestNotInstallable = "request_not_installable"
	// RequestTargetChanged: {version, requested}.
	RequestTargetChanged = "request_target_changed"
	// RequestOutsideWindow: no parameters.
	RequestOutsideWindow = "request_outside_window"
	// RequestInterrupted: the updater restarted while handling the request. No parameters.
	RequestInterrupted = "request_interrupted"
	// RequestExpired: no updater picked the request up. No parameters.
	RequestExpired = "request_expired"
)

// ParamError is the parameter holding the English error appended to a failed request's message
// ("<message>: <error>").
const ParamError = "error"

// Params are the string parameters of a message.
type Params map[string]string

type def struct {
	code string
	// text is the English template; {name} is a parameter.
	text string
	re   *regexp.Regexp
	keys []string
}

var paramRe = regexp.MustCompile(`\{([a-z_]+)\}`)

// defs in match order (the first matching pattern wins).
var defs = func() []*def {
	list := []struct{ code, text string }{
		{ModeOff, "OPENLOG_UPDATER_MODE=off"},
		{UpdateAvailableNotify, "openlog {version} is available (OPENLOG_UPDATER_MODE=notify)"},
		{UpdateAvailable, "openlog {version} is available"},
		{WaitingForWindow, "openlog {version} will be installed in the next maintenance window"},
		{Updated, "updated {from} → {to}"},
		{RolledBack, "update to {to} failed and was rolled back to {from}"},
		{RollbackFailed, "update to {to} failed and the rollback to {from} failed: manual action required"},
		{FailedBeforeChange, "update to {to} failed before services were changed"},
		{UpToDateNewest, "{version} is the newest release on channel {channel}"},
		{RequestModeOff, "updates are disabled (OPENLOG_UPDATER_MODE=off)"},
		{RequestNotInstallable, "{version} is not installable: {reason}"},
		{RequestTargetChanged, "the release to install is now {version}, not the requested {requested}: check again and confirm the new version"},
		{RequestOutsideWindow, "outside the maintenance window (OPENLOG_UPDATER_MAINTENANCE_WINDOW): confirm installing outside the window to update now"},
		{RequestInterrupted, "interrupted: openlog-updater restarted while handling the request"},
		{RequestExpired, "not picked up by openlog-updater within 15m (is an updater with request support running?)"},
		{UpToDateNoEligible, "no eligible release newer than {version}: {details}"},
	}
	out := make([]*def, 0, len(list))
	for _, l := range list {
		d := &def{code: l.code, text: l.text}
		var pat strings.Builder
		pat.WriteString(`^`)
		last := 0
		for _, m := range paramRe.FindAllStringSubmatchIndex(l.text, -1) {
			pat.WriteString(regexp.QuoteMeta(l.text[last:m[0]]))
			key := l.text[m[2]:m[3]]
			d.keys = append(d.keys, key)
			switch key {
			case "reason", "details":
				pat.WriteString(`(.+)`) // free text, always last
			default:
				pat.WriteString(`([^\s:]+)`) // versions, channels
			}
			last = m[1]
		}
		pat.WriteString(regexp.QuoteMeta(l.text[last:]))
		if !hasFreeText(d.keys) {
			pat.WriteString(`(?:: (.+))?`) // optional appended error
		}
		pat.WriteString(`$`)
		d.re = regexp.MustCompile(`(?s)` + pat.String())
		out = append(out, d)
	}
	return out
}()

func hasFreeText(keys []string) bool {
	for _, k := range keys {
		if k == "reason" || k == "details" {
			return true
		}
	}
	return false
}

// Format returns the English text of code with params ("" for an unknown code). A params[ParamError]
// is appended as ": <error>".
func Format(code string, params Params) string {
	for _, d := range defs {
		if d.code != code {
			continue
		}
		s := paramRe.ReplaceAllStringFunc(d.text, func(m string) string { return params[m[1:len(m)-1]] })
		if e := params[ParamError]; e != "" {
			s += ": " + e
		}
		return s
	}
	return ""
}

// Parse maps an English message produced by Format back to its code and params. ok is false for
// any other text (e.g. a raw error).
func Parse(text string) (code string, params Params, ok bool) {
	for _, d := range defs {
		m := d.re.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		params = Params{}
		for i, k := range d.keys {
			params[k] = m[i+1]
		}
		if len(m) > len(d.keys)+1 && m[len(d.keys)+1] != "" {
			params[ParamError] = m[len(d.keys)+1]
		}
		return d.code, params, true
	}
	return "", nil, false
}
