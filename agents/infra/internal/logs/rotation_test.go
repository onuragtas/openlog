package logs

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// writeNumbered appends lines "<prefix><i>" for i in [from, to).
func (h *harness) writeNumbered(p, prefix string, from, to int) {
	var b strings.Builder
	for i := from; i < to; i++ {
		fmt.Fprintf(&b, "%s%d\n", prefix, i)
	}
	h.write(p, b.String())
}

// checkExactlyOnce fails unless the emitted bodies are exactly the given line sets, each line
// once, and the rotated file is no longer tailed.
func checkExactlyOnce(t *testing.T, h *harness, sets map[string]int) {
	t.Helper()
	seen := map[string]int{}
	for _, b := range h.sink.bodies() {
		if strings.Contains(b, "\n") {
			t.Fatalf("record with several lines (%d bytes): %.80q", len(b), b)
		}
		seen[b]++
	}
	want := 0
	for prefix, n := range sets {
		want += n
		for i := 0; i < n; i++ {
			if c := seen[fmt.Sprintf("%s%d", prefix, i)]; c != 1 {
				t.Fatalf("line %s%d emitted %d times (%d records, %d unique, want %d)", prefix, i, c, len(h.sink.bodies()), len(seen), want)
			}
		}
	}
	if len(seen) != want {
		t.Errorf("%d unique records, want %d", len(seen), want)
	}
	if files := h.m.Files(); len(files) != 1 || files[0] != "/var/log/app/a.log" {
		t.Errorf("tailed files after rotation = %v", files)
	}
}

// Lines written just before a rename+create rotation, not read yet, still come from the renamed
// file (read to its end), followed by the new file.
func TestRotationUnreadLines(t *testing.T) {
	t.Run("unread at rotation", func(t *testing.T) {
		h := newHarness(t)
		h.start()
		h.write("var/log/app/a.log", "old 0\n")
		h.tick(time.Second)
		h.writeNumbered("var/log/app/a.log", "old ", 1, 500)
		if err := os.Rename(h.path("var/log/app/a.log"), h.path("var/log/app/a.log.1")); err != nil {
			t.Fatal(err)
		}
		h.writeNumbered("var/log/app/a.log", "new ", 0, 300)
		for i := 0; i < 3; i++ {
			h.tick(time.Second)
		}
		h.tick(rotateGrace)
		got := h.sink.bodies()
		if len(got) != 800 || got[0] != "old 0" || got[499] != "old 499" || got[500] != "new 0" || got[799] != "new 299" {
			t.Fatalf("%d records, order: %q %q %q", len(got), got[0], got[min(499, len(got)-1)], got[min(500, len(got)-1)])
		}
		checkExactlyOnce(t, h, map[string]int{"old ": 500, "new ": 300})
	})

	// More unread data than one poll reads (readBudget).
	t.Run("unread beyond read budget", func(t *testing.T) {
		h := newHarness(t)
		h.start()
		h.write("var/log/app/a.log", "old 0\n")
		h.tick(time.Second)
		prefix := "old " + strings.Repeat("x", 200) + " "
		n := 2*readBudget/(len(prefix)+8) + 1
		h.writeNumbered("var/log/app/a.log", prefix, 0, n)
		f, _ := os.Stat(h.path("var/log/app/a.log"))
		if f.Size() <= readBudget {
			t.Fatalf("burst %d bytes, want > %d", f.Size(), readBudget)
		}
		if err := os.Rename(h.path("var/log/app/a.log"), h.path("var/log/app/a.log.1")); err != nil {
			t.Fatal(err)
		}
		h.writeNumbered("var/log/app/a.log", "new ", 0, 100)
		for i := 0; i < 6; i++ {
			h.tick(time.Second)
		}
		h.tick(rotateGrace)
		checkExactlyOnce(t, h, map[string]int{"old ": 1, prefix: n, "new ": 100})
	})

	// Rate-limited: the renamed file is still being read when rotateGrace has passed since its
	// last read; it must not be closed before its end (short lines: one read chunk holds more
	// lines than the per-second limit).
	t.Run("rate limited", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.RateLimitLines = 100
		h.start()
		h.write("var/log/app/a.log", "old 0\n")
		h.tick(time.Second)
		h.writeNumbered("var/log/app/a.log", "old ", 1, 1500)
		if err := os.Rename(h.path("var/log/app/a.log"), h.path("var/log/app/a.log.1")); err != nil {
			t.Fatal(err)
		}
		h.writeNumbered("var/log/app/a.log", "new ", 0, 200)
		for i := 0; i < 40; i++ {
			h.tick(time.Second)
		}
		h.tick(rotateGrace)
		checkExactlyOnce(t, h, map[string]int{"old ": 1500, "new ": 200})
	})
}
