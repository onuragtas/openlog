package logs

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

type sink struct {
	mu      sync.Mutex
	records []*logspb.LogRecord
	pending []func()
	noAck   bool
}

func (s *sink) emit(ld *logspb.LogsData, _ int, ack func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rl := range ld.ResourceLogs {
		for _, sl := range rl.ScopeLogs {
			s.records = append(s.records, sl.LogRecords...)
		}
	}
	if s.noAck {
		s.pending = append(s.pending, ack)
		return
	}
	ack()
}

func (s *sink) bodies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.records {
		out = append(out, r.Body.GetStringValue())
	}
	return out
}

func (s *sink) reset() {
	s.mu.Lock()
	s.records = nil
	s.mu.Unlock()
}

func attr(r *logspb.LogRecord, k string) string {
	for _, kv := range r.Attributes {
		if kv.Key == k {
			if v, ok := kv.Value.Value.(*commonpb.AnyValue_BoolValue); ok {
				return strconv.FormatBool(v.BoolValue)
			}
			return kv.Value.GetStringValue()
		}
	}
	return ""
}

func contextWithStop(stop <-chan struct{}) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

type harness struct {
	t     *testing.T
	root  string
	state string
	cfg   config.LogsConfig
	sink  *sink
	now   time.Time
	m     *Manager
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, root: t.TempDir(), state: t.TempDir(), sink: &sink{}, now: time.Unix(1704067200, 0)}
	h.cfg = config.Default().Logs
	h.cfg.StartAt = "beginning"
	h.cfg.RateLimitLines = 0
	h.cfg.Files = []config.LogFile{{Path: "/var/log/app/*.log", Exclude: []string{"*.debug.log"}, Attributes: map[string]string{"team": "core"}}}
	if err := os.MkdirAll(filepath.Join(h.root, "var/log/app"), 0o755); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) start() {
	h.m = New(Options{
		Config: h.cfg, FS: hostfs.New(h.root), StateDir: h.state, Emit: h.sink.emit,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return h.now },
	})
	h.m.started = h.now
	h.m.loadState()
}

func (h *harness) path(p string) string { return filepath.Join(h.root, p) }

func (h *harness) write(p, s string) {
	f, err := os.OpenFile(h.path(p), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		h.t.Fatal(err)
	}
	f.Close()
}

// tick advances the clock and runs one poll round.
func (h *harness) tick(d time.Duration) {
	h.now = h.now.Add(d)
	h.m.tick(h.now)
}

func (h *harness) stop() {
	for _, t := range h.m.tailers {
		h.m.flushTailer(t, h.now)
		t.f.Close()
	}
	h.m.flush()
	if err := h.m.SaveState(); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) savedOffsets() map[string]int64 {
	b, err := os.ReadFile(filepath.Join(h.state, StateFile))
	if err != nil {
		h.t.Fatal(err)
	}
	var doc stateDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		h.t.Fatal(err)
	}
	out := map[string]int64{}
	for _, st := range doc.Files {
		out[st.Path] = st.Offset
	}
	return out
}

func TestTailBasicsAndAttributes(t *testing.T) {
	h := newHarness(t)
	h.write("var/log/app/a.log", "2024/01/01 10:00:00 [error] 12#0: upstream timed out\r\nplain line\n")
	h.write("var/log/app/x.debug.log", "excluded\n")
	h.write("var/log/app/a.log", "bad \xff utf8\npartial")
	h.start()
	h.tick(time.Second)
	if got := h.sink.bodies(); !reflect.DeepEqual(got, []string{"2024/01/01 10:00:00 [error] 12#0: upstream timed out", "plain line", "bad � utf8"}) {
		t.Fatalf("bodies = %q", got)
	}
	r := h.sink.records[0]
	if attr(r, "log.file.path") != "/var/log/app/a.log" || attr(r, "log.file.name") != "a.log" || attr(r, AttrSource) != "file" ||
		attr(r, "team") != "core" || r.SeverityNumber != logspb.SeverityNumber_SEVERITY_NUMBER_ERROR || r.SeverityText != "ERROR" {
		t.Errorf("record = %v", r)
	}
	if h.sink.records[1].SeverityNumber != 0 || r.TimeUnixNano != 0 || r.ObservedTimeUnixNano == 0 {
		t.Error("severity/timestamps")
	}
	// The partial line is kept until a newline arrives or the file is idle.
	h.tick(time.Second)
	if len(h.sink.bodies()) != 3 {
		t.Fatal("partial line emitted too early")
	}
	h.write("var/log/app/a.log", " line\n")
	h.tick(time.Second)
	if b := h.sink.bodies(); b[len(b)-1] != "partial line" {
		t.Errorf("bodies = %q", b)
	}
	h.write("var/log/app/a.log", "no newline at all")
	h.tick(time.Second)
	h.tick(partialFlush)
	if b := h.sink.bodies(); b[len(b)-1] != "no newline at all" {
		t.Errorf("idle partial flush: %q", b)
	}
	h.stop()
	fi, _ := os.Stat(h.path("var/log/app/a.log"))
	if off := h.savedOffsets()["/var/log/app/a.log"]; off != fi.Size() {
		t.Errorf("saved offset = %d, want %d", off, fi.Size())
	}
}

func TestStartAtEndAndResume(t *testing.T) {
	h := newHarness(t)
	h.cfg.StartAt = "end"
	h.write("var/log/app/a.log", "old history\n")
	h.start()
	h.tick(time.Second)
	if len(h.sink.bodies()) != 0 {
		t.Fatalf("start_at end read history: %q", h.sink.bodies())
	}
	h.write("var/log/app/a.log", "new 1\n")
	h.tick(time.Second)
	// A file created after the first scan is read from its beginning.
	h.write("var/log/app/b.log", "b first\n")
	h.tick(scanInterval)
	if got := h.sink.bodies(); !reflect.DeepEqual(got, []string{"new 1", "b first"}) {
		t.Fatalf("bodies = %q", got)
	}
	h.stop()

	// Restart: offsets resume, nothing is duplicated, lines written while down are read.
	h.write("var/log/app/a.log", "while down\n")
	h.sink.reset()
	h.start()
	h.tick(time.Second)
	if got := h.sink.bodies(); !reflect.DeepEqual(got, []string{"while down"}) {
		t.Fatalf("after restart = %q", got)
	}
	h.stop()

	// Rotated while the agent was down: the new file at a known path is read from the start.
	if err := os.Rename(h.path("var/log/app/a.log"), h.path("var/log/app/a.log.1")); err != nil {
		t.Fatal(err)
	}
	h.write("var/log/app/a.log", "fresh file\n")
	h.sink.reset()
	h.start()
	h.tick(time.Second)
	if got := h.sink.bodies(); !reflect.DeepEqual(got, []string{"fresh file"}) {
		t.Fatalf("rotated while down = %q", got)
	}
}

func TestRotationRenameCreate(t *testing.T) {
	h := newHarness(t)
	h.start()
	h.write("var/log/app/a.log", "1\n2\n")
	h.tick(time.Second)
	old, err := os.OpenFile(h.path("var/log/app/a.log"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(h.path("var/log/app/a.log"), h.path("var/log/app/a.log.1")); err != nil {
		t.Fatal(err)
	}
	old.WriteString("3 (late write to the rotated file)\n")
	old.Close()
	h.write("var/log/app/a.log", "4\n")
	h.tick(time.Second)
	h.tick(time.Second)
	h.tick(rotateGrace)
	got := h.sink.bodies()
	want := []string{"1", "2", "3 (late write to the rotated file)", "4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bodies = %q", got)
	}
	if files := h.m.Files(); !reflect.DeepEqual(files, []string{"/var/log/app/a.log"}) {
		t.Errorf("rotated file still open: %v", files)
	}
	for _, r := range h.sink.records {
		if attr(r, "log.file.path") != "/var/log/app/a.log" {
			t.Errorf("path attr = %s", attr(r, "log.file.path"))
		}
	}
}

func TestCopyTruncate(t *testing.T) {
	h := newHarness(t)
	h.start()
	h.write("var/log/app/a.log", "a long first generation line\nsecond\n")
	h.tick(time.Second)
	if err := os.Truncate(h.path("var/log/app/a.log"), 0); err != nil {
		t.Fatal(err)
	}
	h.write("var/log/app/a.log", "after\n")
	h.tick(time.Second)
	if got := h.sink.bodies(); !reflect.DeepEqual(got, []string{"a long first generation line", "second", "after"}) {
		t.Fatalf("bodies = %q", got)
	}
	h.stop()
	if off := h.savedOffsets()["/var/log/app/a.log"]; off != int64(len("after\n")) {
		t.Errorf("offset after truncate = %d", off)
	}

	// Restart with start_at end: the truncated file's saved state still matches,
	// so a line written while the agent was down is read.
	h.cfg.StartAt = "end"
	h.write("var/log/app/a.log", "while down\n")
	h.sink.reset()
	h.start()
	h.tick(time.Second)
	if got := h.sink.bodies(); !reflect.DeepEqual(got, []string{"while down"}) {
		t.Fatalf("after restart = %q", got)
	}
	h.stop()

	// Truncated and rewritten while down (fingerprint mismatch at a known path): read from the start.
	if err := os.WriteFile(h.path("var/log/app/a.log"), []byte("rewritten\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.sink.reset()
	h.start()
	h.tick(time.Second)
	if got := h.sink.bodies(); !reflect.DeepEqual(got, []string{"rewritten"}) {
		t.Fatalf("rewritten while down = %q", got)
	}
}

func TestMultilineAndMaxLine(t *testing.T) {
	h := newHarness(t)
	h.cfg.MaxLineBytes = 256
	h.cfg.Files[0].MultilineStart = `^\d{4}-\d{2}-\d{2} `
	h.start()
	h.write("var/log/app/a.log", "orphan continuation\n2024-01-01 ERROR boom\n  at a.b(C.java:1)\n  at d.e(F.java:2)\n2024-01-01 INFO next\n")
	h.tick(time.Second)
	if got := h.sink.bodies(); !reflect.DeepEqual(got, []string{"orphan continuation", "2024-01-01 ERROR boom\n  at a.b(C.java:1)\n  at d.e(F.java:2)"}) {
		t.Fatalf("bodies = %q", got)
	}
	// The pending record is not committed: the offset points at its start line.
	h.m.flush()
	pendStart := int64(len("orphan continuation\n2024-01-01 ERROR boom\n  at a.b(C.java:1)\n  at d.e(F.java:2)\n"))
	if st := h.m.committed[h.m.tailers[keys(h.m.tailers)[0]].key]; st.Offset != pendStart {
		t.Errorf("committed offset = %d, want %d", st.Offset, pendStart)
	}
	h.tick(multilineFlush)
	if b := h.sink.bodies(); b[len(b)-1] != "2024-01-01 INFO next" {
		t.Errorf("idle multiline flush: %q", b)
	}

	h.sink.reset()
	long := strings.Repeat("x", 1000)
	h.write("var/log/app/a.log", "2024-01-02 "+long+"\n2024-01-03 short\n")
	h.tick(time.Second)
	h.tick(multilineFlush)
	recs := h.sink.records
	if len(recs) != 2 || len(recs[0].Body.GetStringValue()) != 256 || attr(recs[0], AttrTruncated) != "true" ||
		recs[1].Body.GetStringValue() != "2024-01-03 short" || attr(recs[1], AttrTruncated) != "" {
		t.Fatalf("truncation: %d records %q", len(recs), h.sink.bodies())
	}

	// An over-long line without newline is emitted truncated once; its rest is skipped.
	h.sink.reset()
	h.cfg.Files[0].MultilineStart = ""
	h.stop()
	h.start()
	h.write("var/log/app/a.log", strings.Repeat("y", 300000)+"\nafter long\n")
	for i := 0; i < 5; i++ {
		h.tick(time.Second)
	}
	if got := h.sink.bodies(); len(got) != 2 || len(got[0]) != 256 || got[1] != "after long" {
		t.Fatalf("over-long line: %d records", len(got))
	}
}

func keys(m map[string]*tailer) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestRateLimitAndAck(t *testing.T) {
	h := newHarness(t)
	h.cfg.RateLimitLines = 3
	h.sink.noAck = true
	h.start()
	h.write("var/log/app/a.log", "1\n2\n3\n4\n5\n6\n7\n")
	h.tick(0)
	if n := len(h.sink.bodies()); n != 3 {
		t.Fatalf("burst = %d records, want 3", n)
	}
	h.tick(time.Second)
	h.tick(time.Second)
	if got := h.sink.bodies(); !reflect.DeepEqual(got, []string{"1", "2", "3", "4", "5", "6", "7"}) {
		t.Fatalf("bodies = %q", got)
	}
	// Nothing acknowledged yet: the committed offset stays at 0.
	for _, st := range h.m.committed {
		if st.Offset != 0 {
			t.Fatalf("offset committed without ack: %d", st.Offset)
		}
	}
	for _, ack := range h.sink.pending {
		ack()
	}
	for _, st := range h.m.committed {
		if st.Offset != 14 {
			t.Errorf("offset after ack = %d", st.Offset)
		}
	}
}

func TestPausedKeepsDataAtSource(t *testing.T) {
	h := newHarness(t)
	h.start()
	paused := true
	h.m.paused = func() bool { return paused }
	h.write("var/log/app/a.log", "1\n")
	h.tick(time.Second)
	if len(h.sink.bodies()) != 0 {
		t.Fatal("read while paused")
	}
	paused = false
	h.tick(time.Second)
	if len(h.sink.bodies()) != 1 {
		t.Fatal("not resumed")
	}
}

func TestDiscoveryDriven(t *testing.T) {
	h := newHarness(t)
	h.cfg.Files = nil
	h.cfg.AutoFromDiscovery = true
	h.cfg.MaskSecrets = true
	if err := os.MkdirAll(h.path("var/log/nginx"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.write("var/log/nginx/error.log", "old\n")
	h.start()
	h.tick(time.Second)
	if len(h.m.Files()) != 0 {
		t.Fatal("tailing before discovery")
	}
	h.m.SetDiscovered([]DiscoveredLog{{Path: "/var/log/nginx/*.log", DiscoveryID: "nginx"}})
	h.write("var/log/nginx/error.log", "GET /login?x=1 password=hunter2 mysql://app:s3cret@db/app\n")
	h.tick(time.Second)
	recs := h.sink.records
	if len(recs) != 2 || attr(recs[0], AttrDiscoveryID) != "nginx" {
		t.Fatalf("records = %v", h.sink.bodies())
	}
	if b := recs[1].Body.GetStringValue(); b != "GET /login?x=1 password=*** mysql://app:***@db/app" {
		t.Errorf("masked body = %q", b)
	}
	// Service gone: the file is no longer tailed.
	h.m.SetDiscovered(nil)
	h.tick(time.Second)
	if len(h.m.Files()) != 0 {
		t.Error("file still tailed after discovery removed it")
	}

	// Explicit file + auto_from_discovery off: the discovery id is still attached.
	h2 := newHarness(t)
	h2.cfg.Files = []config.LogFile{{Path: "/var/log/app/*.log"}}
	h2.start()
	h2.m.SetDiscovered([]DiscoveredLog{{Path: "/var/log/app/a.log", DiscoveryID: "myapp"}, {Path: "/var/log/other/*.log", DiscoveryID: "x"}})
	h2.write("var/log/app/a.log", "hello\n")
	h2.tick(time.Second)
	if len(h2.sink.records) != 1 || attr(h2.sink.records[0], AttrDiscoveryID) != "myapp" || len(h2.m.Files()) != 1 {
		t.Errorf("explicit + discovery id: %v %v", h2.sink.bodies(), h2.m.Files())
	}
}

func TestParseSeverity(t *testing.T) {
	cases := map[string]logspb.SeverityNumber{
		"2024/01/01 12:00:00 [error] 1#0: *1 open() failed":                    logspb.SeverityNumber_SEVERITY_NUMBER_ERROR,
		"[Mon Jan 01 12:00:00.1 2024] [core:warn] [pid 1] AH00111":             logspb.SeverityNumber_SEVERITY_NUMBER_WARN,
		"2024-01-01 12:00:00.000 UTC [123] FATAL:  password authentication":    logspb.SeverityNumber_SEVERITY_NUMBER_FATAL,
		"2024-01-01T12:00:00.000000Z 0 [Warning] [MY-010068] [Server] CA cert": logspb.SeverityNumber_SEVERITY_NUMBER_WARN,
		`{"time":"2024-01-01T12:00:00Z","level":"debug","msg":"x"}`:            logspb.SeverityNumber_SEVERITY_NUMBER_DEBUG,
		"ts=1 level=info msg=started":                                          logspb.SeverityNumber_SEVERITY_NUMBER_INFO,
		`127.0.0.1 - - [01/Jan/2024:12:00:00 +0000] "GET /error HTTP/1.1" 404`: logspb.SeverityNumber_SEVERITY_NUMBER_UNSPECIFIED,
		"errors are fine as part of a word: errorless":                         logspb.SeverityNumber_SEVERITY_NUMBER_UNSPECIFIED,
		strings.Repeat("x ", 70) + "ERROR too far":                             logspb.SeverityNumber_SEVERITY_NUMBER_UNSPECIFIED,
	}
	for line, want := range cases {
		if got, _ := ParseSeverity(line); got != want {
			t.Errorf("%q → %v, want %v", line, got, want)
		}
	}
}

func exportEntry(fields ...string) []byte {
	var b bytes.Buffer
	for _, f := range fields {
		if name, val, ok := strings.Cut(f, "\x00"); ok { // binary encoding
			b.WriteString(name + "\n")
			binary.Write(&b, binary.LittleEndian, uint64(len(val)))
			b.WriteString(val + "\n")
			continue
		}
		b.WriteString(f + "\n")
	}
	b.WriteString("\n")
	return b.Bytes()
}

func TestExportReaderAndJournalRecord(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(exportEntry("__CURSOR=s=abc;i=1", "__REALTIME_TIMESTAMP=1704067200123456", "PRIORITY=3", "_SYSTEMD_UNIT=nginx.service",
		"_PID=812", "_COMM=nginx", "SYSLOG_IDENTIFIER=nginx", "_BOOT_ID=ignored", "MESSAGE=connect() failed token=abc"))
	stream.Write(exportEntry("__CURSOR=s=abc;i=2", "PRIORITY=6", "MESSAGE\x00line one\nline two", "COREDUMP\x00\x00\x01\x02binary"))
	r := NewExportReader(&stream)
	e1, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if e1["_BOOT_ID"] != "" || e1["MESSAGE"] != "connect() failed token=abc" || e1["__CURSOR"] != "s=abc;i=1" {
		t.Errorf("entry 1 = %v", e1)
	}
	rec := JournalRecord(e1, 64, time.Unix(1, 0), true)
	if rec.TimeUnixNano != 1704067200123456000 || rec.SeverityNumber != logspb.SeverityNumber_SEVERITY_NUMBER_ERROR || rec.SeverityText != "ERROR" ||
		attr(rec, AttrSystemdUnit) != "nginx.service" || attr(rec, "process.command") != "nginx" || attr(rec, AttrSource) != "journald" ||
		attr(rec, AttrSyslogIdentifier) != "nginx" || rec.Body.GetStringValue() != "connect() failed token=***" {
		t.Errorf("record = %v", rec)
	}
	var pid int64
	for _, kv := range rec.Attributes {
		if kv.Key == "process.pid" {
			pid = kv.Value.GetIntValue()
		}
	}
	if pid != 812 {
		t.Errorf("pid = %d", pid)
	}
	e2, err := r.Next()
	if err != nil || e2["MESSAGE"] != "line one\nline two" || e2["COREDUMP"] != "" {
		t.Fatalf("entry 2 = %q %v", e2, err)
	}
	if rec := JournalRecord(e2, 5, time.Unix(1, 0), false); rec.Body.GetStringValue() != "line " || attr(rec, AttrTruncated) != "true" ||
		rec.SeverityNumber != logspb.SeverityNumber_SEVERITY_NUMBER_INFO {
		t.Errorf("truncated record = %v", rec)
	}
	if _, err := r.Next(); err != io.EOF {
		t.Errorf("end = %v", err)
	}
	// A stream cut inside a binary field is an error, not a silent entry.
	cut := exportEntry("MESSAGE\x00abcdef")
	if _, err := NewExportReader(bytes.NewReader(cut[:12])).Next(); err == nil || err == io.EOF {
		t.Errorf("truncated stream: %v", err)
	}
}

func TestJournalctlArgs(t *testing.T) {
	got := JournalctlArgs("/host", "s=1", "end", []string{"nginx.service"}, "warning")
	want := []string{"--output=export", "--follow", "--no-pager", "--root=/host", "--after-cursor=s=1", "--unit=nginx.service", "--priority=warning"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v", got)
	}
	if a := JournalctlArgs("/", "", "end", nil, ""); a[len(a)-1] != "--lines=0" {
		t.Errorf("end = %v", a)
	}
	if a := JournalctlArgs("/", "", "beginning", nil, ""); a[len(a)-1] != "--no-tail" {
		t.Errorf("beginning = %v", a)
	}
}

// A fake journalctl (shell script) exercises the subprocess path and cursor persistence.
func TestJournaldSubprocess(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	h := newHarness(t)
	h.cfg.Files = nil
	h.cfg.Journald.Enabled = true
	dir := t.TempDir()
	data := filepath.Join(dir, "entries")
	if err := os.WriteFile(data, append(exportEntry("__CURSOR=c1", "PRIORITY=4", "MESSAGE=first"), exportEntry("__CURSOR=c2", "MESSAGE=second")...), 0o644); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(dir, "args")
	script := filepath.Join(dir, "journalctl")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\" >> "+argsFile+"\ncat "+data+"\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.cfg.Journald.JournalctlPath = script
	h.start()
	h.m.cfg.Journald = h.cfg.Journald
	done := make(chan struct{})
	ctxStop := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := contextWithStop(ctxStop)
		defer cancel()
		h.m.Run(ctx)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for len(h.sink.bodies()) < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	close(ctxStop)
	<-done
	if got := h.sink.bodies(); !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("bodies = %q", got)
	}
	if err := h.m.SaveState(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(h.state, StateFile))
	if !strings.Contains(string(b), `"cursor":"c2"`) {
		t.Errorf("state = %s", b)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--output=export") || !strings.Contains(string(args), "--no-tail") {
		t.Errorf("args = %s", args)
	}
}
