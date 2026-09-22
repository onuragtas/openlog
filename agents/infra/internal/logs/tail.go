package logs

import (
	"bytes"
	"hash/fnv"
	"io"
	"os"
	"regexp"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/osutil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// Tailing constants.
const (
	readChunk      = 64 << 10
	readBudget     = 4 << 20 // bytes per file per poll
	fingerprintLen = 1024
	// rotateGrace keeps reading a file for this long after its path was
	// renamed or removed (writers may still append to the old file).
	rotateGrace = 5 * time.Second
	// partialFlush emits a trailing line without newline after the file was idle this long.
	partialFlush = 5 * time.Second
	// multilineFlush emits a pending multiline record after the file was idle this long.
	multilineFlush = 2 * time.Second
)

// source is one configured or discovered glob.
type source struct {
	glob        string
	exclude     []string
	multiline   *regexp.Regexp
	attrs       map[string]string
	discoveryID string // set for discovery-driven sources
	// Container json-file logs (containers.go): the container, its state when listed and the
	// Docker time before which records are skipped (logs.start_at=end, already read).
	ctr         *ctrLog
	ctrState    string
	ctrFinished string
	since       time.Time
	// ctrCRI: the container log is in the CRI log format (containerd, CRI-O) instead of json-file.
	ctrCRI bool
}

type pending struct {
	start     int64
	body      []byte
	truncated bool
}

// tailer follows one file, identified by device and inode.
type tailer struct {
	key         string
	host, local string
	src         *source
	attrs       []*commonpb.KeyValue

	f        *os.File
	dev, ino uint64
	gen      uint64 // incremented on truncation

	readOff  int64  // bytes read from the file
	buf      []byte // unconsumed bytes (partial line)
	bufStart int64  // file offset of buf[0]
	skipping bool   // discarding the rest of an over-long line
	pend     *pending
	// Container logs: partial json-file messages per stream, time filter; drain: a rotated
	// file read to its end and then closed.
	dpart [3]pendingLine
	// mgroup groups complete container log lines into multiline records (src.multiline).
	mgroup multilineGrouper
	since  time.Time
	drain  bool

	lastData time.Time
	goneAt   time.Time // path renamed/removed; zero while in place
	tokens   float64
	refill   time.Time

	fpLen  int
	fpHash uint64
}

// commitOffset is the offset from which reading must resume so that no
// unemitted data is lost: the start of a pending multiline record or of the
// buffered partial line.
func (t *tailer) commitOffset() int64 {
	off := t.bufStart
	if t.pend != nil && t.pend.start < off {
		off = t.pend.start
	}
	for i := range t.dpart {
		if p := &t.dpart[i]; p.started && p.start < off {
			off = p.start
		}
		if p := &t.mgroup.parts[i]; p.started && p.start < off {
			off = p.start
		}
	}
	return off
}

func (t *tailer) hasPartial() bool {
	for i := range t.dpart {
		if t.dpart[i].started {
			return true
		}
	}
	return t.mgroup.pending()
}

// unread reports whether the file has data not read yet or the buffer holds complete lines not
// emitted yet (both happen while the tailer is rate-limited). A trailing partial line does not
// count: flushTailer emits it.
func (t *tailer) unread() bool {
	fi, err := t.f.Stat()
	return (err == nil && fi.Size() > t.readOff) || bytes.IndexByte(t.buf, '\n') >= 0
}

// rateLimit is the lines-per-second limit of the tailer's source.
func (m *Manager) rateLimit(t *tailer) int {
	if t.src.ctr != nil {
		return m.cfg.Containers.RateLimitLines
	}
	return m.cfg.RateLimitLines
}

// identity returns the device and inode (Windows: volume serial and file index) of the file at path.
func identity(path string, fi os.FileInfo) (dev, ino uint64, ok bool) {
	return osutil.FileIdentity(path, fi)
}

// prefixHash hashes the first n bytes of f.
func prefixHash(f *os.File, n int) (int, uint64) {
	buf := make([]byte, n)
	got, _ := f.ReadAt(buf, 0)
	h := fnv.New64a()
	h.Write(buf[:got])
	return got, h.Sum64()
}

func (m *Manager) canEmit(t *tailer) bool {
	return m.rateLimit(t) <= 0 || t.tokens >= 1
}

func (m *Manager) refillTokens(t *tailer, now time.Time) {
	rate := float64(m.rateLimit(t))
	if rate <= 0 {
		return
	}
	if t.refill.IsZero() {
		t.tokens, t.refill = rate, now
		return
	}
	t.tokens = min(rate, t.tokens+now.Sub(t.refill).Seconds()*rate)
	t.refill = now
}

// pollFile reads new data, handles copytruncate and idle flushes.
func (m *Manager) pollFile(t *tailer, now time.Time) {
	m.refillTokens(t, now)
	if fi, err := t.f.Stat(); err == nil && fi.Size() < t.readOff {
		// copytruncate: emit what belongs to the old content, restart at 0.
		m.flushTailer(t, now)
		if _, err := t.f.Seek(0, io.SeekStart); err == nil {
			t.readOff, t.bufStart, t.buf, t.skipping = 0, 0, t.buf[:0], false
			t.gen++
			t.fpLen, t.fpHash = prefixHash(t.f, fingerprintLen)
			m.resetFingerprint(t)
			m.log.Debug("log file truncated; reading from the start", "path", t.host)
		}
	}
	m.processLines(t, now) // leftovers from a rate-limited poll
	chunk := m.chunk
	for budget := readBudget; budget > 0 && m.canEmit(t); {
		n, err := t.f.Read(chunk)
		if n > 0 {
			t.buf = append(t.buf, chunk[:n]...)
			t.readOff += int64(n)
			budget -= n
			t.lastData = now
			m.processLines(t, now)
		}
		if n == 0 || err != nil {
			break
		}
	}
	idle := now.Sub(t.lastData)
	if len(t.buf) > 0 && !t.skipping && idle >= partialFlush && m.canEmit(t) {
		line := append([]byte(nil), t.buf...)
		start := t.bufStart
		t.bufStart += int64(len(t.buf))
		t.buf = t.buf[:0]
		m.handleLine(t, line, start, false)
	}
	if t.pend != nil && idle >= multilineFlush && m.canEmit(t) {
		p := t.pend
		t.pend = nil
		m.emitFileRecord(t, p.body, p.truncated)
	}
	if t.src.ctr != nil && idle >= t.mgroup.idle() && m.canEmit(t) {
		m.flushContainerParts(t)
	} else if t.src.ctr != nil && t.mgroup.pending() && m.canEmit(t) {
		// A stream idle while the other one keeps writing.
		m.flushContainerGroups(t, now)
	}
	if t.fpLen < fingerprintLen && t.readOff > int64(t.fpLen) {
		t.fpLen, t.fpHash = prefixHash(t.f, fingerprintLen)
		m.updateFingerprint(t)
	}
	if cap(t.buf)-len(t.buf) < readChunk {
		t.buf = append(make([]byte, 0, len(t.buf)+2*readChunk), t.buf...)
	}
}

func (m *Manager) processLines(t *tailer, now time.Time) {
	maxLine := m.cfg.MaxLineBytes
	for m.canEmit(t) {
		i := bytes.IndexByte(t.buf, '\n')
		if i < 0 {
			if len(t.buf) > maxLine {
				if t.skipping {
					t.bufStart += int64(len(t.buf))
					t.buf = t.buf[:0]
					break
				}
				head := append([]byte(nil), t.buf[:maxLine]...)
				start := t.bufStart
				t.bufStart += int64(len(t.buf))
				t.buf = t.buf[:0]
				t.skipping = true
				m.handleLine(t, head, start, true)
			}
			break
		}
		line := t.buf[:i]
		start := t.bufStart
		t.buf = t.buf[i+1:]
		t.bufStart = start + int64(i) + 1
		if t.skipping {
			t.skipping = false
			continue
		}
		truncated := false
		if len(line) > maxLine {
			line, truncated = line[:maxLine], true
		}
		m.handleLine(t, line, start, truncated)
	}
}

// handleLine applies multiline grouping. line may alias the read buffer.
func (m *Manager) handleLine(t *tailer, line []byte, start int64, truncated bool) {
	if t.src.ctr != nil {
		m.handleContainerLine(t, line, start, truncated)
		return
	}
	line = bytes.TrimSuffix(line, []byte{'\r'})
	re := t.src.multiline
	if re == nil {
		m.emitFileRecord(t, line, truncated)
		return
	}
	if re.Match(line) {
		old := t.pend
		t.pend = &pending{start: start, body: append([]byte(nil), line...), truncated: truncated}
		if old != nil {
			m.emitFileRecord(t, old.body, old.truncated)
		}
		return
	}
	if t.pend == nil { // no start line seen yet
		m.emitFileRecord(t, line, truncated)
		return
	}
	room := m.cfg.MaxLineBytes - len(t.pend.body) - 1
	if room < len(line) || truncated {
		t.pend.truncated = true
	}
	if room > 0 {
		t.pend.body = append(append(t.pend.body, '\n'), line[:min(room, len(line))]...)
	}
}

// flushTailer emits the pending record and the partial line (on close or truncation).
func (m *Manager) flushTailer(t *tailer, now time.Time) {
	if len(t.buf) > 0 && !t.skipping {
		line := append([]byte(nil), t.buf...)
		start := t.bufStart
		t.bufStart += int64(len(t.buf))
		t.buf = t.buf[:0]
		m.handleLine(t, line, start, false)
	}
	if t.pend != nil {
		p := t.pend
		t.pend = nil
		m.emitFileRecord(t, p.body, p.truncated)
	}
	if t.src.ctr != nil {
		m.flushContainerParts(t)
	}
}
