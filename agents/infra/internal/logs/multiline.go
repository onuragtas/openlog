package logs

import (
	"bytes"
	"regexp"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/containers"
)

// LabelMultiline on a container sets the regex of the first line of a multiline record in its log. It takes
// precedence over the multiline_start of logs.containers.include items.
const LabelMultiline = "openlog.logs.multiline"

// maxCachedPatterns bounds the compiled multiline patterns kept by a manager.
const maxCachedPatterns = 256

// containerMultiline returns the multiline start regex of a container: the openlog.logs.multiline label,
// else the multiline_start of the first matching include item; nil groups nothing. Run goroutine only.
func (m *Manager) containerMultiline(c *containers.Container) *regexp.Regexp {
	pattern, fromLabel := c.Labels[LabelMultiline], true
	if pattern == "" {
		fromLabel = false
		for _, cm := range m.cfg.Containers.Include {
			if cm.MultilineStart != "" && containerMatches(cm, c) {
				pattern = cm.MultilineStart
				break
			}
		}
	}
	if pattern == "" {
		return nil
	}
	if re, ok := m.ctrPatterns[pattern]; ok {
		return re
	}
	re, err := regexp.Compile(pattern)
	if err != nil && fromLabel {
		// Include patterns were validated with the configuration.
		m.log.Warn("invalid multiline pattern in container label; lines are not grouped", "container", c.Name, "label", LabelMultiline, "error", err)
	}
	if m.ctrPatterns == nil || len(m.ctrPatterns) >= maxCachedPatterns {
		m.ctrPatterns = map[string]*regexp.Regexp{}
	}
	m.ctrPatterns[pattern] = re // nil for an invalid pattern: warned once
	return re
}

// groupEmitFunc receives a finished record: the time of its first line (ts) and of its last line (last,
// the read position); false stops the caller.
type groupEmitFunc func(stream int, body []byte, ts, last time.Time, truncated bool) bool

// multilineGrouper joins container log lines into multiline records per stream (stdout and stderr are
// grouped separately), like logs.files multiline_start: a line matching re starts a record, other lines
// are appended to the pending record of their stream (or emitted alone before the first start line).
// Records are limited to maxBytes (logs.max_line_bytes, marked truncated) and flushed after
// multilineFlush without a new line of their stream.
type multilineGrouper struct {
	re       *regexp.Regexp
	maxBytes int
	parts    [3]pendingLine
	lastAt   [3]time.Time
}

func (g *multilineGrouper) pending() bool {
	for i := range g.parts {
		if g.parts[i].started {
			return true
		}
	}
	return false
}

// take removes the pending record of a stream, returning a copy.
func (g *multilineGrouper) take(stream int) pendingLine {
	p := &g.parts[stream]
	old := pendingLine{buf: append([]byte(nil), p.buf...), ts: p.ts, last: p.last, truncated: p.truncated, started: p.started}
	p.reset()
	return old
}

// push handles one complete line; start is its file offset (0 for streams). The pending record is
// replaced before an older record is emitted, so file checkpoints never pass unemitted lines.
func (g *multilineGrouper) push(stream int, line []byte, ts time.Time, start int64, truncated bool, now time.Time, emit groupEmitFunc) bool {
	if stream < 0 || stream >= len(g.parts) {
		stream = 0
	}
	p := &g.parts[stream]
	if g.re == nil || g.re.Match(line) {
		var old pendingLine
		if p.started {
			old = g.take(stream)
		}
		if g.re != nil {
			p.start, p.ts, p.last = start, ts, ts
			p.add(line, g.maxBytes)
			p.truncated = p.truncated || truncated
			g.lastAt[stream] = now
		}
		if old.started && !emit(stream, old.buf, old.ts, old.last, old.truncated) {
			return false
		}
		if g.re == nil {
			return emit(stream, line, ts, ts, truncated)
		}
		return true
	}
	if !p.started { // no start line seen yet
		return emit(stream, line, ts, ts, truncated)
	}
	p.add([]byte{'\n'}, g.maxBytes)
	p.add(line, g.maxBytes)
	p.truncated = p.truncated || truncated
	if !ts.IsZero() {
		p.last = ts
	}
	g.lastAt[stream] = now
	return true
}

// flushIdle emits pending records whose stream had no line for idle.
func (g *multilineGrouper) flushIdle(now time.Time, idle time.Duration, emit groupEmitFunc) bool {
	for i := range g.parts {
		if g.parts[i].started && now.Sub(g.lastAt[i]) >= idle {
			old := g.take(i)
			if !emit(i, old.buf, old.ts, old.last, old.truncated) {
				return false
			}
		}
	}
	return true
}

// flushAll emits every pending record.
func (g *multilineGrouper) flushAll(emit groupEmitFunc) bool {
	return g.flushIdle(time.Time{}, time.Duration(-1<<63), emit)
}

// parseCRILine decodes one line of the CRI log format written by containerd and CRI-O:
// "<RFC3339Nano> <stdout|stderr> <tags> <message>", tags "F" (full line) or "P" (partial; the message
// continues in the next line of the stream), possibly followed by ":"-separated further tags.
func parseCRILine(line []byte) (ts time.Time, stream string, partial bool, msg []byte, ok bool) {
	ts, rest, ok := containers.SplitTimestamp(line)
	if !ok {
		return time.Time{}, "", false, nil, false
	}
	i := bytes.IndexByte(rest, ' ')
	if i < 0 {
		return time.Time{}, "", false, nil, false
	}
	stream, rest = string(rest[:i]), rest[i+1:]
	if stream != "stdout" && stream != "stderr" {
		return time.Time{}, "", false, nil, false
	}
	tags := rest
	if j := bytes.IndexByte(rest, ' '); j >= 0 {
		tags, msg = rest[:j], rest[j+1:]
	} else {
		msg = rest[len(rest):]
	}
	if k := bytes.IndexByte(tags, ':'); k >= 0 {
		tags = tags[:k]
	}
	switch string(tags) {
	case "F":
	case "P":
		partial = true
	default:
		return time.Time{}, "", false, nil, false
	}
	return ts, stream, partial, msg, true
}
