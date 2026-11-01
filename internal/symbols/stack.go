package symbols

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Mapping is an R8 or ProGuard mapping file, enough of it to retrace a stack.
type Mapping struct {
	classes map[string]*mappedClass
}

type mappedClass struct {
	name, file string
	methods    map[string][]mappedMethod
}

type mappedMethod struct {
	name                           string
	start, end, origStart, origEnd int
}

var (
	mapClassRe  = regexp.MustCompile(`^(\S+) -> (\S+):$`)
	mapMethodRe = regexp.MustCompile(`^\s+(?:(\d+):(\d+):)?\S+ ([^\s(]+)\([^)]*\)(?::(\d+)(?::(\d+))?)? -> (\S+)$`)
	mapFileRe   = regexp.MustCompile(`"fileName"\s*:\s*"([^"]+)"`)
)

// ParseMapping reads a mapping file, gzip or plain.
func ParseMapping(body []byte) (*Mapping, error) {
	plain, err := decompress(body)
	if err != nil {
		return nil, err
	}
	m := &Mapping{classes: map[string]*mappedClass{}}
	s := bufio.NewScanner(bytes.NewReader(plain))
	s.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var cur *mappedClass
	for s.Scan() {
		line := s.Text()
		if c := mapClassRe.FindStringSubmatch(line); c != nil {
			cur = &mappedClass{name: c[1], methods: map[string][]mappedMethod{}}
			m.classes[c[2]] = cur
			continue
		}
		if cur == nil {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			if f := mapFileRe.FindStringSubmatch(line); f != nil {
				cur.file = f[1]
			}
			continue
		}
		if c := mapMethodRe.FindStringSubmatch(line); c != nil {
			start, _ := strconv.Atoi(c[1])
			end, _ := strconv.Atoi(c[2])
			os, _ := strconv.Atoi(c[4])
			oe, _ := strconv.Atoi(c[5])
			if oe == 0 {
				oe = os
			}
			cur.methods[c[6]] = append(cur.methods[c[6]], mappedMethod{c[3], start, end, os, oe})
		}
	}
	if err := s.Err(); err != nil {
		return nil, fmt.Errorf("symbols: %w", err)
	}
	if len(m.classes) == 0 {
		return nil, errors.New("symbols: not a mapping file: no class lines")
	}
	return m, nil
}

// Class returns the original name of an obfuscated class, or the name it was given.
func (m *Mapping) Class(obfuscated string) string {
	if c := m.classes[obfuscated]; c != nil {
		return c.name
	}
	return obfuscated
}

// retrace resolves one frame. An obfuscated line number selects among methods sharing a name, which R8 does freely.
func (m *Mapping) retrace(class, method string, line int) (string, string, int, bool) {
	c := m.classes[class]
	if c == nil {
		return "", "", 0, false
	}
	file := c.file
	if file == "" {
		simple := c.name[strings.LastIndexByte(c.name, '.')+1:]
		if i := strings.IndexByte(simple, '$'); i > 0 {
			simple = simple[:i]
		}
		file = simple + ".java"
	}
	candidates := c.methods[method]
	var hit *mappedMethod
	for i := range candidates {
		mm := &candidates[i]
		if mm.end > 0 && line >= mm.start && line <= mm.end {
			hit = mm
			break
		}
		if mm.end == 0 && hit == nil {
			hit = mm
		}
	}
	if hit == nil {
		return c.name + "." + method, file, line, true
	}
	orig := line
	switch {
	case hit.origStart > 0 && hit.end > 0 && hit.origEnd > hit.origStart:
		orig = hit.origStart + line - hit.start
	case hit.origStart > 0:
		orig = hit.origStart
	}
	return c.name + "." + hit.name, file, orig, true
}

// Resolver finds what undoes a stack. Each returns nil when nothing was uploaded for it.
type Resolver struct {
	Mapping func() *Mapping
	Image   func(id string) *Table
}

// ClassName undoes an obfuscated exception type, whose stack line alone would still read a.b: boom.
func (r Resolver) ClassName(name string) string {
	if r.Mapping != nil {
		if m := r.Mapping(); m != nil {
			return m.Class(name)
		}
	}
	return name
}

// Result is a stack after symbolication. Frame is what crashes group on: the first function symbols resolved, since an image
// with symbols uploaded is the application's own, or failing that the first name the device itself knew. Missing names the images
// of a native stack that look like the application's and have no symbols, and MissingMapping says the same of a Java stack.
type Result struct {
	Stack          string
	Resolved       int
	Frame          string
	Missing        []string
	MissingMapping bool
}

var (
	// #00 UnityFramework 0123abc +0x1a2b, as the SDKs write a native frame (mobile-crash.md §3), with a name when the device knew one.
	nativeFrameRe = regexp.MustCompile(`^#(\d+) (\S+) ([0-9a-fA-F-]{16,}) \+0x([0-9a-fA-F]+)(?: (.*))?$`)
	javaFrameRe   = regexp.MustCompile(`^(\s*at )([\w$.<>]+)\.([\w$<>]+)\(([^:)]*)(?::(\d+))?\)(.*)$`)
)

// Symbolicate rewrites every frame it can. It never fails: an unresolved frame stays as the device sent it.
func Symbolicate(stack string, r Resolver) Result {
	res := Result{}
	if stack == "" {
		return res
	}
	lines := strings.Split(stack, "\n")
	missing := map[string]bool{}
	var mapping *Mapping
	mappingAsked := false
	sawObfuscated := false
	named := ""
	var unnamedMachO []string
	for i, line := range lines {
		if f := nativeFrameRe.FindStringSubmatch(line); f != nil {
			id := NormalizeImageID(f[3])
			offset, err := strconv.ParseUint(f[4], 16, 64)
			var t *Table
			if err == nil && r.Image != nil {
				t = r.Image(id)
			}
			if t == nil {
				// A frame the device named is the system's, whose symbols nobody can upload, as is a dylib on iOS, where
				// no frame is named. Listing those would bury the one line someone can act on.
				if !missing[id] && f[5] == "" && !strings.HasSuffix(f[2], ".dylib") {
					missing[id] = true
					entry := f[2] + " " + id
					if strings.Contains(f[3], "-") {
						unnamedMachO = append(unnamedMachO, entry)
					} else {
						res.Missing = append(res.Missing, entry)
					}
				}
				if named == "" {
					named = f[5]
				}
				continue
			}
			loc, ok := t.Lookup(offset)
			if !ok {
				continue
			}
			res.Resolved++
			if res.Frame == "" {
				res.Frame = loc.Function
			}
			lines[i] = fmt.Sprintf("#%s %s %s", f[1], f[2], loc.Function)
			if loc.File != "" {
				lines[i] += fmt.Sprintf(" (%s:%d)", loc.File, loc.Line)
			}
			continue
		}
		if f := javaFrameRe.FindStringSubmatch(line); f != nil {
			if !mappingAsked {
				mappingAsked = true
				if r.Mapping != nil {
					mapping = r.Mapping()
				}
			}
			// R8's own mark. "Unknown Source" is no sign of it: the system's frames say that on any build.
			if f[4] == "SourceFile" {
				sawObfuscated = true
			}
			if mapping == nil {
				continue
			}
			n, _ := strconv.Atoi(f[5])
			method, file, orig, ok := mapping.retrace(f[2], f[3], n)
			if !ok {
				continue
			}
			res.Resolved++
			if res.Frame == "" {
				res.Frame = method
			}
			lines[i] = fmt.Sprintf("%s%s(%s:%d)%s", f[1], method, file, orig, f[6])
		}
	}
	if res.Frame == "" {
		res.Frame = named
	}
	// MetricKit names no frame and iOS frameworks have no telling suffix, so UIKitCore looks just like the application. Once
	// symbols resolved anything, the application's image is known, and the rest are taken to be the system's.
	if res.Resolved == 0 {
		res.Missing = append(res.Missing, unnamedMachO...)
	}
	res.MissingMapping = sawObfuscated && mapping == nil
	res.Stack = strings.Join(lines, "\n")
	return res
}
