package sourcemaps

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// A browser sends whatever `error.stack` its engine produced (agents/browser/src/errors.ts), so both shapes
// in the wild have to be read here:
//
//	V8 (Chrome, Edge, Node):   "    at greet (https://shop.example.com/main.3f2a1b9c.js:1:842)"
//	                           "    at https://shop.example.com/main.3f2a1b9c.js:1:842"   (no function)
//	SpiderMonkey / JSC:        "greet@https://shop.example.com/main.3f2a1b9c.js:1:842"
//
// These deliberately do NOT reuse internal/apm's frame regexes: those feed the error fingerprint and throw
// the line and column away on purpose, and a fingerprint change would re-key every error group that exists.
var (
	v8FrameRe     = regexp.MustCompile(`^(\s*at )(?:(.+?) \()?(.+?):(\d+):(\d+)(\)?)\s*$`)
	atSignFrameRe = regexp.MustCompile(`^(\s*)(?:(.*?)@)(.+?):(\d+):(\d+)\s*$`)
)

// Frame is one line of a stack. A line that is not a frame keeps only Raw.
type Frame struct {
	// Raw is the line exactly as it arrived.
	Raw string
	// IsFrame reports whether the line parsed as a frame with a position.
	IsFrame  bool
	Function string
	// File is the script as the frame named it: usually an absolute URL.
	File   string
	Line   int
	Column int
	// Original is the position a source map resolved, nil when nothing mapped it.
	Original *Position
}

// ParseStack splits a stack into lines and parses the frames among them. Lines that are not frames (the
// leading "TypeError: …" header, blank lines, engine noise) come back with IsFrame false and are never
// rewritten: symbolication may change how a frame reads, never what the error said.
func ParseStack(stack string) []Frame {
	if stack == "" {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(stack, "\r\n", "\n"), "\n")
	out := make([]Frame, 0, len(lines))
	for _, l := range lines {
		out = append(out, parseFrameLine(l))
	}
	return out
}

func parseFrameLine(line string) Frame {
	f := Frame{Raw: line}
	for _, re := range []*regexp.Regexp{v8FrameRe, atSignFrameRe} {
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ln, err1 := strconv.Atoi(m[4])
		col, err2 := strconv.Atoi(m[5])
		if err1 != nil || err2 != nil || ln < 1 || col < 1 {
			// A frame whose numbers do not parse is text, not a position: leave it alone.
			return f
		}
		f.IsFrame, f.Function, f.File, f.Line, f.Column = true, m[2], m[3], ln, col
		return f
	}
	return f
}

// ScriptName is the key a map is stored under: the last path segment of the script URL, without the query
// string or fragment. Bundles carry a content hash in that name ("main.3f2a1b9c.js"), which is what ties a
// stack to one build — RUM spans carry no release identifier of their own (rum.md §3.3).
func ScriptName(file string) string {
	s := file
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// Resolver returns the parsed map for a script name, or nil when none is stored. It is a function so the
// caller decides where maps come from and how they are cached; this package never reads storage.
type Resolver func(script string) *Map

// Symbolicate resolves every frame it can and returns the rewritten stack together with the number of frames
// that resolved. The original text is not modified in place: the caller keeps both, because a stack that was
// only partly mapped is still read against the minified one.
func Symbolicate(stack string, resolve Resolver) (string, int) {
	frames := ParseStack(stack)
	if len(frames) == 0 {
		return stack, 0
	}
	// One map per script for the whole stack: a frame set from one bundle would otherwise parse the same
	// document for every line.
	maps := map[string]*Map{}
	resolved := 0
	lines := make([]string, len(frames))
	for i, f := range frames {
		lines[i] = f.Raw
		if !f.IsFrame {
			continue
		}
		script := ScriptName(f.File)
		m, seen := maps[script]
		if !seen {
			m = resolve(script)
			maps[script] = m
		}
		if m == nil {
			continue
		}
		pos, ok := m.Lookup(f.Line, f.Column)
		if !ok {
			continue
		}
		frames[i].Original = &pos
		lines[i] = rewrite(f, pos)
		resolved++
	}
	if resolved == 0 {
		return stack, 0
	}
	return strings.Join(lines, "\n"), resolved
}

// rewrite renders a resolved frame in the V8 shape a developer reads fastest, keeping the original
// indentation. The map's symbol name wins over the minified one when it has it: "at n (main.js:1:842)"
// becomes "at greet (src/app.ts:5:3)".
func rewrite(f Frame, pos Position) string {
	indent := f.Raw[:len(f.Raw)-len(strings.TrimLeft(f.Raw, " \t"))]
	name := pos.Name
	if name == "" {
		name = f.Function
	}
	where := fmt.Sprintf("%s:%d:%d", pos.Source, pos.Line, pos.Column)
	if name == "" {
		return indent + "at " + where
	}
	return indent + "at " + name + " (" + where + ")"
}
