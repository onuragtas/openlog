package logs

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// TestJournaldRealJournal runs the journald input with the real journalctl (--root) against a
// real journal file, written by systemd-journal-remote from export-format input: entries read
// from the beginning, --follow picks up entries appended while running, and the persisted cursor
// resumes after a restart without duplicates. Skipped unless both tools are installed, e.g.
//
//	docker run --rm -v "$PWD/../..":/src -w /src/agents/infra golang:1.26 sh -c \
//	  'apt-get update -qq && apt-get install -y -qq systemd systemd-journal-remote >/dev/null &&
//	   go test -count=1 -run TestJournaldRealJournal -v ./internal/logs/'
func TestJournaldRealJournal(t *testing.T) {
	journalctl, err := exec.LookPath("journalctl")
	if err != nil {
		t.Skip("journalctl not installed")
	}
	remote := ""
	for _, p := range []string{"/usr/lib/systemd/systemd-journal-remote", "/lib/systemd/systemd-journal-remote"} {
		if _, err := os.Stat(p); err == nil {
			remote = p
			break
		}
	}
	if remote == "" {
		t.Skip("systemd-journal-remote not installed")
	}

	const (
		machineID = "0e2e00000000000000000000000000aa"
		bootID    = "0e2e00000000000000000000000000bb"
	)
	h := newHarness(t)
	h.cfg.Files = nil
	h.cfg.Journald.Enabled = true
	h.cfg.Journald.JournalctlPath = journalctl
	// journalctl shows the journals of the local machine: <root>/etc/machine-id and
	// <root>/var/log/journal/<machine-id>/system.journal.
	dir := h.path("var/log/journal/" + machineID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(h.path("etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.path("etc/machine-id"), []byte(machineID+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seq := 0
	appendJournal := func(entries ...[]string) {
		t.Helper()
		var in bytes.Buffer
		for _, fields := range entries {
			seq++
			in.Write(exportEntry(append([]string{
				"__REALTIME_TIMESTAMP=" + strconv.FormatInt(time.Now().UnixMicro(), 10),
				"__MONOTONIC_TIMESTAMP=" + strconv.Itoa(1000000+seq), "_BOOT_ID=" + bootID, "_MACHINE_ID=" + machineID,
				"_HOSTNAME=journal-test", "_PID=" + strconv.Itoa(4000+seq), "_COMM=writer", "SYSLOG_IDENTIFIER=openlog-test",
			}, fields...)...))
		}
		cmd := exec.Command(remote, "--output="+filepath.Join(dir, "system.journal"), "-")
		cmd.Stdin = &in
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("systemd-journal-remote: %v\n%s", err, out)
		}
	}
	run := func() (stop func()) {
		h.start()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			h.m.Run(ctx)
		}()
		return func() {
			cancel()
			<-done
			if err := h.m.SaveState(); err != nil {
				t.Fatal(err)
			}
		}
	}
	// waitBodies waits for want, then checks that nothing else arrives within two poll intervals.
	waitBodies := func(what string, want []string) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for len(h.sink.bodies()) < len(want) && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		time.Sleep(2 * h.cfg.PollInterval.D())
		if got := h.sink.bodies(); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: bodies = %q, want %q", what, got, want)
		}
	}

	appendJournal([]string{"PRIORITY=3", "MESSAGE=first"}, []string{"PRIORITY=6", "MESSAGE\x00line one\nline two"})
	stop := run()
	waitBodies("from the beginning", []string{"first", "line one\nline two"})
	appendJournal([]string{"PRIORITY=4", "MESSAGE=while running"})
	waitBodies("follow", []string{"first", "line one\nline two", "while running"})
	stop()

	r := h.sink.records[0]
	if r.SeverityNumber != logspb.SeverityNumber_SEVERITY_NUMBER_ERROR || r.TimeUnixNano == 0 || attr(r, AttrSource) != SourceJournald ||
		attr(r, AttrSyslogIdentifier) != "openlog-test" || attr(r, "process.command") != "writer" {
		t.Errorf("record = %v", r)
	}
	if r := h.sink.records[2]; r.SeverityNumber != logspb.SeverityNumber_SEVERITY_NUMBER_WARN {
		t.Errorf("severity of the followed entry = %v", r.SeverityNumber)
	}

	// Restart: the saved cursor resumes after "while running".
	appendJournal([]string{"PRIORITY=6", "MESSAGE=while down"})
	h.sink.reset()
	stop = run()
	waitBodies("after restart", []string{"while down"})
	stop()
}
