package phpforwarder

import (
	"container/list"
	"sort"
	"time"
)

// maxPendingBytes bounds the datagram bytes held for reassembly; the oldest traces are evicted beyond it.
const maxPendingBytes = 64 << 20

type traceKey struct {
	pid     int64
	traceID [16]byte
}

// trace is a finished message sequence: all parts of a (pid, trace_id), sorted by seq.
type trace struct {
	parts    []*message
	complete bool // every part up to last arrived
}

type pendingTrace struct {
	key     traceKey
	created time.Time
	parts   map[int]*message
	lastSeq int // -1 until the part with last=true arrived
	bytes   int
}

// assembler reassembles split messages per (pid, trace_id) (php-agent.md §2.3). Not safe for concurrent use.
type assembler struct {
	timeout    time.Duration
	maxPending int
	maxBytes   int

	order   *list.List // *pendingTrace, oldest first (creation order == deadline order)
	byKey   map[traceKey]*list.Element
	bytes   int
	evicted int // messages dropped by eviction or as duplicates since the last takeDropped
}

func newAssembler(timeout time.Duration, maxPending int) *assembler {
	return &assembler{timeout: timeout, maxPending: maxPending, maxBytes: maxPendingBytes,
		order: list.New(), byKey: map[traceKey]*list.Element{}}
}

// add stores one message and returns the trace it completed, if any.
func (a *assembler) add(m *message, now time.Time) *trace {
	k := traceKey{m.pid, m.traceID}
	el, ok := a.byKey[k]
	if !ok {
		if m.seq == 0 && m.last { // unsplit message: the common case never waits
			return &trace{parts: []*message{m}, complete: true}
		}
		for a.order.Len() > 0 && (a.order.Len() >= a.maxPending || a.bytes+m.size > a.maxBytes) {
			a.evictOldest()
		}
		p := &pendingTrace{key: k, created: now, parts: map[int]*message{}, lastSeq: -1}
		el = a.order.PushBack(p)
		a.byKey[k] = el
	}
	p := el.Value.(*pendingTrace)
	if _, dup := p.parts[m.seq]; dup || (p.lastSeq >= 0 && m.seq > p.lastSeq) || (m.last && p.lastSeq < 0 && hasSeqAbove(p, m.seq)) {
		a.evicted++
		return nil
	}
	p.parts[m.seq] = m
	p.bytes += m.size
	a.bytes += m.size
	if m.last {
		p.lastSeq = m.seq
	}
	if p.lastSeq >= 0 && len(p.parts) == p.lastSeq+1 {
		a.remove(el)
		return p.finish(true)
	}
	return nil
}

func hasSeqAbove(p *pendingTrace, seq int) bool {
	for s := range p.parts {
		if s > seq {
			return true
		}
	}
	return false
}

// expire returns traces older than the reassembly timeout as incomplete.
func (a *assembler) expire(now time.Time) []*trace {
	var out []*trace
	for el := a.order.Front(); el != nil; el = a.order.Front() {
		p := el.Value.(*pendingTrace)
		if now.Sub(p.created) < a.timeout {
			break
		}
		a.remove(el)
		out = append(out, p.finish(false))
	}
	return out
}

// flush returns every pending trace as incomplete (shutdown).
func (a *assembler) flush() []*trace {
	var out []*trace
	for el := a.order.Front(); el != nil; el = a.order.Front() {
		a.remove(el)
		out = append(out, el.Value.(*pendingTrace).finish(false))
	}
	return out
}

func (a *assembler) len() int { return a.order.Len() }

// takeDropped returns and resets the number of dropped messages.
func (a *assembler) takeDropped() int {
	n := a.evicted
	a.evicted = 0
	return n
}

func (a *assembler) evictOldest() {
	el := a.order.Front()
	a.evicted += len(el.Value.(*pendingTrace).parts)
	a.remove(el)
}

func (a *assembler) remove(el *list.Element) {
	p := el.Value.(*pendingTrace)
	a.order.Remove(el)
	delete(a.byKey, p.key)
	a.bytes -= p.bytes
}

func (p *pendingTrace) finish(complete bool) *trace {
	t := &trace{parts: make([]*message, 0, len(p.parts)), complete: complete}
	for _, m := range p.parts {
		t.parts = append(t.parts, m)
	}
	sort.Slice(t.parts, func(i, j int) bool { return t.parts[i].seq < t.parts[j].seq })
	return t
}
