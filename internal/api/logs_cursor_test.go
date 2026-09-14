package api

import (
	"strings"
	"testing"
)

func TestLogCursorRoundTrip(t *testing.T) {
	p := logPos{ts: 1757757600123456789, key: 18446744073709551615}
	c := encodeLogCursor(p, 3)
	got, n, err := decodeLogCursor(c)
	if err != nil || got != p || n != 3 {
		t.Fatalf("decode(%q) = %+v %d %v", c, got, n, err)
	}
	for _, bad := range []string{"x", "e30", strings.Repeat("a", 600), encodeLogCursor(p, 0)[:10]} {
		if _, _, err := decodeLogCursor(bad); err == nil {
			t.Errorf("cursor %q accepted", bad)
		}
	}
}

// simulate pages through a full, newest-first result with logPage and returns the emitted row indexes.
func simulate(t *testing.T, all []logPos, limit int) []int {
	t.Helper()
	var out []int
	var cursor *logPos
	skip := 0
	for page := 0; page < 1000; page++ {
		// The query returns rows at or before the cursor (timestamp, key), limit + skip + 1 of them.
		start := 0
		if cursor != nil {
			for start < len(all) && (all[start].ts > cursor.ts || (all[start].ts == cursor.ts && all[start].key > cursor.key)) {
				start++
			}
		}
		fetched := all[start:min(len(all), start+limit+skip+1)]
		first, end, next := logPage(fetched, cursor, skip, limit)
		for i := first; i < end; i++ {
			out = append(out, start+i)
		}
		if next == "" {
			return out
		}
		pos, n, err := decodeLogCursor(next)
		if err != nil {
			t.Fatal(err)
		}
		cursor, skip = &pos, n
	}
	t.Fatal("pagination did not terminate")
	return nil
}

func TestLogPageTies(t *testing.T) {
	// Newest first: many rows in the same nanosecond, some fully identical (same key).
	all := []logPos{
		{ts: 9, key: 5},
		{ts: 7, key: 9}, {ts: 7, key: 9}, {ts: 7, key: 9}, {ts: 7, key: 9}, {ts: 7, key: 4}, {ts: 7, key: 4}, {ts: 7, key: 1},
		{ts: 3, key: 8}, {ts: 3, key: 8},
		{ts: 1, key: 2},
	}
	for limit := 1; limit <= len(all)+1; limit++ {
		got := simulate(t, all, limit)
		if len(got) != len(all) {
			t.Fatalf("limit %d: emitted %v", limit, got)
		}
		for i, idx := range got {
			if idx != i {
				t.Fatalf("limit %d: emitted %v (duplicate or skipped row)", limit, got)
			}
		}
	}
}

func TestLogPageNoCursorWhenComplete(t *testing.T) {
	all := []logPos{{ts: 2, key: 1}, {ts: 1, key: 1}}
	if _, end, next := logPage(all, nil, 0, 2); end != 2 || next != "" {
		t.Errorf("exact page: end %d next %q", end, next)
	}
	if _, end, next := logPage(append(all, logPos{ts: 0, key: 0}), nil, 0, 2); end != 2 || next == "" {
		t.Errorf("full page with more rows: end %d next %q", end, next)
	}
}
