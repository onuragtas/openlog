package phpforwarder

import (
	"fmt"
	"testing"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// part builds part seq of a split trace: spans[i] has id 00..0<seq+1><i>, the root (seq 0, i 0) has no parent.
func part(t *testing.T, pid int, traceID string, seq int, last bool, spans int) *message {
	t.Helper()
	m, err := decode(msg(t, func(m map[string]any) {
		m["pid"], m["trace_id"], m["seq"], m["last"] = pid, traceID, seq, last
		var ss []any
		for i := 0; i < spans; i++ {
			s := map[string]any{"id": fmt.Sprintf("%014x%02x", seq+1, i), "parent": fmt.Sprintf("%014x%02x", 1, 0),
				"name": "segment", "kind": 1, "start": 1789302480000000000 + i, "dur": 10}
			if seq == 0 && i == 0 {
				s["parent"], s["kind"], s["name"] = "", 2, "GET /"
			}
			ss = append(ss, s)
		}
		m["spans"] = ss
	}))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func traceHex(i int) string { return fmt.Sprintf("%032x", i+1) }

func TestReassemblyOutOfOrder(t *testing.T) {
	a := newAssembler(5*time.Second, 10)
	now := time.Unix(1000, 0)
	if tr := a.add(part(t, 1, traceHex(0), 2, true, 2), now); tr != nil {
		t.Fatal("completed before all parts arrived")
	}
	if tr := a.add(part(t, 1, traceHex(0), 0, false, 3), now); tr != nil {
		t.Fatal("completed without part 1")
	}
	// A different pid with the same trace id is a separate sequence.
	if tr := a.add(part(t, 2, traceHex(0), 1, false, 1), now); tr != nil {
		t.Fatal("pid must be part of the key")
	}
	tr := a.add(part(t, 1, traceHex(0), 1, false, 1), now)
	if tr == nil || !tr.complete || len(tr.parts) != 3 || tr.parts[0].seq != 0 || tr.parts[2].seq != 2 {
		t.Fatalf("trace = %+v", tr)
	}
	c := convertTrace(tr)
	if len(c.spans) != 6 || boolAttr(c.spans[0], AttrIncomplete) || !hasAttr(c.spans[0], AttrSamplingRatio) {
		t.Errorf("converted: %d spans, root attrs %v", len(c.spans), c.spans[0].Attributes)
	}
	if a.len() != 1 { // pid 2 still pending
		t.Errorf("pending = %d", a.len())
	}
	// Duplicate part is dropped and counted.
	a.add(part(t, 2, traceHex(0), 1, false, 1), now)
	if n := a.takeDropped(); n != 1 {
		t.Errorf("duplicate dropped = %d", n)
	}
}

func TestReassemblyTimeout(t *testing.T) {
	a := newAssembler(5*time.Second, 10)
	now := time.Unix(1000, 0)
	a.add(part(t, 1, traceHex(0), 0, false, 2), now)                 // root arrived, rest lost
	a.add(part(t, 1, traceHex(1), 1, true, 2), now.Add(time.Second)) // root part lost
	if got := a.expire(now.Add(4999 * time.Millisecond)); len(got) != 0 {
		t.Fatalf("expired early: %d", len(got))
	}
	got := a.expire(now.Add(5 * time.Second))
	if len(got) != 1 || got[0].complete {
		t.Fatalf("expired = %+v", got)
	}
	c := convertTrace(got[0])
	if !boolAttr(c.spans[0], AttrIncomplete) || boolAttr(c.spans[1], AttrIncomplete) {
		t.Errorf("incomplete must be set on the root only: %v / %v", c.spans[0].Attributes, c.spans[1].Attributes)
	}
	got = a.expire(now.Add(6 * time.Second))
	if len(got) != 1 {
		t.Fatalf("second expiry = %d", len(got))
	}
	c = convertTrace(got[0])
	for _, sp := range c.spans {
		if !boolAttr(sp, AttrIncomplete) || !hasAttr(sp, AttrSamplingRatio) {
			t.Errorf("without a root every span is incomplete: %v", sp.Attributes)
		}
		if sp.Flags&flagHasIsRemote != 0 {
			t.Errorf("an unknown parent of an incomplete trace must not be marked remote or local: %x", sp.Flags)
		}
	}
	if a.len() != 0 {
		t.Errorf("pending = %d", a.len())
	}
}

func TestReassemblyEviction(t *testing.T) {
	a := newAssembler(time.Minute, 3)
	now := time.Unix(1000, 0)
	for i := 0; i < 5; i++ {
		a.add(part(t, 1, traceHex(i), 0, false, 1), now.Add(time.Duration(i)*time.Millisecond))
	}
	if a.len() != 3 {
		t.Fatalf("pending = %d, want 3", a.len())
	}
	if n := a.takeDropped(); n != 2 {
		t.Errorf("evicted messages = %d, want 2", n)
	}
	// The two oldest were evicted: completing trace 0 starts a new sequence, trace 4 completes.
	if tr := a.add(part(t, 1, traceHex(4), 1, true, 1), now); tr == nil || !tr.complete {
		t.Error("newest trace must still be pending")
	}
	if tr := a.add(part(t, 1, traceHex(0), 1, true, 1), now); tr != nil {
		t.Error("oldest trace must have been evicted")
	}

	// Byte limit.
	b := newAssembler(time.Minute, 100)
	b.maxBytes = 3 * part(t, 1, traceHex(0), 0, false, 1).size
	for i := 0; i < 5; i++ {
		b.add(part(t, 1, traceHex(i), 0, false, 1), now)
	}
	if b.len() != 3 || b.takeDropped() != 2 {
		t.Errorf("byte limit: pending %d", b.len())
	}
	if fl := b.flush(); len(fl) != 3 || b.len() != 0 || b.bytes != 0 {
		t.Errorf("flush = %d, pending %d, bytes %d", len(fl), b.len(), b.bytes)
	}
}

func TestSinglePartDoesNotWait(t *testing.T) {
	a := newAssembler(time.Second, 1)
	tr := a.add(part(t, 1, traceHex(0), 0, true, 2), time.Now())
	if tr == nil || !tr.complete || a.len() != 0 {
		t.Fatalf("trace = %+v, pending %d", tr, a.len())
	}
}

func hasAttr(sp *tracepb.Span, key string) bool {
	for _, kv := range sp.Attributes {
		if kv.Key == key {
			return true
		}
	}
	return false
}

func boolAttr(sp *tracepb.Span, key string) bool {
	for _, kv := range sp.Attributes {
		if kv.Key == key {
			return kv.Value.GetBoolValue()
		}
	}
	return false
}
