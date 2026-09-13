package logs

import (
	"regexp"
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/mask"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// severityScanBytes is how much of a line the severity heuristic looks at.
const severityScanBytes = 128

// A level word delimited by whitespace, brackets, quotes, '=', ':', '|', ',',
// '(' or '<'. '/' is not a delimiter, so URL paths such as "GET /error" do not match.
var severityRe = regexp.MustCompile(`(?i)(?:^|[\s\[\]"'=:(<|,])(emerg(?:ency)?|alert|crit(?:ical)?|fatal|panic|err(?:or)?|warn(?:ing)?|notice|info|debug|trace)(?:$|[\s\[\]"',:)>|])`)

var severityWords = map[string]struct {
	num  logspb.SeverityNumber
	text string
}{
	"emerg": {logspb.SeverityNumber_SEVERITY_NUMBER_FATAL4, "FATAL"}, "emergency": {logspb.SeverityNumber_SEVERITY_NUMBER_FATAL4, "FATAL"},
	"alert": {logspb.SeverityNumber_SEVERITY_NUMBER_FATAL3, "FATAL"}, "panic": {logspb.SeverityNumber_SEVERITY_NUMBER_FATAL2, "FATAL"},
	"fatal": {logspb.SeverityNumber_SEVERITY_NUMBER_FATAL, "FATAL"}, "crit": {logspb.SeverityNumber_SEVERITY_NUMBER_FATAL, "FATAL"},
	"critical": {logspb.SeverityNumber_SEVERITY_NUMBER_FATAL, "FATAL"},
	"err":      {logspb.SeverityNumber_SEVERITY_NUMBER_ERROR, "ERROR"}, "error": {logspb.SeverityNumber_SEVERITY_NUMBER_ERROR, "ERROR"},
	"warn": {logspb.SeverityNumber_SEVERITY_NUMBER_WARN, "WARN"}, "warning": {logspb.SeverityNumber_SEVERITY_NUMBER_WARN, "WARN"},
	"notice": {logspb.SeverityNumber_SEVERITY_NUMBER_INFO2, "INFO"}, "info": {logspb.SeverityNumber_SEVERITY_NUMBER_INFO, "INFO"},
	"debug": {logspb.SeverityNumber_SEVERITY_NUMBER_DEBUG, "DEBUG"}, "trace": {logspb.SeverityNumber_SEVERITY_NUMBER_TRACE, "TRACE"},
}

// ParseSeverity guesses the severity from the first level word in the first
// 128 bytes of a line: nginx "[error]", Apache "[core:warn]", PostgreSQL
// "ERROR:", MySQL "[Warning]", logfmt "level=info", JSON "\"level\":\"debug\"",
// syslog-style "<warn>". It returns SEVERITY_NUMBER_UNSPECIFIED when nothing matches.
func ParseSeverity(line string) (logspb.SeverityNumber, string) {
	if len(line) > severityScanBytes {
		line = line[:severityScanBytes]
	}
	m := severityRe.FindStringSubmatch(line)
	if m == nil {
		return logspb.SeverityNumber_SEVERITY_NUMBER_UNSPECIFIED, ""
	}
	s := severityWords[strings.ToLower(m[1])]
	return s.num, s.text
}

// sanitize truncates body to maxBytes (at a rune boundary), replaces invalid
// UTF-8 and optionally masks secrets.
func sanitize(body string, maxBytes int, maskSecrets bool) (string, bool) {
	truncated := false
	if maxBytes > 0 && len(body) > maxBytes {
		body = mask.Truncate(body, maxBytes)
		truncated = true
	}
	body = strings.ToValidUTF8(body, "�")
	if maskSecrets {
		body = mask.Text(body)
	}
	return body, truncated
}
