package sourcemaps

import (
	"strings"
	"testing"
)

func TestParseStackReadsBothEngineShapes(t *testing.T) {
	stack := strings.Join([]string{
		"TypeError: cart.items is undefined",
		"    at n (https://shop.example.com/assets/main.3f2a1b9c.js:1:842)",
		"    at https://shop.example.com/assets/main.3f2a1b9c.js:1:1200",
		"handleClick@https://shop.example.com/assets/main.3f2a1b9c.js:2:17",
		"",
	}, "\n")

	frames := ParseStack(stack)
	if len(frames) != 5 {
		t.Fatalf("%d lines, want 5", len(frames))
	}
	// The header and the blank line are text, not positions.
	if frames[0].IsFrame || frames[4].IsFrame {
		t.Error("header or blank line parsed as a frame")
	}
	if f := frames[1]; !f.IsFrame || f.Function != "n" || f.Line != 1 || f.Column != 842 ||
		f.File != "https://shop.example.com/assets/main.3f2a1b9c.js" {
		t.Errorf("v8 frame: %+v", f)
	}
	// V8 also emits frames with no function at all.
	if f := frames[2]; !f.IsFrame || f.Function != "" || f.Column != 1200 {
		t.Errorf("anonymous v8 frame: %+v", f)
	}
	// Firefox and Safari write fn@url:line:col.
	if f := frames[3]; !f.IsFrame || f.Function != "handleClick" || f.Line != 2 || f.Column != 17 {
		t.Errorf("at-sign frame: %+v", f)
	}
}

func TestScriptNameIsTheBundleFileName(t *testing.T) {
	for in, want := range map[string]string{
		"https://shop.example.com/assets/main.3f2a1b9c.js": "main.3f2a1b9c.js",
		"https://shop.example.com/assets/main.js?v=2":      "main.js",
		"https://shop.example.com/assets/main.js#frag":     "main.js",
		"/assets/main.js": "main.js",
		"main.js":         "main.js",
		"https://cdn.example.com/a/b/c/vendor-9f8e7d6c.min.mjs": "vendor-9f8e7d6c.min.mjs",
	} {
		if got := ScriptName(in); got != want {
			t.Errorf("ScriptName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSymbolicateRewritesOnlyResolvedFrames(t *testing.T) {
	m, err := Parse(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(script string) *Map {
		if script == "main.3f2a1b9c.js" {
			return m
		}
		return nil
	}
	stack := strings.Join([]string{
		"TypeError: cart.items is undefined",
		"    at n (https://shop.example.com/main.3f2a1b9c.js:1:1)",
		"    at https://shop.example.com/main.3f2a1b9c.js:1:11",
		// A different bundle with no map stays exactly as it was.
		"    at q (https://cdn.example.com/vendor.abcdef12.js:9:4)",
	}, "\n")

	out, n := Symbolicate(stack, resolve)
	if n != 2 {
		t.Fatalf("%d frames resolved, want 2", n)
	}
	lines := strings.Split(out, "\n")
	// The header is untouched: symbolication changes where an error happened, never what it said.
	if lines[0] != "TypeError: cart.items is undefined" {
		t.Errorf("header rewritten: %q", lines[0])
	}
	// The map's own symbol replaces the minified one, and indentation survives.
	if lines[1] != "    at greet (src/app.ts:1:1)" {
		t.Errorf("frame 1: %q", lines[1])
	}
	if lines[2] != "    at boom (src/app.ts:5:3)" {
		t.Errorf("frame 2: %q", lines[2])
	}
	if lines[3] != "    at q (https://cdn.example.com/vendor.abcdef12.js:9:4)" {
		t.Errorf("unmapped frame was rewritten: %q", lines[3])
	}
}

func TestSymbolicateWithoutMapsReturnsTheStackUnchanged(t *testing.T) {
	stack := "Error: boom\n    at n (https://shop.example.com/main.3f2a1b9c.js:1:1)"
	out, n := Symbolicate(stack, func(string) *Map { return nil })
	if n != 0 || out != stack {
		t.Errorf("stack changed without a map: %d %q", n, out)
	}
	// An empty stack is common (a cross-origin "Script error." has none).
	if out, n := Symbolicate("", func(string) *Map { return nil }); n != 0 || out != "" {
		t.Errorf("empty stack: %d %q", n, out)
	}
}

func TestSymbolicateResolvesEachScriptOnce(t *testing.T) {
	m, err := Parse(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
	resolve := func(script string) *Map {
		calls[script]++
		if script == "main.3f2a1b9c.js" {
			return m
		}
		return nil
	}
	stack := strings.Repeat("    at n (https://shop.example.com/main.3f2a1b9c.js:1:1)\n", 5) +
		strings.Repeat("    at q (https://cdn.example.com/vendor.js:1:1)\n", 3)
	if _, n := Symbolicate(stack, resolve); n != 5 {
		t.Fatalf("%d frames resolved, want 5", n)
	}
	// Storage is behind the resolver, so asking it once per script is the difference between one read and
	// one per frame.
	if calls["main.3f2a1b9c.js"] != 1 || calls["vendor.js"] != 1 {
		t.Errorf("resolver called %v", calls)
	}
}
