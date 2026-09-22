package logs

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/containers"
)

func TestParseCRILine(t *testing.T) {
	cases := []struct {
		line, stream, msg string
		partial, ok       bool
	}{
		{"2026-09-14T10:00:00.123456789Z stdout F GET / 200", "stdout", "GET / 200", false, true},
		{"2026-09-14T10:00:00Z stderr P:extra part one ", "stderr", "part one ", true, true},
		{"2026-09-14T10:00:00Z stdout F", "stdout", "", false, true},
		{"2026-09-14T10:00:00Z stdin F x", "", "", false, false},
		{"2026-09-14T10:00:00Z stdout X x", "", "", false, false},
		{"plain text line", "", "", false, false},
	}
	for _, c := range cases {
		_, stream, partial, msg, ok := parseCRILine([]byte(c.line))
		if ok != c.ok || (ok && (stream != c.stream || partial != c.partial || string(msg) != c.msg)) {
			t.Errorf("%q → %q %v %q %v", c.line, stream, partial, msg, ok)
		}
	}
}

func TestContainerCRIFileLogs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux fixture (POSIX paths, file modes, unix sockets or shell scripts); not portable to Windows")
	}
	h, rs := containerHarness(t)
	logPath := "/var/log/pods/prod_web-7d9_uid/app/0.log"
	if err := os.MkdirAll(filepath.Dir(h.path(logPath)), 0o755); err != nil {
		t.Fatal(err)
	}
	c := containers.Container{ID: ctrID, Name: "app", Runtime: "containerd", Image: "docker.io/library/nginx:1.25", State: "running",
		Labels: map[string]string{"io.kubernetes.pod.name": "web-7d9", "io.kubernetes.pod.namespace": "prod", "io.kubernetes.container.name": "app"}}
	c.Apply(containers.Details{LogPath: logPath, LogDriver: containers.LogDriverCRI})
	src := &fakeContainers{list: []containers.Container{c}}
	h.write(logPath, "2026-09-14T10:00:00.000000001Z stdout F GET / 200\n"+
		"2026-09-14T10:00:01Z stderr P long message part 1, \n"+
		"2026-09-14T10:00:00.5Z stdout F between\n"+
		"2026-09-14T10:00:01.5Z stderr F part 2\n"+
		"2026-09-14T10:00:02Z stdout F\n"+
		"garbage\n")
	startContainers(h, rs, src)
	h.tick(time.Second)
	want := []string{"GET / 200", "between", "long message part 1, part 2", "", "garbage"}
	if got := h.sink.bodies(); !reflect.DeepEqual(got, want) {
		t.Fatalf("bodies = %q", got)
	}
	r := h.sink.records
	if attr(r[0], AttrIOStream) != "stdout" || attr(r[2], AttrIOStream) != "stderr" || attr(r[4], AttrIOStream) != "" ||
		r[2].TimeUnixNano != uint64(time.Date(2026, 9, 14, 10, 0, 1, 0, time.UTC).UnixNano()) {
		t.Errorf("records %v", r)
	}
	if res := rs.res[0]; res["container.runtime"] != "containerd" || res["k8s.pod.name"] != "web-7d9" || res["k8s.namespace.name"] != "prod" || res["container.name"] != "app" {
		t.Errorf("resource %v", res)
	}
	if uris := src.streamURIs(); len(uris) != 0 {
		t.Errorf("CRI containers must not use the Docker log stream: %v", uris)
	}
	// Without a readable file the CRI container is not read at all (no API fallback).
	c.Apply(containers.Details{LogPath: "/var/log/pods/missing/0.log", LogDriver: containers.LogDriverCRI})
	if mode := h.m.containerMode(&c, true); mode != "" {
		t.Errorf("mode = %q", mode)
	}
}

func TestContainerMultilineFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux fixture (POSIX paths, file modes, unix sockets or shell scripts); not portable to Windows")
	}
	h, rs := containerHarness(t)
	h.cfg.MaxLineBytes = 256
	h.cfg.Containers.Include = []config.ContainerMatch{{Name: "billing", MultilineStart: `^\d{4}-\d{2}-\d{2} `}, {Name: "*"}}
	logPath := "/var/lib/docker/containers/" + ctrID + "/" + ctrID + "-json.log"
	if err := os.MkdirAll(filepath.Dir(h.path(logPath)), 0o755); err != nil {
		t.Fatal(err)
	}
	c := containers.Container{ID: ctrID, Name: "billing", Runtime: "docker", State: "running", Labels: map[string]string{}}
	c.Apply(containers.Details{LogPath: logPath, LogDriver: "json-file"})
	first := jsonLine("stdout", "2026-09-14 10:00:00 ERROR boom\n", "2026-09-14T10:00:00Z")
	h.write(logPath, first+
		jsonLine("stderr", "2026-09-14 10:00:00 WARN slow\n", "2026-09-14T10:00:00.1Z")+
		jsonLine("stdout", "  atFoo.bar\n", "2026-09-14T10:00:00.2Z")+
		jsonLine("stdout", "  atBaz.qux\n", "2026-09-14T10:00:00.3Z")+
		jsonLine("stderr", "  took 3s\n", "2026-09-14T10:00:00.4Z")+
		jsonLine("stdout", "2026-09-14 10:00:01 INFO ok\n", "2026-09-14T10:00:01Z"))
	startContainers(h, rs, &fakeContainers{list: []containers.Container{c}})
	h.tick(time.Second)
	group := "2026-09-14 10:00:00 ERROR boom\n  atFoo.bar\n  atBaz.qux"
	if got := h.sink.bodies(); !reflect.DeepEqual(got, []string{group}) {
		t.Fatalf("bodies = %q", got)
	}
	if r := h.sink.records[0]; r.TimeUnixNano != uint64(time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC).UnixNano()) || attr(r, AttrIOStream) != "stdout" {
		t.Errorf("record %v", r)
	}
	var tl *tailer
	for _, x := range h.m.tailers {
		tl = x
	}
	// The pending stderr record starts at the second json line: the checkpoint must not pass it.
	if off := tl.commitOffset(); off != int64(len(first)) {
		t.Errorf("commit offset = %d, want %d", off, len(first))
	}
	if cl := h.m.ctrLogs[ctrID]; !cl.lastTS.Equal(time.Date(2026, 9, 14, 10, 0, 0, 300000000, time.UTC)) {
		t.Errorf("position = %v (want the time of the group's last line)", cl.lastTS)
	}
	h.tick(multilineFlush)
	want := []string{group, "2026-09-14 10:00:01 INFO ok", "2026-09-14 10:00:00 WARN slow\n  took 3s"}
	if got := h.sink.bodies(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after idle flush = %q", got)
	}

	// A stream idle while the other keeps writing is flushed on its own.
	h.sink.reset()
	h.write(logPath, jsonLine("stderr", "2026-09-14 10:00:02 ERROR a\n", "2026-09-14T10:00:02Z"))
	h.tick(time.Second)
	for i := 0; i < 3; i++ {
		h.write(logPath, jsonLine("stdout", "2026-09-14 10:00:03 INFO tick\n", "2026-09-14T10:00:03Z"))
		h.tick(time.Second)
	}
	if got := h.sink.bodies(); len(got) < 2 || got[0] != "2026-09-14 10:00:03 INFO tick" || !contains(got, "2026-09-14 10:00:02 ERROR a") {
		t.Errorf("idle stream flush = %q", got)
	}

	// Records are cut at max_line_bytes.
	h.tick(multilineFlush)
	h.sink.reset()
	lines := jsonLine("stdout", "2026-09-14 10:00:04 ERROR big\n", "2026-09-14T10:00:04Z")
	for i := 0; i < 40; i++ {
		lines += jsonLine("stdout", "    at frame number xx\n", "2026-09-14T10:00:04Z")
	}
	h.write(logPath, lines)
	h.tick(time.Second)
	h.tick(multilineFlush)
	got := h.sink.records
	if len(got) != 1 || len(got[0].Body.GetStringValue()) > 256 || attr(got[0], AttrTruncated) != "true" ||
		!strings.HasPrefix(got[0].Body.GetStringValue(), "2026-09-14 10:00:04 ERROR big\n    at frame") {
		t.Errorf("truncated record = %v", got)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestContainerMultilinePrecedence(t *testing.T) {
	h, rs := containerHarness(t)
	h.cfg.Containers.Include = []config.ContainerMatch{{Name: "billing*", MultilineStart: `^\d`}, {Name: "*"}}
	startContainers(h, rs, nil)
	pattern := func(c containers.Container) string {
		if re := h.m.containerMultiline(&c); re != nil {
			return re.String()
		}
		return ""
	}
	if p := pattern(containers.Container{Name: "billing-1"}); p != `^\d` {
		t.Errorf("include = %q", p)
	}
	if p := pattern(containers.Container{Name: "billing-1", Labels: map[string]string{LabelMultiline: `^START`}}); p != `^START` {
		t.Errorf("label must win: %q", p)
	}
	if p := pattern(containers.Container{Name: "web"}); p != "" {
		t.Errorf("no pattern: %q", p)
	}
	if p := pattern(containers.Container{Name: "web", Labels: map[string]string{LabelMultiline: `(`}}); p != "" {
		t.Errorf("invalid label pattern: %q", p)
	}
}

func TestContainerMultilineAPIStream(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux fixture (POSIX paths, file modes, unix sockets or shell scripts); not portable to Windows")
	}
	h, rs := containerHarness(t)
	c := containers.Container{ID: ctrID, Name: "worker", Runtime: "docker", State: "running", Labels: map[string]string{LabelMultiline: `^\S`}}
	c.Apply(containers.Details{LogDriver: "local"})
	body := dockerFrame(1, "2026-09-14T10:00:00Z Traceback (most recent call last):\n") +
		dockerFrame(1, "2026-09-14T10:00:00.1Z   File \"app.py\", line 1\n") +
		dockerFrame(2, "2026-09-14T10:00:00.2Z E stderr line\n") +
		dockerFrame(1, "2026-09-14T10:00:01Z next\n")
	src := &fakeContainers{list: []containers.Container{c}, bodies: map[string][]byte{ctrID: []byte(body)}}
	startContainers(h, rs, src)
	h.tick(time.Second)
	h.drainEntries(3)
	want := []string{"Traceback (most recent call last):\n  File \"app.py\", line 1", "next", "E stderr line"}
	if got := h.sink.bodies(); !reflect.DeepEqual(got, want) {
		t.Fatalf("bodies = %q", got)
	}
	waitStreams(t, h.m)
	if st := h.m.streams[ctrID]; !st.last.Equal(time.Date(2026, 9, 14, 10, 0, 1, 0, time.UTC)) {
		t.Errorf("stream position = %v", st.last)
	}
}

// A stack trace is one event, and every runtime indents its frames. With logs.join_continuations on, an
// indented or "at …" line joins the record before it even though no multiline_start applies; a line that
// is not a continuation ends that record and starts the next. Taken from a real Rocket.Chat crash, which
// arrived as one log line per frame.
func TestGrouperJoinsContinuationsWhenAsked(t *testing.T) {
	lines := []string{
		"errorClass [Error]: [An error occurred when creating an index]",
		"    at Collection.createIndexAsync (packages/mongo/collection/methods_index.js:64:15)",
		"    at module.wrapAsync.self (packages/accounts-password/password_server.js:1097:1) {",
		"  isClientSafe: true,",
		"Node.js v22.13.1",
	}
	collect := func(join bool) []string {
		var got []string
		emit := func(_ int, body []byte, _, _ time.Time, _ bool) bool {
			got = append(got, string(body))
			return true
		}
		g := &multilineGrouper{maxBytes: 1 << 20, join: join}
		now := time.Unix(0, 0)
		for _, l := range lines {
			g.push(0, []byte(l), time.Time{}, 0, false, now, emit)
		}
		g.flushAll(emit)
		return got
	}

	want := []string{
		"errorClass [Error]: [An error occurred when creating an index]\n" +
			"    at Collection.createIndexAsync (packages/mongo/collection/methods_index.js:64:15)\n" +
			"    at module.wrapAsync.self (packages/accounts-password/password_server.js:1097:1) {\n" +
			"  isClientSafe: true,",
		"Node.js v22.13.1",
	}
	if got := collect(true); !reflect.DeepEqual(got, want) {
		t.Errorf("joined = %q, want %q", got, want)
	}
	// Off (the default): every line is its own record, exactly as before. This pins the default so it
	// cannot be flipped without a test saying so.
	if got := collect(false); !reflect.DeepEqual(got, lines) {
		t.Errorf("not joined = %q, want one record per line", got)
	}
}

// Java's unindented continuations are recognised too, and a Java-style trace ends at the next real line.
func TestGrouperJoinsJavaContinuations(t *testing.T) {
	var got []string
	emit := func(_ int, body []byte, _, _ time.Time, _ bool) bool { got = append(got, string(body)); return true }
	g := &multilineGrouper{maxBytes: 1 << 20, join: true}
	now := time.Unix(0, 0)
	for _, l := range []string{
		"Exception in thread \"main\" java.lang.IllegalStateException: boom",
		"\tat com.example.App.main(App.java:12)",
		"Caused by: java.io.IOException: disk",
		"\t... 12 more",
		"INFO  ready",
	} {
		g.push(0, []byte(l), time.Time{}, 0, false, now, emit)
	}
	g.flushAll(emit)
	if len(got) != 2 || !strings.Contains(got[0], "Caused by:") || got[1] != "INFO  ready" {
		t.Fatalf("records = %q", got)
	}
}
