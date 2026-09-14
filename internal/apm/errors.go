package apm

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/cespare/xxhash/v2"
)

// MaxErrorMessage bounds error_message (bytes).
const MaxErrorMessage = 512

const exceptionEvent = "exception"

var (
	quotedRe = regexp.MustCompile("(^|[^\\pL\\pN_])(?:'[^']*'|\"[^\"]*\"|`[^`]*`)")
	uuidRe   = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	emailRe  = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	ipRe     = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}(?::\d{1,5})?\b`)
	hexRe    = regexp.MustCompile(`\b(?:0x)?[0-9a-fA-F]*[0-9][0-9a-fA-F]*\b`)
	numberRe = regexp.MustCompile(`(^|[^\pL\pN_])[-+]?\d+(?:\.\d+)?`)
	spacesRe = regexp.MustCompile(`\s+`)
	// ISO 8601 / RFC 3339 timestamps (date and time), apm.md §3.1 (fingerprint v2).
	timestampRe = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(?::\d{2}(?:[.,]\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?`)
	// Identifiers glued to a word prefix with "_" (req_8f3a9c2b1d, order_123456): word boundaries do not
	// separate them, so hexRe/numberRe never matched.
	prefixedIDRe = regexp.MustCompile(`\b((?:[A-Za-z][A-Za-z0-9]*_)+)([A-Za-z0-9]+)\b`)
)

// prefixedID replaces the id part of prefix_id when it is ≥ 4 digits, or ≥ 6 characters mixing digits and letters.
func prefixedID(m string) string {
	sub := prefixedIDRe.FindStringSubmatch(m)
	id := sub[2]
	digits, letters := 0, 0
	for _, c := range id {
		if c >= '0' && c <= '9' {
			digits++
		} else {
			letters++
		}
	}
	if (letters == 0 && digits >= 4) || (digits > 0 && letters > 0 && len(id) >= 6) {
		return sub[1] + "<id>"
	}
	return m
}

// NormalizeMessage strips variable parts from an error message (apm.md §3.1).
func NormalizeMessage(msg string) string {
	s := quotedRe.ReplaceAllString(msg, "$1'?'")
	s = timestampRe.ReplaceAllString(s, "<ts>")
	s = uuidRe.ReplaceAllString(s, "<uuid>")
	s = prefixedIDRe.ReplaceAllStringFunc(s, prefixedID)
	s = emailRe.ReplaceAllString(s, "<email>")
	s = ipRe.ReplaceAllString(s, "<ip>")
	s = hexRe.ReplaceAllStringFunc(s, func(m string) string {
		h := strings.TrimPrefix(m, "0x")
		if len(h) >= 8 {
			return "<hex>"
		}
		return m
	})
	s = numberRe.ReplaceAllString(s, "$1<n>")
	s = strings.TrimSpace(spacesRe.ReplaceAllString(s, " "))
	return Truncate(s, MaxErrorMessage)
}

// ErrorGroup returns the error type, normalized message and group id of an error span.
func ErrorGroup(service, namespace, env string, in *Input, httpStatus uint16) (string, string, uint64) {
	var typ, msg, stack string
	for i := len(in.EventsName) - 1; i >= 0; i-- {
		if in.EventsName[i] == exceptionEvent && i < len(in.EventsAttributes) {
			a := in.EventsAttributes[i]
			typ, msg, stack = a["exception.type"], a["exception.message"], a["exception.stacktrace"]
			break
		}
	}
	if msg == "" {
		msg = in.StatusMessage
	}
	if typ == "" {
		if httpStatus >= 400 {
			typ = "HTTP " + strconv.Itoa(int(httpStatus))
		} else {
			typ = "error"
		}
	}
	norm := NormalizeMessage(msg)
	frame := TopFrame(stack)
	h := xxhash.New()
	for i, part := range []string{service, namespace, env, typ, norm, frame} {
		if i > 0 {
			_, _ = h.Write([]byte{0})
		}
		_, _ = h.WriteString(part)
	}
	id := h.Sum64()
	if id == 0 {
		id = 1 // 0 means "no error group"
	}
	return Truncate(typ, 256), norm, id
}

// GroupIDString formats an error group id as 16 hex digits.
func GroupIDString(id uint64) string {
	s := strconv.FormatUint(id, 16)
	return strings.Repeat("0", 16-len(s)) + s
}

// ParseGroupID parses 16 hex digits.
func ParseGroupID(s string) (uint64, bool) {
	if len(s) != 16 {
		return 0, false
	}
	id, err := strconv.ParseUint(s, 16, 64)
	return id, err == nil && id != 0
}

type frame struct{ fn, file string }

var (
	nodeFrameRe   = regexp.MustCompile(`^\s*at (?:(.+?) \()?(.+?)(?::\d+)?(?::\d+)?\)?$`)
	phpFrameRe    = regexp.MustCompile(`^#\d+ (.+?)\((\d+)\): (.+)$`)
	javaFrameRe   = regexp.MustCompile(`^\s*at ([^\s(]+)\((.*?)(?::\d+)?\)$`)
	pythonFrameRe = regexp.MustCompile(`^\s*File "(.+?)", line \d+, in (.+)$`)
	goFileRe      = regexp.MustCompile(`^\t(.+?\.go)(?::\d+)?(?: \+0x[0-9a-f]+)?$`)
)

// TopFrame returns "function@file" of the top in-app stack frame (apm.md §3.2).
func TopFrame(stack string) string {
	if stack == "" {
		return ""
	}
	frames := parseFrames(stack)
	if len(frames) == 0 {
		return ""
	}
	for _, f := range frames {
		if !isLibraryFrame(f) {
			return frameKey(f)
		}
	}
	return frameKey(frames[0])
}

var (
	goFuncLitRe     = regexp.MustCompile(`\.func\d+(\.\d+)*`)
	goGenericRe     = regexp.MustCompile(`\[[^\[\]]*\]`)
	javaLambdaRe    = regexp.MustCompile(`\$\$Lambda(?:\$\d+)?(?:/0x[0-9a-fA-F]+)?`)
	javaLambdaFnRe  = regexp.MustCompile(`lambda\$([A-Za-z0-9_]+)\$\d+`)
	javaGeneratedRe = regexp.MustCompile(`(\$Proxy|GeneratedMethodAccessor|GeneratedConstructorAccessor|\$\$EnhancerBySpringCGLIB\$\$)[0-9a-fA-F]+`)
	phpAnonClassRe  = regexp.MustCompile(`class@anonymous[^\s:]*(?::\d+)?(?:\$[0-9a-fA-F]+)?`)
	bundleHashRe    = regexp.MustCompile(`[.-][0-9a-fA-F]{8,}(\.(?:min\.)?m?[jt]sx?)$`)
	releaseDirRe    = regexp.MustCompile(`^(?:\d{8,}|[0-9a-fA-F]{12,}|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`)
)

// frameKey is "function@file" with deploy- and build-specific parts removed (apm.md §3.2, fingerprint v2):
// numbered Go func literals and generic instantiations, Java lambda/proxy/accessor class numbers, PHP anonymous
// class suffixes, content hashes in bundled JS file names, query strings and release directories (≥ 8 digits,
// ≥ 12 hex characters or a UUID) in paths.
func frameKey(f frame) string {
	fn := goFuncLitRe.ReplaceAllString(f.fn, ".func")
	fn = goGenericRe.ReplaceAllString(fn, "[...]")
	fn = javaLambdaRe.ReplaceAllString(fn, "$$$$Lambda")
	fn = javaLambdaFnRe.ReplaceAllString(fn, "lambda$$$1")
	fn = javaGeneratedRe.ReplaceAllString(fn, "$1")
	fn = phpAnonClassRe.ReplaceAllString(fn, "class@anonymous")
	return fn + "@" + normalizeFramePath(f.file)
}

func normalizeFramePath(p string) string {
	p = strings.TrimPrefix(p, "file://")
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		if i < len(parts)-1 && releaseDirRe.MatchString(seg) {
			parts[i] = "<id>"
		}
	}
	p = strings.Join(parts, "/")
	return bundleHashRe.ReplaceAllString(p, "$1")
}

func parseFrames(stack string) []frame {
	lines := strings.Split(strings.ReplaceAll(stack, "\r\n", "\n"), "\n")
	var out []frame
	for i, line := range lines {
		if m := goFileRe.FindStringSubmatch(line); m != nil && i > 0 {
			fn := strings.TrimSpace(lines[i-1])
			if p := strings.LastIndex(fn, "("); p > 0 && strings.HasSuffix(fn, ")") {
				fn = fn[:p]
			}
			out = append(out, frame{fn: fn, file: m[1]})
			continue
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "at "):
			if m := javaFrameRe.FindStringSubmatch(line); m != nil && (!strings.Contains(m[1], "/") || javaLambdaRe.MatchString(m[1])) {
				out = append(out, frame{fn: m[1], file: m[2]})
			} else if m := nodeFrameRe.FindStringSubmatch(line); m != nil {
				fn := m[1]
				if fn == "" {
					fn = "<anonymous>"
				}
				out = append(out, frame{fn: fn, file: m[2]})
			}
		case strings.HasPrefix(trimmed, "#"):
			if m := phpFrameRe.FindStringSubmatch(trimmed); m != nil {
				fn := m[3]
				if p := strings.Index(fn, "("); p > 0 {
					fn = fn[:p]
				}
				out = append(out, frame{fn: fn, file: m[1]})
			}
		case strings.HasPrefix(trimmed, "File \""):
			if m := pythonFrameRe.FindStringSubmatch(line); m != nil {
				out = append(out, frame{fn: m[2], file: m[1]})
			}
		}
	}
	// Python prints the innermost frame last.
	if strings.Contains(stack, "Traceback (most recent call last)") {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out
}

var libraryPathParts = []string{"/vendor/", "node_modules", "/usr/local/go/", "/usr/lib/go", "/go/pkg/mod/", "GOROOT",
	"node:", "<anonymous>", "/usr/share/php", "/usr/lib/python", "site-packages", "internal/process", "internal/modules"}

var libraryFuncPrefixes = []string{"runtime.", "runtime/", "net/http.", "panic", "testing.", "go.opentelemetry.io/"}

func isLibraryFrame(f frame) bool {
	for _, p := range libraryPathParts {
		if strings.Contains(f.file, p) {
			return true
		}
	}
	for _, p := range libraryFuncPrefixes {
		if strings.HasPrefix(f.fn, p) {
			return true
		}
	}
	return f.file == ""
}
