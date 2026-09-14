package logs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// maxJournalField bounds a single binary field of the export format.
const maxJournalField = 64 << 20

// journalFields are the export-format fields the agent keeps.
var journalFields = map[string]bool{
	"MESSAGE": true, "PRIORITY": true, "_SYSTEMD_UNIT": true, "_PID": true, "_COMM": true,
	"SYSLOG_IDENTIFIER": true, "__CURSOR": true, "__REALTIME_TIMESTAMP": true,
}

// ExportReader parses the journal export format ("journalctl -o export"):
// entries are separated by an empty line; a field is either "NAME=value\n" or,
// for values that contain newlines or binary data, "NAME\n" followed by a
// little-endian uint64 length, the raw value and "\n".
type ExportReader struct {
	r *bufio.Reader
}

// NewExportReader returns a parser reading from r.
func NewExportReader(r io.Reader) *ExportReader {
	return &ExportReader{r: bufio.NewReaderSize(r, 64<<10)}
}

// Next returns the kept fields of the next entry, or io.EOF.
func (e *ExportReader) Next() (map[string]string, error) {
	fields := map[string]string{}
	for {
		line, err := e.readLine()
		if err != nil {
			if errors.Is(err, io.EOF) && len(fields) > 0 {
				return fields, nil
			}
			return nil, err
		}
		if len(line) == 0 {
			if len(fields) > 0 {
				return fields, nil
			}
			continue
		}
		if i := bytes.IndexByte(line, '='); i >= 0 {
			if name := string(line[:i]); journalFields[name] {
				fields[name] = string(line[i+1:])
			}
			continue
		}
		name := string(line)
		var size [8]byte
		if _, err := io.ReadFull(e.r, size[:]); err != nil {
			return nil, fmt.Errorf("journal export: field %s: %w", name, unexpectedEOF(err))
		}
		n := binary.LittleEndian.Uint64(size[:])
		if n > maxJournalField {
			return nil, fmt.Errorf("journal export: field %s too large (%d bytes)", name, n)
		}
		if journalFields[name] {
			buf := make([]byte, n)
			if _, err := io.ReadFull(e.r, buf); err != nil {
				return nil, fmt.Errorf("journal export: field %s: %w", name, unexpectedEOF(err))
			}
			fields[name] = string(buf)
		} else if _, err := io.CopyN(io.Discard, e.r, int64(n)); err != nil {
			return nil, fmt.Errorf("journal export: field %s: %w", name, unexpectedEOF(err))
		}
		if b, err := e.r.ReadByte(); err != nil || b != '\n' {
			return nil, fmt.Errorf("journal export: field %s: missing newline after binary value", name)
		}
	}
}

func unexpectedEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// readLine returns one line without its newline. Lines longer than the
// buffer are accumulated (capped at maxJournalField).
func (e *ExportReader) readLine() ([]byte, error) {
	line, err := e.r.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		full := append([]byte(nil), line...)
		for errors.Is(err, bufio.ErrBufferFull) {
			line, err = e.r.ReadSlice('\n')
			if len(full) < maxJournalField {
				full = append(full, line...)
			}
		}
		line = full
	}
	if err != nil {
		if errors.Is(err, io.EOF) && len(line) > 0 {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return line[:len(line)-1], nil
}

// journalSeverity maps syslog PRIORITY (0..7) to OTel severity.
var journalSeverity = []struct {
	num  logspb.SeverityNumber
	text string
}{
	{logspb.SeverityNumber_SEVERITY_NUMBER_FATAL4, "FATAL"}, // emerg
	{logspb.SeverityNumber_SEVERITY_NUMBER_FATAL3, "FATAL"}, // alert
	{logspb.SeverityNumber_SEVERITY_NUMBER_FATAL, "FATAL"},  // crit
	{logspb.SeverityNumber_SEVERITY_NUMBER_ERROR, "ERROR"},  // err
	{logspb.SeverityNumber_SEVERITY_NUMBER_WARN, "WARN"},    // warning
	{logspb.SeverityNumber_SEVERITY_NUMBER_INFO2, "INFO"},   // notice
	{logspb.SeverityNumber_SEVERITY_NUMBER_INFO, "INFO"},    // info
	{logspb.SeverityNumber_SEVERITY_NUMBER_DEBUG, "DEBUG"},  // debug
}

// JournalRecord converts export fields to a log record. The body is
// truncated to maxBytes and sanitized to valid UTF-8.
func JournalRecord(f map[string]string, maxBytes int, observed time.Time, maskSecrets bool) *logspb.LogRecord {
	rec := &logspb.LogRecord{ObservedTimeUnixNano: uint64(observed.UnixNano())}
	if usec, err := strconv.ParseUint(f["__REALTIME_TIMESTAMP"], 10, 64); err == nil {
		rec.TimeUnixNano = usec * 1000
	}
	if p, err := strconv.Atoi(f["PRIORITY"]); err == nil && p >= 0 && p < len(journalSeverity) {
		rec.SeverityNumber, rec.SeverityText = journalSeverity[p].num, journalSeverity[p].text
	}
	body, truncated := sanitize(f["MESSAGE"], maxBytes, maskSecrets)
	rec.Body = &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: body}}
	attrs := []*commonpb.KeyValue{otlputil.Str(AttrSource, SourceJournald)}
	if v := f["_SYSTEMD_UNIT"]; v != "" {
		attrs = append(attrs, otlputil.Str(AttrSystemdUnit, v))
	}
	if pid, err := strconv.ParseInt(f["_PID"], 10, 64); err == nil {
		attrs = append(attrs, otlputil.Int("process.pid", pid))
	}
	if v := f["_COMM"]; v != "" {
		attrs = append(attrs, otlputil.Str("process.command", v))
	}
	if v := f["SYSLOG_IDENTIFIER"]; v != "" {
		attrs = append(attrs, otlputil.Str(AttrSyslogIdentifier, v))
	}
	if truncated {
		attrs = append(attrs, otlputil.Bool(AttrTruncated, true))
	}
	rec.Attributes = attrs
	return rec
}

// JournalctlArgs builds the journalctl command line.
func JournalctlArgs(root, cursor, startAt string, units []string, priority string) []string {
	args := []string{"--output=export", "--follow", "--no-pager"}
	if root != "" && root != "/" {
		args = append(args, "--root="+root)
	}
	switch {
	case cursor != "":
		args = append(args, "--after-cursor="+cursor)
	case startAt == "beginning":
		args = append(args, "--no-tail")
	default:
		args = append(args, "--lines=0")
	}
	for _, u := range units {
		args = append(args, "--unit="+u)
	}
	if priority != "" {
		args = append(args, "--priority="+priority)
	}
	return args
}

type journalEntry struct {
	rec    *logspb.LogRecord
	cursor string
	// cursorKey names a per-input cursor (Windows Event Log channel bookmarks); "" is the journald cursor.
	cursorKey string
}

// tailBuffer keeps the last bytes written to it (journalctl stderr).
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 4096 {
		t.buf = t.buf[len(t.buf)-4096:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

// runJournald runs journalctl and forwards entries until ctx ends. The
// subprocess is restarted with backoff, resuming after the last cursor read.
func (m *Manager) runJournald(ctx context.Context, out chan<- journalEntry) {
	cfg := m.cfg.Journald
	cursor := m.persistedCursor()
	backoff := time.Second
	for ctx.Err() == nil {
		n, last, err := m.journalOnce(ctx, out, cfg.JournalctlPath, cursor)
		if last != "" {
			cursor = last
		}
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, exec.ErrNotFound) {
			m.log.Warn("journald input disabled: journalctl not found", "journalctl_path", cfg.JournalctlPath)
			return
		}
		if n == 0 && cursor != "" && err != nil && strings.Contains(strings.ToLower(err.Error()), "cursor") {
			m.log.Warn("journal cursor rejected; starting from logs.start_at", "error", err)
			cursor = ""
		}
		if n > 0 {
			backoff = time.Second
		}
		m.log.Warn("journalctl exited; restarting", "error", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

func (m *Manager) journalOnce(ctx context.Context, out chan<- journalEntry, bin, cursor string) (int, string, error) {
	cfg := m.cfg.Journald
	cmd := exec.CommandContext(ctx, bin, JournalctlArgs(m.fs.Root(), cursor, m.cfg.StartAt, cfg.Units, cfg.Priority)...)
	stderr := &tailBuffer{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, "", err
	}
	if err := cmd.Start(); err != nil {
		return 0, "", err
	}
	r := NewExportReader(stdout)
	n := 0
	last := ""
	var readErr error
	for {
		f, err := r.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				readErr = err
			}
			break
		}
		rec := JournalRecord(f, m.cfg.MaxLineBytes, m.now(), m.cfg.MaskSecrets)
		select {
		case out <- journalEntry{rec: rec, cursor: f["__CURSOR"]}:
		case <-ctx.Done():
			_ = cmd.Wait()
			return n, last, ctx.Err()
		}
		n++
		if c := f["__CURSOR"]; c != "" {
			last = c
		}
	}
	waitErr := cmd.Wait()
	if readErr == nil {
		readErr = waitErr
	}
	if msg := stderr.String(); msg != "" {
		readErr = fmt.Errorf("%v: %s", readErr, msg)
	}
	return n, last, readErr
}
