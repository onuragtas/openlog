// Package logs collects log records from files (glob patterns, rotation aware,
// persisted offsets) and from journald (journalctl export format, persisted
// cursor) and emits them as OTLP LogRecords.
//
// Delivery is at-least-once: offsets and the journal cursor are committed only
// after the exporter acknowledged a batch (sent, persisted to the disk buffer
// or definitively dropped), and the state file is written atomically.
package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// Attribute names and values of log records (semantic-conventions §4).
const (
	AttrSource           = "openlog.log.source"
	AttrDiscoveryID      = "openlog.discovery.id"
	AttrSystemdUnit      = "openlog.systemd.unit"
	AttrSyslogIdentifier = "openlog.syslog.identifier"
	AttrTruncated        = "openlog.log.truncated"
	SourceFile           = "file"
	SourceJournald       = "journald"
)

// StateFile is the name of the offsets/cursor file in the state dir.
const StateFile = "logs-state.json"

const (
	scanInterval    = 10 * time.Second
	saveInterval    = 5 * time.Second
	keepSavedFor    = 10 * time.Minute
	maxFiles        = 512
	batchMaxRecords = 1000
)

// DiscoveredLog is a log glob declared by the rule of a discovered service.
type DiscoveredLog struct {
	Path        string
	DiscoveryID string
}

// Emitter hands a batch to the export pipeline. ack must be called exactly
// once, when the batch was sent, persisted or dropped.
type Emitter func(ld *logspb.LogsData, records int, ack func())

// Options configure a Manager.
type Options struct {
	Config        config.LogsConfig
	FS            *hostfs.FS
	StateDir      string
	Resource      *resourcepb.Resource
	Scope         *commonpb.InstrumentationScope
	Emit          Emitter
	Paused        func() bool // export backlog: stop reading, keep data at its source
	Log           *slog.Logger
	MaxBatchBytes int
	Now           func() time.Time
	// Containers lists containers and streams their logs (logs.containers); nil: only
	// json-file logs found in logs.containers.docker_containers_dir are read.
	Containers ContainerSource
}

type fileState struct {
	Path   string `json:"path"`
	Dev    uint64 `json:"dev"`
	Ino    uint64 `json:"ino"`
	Offset int64  `json:"offset"`
	FPLen  int    `json:"fp_len"`
	FPHash uint64 `json:"fp_hash"`
	gen    uint64
}

type stateDoc struct {
	Files    map[string]*fileState `json:"files"`
	Journald struct {
		Cursor string `json:"cursor,omitempty"`
	} `json:"journald"`
	// Containers: container id -> Docker time (unix ns) of the last delivered API stream record.
	Containers map[string]int64 `json:"containers,omitempty"`
}

type fileCheckpoint struct {
	gen    uint64
	offset int64
}

type batch struct {
	groups  []*recordGroup
	records int
	bytes   int
	files   map[string]fileCheckpoint
	cursor  string
	seq     uint64
	streams map[string]time.Time // container API stream positions
}

// recordGroup holds the records of one resource (host, or host + container) in a batch.
type recordGroup struct {
	res     *resourcepb.Resource
	records []*logspb.LogRecord
}

func newBatch() batch {
	return batch{files: map[string]fileCheckpoint{}, streams: map[string]time.Time{}}
}

// Manager runs all log inputs.
type Manager struct {
	cfg      config.LogsConfig
	fs       *hostfs.FS
	stateDir string
	res      *resourcepb.Resource
	scope    *commonpb.InstrumentationScope
	emit     Emitter
	paused   func() bool
	log      *slog.Logger
	maxBytes int
	now      func() time.Time
	chunk    []byte

	explicit []*source

	discMu      sync.Mutex
	discovered  []DiscoveredLog
	discChanged bool

	tailers   map[string]*tailer
	seenGlobs map[string]bool
	lastScan  time.Time
	started   time.Time
	warnedMax bool
	batch     batch
	jseq      uint64
	runCtx    context.Context

	// Container logs (containers.go); used by the Run goroutine only.
	ctrSrc        ContainerSource
	ctrLogs       map[string]*ctrLog
	ctrEntries    chan containerEntry
	streams       map[string]*apiStream
	ctrFileSrcs   []*source
	ctrDrained    map[string]string
	ctrScanned    bool
	ctrWarnedMax  bool
	ctrWarnedList bool

	stMu      sync.Mutex
	committed map[string]*fileState
	cursor    string
	cursorSeq uint64
	ctrSince  map[string]time.Time
	dirty     bool
	saved     stateDoc
	lastSave  time.Time
}

// New builds a manager; invalid regular expressions were rejected by config validation.
func New(o Options) *Manager {
	m := &Manager{
		cfg: o.Config, fs: o.FS, stateDir: o.StateDir, res: o.Resource, scope: o.Scope, emit: o.Emit,
		paused: o.Paused, log: o.Log, maxBytes: o.MaxBatchBytes, now: o.Now,
		chunk: make([]byte, readChunk), tailers: map[string]*tailer{}, seenGlobs: map[string]bool{},
		committed: map[string]*fileState{},
		ctrLogs:   map[string]*ctrLog{}, streams: map[string]*apiStream{}, ctrDrained: map[string]string{}, ctrSince: map[string]time.Time{},
	}
	if o.Containers != nil {
		m.ctrSrc = o.Containers
	}
	if o.Config.Containers.Enabled {
		m.ctrEntries = make(chan containerEntry, ctrEntryBuffer)
	}
	if m.log == nil {
		m.log = slog.Default()
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.maxBytes <= 0 {
		m.maxBytes = 1 << 20
	}
	for _, f := range o.Config.Files {
		s := &source{glob: f.Path, exclude: f.Exclude, attrs: f.Attributes}
		if f.MultilineStart != "" {
			s.multiline = regexp.MustCompile(f.MultilineStart)
		}
		m.explicit = append(m.explicit, s)
	}
	m.batch = newBatch()
	return m
}

// SetDiscovered replaces the discovered log globs (called after each discovery run).
func (m *Manager) SetDiscovered(ds []DiscoveredLog) {
	sort.Slice(ds, func(i, j int) bool {
		if ds[i].Path != ds[j].Path {
			return ds[i].Path < ds[j].Path
		}
		return ds[i].DiscoveryID < ds[j].DiscoveryID
	})
	m.discMu.Lock()
	defer m.discMu.Unlock()
	if fmt.Sprint(ds) != fmt.Sprint(m.discovered) {
		m.discovered, m.discChanged = ds, true
	}
}

func (m *Manager) discoveredLogs() ([]DiscoveredLog, bool) {
	m.discMu.Lock()
	defer m.discMu.Unlock()
	changed := m.discChanged
	m.discChanged = false
	return m.discovered, changed
}

// Run collects logs until ctx is cancelled. The final batch is emitted before
// it returns; call SaveState after the pipeline has settled.
func (m *Manager) Run(ctx context.Context) {
	m.started = m.now()
	m.runCtx = ctx
	m.loadState()
	var journal chan journalEntry
	if m.cfg.Journald.Enabled {
		journal = make(chan journalEntry, 512)
		jctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go m.runJournald(jctx, journal)
	}
	ticker := time.NewTicker(m.cfg.PollInterval.D())
	defer ticker.Stop()
	m.tick(m.now())
	for {
		var jch <-chan journalEntry
		var cch <-chan containerEntry
		if !m.isPaused() {
			if journal != nil {
				jch = journal
			}
			cch = m.ctrEntries
		}
		select {
		case <-ctx.Done():
			m.stopStreams()
			for _, t := range m.tailers {
				m.flushTailer(t, m.now())
				t.f.Close()
			}
			m.flush()
			return
		case e := <-jch:
			m.addJournal(e)
		case e := <-cch:
			m.addContainerEntry(e)
		case <-ticker.C:
			m.tick(m.now())
		}
	}
}

func (m *Manager) isPaused() bool { return m.paused != nil && m.paused() }

func (m *Manager) tick(now time.Time) {
	if !m.isPaused() {
		_, changed := m.discoveredLogs()
		if changed || now.Sub(m.lastScan) >= scanInterval || m.lastScan.IsZero() {
			m.scan(now)
		}
		keys := make([]string, 0, len(m.tailers))
		for k := range m.tailers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			t := m.tailers[k]
			m.pollFile(t, now)
			m.checkRotation(t, now)
		}
		m.flush()
	}
	if now.Sub(m.lastSave) >= saveInterval {
		if err := m.SaveState(); err != nil {
			m.log.Warn("log state not saved", "error", err)
		}
	}
}

// sources returns explicit sources followed by discovery-driven ones.
func (m *Manager) sources() []*source {
	out := append([]*source(nil), m.explicit...)
	if !m.cfg.AutoFromDiscovery {
		return out
	}
	ds, _ := m.discoveredLogsNoReset()
	for _, d := range ds {
		out = append(out, &source{glob: d.Path, discoveryID: d.DiscoveryID})
	}
	return out
}

func (m *Manager) hostPath(local string) string {
	if m.fs.IsHostRoot() {
		return local
	}
	return "/" + strings.TrimPrefix(strings.TrimPrefix(local, m.fs.Root()), "/")
}

func excluded(s *source, host string) bool {
	for _, e := range s.exclude {
		if ok, _ := filepath.Match(e, host); ok {
			return true
		}
		if ok, _ := filepath.Match(e, path.Base(host)); ok {
			return true
		}
	}
	return false
}

// scan expands the globs, opens new files and drops files no source wants.
func (m *Manager) scan(now time.Time) {
	m.lastScan = now
	// Container logs first: a configured glob that also matches a json-file log does not claim it.
	srcs := append(m.containerSources(now), m.sources()...)
	wanted := map[string]bool{}
	for _, s := range srcs {
		initial := !m.seenGlobs[s.glob]
		m.seenGlobs[s.glob] = true
		if s.ctr != nil {
			initial = s.ctr.initial
		}
		matches, _ := filepath.Glob(m.fs.Path(s.glob))
		for _, local := range matches {
			host := m.hostPath(local)
			if excluded(s, host) {
				continue
			}
			fi, err := os.Stat(local)
			if err != nil || !fi.Mode().IsRegular() {
				continue
			}
			dev, ino, ok := identity(fi)
			if !ok {
				continue
			}
			key := keyOf(dev, ino)
			if wanted[key] {
				continue // an earlier source already claimed it
			}
			wanted[key] = true
			if t, ok := m.tailers[key]; ok {
				if t.host != host {
					t.host, t.local = host, local
					m.updatePath(t)
				}
				t.src = s
				continue
			}
			if len(m.tailers) >= maxFiles {
				if !m.warnedMax {
					m.log.Warn("too many log files; not tailing more", "limit", maxFiles, "path", host)
					m.warnedMax = true
				}
				continue
			}
			m.open(s, local, host, fi, dev, ino, key, initial, now)
			if s.ctr != nil {
				m.resumeRotated(s, local, host, wanted, now)
			}
		}
	}
	ds, _ := m.discoveredLogsNoReset()
	for key, t := range m.tailers {
		if !wanted[key] && t.goneAt.IsZero() && !t.drain {
			// Still at its path but no source matches any more: stop tailing.
			if fi, err := os.Stat(t.local); err == nil {
				if dev, ino, _ := identity(fi); dev == t.dev && ino == t.ino {
					m.closeTailer(t, now)
					continue
				}
			}
		}
		t.attrs = fileAttrs(t, ds)
	}
}

func (m *Manager) discoveredLogsNoReset() ([]DiscoveredLog, bool) {
	m.discMu.Lock()
	defer m.discMu.Unlock()
	return m.discovered, false
}

// fileAttrs builds log.file.* attributes, user attributes and the discovery
// id of the first discovered service whose log glob matches the path.
func fileAttrs(t *tailer, ds []DiscoveredLog) []*commonpb.KeyValue {
	if t.src.ctr != nil {
		return attrsContainer // records carry their own attributes (containerRecord)
	}
	attrs := []*commonpb.KeyValue{
		otlputil.Str(AttrSource, SourceFile),
		otlputil.Str("log.file.path", t.host),
		otlputil.Str("log.file.name", path.Base(t.host)),
	}
	id := t.src.discoveryID
	if id == "" {
		for _, d := range ds {
			if ok, _ := filepath.Match(d.Path, t.host); ok {
				id = d.DiscoveryID
				break
			}
		}
	}
	if id != "" {
		attrs = append(attrs, otlputil.Str(AttrDiscoveryID, id))
	}
	keys := make([]string, 0, len(t.src.attrs))
	for k := range t.src.attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		attrs = append(attrs, otlputil.Str(k, t.src.attrs[k]))
	}
	return attrs
}

func (m *Manager) open(s *source, local, host string, fi os.FileInfo, dev, ino uint64, key string, initial bool, now time.Time) {
	f, err := os.Open(local)
	if err != nil {
		m.log.Debug("cannot open log file", "path", host, "error", err)
		return
	}
	fpLen, fpHash := prefixHash(f, fingerprintLen)
	offset := int64(0)
	how := "beginning"
	m.stMu.Lock()
	saved, hasSaved := m.saved.Files[key]
	// A known path whose saved state does not match this file (rotated,
	// truncated and rewritten, or inode reused while the agent was down) is
	// read from the start, so nothing written during the downtime is skipped.
	knownPath := false
	for _, st := range m.saved.Files {
		if st.Path == host {
			knownPath = true
		}
	}
	m.stMu.Unlock()
	switch {
	case hasSaved && saved.FPLen <= fpLen && samePrefix(f, saved.FPLen, saved.FPHash):
		offset, how = saved.Offset, "saved offset"
		if offset > fi.Size() {
			offset, how = 0, "beginning (truncated)"
		}
	case initial && m.cfg.StartAt == "end" && !knownPath:
		offset, how = fi.Size(), "end"
	}
	if _, err := f.Seek(offset, 0); err != nil {
		f.Close()
		return
	}
	t := &tailer{
		key: key, host: host, local: local, src: s, f: f, dev: dev, ino: ino,
		readOff: offset, bufStart: offset, buf: make([]byte, 0, 2*readChunk), lastData: now,
		fpLen: fpLen, fpHash: fpHash,
	}
	if how != "saved offset" {
		t.since = s.since // a saved position resumes without a time filter
	}
	ds, _ := m.discoveredLogsNoReset()
	t.attrs = fileAttrs(t, ds)
	m.tailers[key] = t
	m.stMu.Lock()
	m.committed[key] = &fileState{Path: host, Dev: dev, Ino: ino, Offset: offset, FPLen: fpLen, FPHash: fpHash}
	m.dirty = true
	m.stMu.Unlock()
	m.log.Info("tailing log file", "path", host, "from", how, "offset", offset)
}

func samePrefix(f *os.File, n int, hash uint64) bool {
	got, h := prefixHash(f, n)
	return got == n && h == hash
}

// checkRotation detects rename+create and removal: the old file is read for
// rotateGrace after its last data, then closed. The scan opens the new file.
func (m *Manager) checkRotation(t *tailer, now time.Time) {
	fi, err := os.Stat(t.local)
	same := false
	if err == nil {
		dev, ino, _ := identity(fi)
		same = dev == t.dev && ino == t.ino
	}
	if same {
		t.goneAt = time.Time{}
		if m.drained(t, fi, now) {
			m.closeTailer(t, now)
		}
		return
	}
	if t.goneAt.IsZero() {
		t.goneAt = now
		m.lastScan = time.Time{} // pick up the new file on the next tick
	}
	if now.Sub(t.goneAt) >= rotateGrace && now.Sub(t.lastData) >= rotateGrace {
		m.closeTailer(t, now)
	}
}

func (m *Manager) closeTailer(t *tailer, now time.Time) {
	m.pollFile(t, now)
	m.flushTailer(t, now)
	t.f.Close()
	delete(m.tailers, t.key)
	m.stMu.Lock()
	delete(m.committed, t.key)
	m.dirty = true
	m.stMu.Unlock()
	m.log.Info("stopped tailing log file", "path", t.host)
}

func (m *Manager) updatePath(t *tailer) {
	m.stMu.Lock()
	if st, ok := m.committed[t.key]; ok {
		st.Path = t.host
		m.dirty = true
	}
	m.stMu.Unlock()
}

// resetFingerprint replaces the saved fingerprint after a truncation.
func (m *Manager) resetFingerprint(t *tailer) {
	m.stMu.Lock()
	if st, ok := m.committed[t.key]; ok {
		st.FPLen, st.FPHash = t.fpLen, t.fpHash
		m.dirty = true
	}
	m.stMu.Unlock()
}

func (m *Manager) updateFingerprint(t *tailer) {
	m.stMu.Lock()
	if st, ok := m.committed[t.key]; ok && st.FPLen < t.fpLen {
		st.FPLen, st.FPHash = t.fpLen, t.fpHash
		m.dirty = true
	}
	m.stMu.Unlock()
}

func (m *Manager) emitFileRecord(t *tailer, line []byte, truncated bool) {
	body, trunc := sanitize(string(line), m.cfg.MaxLineBytes, m.cfg.MaskSecrets)
	rec := &logspb.LogRecord{
		ObservedTimeUnixNano: uint64(m.now().UnixNano()),
		Body:                 &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: body}},
		Attributes:           t.attrs,
	}
	if truncated || trunc {
		rec.Attributes = append(append(make([]*commonpb.KeyValue, 0, len(t.attrs)+1), t.attrs...), otlputil.Bool(AttrTruncated, true))
	}
	if m.cfg.ParseSeverity {
		rec.SeverityNumber, rec.SeverityText = ParseSeverity(body)
	}
	if m.rateLimit(t) > 0 {
		t.tokens--
	}
	m.add(nil, rec, len(body))
	m.batch.files[t.key] = fileCheckpoint{gen: t.gen, offset: t.commitOffset()}
	m.flushIfFull()
}

func (m *Manager) addJournal(e journalEntry) {
	m.add(nil, e.rec, len(e.rec.GetBody().GetStringValue()))
	if e.cursor != "" {
		m.jseq++
		m.batch.cursor, m.batch.seq = e.cursor, m.jseq
	}
	m.flushIfFull()
}

// add appends a record of res (nil: the host resource) to the batch.
func (m *Manager) add(res *resourcepb.Resource, rec *logspb.LogRecord, bodyLen int) {
	if res == nil {
		res = m.res
	}
	var g *recordGroup
	if n := len(m.batch.groups); n > 0 && m.batch.groups[n-1].res == res {
		g = m.batch.groups[n-1]
	} else {
		for _, x := range m.batch.groups {
			if x.res == res {
				g = x
				break
			}
		}
		if g == nil {
			g = &recordGroup{res: res}
			m.batch.groups = append(m.batch.groups, g)
		}
	}
	g.records = append(g.records, rec)
	m.batch.records++
	m.batch.bytes += bodyLen + 64 + 16*len(rec.Attributes)
}

func (m *Manager) flushIfFull() {
	if m.batch.records >= batchMaxRecords || m.batch.bytes >= m.maxBytes {
		m.flush()
	}
}

func (m *Manager) flush() {
	b := m.batch
	m.batch = newBatch()
	if b.records == 0 {
		m.ack(b) // checkpoints without records (e.g. skipped over-long tails)
		return
	}
	ld := &logspb.LogsData{ResourceLogs: make([]*logspb.ResourceLogs, 0, len(b.groups))}
	for _, g := range b.groups {
		ld.ResourceLogs = append(ld.ResourceLogs, &logspb.ResourceLogs{
			Resource:  g.res,
			ScopeLogs: []*logspb.ScopeLogs{{Scope: m.scope, LogRecords: g.records}},
		})
	}
	m.emit(ld, b.records, func() { m.ack(b) })
}

func (m *Manager) ack(b batch) {
	m.stMu.Lock()
	defer m.stMu.Unlock()
	for key, cp := range b.files {
		st, ok := m.committed[key]
		if !ok {
			continue
		}
		if cp.gen > st.gen || (cp.gen == st.gen && cp.offset > st.Offset) {
			st.gen, st.Offset = cp.gen, cp.offset
			m.dirty = true
		}
	}
	if b.cursor != "" && b.seq > m.cursorSeq {
		m.cursor, m.cursorSeq = b.cursor, b.seq
		m.dirty = true
	}
	for id, ts := range b.streams {
		if ts.After(m.ctrSince[id]) {
			m.ctrSince[id] = ts
			m.dirty = true
		}
	}
}

func (m *Manager) persistedCursor() string {
	m.stMu.Lock()
	defer m.stMu.Unlock()
	return m.cursor
}

func (m *Manager) statePath() string { return filepath.Join(m.stateDir, StateFile) }

func (m *Manager) loadState() {
	b, err := os.ReadFile(m.statePath())
	if err != nil {
		return
	}
	var doc stateDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		m.log.Warn("ignoring unreadable log state", "file", m.statePath(), "error", err)
		return
	}
	m.stMu.Lock()
	defer m.stMu.Unlock()
	m.saved = doc
	m.cursor = doc.Journald.Cursor
	for id, ns := range doc.Containers {
		m.ctrSince[id] = time.Unix(0, ns).UTC()
	}
}

// SaveState atomically writes offsets and the journal cursor.
func (m *Manager) SaveState() error {
	m.stMu.Lock()
	m.lastSave = m.now()
	if !m.dirty && m.lastSave.Sub(m.started) >= keepSavedFor {
		m.stMu.Unlock()
		return nil
	}
	doc := stateDoc{Files: map[string]*fileState{}}
	if m.now().Sub(m.started) < keepSavedFor {
		// Files not opened yet (e.g. discovery-driven paths arrive later) keep their offsets for a while.
		for k, st := range m.saved.Files {
			doc.Files[k] = st
		}
	}
	for k, st := range m.committed {
		c := *st
		doc.Files[k] = &c
	}
	doc.Journald.Cursor = m.cursor
	if len(m.ctrSince) > 0 {
		doc.Containers = make(map[string]int64, len(m.ctrSince))
		for id, ts := range m.ctrSince {
			doc.Containers[id] = ts.UnixNano()
		}
	}
	m.dirty = false
	m.stMu.Unlock()

	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.stateDir, 0o750); err != nil {
		return err
	}
	tmp := m.statePath() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, m.statePath())
}

func keyOf(dev, ino uint64) string { return fmt.Sprintf("%d:%d", dev, ino) }

// Files returns the host paths currently tailed (for diagnostics and tests).
func (m *Manager) Files() []string {
	var out []string
	for _, t := range m.tailers {
		out = append(out, t.host)
	}
	sort.Strings(out)
	return out
}
