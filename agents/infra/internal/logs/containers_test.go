package logs

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/containers"
	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

type fakeContainers struct {
	mu     sync.Mutex
	list   []containers.Container
	err    error
	bodies map[string][]byte
	uris   []string
}

func (f *fakeContainers) List(context.Context, time.Duration) ([]containers.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]containers.Container(nil), f.list...), f.err
}

func (f *fakeContainers) Stream(_ context.Context, uri string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uris = append(f.uris, uri)
	id := strings.Split(uri, "/")[2]
	return io.NopCloser(strings.NewReader(string(f.bodies[id]))), nil
}

func (f *fakeContainers) streamURIs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.uris...)
}

// resourceSink records the resource attributes of every emitted record.
type resourceSink struct {
	sink
	res []map[string]string
}

func (s *resourceSink) emit(ld *logspb.LogsData, n int, ack func()) {
	s.mu.Lock()
	for _, rl := range ld.ResourceLogs {
		m := map[string]string{}
		for _, kv := range rl.Resource.GetAttributes() {
			m[kv.Key] = kv.Value.GetStringValue()
		}
		for _, sl := range rl.ScopeLogs {
			for range sl.LogRecords {
				s.res = append(s.res, m)
			}
		}
	}
	s.mu.Unlock()
	s.sink.emit(ld, n, ack)
}

func startContainers(h *harness, rs *resourceSink, src ContainerSource) {
	h.m = New(Options{
		Config: h.cfg, FS: hostfs.New(h.root), StateDir: h.state, Emit: rs.emit, Containers: src,
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{Key: "host.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "h1"}}}}},
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return h.now },
	})
	h.m.started = h.now
	h.m.loadState()
}

func containerHarness(t *testing.T) (*harness, *resourceSink) {
	h := newHarness(t)
	h.cfg.Files = nil
	rs := &resourceSink{}
	h.sink = &rs.sink
	return h, rs
}

const ctrID = "c0ffee0000000000000000000000000000000000000000000000000000000001"

func jsonLine(stream, log, ts string) string {
	return `{"log":` + quote(log) + `,"stream":"` + stream + `","time":"` + ts + `"}` + "\n"
}

func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

func TestContainerJSONFileLogs(t *testing.T) {
	h, rs := containerHarness(t)
	logPath := "/var/lib/docker/containers/" + ctrID + "/" + ctrID + "-json.log"
	if err := os.MkdirAll(filepath.Dir(h.path(logPath)), 0o755); err != nil {
		t.Fatal(err)
	}
	c := containers.Container{ID: ctrID, Name: "shop-orders-1", Runtime: "docker", Image: "openlog-apmdemo/orders:1", State: "running",
		Labels: map[string]string{"com.docker.compose.project": "shop", "com.docker.compose.service": "orders"}}
	c.Apply(containers.Details{LogPath: logPath, LogDriver: "json-file"})
	src := &fakeContainers{list: []containers.Container{c}}
	h.write(logPath, jsonLine("stdout", "GET /orders 200\n", "2026-09-14T10:00:00.000000001Z")+
		jsonLine("stderr", "long message part 1, ", "2026-09-14T10:00:01Z")+
		jsonLine("stderr", "part 2\n", "2026-09-14T10:00:01.5Z")+
		jsonLine("stdout", `{"level":"error","msg":"db","trace_id":"5b8efff798038103d269b633813fc60c","span_id":"eee19b7ec3c1b174"}`+"\n", "2026-09-14T10:00:02Z")+
		"not json\n")
	startContainers(h, rs, src)
	h.tick(time.Second)
	want := []string{"GET /orders 200", "long message part 1, part 2", `{"level":"error","msg":"db","trace_id":"5b8efff798038103d269b633813fc60c","span_id":"eee19b7ec3c1b174"}`, "not json"}
	if got := h.sink.bodies(); !reflect.DeepEqual(got, want) {
		t.Fatalf("bodies = %q", got)
	}
	r := h.sink.records
	if attr(r[0], AttrSource) != "container" || attr(r[0], AttrIOStream) != "stdout" || attr(r[1], AttrIOStream) != "stderr" || attr(r[3], AttrIOStream) != "" {
		t.Errorf("attributes %v / %v", r[0].Attributes, r[1].Attributes)
	}
	if r[0].TimeUnixNano != uint64(time.Date(2026, 9, 14, 10, 0, 0, 1, time.UTC).UnixNano()) || r[1].TimeUnixNano != uint64(time.Date(2026, 9, 14, 10, 0, 1, 0, time.UTC).UnixNano()) {
		t.Errorf("timestamps %d %d", r[0].TimeUnixNano, r[1].TimeUnixNano)
	}
	if hex.EncodeToString(r[2].TraceId) != "5b8efff798038103d269b633813fc60c" || hex.EncodeToString(r[2].SpanId) != "eee19b7ec3c1b174" || r[2].SeverityText != "ERROR" {
		t.Errorf("trace context / severity %x %x %s", r[2].TraceId, r[2].SpanId, r[2].SeverityText)
	}
	res := rs.res[0]
	if res["host.id"] != "h1" || res["container.id"] != ctrID || res["container.name"] != "shop-orders-1" || res["docker.compose.service"] != "orders" || res["docker.compose.project"] != "shop" {
		t.Errorf("resource %v", res)
	}

	// A partial message waits for its end; an idle file flushes it.
	h.write(logPath, jsonLine("stdout", "unterminated", "2026-09-14T10:00:03Z"))
	h.tick(time.Second)
	if n := len(h.sink.bodies()); n != 4 {
		t.Fatalf("partial emitted early: %d records", n)
	}
	h.tick(multilineFlush)
	if b := h.sink.bodies(); b[len(b)-1] != "unterminated" {
		t.Fatalf("idle flush: %q", b)
	}
	h.stop()

	// Rotated while the agent is down: the rest of <log>.1 and the new file are both read.
	h.write(logPath, jsonLine("stdout", "written before rotation\n", "2026-09-14T10:01:00Z"))
	if err := os.Rename(h.path(logPath), h.path(logPath+".1")); err != nil {
		t.Fatal(err)
	}
	h.write(logPath, jsonLine("stdout", "new file\n", "2026-09-14T10:02:00Z"))
	h.sink.reset()
	startContainers(h, rs, src)
	h.tick(time.Second)
	got := h.sink.bodies()
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"new file", "written before rotation"}) {
		t.Fatalf("after rotation while down = %q", got)
	}
	h.tick(rotateGrace)
	h.tick(time.Second)
	if files := h.m.Files(); !reflect.DeepEqual(files, []string{logPath}) {
		t.Errorf("rotated file not closed after draining: %v", files)
	}

	// The container stops: its log is read to the end, then closed and not reopened.
	c.State, c.FinishedAt = "exited", h.now.Add(time.Second).Format(time.RFC3339Nano)
	src.mu.Lock()
	src.list = []containers.Container{c}
	src.mu.Unlock()
	h.write(logPath, jsonLine("stderr", "shutting down\n", "2026-09-14T10:03:00Z"))
	h.tick(scanInterval)
	h.tick(ctrDrainIdle)
	h.tick(time.Second)
	if files := h.m.Files(); len(files) != 0 {
		t.Errorf("stopped container still tailed: %v", files)
	}
	h.tick(scanInterval)
	if files := h.m.Files(); len(files) != 0 {
		t.Errorf("drained container reopened: %v", files)
	}
	if b := h.sink.bodies(); b[len(b)-1] != "shutting down" {
		t.Errorf("bodies %q", b)
	}
}

func dockerFrame(stream byte, payload string) string {
	b := []byte{stream, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(b[4:], uint32(len(payload)))
	return string(b) + payload
}

func (h *harness) drainEntries(n int) {
	h.t.Helper()
	deadline := time.After(3 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case e := <-h.m.ctrEntries:
			h.m.addContainerEntry(e)
		case <-deadline:
			h.t.Fatalf("got %d of %d container entries", i, n)
		}
	}
	h.m.flush()
}

func waitStreams(t *testing.T, m *Manager) {
	t.Helper()
	for _, st := range m.streams {
		select {
		case <-st.done:
		case <-time.After(3 * time.Second):
			t.Fatal("stream goroutine did not end")
		}
	}
}

func TestContainerAPIStreamLogs(t *testing.T) {
	h, rs := containerHarness(t)
	c := containers.Container{ID: ctrID, Name: "cache", Runtime: "docker", Image: "redis:7-alpine", State: "running", Labels: map[string]string{}}
	c.Apply(containers.Details{LogDriver: "local"})
	body := dockerFrame(1, "2026-09-14T10:00:00Z Ready to accept connections\n") +
		dockerFrame(2, "2026-09-14T10:00:01Z big-") + dockerFrame(2, "2026-09-14T10:00:01.1Z message\n") +
		dockerFrame(1, "2026-09-14T10:00:02Z last\n")
	src := &fakeContainers{list: []containers.Container{c}, bodies: map[string][]byte{ctrID: []byte(body)}}
	startContainers(h, rs, src)
	h.tick(time.Second)
	h.drainEntries(3)
	if got := h.sink.bodies(); !reflect.DeepEqual(got, []string{"Ready to accept connections", "big-message", "last"}) {
		t.Fatalf("bodies = %q", got)
	}
	if attr(h.sink.records[1], AttrIOStream) != "stderr" || rs.res[0]["container.name"] != "cache" {
		t.Errorf("record %v resource %v", h.sink.records[1].Attributes, rs.res[0])
	}
	if uris := src.streamURIs(); len(uris) != 1 || !strings.Contains(uris[0], "follow=1") || !strings.HasSuffix(uris[0], "since=0") {
		t.Errorf("uris = %v", uris)
	}
	waitStreams(t, h.m)
	h.stop()

	// The stream ended (EOF) while the container runs: it is reopened after the last record,
	// and a restarted agent resumes from the saved position.
	h.sink.reset()
	startContainers(h, rs, src)
	h.tick(time.Second)
	waitStreams(t, h.m)
	uris := src.streamURIs()
	if last := uris[len(uris)-1]; !strings.HasSuffix(last, "since="+containers.SinceParam(time.Date(2026, 9, 14, 10, 0, 2, 1, time.UTC))) {
		t.Errorf("resume uri = %s", last)
	}
	select {
	case e := <-h.m.ctrEntries:
		t.Errorf("already delivered record sent again: %v", e.rec.Body)
	default:
	}
}

func TestContainerSelection(t *testing.T) {
	h, rs := containerHarness(t)
	h.cfg.Containers.MaxContainers = 3
	h.cfg.Containers.Exclude = []config.ContainerMatch{{ComposeService: "loadgen"}}
	startContainers(h, rs, nil)
	mk := func(name, state string, labels map[string]string) containers.Container {
		return containers.Container{ID: name, Name: name, Image: "docker.io/library/" + name + ":1", State: state, Labels: labels}
	}
	old := mk("old", "exited", nil)
	old.FinishedAt = h.now.Add(-time.Hour).Format(time.RFC3339Nano)
	recent := mk("recent", "exited", nil)
	recent.FinishedAt = h.now.Add(time.Minute).Format(time.RFC3339Nano)
	cs := []containers.Container{
		mk("web", "running", nil), mk("quiet", "running", map[string]string{LabelLogs: "false"}),
		mk("gen", "running", map[string]string{"com.docker.compose.service": "loadgen"}), old, recent,
		mk("api", "running", nil), mk("zz", "running", nil),
	}
	var names []string
	for _, c := range h.m.selectContainers(cs) {
		names = append(names, c.Name)
	}
	if !reflect.DeepEqual(names, []string{"api", "web", "zz"}) {
		t.Errorf("selected = %v", names)
	}
	h.m.cfg.Containers.MaxContainers = 10
	h.m.cfg.Containers.Include = []config.ContainerMatch{{Image: "web*"}, {Name: "rec*"}, {Label: LabelLogs + "=f*"}}
	names = nil
	for _, c := range h.m.selectContainers(cs) {
		names = append(names, c.Name)
	}
	if !reflect.DeepEqual(names, []string{"web", "recent"}) {
		t.Errorf("included = %v", names)
	}
}

func TestLineSplitterAndTraceContext(t *testing.T) {
	type line struct {
		stream int
		text   string
		ts     time.Time
		trunc  bool
	}
	var got []line
	emit := func(stream int, b []byte, ts time.Time, truncated bool) bool {
		got = append(got, line{stream, string(b), ts, truncated})
		return true
	}
	raw := &lineSplitter{raw: true, maxBytes: 10}
	raw.push(1, []byte("2026-09-14T10:00:00Z hel"), emit)
	raw.push(1, []byte("lo\r\n2026-09-14T10:00:01Z 0123456789ABCDEF\n"), emit)
	raw.push(1, []byte("tail"), emit)
	raw.flush(emit)
	t0 := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	want := []line{{1, "hello", t0, false}, {1, "0123456789ABCDEF", t0.Add(time.Second), false}, {1, "tail", time.Time{}, false}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("raw = %+v", got)
	}

	got = nil
	mux := &lineSplitter{maxBytes: 64}
	mux.push(2, []byte("2026-09-14T10:00:00Z a\nb\n"), emit)
	if !reflect.DeepEqual(got, []line{{2, "a", t0, false}, {2, "b", t0, false}}) {
		t.Errorf("multiplexed = %+v", got)
	}

	for body, want := range map[string]string{
		`{"traceId":"5B8EFFF798038103D269B633813FC60C","spanId":"eee19b7ec3c1b174"}`: "5b8efff798038103d269b633813fc60c/eee19b7ec3c1b174",
		`{"trace_id":"00000000000000000000000000000000"}`:                            "/",
		`{"trace.id":"5b8efff798038103d269b633813fc60c"}`:                            "5b8efff798038103d269b633813fc60c/",
		`plain trace_id=5b8efff798038103d269b633813fc60c`:                            "/",
	} {
		tid, sid := traceContext(body)
		if g := hex.EncodeToString(tid) + "/" + hex.EncodeToString(sid); g != want {
			t.Errorf("%s → %s", body, g)
		}
	}
}
