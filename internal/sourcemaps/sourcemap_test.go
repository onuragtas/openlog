package sourcemaps

import (
	"encoding/json"
	"strings"
	"testing"
)

// encodeVLQ is the test's own encoder. The decoder is not checked against it alone — that would only prove
// the two agree — so the anchors below pin the encoder to values from the specification first.
func encodeVLQ(vals ...int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var b strings.Builder
	for _, v := range vals {
		u := v << 1
		if v < 0 {
			u = (-v << 1) | 1
		}
		for {
			digit := u & vlqValueMask
			u >>= vlqShift
			if u > 0 {
				digit |= vlqContinuation
			}
			b.WriteByte(alphabet[digit])
			if u == 0 {
				break
			}
		}
	}
	return b.String()
}

func TestVLQKnownValues(t *testing.T) {
	// From the Source Map v3 specification: "A" is 0, "C" is 1, "D" is -1, "gB" is 16 (a continuation).
	for _, c := range []struct {
		in   string
		want []int
	}{
		{"A", []int{0}},
		{"C", []int{1}},
		{"D", []int{-1}},
		{"gB", []int{16}},
		{"AAAA", []int{0, 0, 0, 0}},
		{"KAAA", []int{5, 0, 0, 0}},
	} {
		got, err := decodeVLQ(c.in)
		if err != nil {
			t.Fatalf("decode %q: %v", c.in, err)
		}
		if len(got) != len(c.want) {
			t.Fatalf("decode %q = %v, want %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("decode %q = %v, want %v", c.in, got, c.want)
			}
		}
		// The encoder must produce what the specification says, which is what makes it usable as a fixture
		// builder below.
		if enc := encodeVLQ(c.want...); enc != c.in {
			t.Errorf("encode %v = %q, want %q", c.want, enc, c.in)
		}
	}
}

func TestVLQRejectsGarbage(t *testing.T) {
	if _, err := decodeVLQ("!"); err == nil {
		t.Error("a character outside the alphabet must not decode")
	}
	// A trailing continuation bit with nothing after it is a truncated quantity, not a zero.
	if _, err := decodeVLQ("g"); err == nil {
		t.Error("truncated VLQ must not decode")
	}
}

// fixture builds a map whose generated line 1 holds two segments and line 2 one, mirroring what a bundler
// emits for `function greet(){throw new Error()}` minified onto one line.
func fixture(t *testing.T) []byte {
	t.Helper()
	// [genCol, srcIdx, srcLine, srcCol, nameIdx], each field a delta from the previous segment.
	line1 := strings.Join([]string{
		encodeVLQ(0, 0, 0, 0, 0),  // gen 1:1  -> app.ts 1:1   name "greet"
		encodeVLQ(10, 0, 4, 2, 1), // gen 1:11 -> app.ts 5:3   name "boom"
	}, ",")
	line2 := encodeVLQ(0, 1, 0, 0) // gen 2:1 -> util.ts 5:3, no name
	doc := map[string]any{
		"version":    3,
		"file":       "main.3f2a1b9c.js",
		"sourceRoot": "src/",
		"sources":    []string{"app.ts", "util.ts"},
		"names":      []string{"greet", "boom"},
		"mappings":   line1 + ";" + line2,
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLookupResolvesFrames(t *testing.T) {
	m, err := Parse(fixture(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.File != "main.3f2a1b9c.js" || m.Segments() != 3 {
		t.Fatalf("file %q, %d segments", m.File, m.Segments())
	}
	// sourceRoot is applied, so a frame names the path a developer recognises.
	if m.Sources[0] != "src/app.ts" || m.Sources[1] != "src/util.ts" {
		t.Fatalf("sources %v", m.Sources)
	}

	for _, c := range []struct {
		line, col int
		want      Position
	}{
		{1, 1, Position{Source: "src/app.ts", Line: 1, Column: 1, Name: "greet"}},
		// A column inside a segment resolves to that segment: a frame points at the start of an
		// expression, not at a mapping boundary.
		{1, 7, Position{Source: "src/app.ts", Line: 1, Column: 1, Name: "greet"}},
		{1, 11, Position{Source: "src/app.ts", Line: 5, Column: 3, Name: "boom"}},
		{1, 40, Position{Source: "src/app.ts", Line: 5, Column: 3, Name: "boom"}},
		{2, 1, Position{Source: "src/util.ts", Line: 5, Column: 3}},
	} {
		got, ok := m.Lookup(c.line, c.col)
		if !ok || got != c.want {
			t.Errorf("Lookup(%d,%d) = %+v %v, want %+v", c.line, c.col, got, ok, c.want)
		}
	}
}

func TestLookupMisses(t *testing.T) {
	m, err := Parse(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	// A line the map says nothing about, and the zero/negative inputs a bad frame parser could hand over.
	for _, c := range [][2]int{{3, 1}, {0, 1}, {1, 0}, {-1, -1}} {
		if _, ok := m.Lookup(c[0], c[1]); ok {
			t.Errorf("Lookup(%d,%d) resolved, want a miss", c[0], c[1])
		}
	}
	var nilMap *Map
	if _, ok := nilMap.Lookup(1, 1); ok {
		t.Error("a nil map must not resolve")
	}
}

func TestParseRejectsUnusableDocuments(t *testing.T) {
	if _, err := Parse([]byte(`{"version":3,"sections":[]}`)); err != ErrIndexMap {
		t.Errorf("index map: %v", err)
	}
	if _, err := Parse([]byte(`{"version":2,"mappings":""}`)); err == nil {
		t.Error("version 2 must be refused")
	}
	if _, err := Parse([]byte("not json")); err == nil {
		t.Error("garbage must be refused")
	}
	// A segment that points past the sources table would otherwise become a frame naming nothing.
	bad, _ := json.Marshal(map[string]any{
		"version": 3, "sources": []string{"a.ts"}, "names": []string{},
		"mappings": encodeVLQ(0, 5, 0, 0),
	})
	if _, err := Parse(bad); err == nil {
		t.Error("a segment outside the sources table must be refused")
	}
}
