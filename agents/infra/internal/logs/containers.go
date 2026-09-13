package logs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/containers"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// Container logs (logs.containers, semantic-conventions §4): stdout/stderr of Docker containers,
// read from the json-file log when the agent can open it (CAP_DAC_READ_SEARCH or root) and
// otherwise streamed from the Docker Engine API (GET /containers/{id}/logs, docker group).

// ContainerSource lists containers and opens log streams (containers.Source).
type ContainerSource interface {
	List(ctx context.Context, maxAge time.Duration) ([]containers.Container, error)
	Stream(ctx context.Context, uri string) (io.ReadCloser, error)
}

// LabelLogs set to "false" on a container disables its log collection.
const LabelLogs = "openlog.logs"

const (
	// ctrDrainIdle closes the log of a stopped container after it was read to the end and idle this long.
	ctrDrainIdle     = 30 * time.Second
	streamBackoffMin = 30 * time.Second
	streamBackoffMax = 5 * time.Minute
	ctrEntryBuffer   = 1024
)

// ctrLog is the log identity of one container: host resource attributes plus the container attributes.
type ctrLog struct {
	id  string
	key string // attribute fingerprint; a change rebuilds res
	res *resourcepb.Resource
	// initial: running in the first container listing (logs.start_at applies to its json-file log).
	initial bool
	// lastTS is the Docker time of the newest record read from the json-file log.
	lastTS time.Time
}

type containerEntry struct {
	id  string
	res *resourcepb.Resource
	rec *logspb.LogRecord
	n   int
	ts  time.Time
}

// apiStream is one GET /containers/{id}/logs?follow=1 reader goroutine.
type apiStream struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error // set by the goroutine before done is closed
	ended  bool
	// Container state and finish time when the stream started.
	state, finished string
	last            time.Time // newest record handed to the batch
	failures        int
	retryAt         time.Time
	drainedFor      string // FinishedAt of the stopped container whose logs were read to the end
	warned          bool
}

func runningState(s string) bool { return s == "running" || s == "paused" || s == "restarting" }

// ctrKey fingerprints container attributes.
func ctrKey(attrs []*commonpb.KeyValue) string {
	var b strings.Builder
	for _, a := range attrs {
		b.WriteString(a.Key)
		b.WriteByte('=')
		if arr := a.Value.GetArrayValue(); arr != nil {
			for _, v := range arr.Values {
				b.WriteString(v.GetStringValue())
				b.WriteByte(',')
			}
		} else {
			b.WriteString(a.Value.GetStringValue())
		}
		b.WriteByte(0)
	}
	return b.String()
}

func (m *Manager) ctrLogFor(c *containers.Container) *ctrLog {
	cl := m.ctrLogs[c.ID]
	if cl == nil {
		cl = &ctrLog{id: c.ID, initial: !m.ctrScanned && runningState(c.State)}
		m.ctrLogs[c.ID] = cl
	}
	attrs := containers.Attributes(c.ID, c.Runtime, c)
	if key := ctrKey(attrs); key != cl.key || cl.res == nil {
		host := m.res.GetAttributes()
		all := make([]*commonpb.KeyValue, 0, len(host)+len(attrs))
		cl.key, cl.res = key, &resourcepb.Resource{Attributes: append(append(all, host...), attrs...)}
	}
	return cl
}

func globMatch(pattern, v string) bool {
	if pattern == "" {
		return true
	}
	ok, _ := filepath.Match(pattern, v)
	return ok
}

// containerMatches reports whether every non-empty field of cm matches c.
func containerMatches(cm config.ContainerMatch, c *containers.Container) bool {
	// Image globs match the reference, its name without tag or its last path segment
	// ("nginx*" matches docker.io/library/nginx:1.25).
	image, _ := containers.ImageName(c.Image)
	imageOK := cm.Image == "" || globMatch(cm.Image, c.Image) || globMatch(cm.Image, image) || globMatch(cm.Image, path.Base(c.Image))
	if !globMatch(cm.Name, c.Name) || !imageOK ||
		!globMatch(cm.ComposeProject, c.Labels["com.docker.compose.project"]) || !globMatch(cm.ComposeService, c.Labels["com.docker.compose.service"]) {
		return false
	}
	if cm.Label != "" {
		k, pattern, hasValue := strings.Cut(cm.Label, "=")
		v, ok := c.Labels[k]
		if !ok || (hasValue && !globMatch(pattern, v)) {
			return false
		}
	}
	return true
}

// containerIncluded applies the openlog.logs label and logs.containers include/exclude.
func containerIncluded(cfg config.ContainerLogs, c *containers.Container) bool {
	if strings.EqualFold(c.Labels[LabelLogs], "false") || c.LogDriver == "none" {
		return false
	}
	for _, cm := range cfg.Exclude {
		if containerMatches(cm, c) {
			return false
		}
	}
	if len(cfg.Include) == 0 {
		return true
	}
	for _, cm := range cfg.Include {
		if containerMatches(cm, c) {
			return true
		}
	}
	return false
}

// containerLogActive reports whether a tailer or a running stream reads the container's log.
func (m *Manager) containerLogActive(id string) bool {
	if st := m.streams[id]; st != nil && !st.ended {
		return true
	}
	for _, t := range m.tailers {
		if t.src.ctr != nil && t.src.ctr.id == id {
			return true
		}
	}
	return false
}

func (m *Manager) hasCheckpoint(c *containers.Container) bool {
	m.stMu.Lock()
	defer m.stMu.Unlock()
	if _, ok := m.ctrSince[c.ID]; ok {
		return true
	}
	for _, st := range m.saved.Files {
		if c.LogPath != "" && st.Path == c.LogPath {
			return true
		}
	}
	return false
}

// selectContainers returns the containers whose logs are read: running ones, and stopped ones
// until their log was read to the end (stopped after the agent started, being read, or with a
// saved position). Running containers come first; at most max_containers.
func (m *Manager) selectContainers(cs []containers.Container) []containers.Container {
	var out []containers.Container
	for i := range cs {
		c := &cs[i]
		if !containerIncluded(m.cfg.Containers, c) {
			continue
		}
		if !runningState(c.State) && !m.containerLogActive(c.ID) {
			drained := c.FinishedAt != "" && m.ctrDrained[c.ID] == c.FinishedAt
			finished, err := time.Parse(time.RFC3339Nano, c.FinishedAt)
			recent := err == nil && !finished.Before(m.started)
			if drained || !(recent || m.hasCheckpoint(c)) {
				continue
			}
		}
		out = append(out, *c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := runningState(out[i].State), runningState(out[j].State); a != b {
			return a
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	if limit := m.cfg.Containers.MaxContainers; limit > 0 && len(out) > limit {
		if !m.ctrWarnedMax {
			m.log.Warn("too many containers; not collecting logs of more", "limit", limit, "containers", len(out))
			m.ctrWarnedMax = true
		}
		out = out[:limit]
	}
	return out
}

// containerMode decides how a container's log is read: "file", "api" or "" (not readable).
func (m *Manager) containerMode(c *containers.Container, fromAPI bool) string {
	fileOK := func() bool {
		if c.LogPath == "" || (c.LogDriver != "" && c.LogDriver != "json-file") {
			return false
		}
		f, err := os.Open(m.fs.Path(c.LogPath))
		if err != nil {
			return false
		}
		f.Close()
		return true
	}
	// Tty decides the stream framing; it is known once the container was inspected.
	apiOK := fromAPI && c.Inspected()
	switch m.cfg.Containers.Source {
	case config.ContainerLogSourceFile:
		if fileOK() {
			return config.ContainerLogSourceFile
		}
	case config.ContainerLogSourceAPI:
		if apiOK {
			return config.ContainerLogSourceAPI
		}
	default:
		if fileOK() {
			return config.ContainerLogSourceFile
		}
		if apiOK {
			return config.ContainerLogSourceAPI
		}
	}
	return ""
}

// listContainers lists containers through the Docker Engine API, falling back to Docker's
// containers directory. ok is false when neither worked.
func (m *Manager) listContainers() (cs []containers.Container, fromAPI, ok bool) {
	if m.ctrSrc != nil {
		cs, err := m.ctrSrc.List(context.Background(), scanInterval/2)
		if err == nil {
			return cs, true, true
		}
		if !errors.Is(err, containers.ErrNoRuntime) && !m.ctrWarnedList {
			m.log.Info("docker API not usable for container logs; reading the containers directory", "error", err, "dir", m.cfg.Containers.DockerDir)
			m.ctrWarnedList = true
		}
	}
	cs, ok = m.containerDirs()
	return cs, false, ok
}

var containerIDRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// dockerConfigV2 is the part of <containers dir>/<id>/config.v2.json used without the API.
type dockerConfigV2 struct {
	Name    string `json:"Name"`
	LogPath string `json:"LogPath"`
	Config  struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
		Tty    bool              `json:"Tty"`
	} `json:"Config"`
	State struct {
		Running    bool   `json:"Running"`
		Paused     bool   `json:"Paused"`
		Restarting bool   `json:"Restarting"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
	} `json:"State"`
}

// containerDirs lists containers from Docker's containers directory (json-file logs only).
// Names, images and labels come from config.v2.json when it is readable.
func (m *Manager) containerDirs() ([]containers.Container, bool) {
	dir := m.cfg.Containers.DockerDir
	entries, err := os.ReadDir(m.fs.Path(dir))
	if err != nil {
		return nil, false
	}
	var out []containers.Container
	for _, e := range entries {
		if !e.IsDir() || !containerIDRe.MatchString(e.Name()) {
			continue
		}
		id := e.Name()
		c := containers.Container{ID: id, Runtime: "docker", State: "running", Labels: map[string]string{},
			LogPath: path.Join(dir, id, id+"-json.log"), LogDriver: "json-file"}
		if b, err := os.ReadFile(m.fs.Path(path.Join(dir, id, "config.v2.json"))); err == nil {
			var cfg dockerConfigV2
			if json.Unmarshal(b, &cfg) == nil {
				c.Name, c.Image, c.Tty = strings.TrimPrefix(cfg.Name, "/"), cfg.Config.Image, cfg.Config.Tty
				for k, v := range cfg.Config.Labels {
					c.Labels[k] = v
				}
				if cfg.LogPath != "" {
					c.LogPath = cfg.LogPath
				}
				switch {
				case cfg.State.Restarting:
					c.State = "restarting"
				case cfg.State.Paused:
					c.State = "paused"
				case cfg.State.Running:
					c.State = "running"
				default:
					c.State = "exited"
				}
				c.Apply(containers.Details{StartedAt: parseTime(cfg.State.StartedAt), FinishedAt: parseTime(cfg.State.FinishedAt), Tty: cfg.Config.Tty,
					LogPath: c.LogPath, LogDriver: "json-file"})
			}
		}
		out = append(out, c)
	}
	return out, true
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() <= 1 {
		return time.Time{}
	}
	return t
}

// containerSources lists containers, starts and stops API log streams and returns the file
// sources of containers whose json-file log is tailed. A failed listing keeps the previous state.
func (m *Manager) containerSources(now time.Time) []*source {
	if !m.cfg.Containers.Enabled {
		return nil
	}
	m.reapStreams(now)
	cs, fromAPI, ok := m.listContainers()
	if !ok {
		return m.ctrFileSrcs
	}
	listed := make(map[string]bool, len(cs))
	for _, c := range cs {
		listed[c.ID] = true
	}
	var srcs []*source
	wantAPI := map[string]bool{}
	for _, c := range m.selectContainers(cs) {
		cl := m.ctrLogFor(&c)
		switch m.containerMode(&c, fromAPI) {
		case config.ContainerLogSourceFile:
			since := cl.lastTS
			if !since.IsZero() {
				since = since.Add(time.Nanosecond)
			} else if m.cfg.StartAt == "end" {
				since = m.started
			}
			srcs = append(srcs, &source{glob: c.LogPath, ctr: cl, ctrState: c.State, ctrFinished: c.FinishedAt, since: since})
		case config.ContainerLogSourceAPI:
			wantAPI[c.ID] = true
			m.ensureStream(&c, cl, now)
		}
	}
	for id, st := range m.streams {
		if !wantAPI[id] {
			if !st.ended {
				st.cancel()
			} else if !listed[id] {
				delete(m.streams, id)
			}
		}
	}
	if fromAPI {
		// Forget containers that were removed.
		for id := range m.ctrLogs {
			if !listed[id] && !m.containerLogActive(id) {
				delete(m.ctrLogs, id)
				delete(m.ctrDrained, id)
				m.stMu.Lock()
				if _, ok := m.ctrSince[id]; ok {
					delete(m.ctrSince, id)
					m.dirty = true
				}
				m.stMu.Unlock()
			}
		}
	}
	m.ctrScanned = true
	m.ctrFileSrcs = srcs
	return srcs
}

// reapStreams records the end of finished stream goroutines (failures back off).
func (m *Manager) reapStreams(now time.Time) {
	for id, st := range m.streams {
		if st.ended {
			continue
		}
		select {
		case <-st.done:
		default:
			continue
		}
		st.ended = true
		if st.err != nil {
			st.failures++
			st.retryAt = now.Add(min(streamBackoffMin<<(min(st.failures, 5)-1), streamBackoffMax))
			if !st.warned {
				m.log.Warn("container log stream failed", "container", id, "error", st.err, "retry_in", st.retryAt.Sub(now))
				st.warned = true
			}
			continue
		}
		st.failures, st.warned = 0, false
		if !runningState(st.state) {
			st.drainedFor = st.finished
			m.ctrDrained[id] = st.finished
		}
	}
}

func (m *Manager) baseCtx() context.Context {
	if m.runCtx != nil {
		return m.runCtx
	}
	return context.Background()
}

// ensureStream starts the log stream of a container unless one runs, a failure backs off or the
// stopped container's log was read to the end.
func (m *Manager) ensureStream(c *containers.Container, cl *ctrLog, now time.Time) {
	old := m.streams[c.ID]
	if old != nil {
		if !old.ended || now.Before(old.retryAt) || (!runningState(c.State) && old.drainedFor != "" && old.drainedFor == c.FinishedAt) {
			return
		}
	}
	var since time.Time
	m.stMu.Lock()
	committed, hasCommitted := m.ctrSince[c.ID]
	m.stMu.Unlock()
	switch {
	case old != nil && !old.last.IsZero():
		since = old.last.Add(time.Nanosecond)
	case hasCommitted:
		since = committed.Add(time.Nanosecond)
	case m.cfg.StartAt == "end":
		since = m.started
	}
	ctx, cancel := context.WithCancel(m.baseCtx())
	st := &apiStream{cancel: cancel, done: make(chan struct{}), state: c.State, finished: c.FinishedAt, last: since}
	if old != nil {
		st.failures, st.warned, st.retryAt = old.failures, old.warned, old.retryAt
		if since.IsZero() {
			st.last = old.last
		}
	}
	m.streams[c.ID] = st
	go m.runStream(ctx, st, c.ID, c.Tty, cl.res, since)
}

// runStream reads one container's log stream into m.ctrEntries (rate limited per container;
// a full channel blocks the stream, which keeps unread data in Docker).
func (m *Manager) runStream(ctx context.Context, st *apiStream, id string, tty bool, res *resourcepb.Resource, since time.Time) {
	defer close(st.done)
	uri := "/containers/" + id + "/logs?follow=1&stdout=1&stderr=1&timestamps=1&since=" + containers.SinceParam(since)
	body, err := m.ctrSrc.Stream(ctx, uri)
	if err != nil {
		if ctx.Err() == nil {
			st.err = err
		}
		return
	}
	defer body.Close()
	rate := float64(m.cfg.Containers.RateLimitLines)
	tokens, refill := rate, time.Now()
	send := func(stream int, line []byte, ts time.Time, truncated bool) bool {
		if !since.IsZero() && !ts.IsZero() && ts.Before(since) {
			return true // engines round since down to seconds
		}
		if rate > 0 {
			now := time.Now()
			tokens, refill = min(rate, tokens+now.Sub(refill).Seconds()*rate), now
			if tokens < 1 {
				select {
				case <-time.After(time.Duration((1 - tokens) / rate * float64(time.Second))):
				case <-ctx.Done():
					return false
				}
				tokens, refill = 1, time.Now()
			}
			tokens--
		}
		rec, n := m.containerRecord(line, streamName(stream), ts, truncated, time.Now())
		select {
		case m.ctrEntries <- containerEntry{id: id, res: res, rec: rec, n: n, ts: ts}:
			return true
		case <-ctx.Done():
			return false
		}
	}
	sp := &lineSplitter{raw: tty, maxBytes: m.cfg.MaxLineBytes}
	fr := containers.NewFrameReader(body, tty)
	for {
		stream, p, err := fr.Next()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, io.EOF) {
				st.err = err
			}
			if ctx.Err() == nil {
				sp.flush(send)
			}
			return
		}
		if !sp.push(stream, p, send) {
			return
		}
	}
}

func (m *Manager) addContainerEntry(e containerEntry) {
	m.add(e.res, e.rec, e.n)
	if st := m.streams[e.id]; st != nil && e.ts.After(st.last) {
		st.last = e.ts
	}
	if e.ts.After(m.batch.streams[e.id]) {
		m.batch.streams[e.id] = e.ts
	}
	m.flushIfFull()
}

// stopStreams cancels every stream and waits (bounded) for the goroutines, keeping entries they already produced.
func (m *Manager) stopStreams() {
	for _, st := range m.streams {
		st.cancel()
	}
	deadline := time.After(2 * time.Second)
	for _, st := range m.streams {
		for waiting := true; waiting; {
			select {
			case <-st.done:
				waiting = false
			case e := <-m.ctrEntries:
				m.addContainerEntry(e)
			case <-deadline:
				return
			}
		}
	}
	for {
		select {
		case e := <-m.ctrEntries:
			m.addContainerEntry(e)
		default:
			return
		}
	}
}

// handleContainerLine parses one json-file line of a container log (partial messages are joined).
func (m *Manager) handleContainerLine(t *tailer, line []byte, start int64, truncated bool) {
	e, ok := parseDockerJSON(line)
	if !ok {
		m.emitContainerFileRecord(t, line, "", time.Time{}, truncated)
		return
	}
	idx := streamIndex(e.Stream)
	p := &t.dpart[idx]
	if !p.started {
		if !t.since.IsZero() && e.Time.Before(t.since) {
			m.batch.files[t.key] = fileCheckpoint{gen: t.gen, offset: t.commitOffset()}
			return // before logs.start_at=end or already read
		}
		p.start, p.ts = start, e.Time
	}
	msg := e.Log
	last := strings.HasSuffix(msg, "\n")
	p.add([]byte(strings.TrimSuffix(msg, "\n")), m.cfg.MaxLineBytes)
	p.truncated = p.truncated || truncated
	if last {
		m.completeContainerPart(t, idx)
	}
}

func (m *Manager) completeContainerPart(t *tailer, idx int) {
	p := &t.dpart[idx]
	body := append([]byte(nil), p.buf...)
	ts, truncated := p.ts, p.truncated
	p.reset()
	m.emitContainerFileRecord(t, []byte(strings.TrimSuffix(string(body), "\r")), streamName(idx), ts, truncated)
}

// flushContainerParts emits unterminated messages (idle file, close).
func (m *Manager) flushContainerParts(t *tailer) {
	for i := range t.dpart {
		if t.dpart[i].started {
			m.completeContainerPart(t, i)
		}
	}
}

func (m *Manager) emitContainerFileRecord(t *tailer, body []byte, stream string, ts time.Time, truncated bool) {
	rec, n := m.containerRecord(body, stream, ts, truncated, m.now())
	if m.rateLimit(t) > 0 {
		t.tokens--
	}
	if cl := t.src.ctr; ts.After(cl.lastTS) {
		cl.lastTS = ts
	}
	m.add(t.src.ctr.res, rec, n)
	m.batch.files[t.key] = fileCheckpoint{gen: t.gen, offset: t.commitOffset()}
	m.flushIfFull()
}

// drained reports whether a tailer can be closed: a rotated file (drain) or the log of a
// stopped container read to the end and idle.
func (m *Manager) drained(t *tailer, fi os.FileInfo, now time.Time) bool {
	atEOF := fi.Size() <= t.readOff && len(t.buf) == 0 && t.pend == nil && !t.hasPartial()
	if !atEOF {
		return false
	}
	switch {
	case t.drain:
		return now.Sub(t.lastData) >= rotateGrace
	case t.src.ctr != nil && !runningState(t.src.ctrState) && now.Sub(t.lastData) >= ctrDrainIdle:
		m.ctrDrained[t.src.ctr.id] = t.src.ctrFinished
		t.src.ctr.initial = false
		return true
	}
	return false
}

// resumeRotated opens "<log>.1" of a container log when the saved position belongs to it: the
// json-file log was rotated while the agent was down, so the remainder of the old file is read
// before the new one (which is read from its beginning).
func (m *Manager) resumeRotated(s *source, local, host string, wanted map[string]bool, now time.Time) {
	m.stMu.Lock()
	var candidates []string
	for key, st := range m.saved.Files {
		if st.Path == host {
			candidates = append(candidates, key)
		}
	}
	m.stMu.Unlock()
	if len(candidates) == 0 {
		return
	}
	fi, err := os.Stat(local + ".1")
	if err != nil || !fi.Mode().IsRegular() {
		return
	}
	dev, ino, ok := identity(fi)
	if !ok {
		return
	}
	key := keyOf(dev, ino)
	if _, open := m.tailers[key]; open || wanted[key] {
		return
	}
	for _, c := range candidates {
		if c == key {
			wanted[key] = true
			m.open(s, local+".1", host+".1", fi, dev, ino, key, false, now)
			if t := m.tailers[key]; t != nil {
				t.drain = true
			}
			return
		}
	}
}
