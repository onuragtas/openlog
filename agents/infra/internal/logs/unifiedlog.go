package logs

// macOS unified log input (logs.unified_log, D-104): `log stream --style ndjson` is run and restarted with
// backoff. The input is live only: records written while the agent was stopped are not read back.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// Log sources and attributes of the macOS unified log and the Windows Event Log (semantic-conventions §4).
const (
	SourceUnifiedLog      = "unified_log"
	SourceWindowsEventLog = "windows_event_log"

	AttrMacOSSubsystem = "openlog.macos.subsystem"
	AttrMacOSCategory  = "openlog.macos.category"

	AttrWindowsChannel  = "openlog.windows.event_log.channel"
	AttrWindowsEventID  = "openlog.windows.event.id"
	AttrWindowsProvider = "openlog.windows.event.provider"
	AttrWindowsRecordID = "openlog.windows.event.record_id"
)

// UnifiedLogArgs builds the log(1) command line.
func UnifiedLogArgs(u config.UnifiedLogInput) []string {
	args := []string{"stream", "--style", "ndjson", "--level", u.Level}
	if u.Predicate != "" {
		args = append(args, "--predicate", u.Predicate)
	}
	return args
}

type unifiedLogLine struct {
	Timestamp        string `json:"timestamp"`
	MessageType      string `json:"messageType"`
	EventType        string `json:"eventType"`
	EventMessage     string `json:"eventMessage"`
	Subsystem        string `json:"subsystem"`
	Category         string `json:"category"`
	ProcessImagePath string `json:"processImagePath"`
	ProcessID        int64  `json:"processID"`
}

const unifiedLogTimeLayout = "2006-01-02 15:04:05.999999-0700"

// UnifiedLogRecord converts one ndjson line of `log stream` to a log record; nil for lines that are not log
// events (the "Filtering the log data" header, activity and signpost events).
func UnifiedLogRecord(line []byte, maxBytes int, observed time.Time, maskSecrets bool) *logspb.LogRecord {
	var l unifiedLogLine
	if len(line) == 0 || line[0] != '{' || json.Unmarshal(line, &l) != nil || l.EventType != "logEvent" {
		return nil
	}
	rec := &logspb.LogRecord{ObservedTimeUnixNano: uint64(observed.UnixNano())}
	if t, err := time.Parse(unifiedLogTimeLayout, l.Timestamp); err == nil {
		rec.TimeUnixNano = uint64(t.UnixNano())
	}
	switch l.MessageType {
	case "Fault":
		rec.SeverityNumber, rec.SeverityText = logspb.SeverityNumber_SEVERITY_NUMBER_FATAL, "FATAL"
	case "Error":
		rec.SeverityNumber, rec.SeverityText = logspb.SeverityNumber_SEVERITY_NUMBER_ERROR, "ERROR"
	case "Default":
		rec.SeverityNumber, rec.SeverityText = logspb.SeverityNumber_SEVERITY_NUMBER_INFO2, "INFO"
	case "Info":
		rec.SeverityNumber, rec.SeverityText = logspb.SeverityNumber_SEVERITY_NUMBER_INFO, "INFO"
	case "Debug":
		rec.SeverityNumber, rec.SeverityText = logspb.SeverityNumber_SEVERITY_NUMBER_DEBUG, "DEBUG"
	}
	body, truncated := sanitize(l.EventMessage, maxBytes, maskSecrets)
	rec.Body = &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: body}}
	attrs := []*commonpb.KeyValue{otlputil.Str(AttrSource, SourceUnifiedLog)}
	if l.Subsystem != "" {
		attrs = append(attrs, otlputil.Str(AttrMacOSSubsystem, l.Subsystem))
	}
	if l.Category != "" {
		attrs = append(attrs, otlputil.Str(AttrMacOSCategory, l.Category))
	}
	if l.ProcessID > 0 {
		attrs = append(attrs, otlputil.Int("process.pid", l.ProcessID))
	}
	if l.ProcessImagePath != "" {
		attrs = append(attrs, otlputil.Str("process.executable.path", l.ProcessImagePath),
			otlputil.Str("process.command", l.ProcessImagePath[strings.LastIndexByte(l.ProcessImagePath, '/')+1:]))
	}
	if truncated {
		attrs = append(attrs, otlputil.Bool(AttrTruncated, true))
	}
	rec.Attributes = attrs
	return rec
}

// runUnifiedLog runs `log stream` until ctx ends, restarting it with backoff.
func (m *Manager) runUnifiedLog(ctx context.Context, out chan<- journalEntry) {
	u := m.cfg.UnifiedLog
	backoff := time.Second
	for ctx.Err() == nil {
		n, err := m.unifiedLogOnce(ctx, out, u)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, exec.ErrNotFound) {
			m.log.Warn("unified log input disabled: log binary not found", "log_path", u.LogPath)
			return
		}
		if n > 0 {
			backoff = time.Second
		}
		m.log.Warn("log stream exited; restarting", "error", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

func (m *Manager) unifiedLogOnce(ctx context.Context, out chan<- journalEntry, u config.UnifiedLogInput) (int, error) {
	cmd := exec.CommandContext(ctx, u.LogPath, UnifiedLogArgs(u)...)
	stderr := &tailBuffer{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	n := 0
	for sc.Scan() {
		rec := UnifiedLogRecord(sc.Bytes(), m.cfg.MaxLineBytes, m.now(), m.cfg.MaskSecrets)
		if rec == nil {
			continue
		}
		select {
		case out <- journalEntry{rec: rec}:
			n++
		case <-ctx.Done():
			_ = cmd.Wait()
			return n, ctx.Err()
		}
	}
	err = cmd.Wait()
	if msg := stderr.String(); msg != "" {
		err = errors.Join(err, errors.New(msg))
	}
	return n, err
}
