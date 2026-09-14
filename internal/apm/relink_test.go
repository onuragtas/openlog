package apm

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var rt0 = time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)

func mins(offsets ...int) []time.Time {
	out := make([]time.Time, len(offsets))
	for i, o := range offsets {
		out[i] = rt0.Add(time.Duration(o) * time.Minute)
	}
	return out
}

func TestLinkMinutes(t *testing.T) {
	ts := rt0.Add(7*time.Minute + 30*time.Second)
	cases := []struct {
		name, kind, service, parent string
		weight                      float64
		ts                          time.Time
		ok                          bool
		first, last                 time.Time
	}{
		{"client", KindClient, "fe", "", 1, ts, true, rt0.Add(7 * time.Minute), rt0.Add(7 * time.Minute)},
		{"producer", KindProducer, "fe", "p", 2, ts, true, rt0.Add(7 * time.Minute), rt0.Add(7 * time.Minute)},
		{"client weight 0", KindClient, "fe", "", 0, ts, false, time.Time{}, time.Time{}},
		{"client no service", KindClient, "", "", 1, ts, false, time.Time{}, time.Time{}},
		// Client minute m links children in [m, m+6m): 10:07:30 belongs to minutes 10:02..10:07.
		{"server", KindServer, "orders", "p", 0, ts, true, rt0.Add(2 * time.Minute), rt0.Add(7 * time.Minute)},
		{"consumer on a minute", KindConsumer, "orders", "p", 1, rt0.Add(7 * time.Minute), true, rt0.Add(2 * time.Minute), rt0.Add(7 * time.Minute)},
		{"server root", KindServer, "orders", "", 1, ts, false, time.Time{}, time.Time{}},
		{"internal", "internal", "orders", "p", 1, ts, false, time.Time{}, time.Time{}},
	}
	for _, c := range cases {
		first, last, ok := LinkMinutes(c.kind, c.service, c.parent, c.weight, c.ts)
		if ok != c.ok || !first.Equal(c.first) || !last.Equal(c.last) {
			t.Errorf("%s: %v %v %v, want %v %v %v", c.name, first, last, ok, c.first, c.last, c.ok)
		}
	}
	// The minute range matches the link statement's child range: [m, m + 1m + childSlack).
	first, last, _ := LinkMinutes(KindServer, "orders", "p", 1, ts)
	for m := first.Add(-time.Minute); !m.After(last.Add(time.Minute)); m = m.Add(time.Minute) {
		in := !ts.Before(m) && ts.Before(m.Add(time.Minute+childSlack))
		if want := !m.Before(first) && !m.After(last); in != want {
			t.Errorf("minute %s: in child range %v, in LinkMinutes %v", m.Format(time.TimeOnly), in, want)
		}
	}
}

func TestCoalesceMinutes(t *testing.T) {
	format := func(ws [][2]time.Time) string {
		var parts []string
		for _, w := range ws {
			parts = append(parts, fmt.Sprintf("%d-%d", int(w[0].Sub(rt0).Minutes()), int(w[1].Sub(rt0).Minutes())))
		}
		return strings.Join(parts, " ")
	}
	if got := format(CoalesceMinutes(mins(0, 1, 2, 5, 7, 8), 0)); got != "0-3 5-6 7-9" {
		t.Errorf("unbounded: %s", got)
	}
	if got := format(CoalesceMinutes(mins(0, 1, 2, 3, 4), 2*time.Minute)); got != "0-2 2-4 4-5" {
		t.Errorf("bounded: %s", got)
	}
	if got := CoalesceMinutes(nil, time.Hour); len(got) != 0 {
		t.Errorf("empty: %v", got)
	}
}

func TestRelinkStatePlanAndWatermark(t *testing.T) {
	now := rt0.Add(3 * time.Hour)
	s := newRelinkState(now)
	if want := now.Add(-relinkRescan - relinkOverlap); !s.since().Equal(want) {
		t.Fatalf("since after start %v, want %v", s.since(), want)
	}
	q := func(minute int, last time.Time) QueuedMinute {
		return QueuedMinute{Minute: rt0.Add(time.Duration(minute) * time.Minute), Last: last}
	}
	until := now.Add(-relinkSettle)
	rows := []QueuedMinute{q(5, until.Add(-time.Minute)), q(1, until.Add(-30*time.Minute)), q(3, until.Add(-2*time.Minute))}

	// Bounded: oldest first, the rest stays pending and holds the watermark.
	batch, rest := s.plan(rows, 2)
	if len(batch) != 2 || batch[0].Minute != rt0.Add(time.Minute) || batch[1].Minute != rt0.Add(3*time.Minute) || len(rest) != 1 {
		t.Fatalf("batch %v rest %v", batch, rest)
	}
	s.finish([]time.Time{batch[0].Minute, batch[1].Minute}, rest, until)
	if want := rest[0].Last.Add(-time.Millisecond); !s.wm.Equal(want) {
		t.Errorf("watermark %v, want just before the pending row %v", s.wm, want)
	}

	// Next pass reads the same rows again: only the pending minute is planned.
	batch, rest = s.plan(rows, 2)
	if len(batch) != 1 || batch[0].Minute != rt0.Add(5*time.Minute) || len(rest) != 0 {
		t.Fatalf("second pass batch %v rest %v", batch, rest)
	}
	until2 := until.Add(5 * time.Minute)
	s.finish([]time.Time{batch[0].Minute}, nil, until2)
	if !s.wm.Equal(until2) {
		t.Errorf("watermark %v, want %v", s.wm, until2)
	}

	// A new row for a re-linked minute (enqueued after the pass's read bound) makes it pending again; a row that
	// became visible late but is older than that bound does not.
	rows = []QueuedMinute{q(1, until.Add(time.Second)), q(3, until.Add(-time.Second))}
	batch, _ = s.plan(rows, 10)
	if len(batch) != 1 || batch[0].Minute != rt0.Add(time.Minute) {
		t.Errorf("re-enqueued minute: batch %v", batch)
	}

	// A failed window keeps its minutes pending and the watermark before them; done entries older than the next
	// read bound are pruned.
	s.finish(nil, []QueuedMinute{q(1, until.Add(time.Second))}, until2.Add(5*time.Minute))
	if !s.wm.Equal(until2) {
		t.Errorf("watermark moved back or past a pending row: %v", s.wm)
	}
	s.finish([]time.Time{rt0.Add(time.Minute)}, nil, until2.Add(time.Hour))
	for m, d := range s.done {
		if !d.After(s.since()) {
			t.Errorf("done entry %v (%v) not pruned (since %v)", m, d, s.since())
		}
	}
	if len(s.done) != 1 {
		t.Errorf("done %v", s.done)
	}
}

func TestRelinkQueueSQL(t *testing.T) {
	q := RelinkQueueSQL("openlog")
	for _, want := range []string{"FROM `openlog`.apm_relink_queue ", "enqueued_at > fromUnixTimestamp64Milli({since:Int64})",
		"enqueued_at <= fromUnixTimestamp64Milli({until:Int64})", "minute < toDateTime({before:Int64}, 'UTC')", "GROUP BY minute ORDER BY minute"} {
		if !strings.Contains(q, want) {
			t.Errorf("query lacks %q: %s", want, q)
		}
	}
}
