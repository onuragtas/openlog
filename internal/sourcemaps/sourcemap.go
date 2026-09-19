// Package sourcemaps reads Source Map v3 documents and maps a generated position back to the original one.
//
// It is written here rather than taken from a library because the format is small and the product's
// dependency budget is not: the whole decoder is the VLQ reader below plus the `mappings` grammar. What it
// gives back is deliberately narrow — a file, a line, a column and a symbol name — because that is what a
// stack frame needs and nothing in openlog serves original *sources*.
//
// Index maps (a document with "sections") are refused rather than half-parsed: they are rare in front-end
// bundlers, and a wrong original position is worse than an honest "no map".
package sourcemaps

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrIndexMap reports a document that uses the "sections" form.
var ErrIndexMap = errors.New("source map: index maps (sections) are not supported")

// Position is an original position. Line and Column are 1-based, like a stack frame prints them; the wire
// format is 0-based and the conversion happens at the edges of this package.
type Position struct {
	Source string
	Line   int
	Column int
	// Name is the original symbol of the segment, empty when the map carries none for it.
	Name string
}

// Map is a parsed source map.
type Map struct {
	// File is the generated file the map belongs to ("file" in the document), empty when absent.
	File string
	// Sources are the original paths, already joined with sourceRoot.
	Sources []string
	Names   []string
	// segs is sorted by (genLine, genCol): the lookup is a binary search per line.
	segs []segment
}

// segment is one entry of the `mappings` field. Fields are resolved (absolute), not the wire deltas.
type segment struct {
	genLine, genCol int
	// srcIdx is -1 for a segment that only marks generated code with no original position.
	srcIdx, srcLine, srcCol int
	// nameIdx is -1 when the segment carries no name.
	nameIdx int
}

type document struct {
	Version    int             `json:"version"`
	File       string          `json:"file"`
	SourceRoot string          `json:"sourceRoot"`
	Sources    []string        `json:"sources"`
	Names      []string        `json:"names"`
	Mappings   string          `json:"mappings"`
	Sections   json.RawMessage `json:"sections"`
}

// Parse decodes a source map document. It does not read "sourcesContent": openlog stores the map to resolve
// frames, never to show original code, so keeping the sources in memory would cost megabytes for nothing.
func Parse(b []byte) (*Map, error) {
	var doc document
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("source map: %w", err)
	}
	if len(doc.Sections) > 0 {
		return nil, ErrIndexMap
	}
	if doc.Version != 3 {
		return nil, fmt.Errorf("source map: unsupported version %d", doc.Version)
	}
	m := &Map{File: doc.File, Names: doc.Names}
	m.Sources = make([]string, len(doc.Sources))
	for i, s := range doc.Sources {
		m.Sources[i] = joinRoot(doc.SourceRoot, s)
	}
	segs, err := decodeMappings(doc.Mappings, len(m.Sources), len(m.Names))
	if err != nil {
		return nil, err
	}
	m.segs = segs
	return m, nil
}

// joinRoot applies sourceRoot the way the spec does: a plain prefix, with one separator.
func joinRoot(root, src string) string {
	if root == "" || src == "" {
		return src
	}
	if strings.HasSuffix(root, "/") {
		return root + strings.TrimPrefix(src, "/")
	}
	return root + "/" + strings.TrimPrefix(src, "/")
}

// Lookup returns the original position of a 1-based generated line and column.
//
// A frame points at the *start* of the failing expression, which rarely coincides with a segment boundary,
// so the answer is the last segment at or before the column — the same rule every source map consumer uses.
// A segment without a source (the "generated code only" form) reports false: naming a wrong file would be
// worse than leaving the frame minified.
func (m *Map) Lookup(line, column int) (Position, bool) {
	if m == nil || line < 1 || column < 1 {
		return Position{}, false
	}
	gl, gc := line-1, column-1
	// First segment of the line.
	i := sort.Search(len(m.segs), func(i int) bool {
		if m.segs[i].genLine != gl {
			return m.segs[i].genLine >= gl
		}
		return m.segs[i].genCol > gc
	})
	if i == 0 {
		return Position{}, false
	}
	s := m.segs[i-1]
	if s.genLine != gl || s.srcIdx < 0 || s.srcIdx >= len(m.Sources) {
		return Position{}, false
	}
	p := Position{Source: m.Sources[s.srcIdx], Line: s.srcLine + 1, Column: s.srcCol + 1}
	if s.nameIdx >= 0 && s.nameIdx < len(m.Names) {
		p.Name = m.Names[s.nameIdx]
	}
	return p, true
}

// Segments reports how many mapping segments the map carries. It exists so a caller can reject a map that
// decoded to nothing before storing it.
func (m *Map) Segments() int { return len(m.segs) }

// decodeMappings walks the `mappings` grammar: lines separated by ";", segments by ",", each segment one,
// four or five VLQ fields. Every field except the generated column carries across lines; the generated
// column restarts at each ";". Indexes are bounds-checked here so a hostile map cannot point past its own
// tables later.
func decodeMappings(mappings string, sources, names int) ([]segment, error) {
	var out []segment
	var srcIdx, srcLine, srcCol, nameIdx int
	genLine := 0
	for _, line := range strings.Split(mappings, ";") {
		genCol := 0
		if line != "" {
			for _, field := range strings.Split(line, ",") {
				if field == "" {
					continue
				}
				vals, err := decodeVLQ(field)
				if err != nil {
					return nil, err
				}
				switch len(vals) {
				case 1, 4, 5:
				default:
					return nil, fmt.Errorf("source map: segment with %d fields", len(vals))
				}
				genCol += vals[0]
				s := segment{genLine: genLine, genCol: genCol, srcIdx: -1, nameIdx: -1}
				if genCol < 0 {
					return nil, errors.New("source map: negative generated column")
				}
				if len(vals) >= 4 {
					srcIdx += vals[1]
					srcLine += vals[2]
					srcCol += vals[3]
					if srcIdx < 0 || srcIdx >= sources || srcLine < 0 || srcCol < 0 {
						return nil, fmt.Errorf("source map: segment points outside the document (source %d)", srcIdx)
					}
					s.srcIdx, s.srcLine, s.srcCol = srcIdx, srcLine, srcCol
				}
				if len(vals) == 5 {
					nameIdx += vals[4]
					if nameIdx < 0 || nameIdx >= names {
						return nil, fmt.Errorf("source map: segment names symbol %d of %d", nameIdx, names)
					}
					s.nameIdx = nameIdx
				}
				out = append(out, s)
			}
		}
		genLine++
	}
	// Bundlers emit segments in order, but the lookup is a binary search and a map that lies about the
	// order would silently return wrong frames, so the order is established here rather than assumed.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].genLine != out[j].genLine {
			return out[i].genLine < out[j].genLine
		}
		return out[i].genCol < out[j].genCol
	})
	return out, nil
}

// base64 alphabet of the VLQ encoding, indexed by character.
var b64 = func() [256]int8 {
	var t [256]int8
	for i := range t {
		t[i] = -1
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	for i := 0; i < len(alphabet); i++ {
		t[alphabet[i]] = int8(i)
	}
	return t
}()

const (
	vlqContinuation = 1 << 5
	vlqValueMask    = vlqContinuation - 1
	vlqShift        = 5
	// vlqMaxShift stops a run of continuation characters from overflowing the accumulator: 32 bits of
	// payload is far past any real line or column, and a map that needs more is malformed.
	vlqMaxShift = 32
)

// decodeVLQ reads the base64 variable-length quantities of one segment. Each quantity is little-endian
// groups of five bits with a continuation bit; the sign lives in the lowest bit of the assembled value.
func decodeVLQ(field string) ([]int, error) {
	var out []int
	value, shift := 0, 0
	for i := 0; i < len(field); i++ {
		d := b64[field[i]]
		if d < 0 {
			return nil, fmt.Errorf("source map: %q is not base64 VLQ", field)
		}
		digit := int(d)
		value += (digit & vlqValueMask) << shift
		if digit&vlqContinuation != 0 {
			shift += vlqShift
			if shift > vlqMaxShift {
				return nil, fmt.Errorf("source map: VLQ too long in %q", field)
			}
			continue
		}
		negative := value&1 == 1
		value >>= 1
		if negative {
			value = -value
		}
		out = append(out, value)
		value, shift = 0, 0
	}
	if shift != 0 {
		return nil, fmt.Errorf("source map: truncated VLQ in %q", field)
	}
	return out, nil
}
