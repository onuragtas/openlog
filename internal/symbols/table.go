// Package symbols turns the stack of a mobile crash back into the developer's own names (mobile-crash.md §3).
//
// A native stack is an image id and an offset, and a release build's Java stack is obfuscated. What undoes both is too large
// to send as it is: a dSYM runs to hundreds of megabytes, and most of it is type information a stack does not need. So the CLI
// reduces it on the build machine to a Table, address range to function, file and line, and the server only ever reads those.
// R8 mappings are text and compress well, so they are uploaded as they are.
package symbols

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/ianlancetaylor/demangle"
)

const tableHeader = "openlog-symbols 1"

// maxDecoded bounds what one upload may expand to, so a small compressed body cannot exhaust the server.
const maxDecoded = 1 << 30

type function struct {
	start, end uint64
	name       string
}

type lineEntry struct {
	addr uint64
	file int32
	line int32
}

// Table maps offsets in one image to source. Both slices are sorted by address.
type Table struct {
	funcs []function
	lines []lineEntry
	files []string
}

// Location is what an offset resolved to. File is empty when the image carried symbols but no line table.
type Location struct {
	Function string
	File     string
	Line     int
}

// Functions reports how many functions the table knows, so an upload with none can be refused.
func (t *Table) Functions() int { return len(t.funcs) }

// Lookup resolves an offset. The offset of a calling frame is its return address, just past the call, so such a frame can read
// one line late.
func (t *Table) Lookup(offset uint64) (Location, bool) {
	i := sort.Search(len(t.funcs), func(i int) bool { return t.funcs[i].start > offset }) - 1
	if i < 0 || offset >= t.funcs[i].end {
		return Location{}, false
	}
	loc := Location{Function: t.funcs[i].name}
	j := sort.Search(len(t.lines), func(j int) bool { return t.lines[j].addr > offset }) - 1
	// A line entry from before this function belongs to something else, and would be confidently wrong.
	if j >= 0 && t.lines[j].addr >= t.funcs[i].start && int(t.lines[j].file) < len(t.files) {
		loc.File, loc.Line = t.files[t.lines[j].file], int(t.lines[j].line)
	}
	return loc, true
}

// finish sorts the table and closes each function at the next one when its size is unknown, as a symbol table's often is.
func (t *Table) finish() {
	sort.SliceStable(t.funcs, func(i, j int) bool { return t.funcs[i].start < t.funcs[j].start })
	out := t.funcs[:0]
	for i, f := range t.funcs {
		if f.name == "" {
			continue
		}
		// Here and not on the server, which only ever reads the result. Swift's mangling is not covered, and stays as it is.
		f.name = demangle.Filter(f.name)
		if len(out) > 0 && out[len(out)-1].start == f.start {
			continue // the first source of a name wins, and DWARF is added before the symbol table
		}
		if f.end <= f.start {
			f.end = f.start + 1
			for _, next := range t.funcs[i+1:] {
				if next.start > f.start {
					f.end = next.start
					break
				}
			}
		}
		out = append(out, f)
	}
	t.funcs = out
	sort.Slice(t.lines, func(i, j int) bool { return t.lines[i].addr < t.lines[j].addr })
}

// Encode writes the table in its gzip form, which is what is uploaded and stored.
func (t *Table) Encode(w io.Writer) error {
	gz := gzip.NewWriter(w)
	b := bufio.NewWriter(gz)
	fmt.Fprintln(b, tableHeader)
	for i, f := range t.files {
		fmt.Fprintf(b, "s %d %s\n", i, f)
	}
	for _, f := range t.funcs {
		fmt.Fprintf(b, "f %x %x %s\n", f.start, f.end-f.start, f.name)
	}
	for _, l := range t.lines {
		fmt.Fprintf(b, "l %x %d %d\n", l.addr, l.file, l.line)
	}
	if err := b.Flush(); err != nil {
		return err
	}
	return gz.Close()
}

// decompress returns the plain bytes of a gzip body, or the body itself when it is not gzip.
func decompress(body []byte) ([]byte, error) {
	if len(body) < 2 || body[0] != 0x1f || body[1] != 0x8b {
		return body, nil
	}
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("symbols: %w", err)
	}
	out, err := io.ReadAll(io.LimitReader(gz, maxDecoded+1))
	if err != nil {
		return nil, fmt.Errorf("symbols: %w", err)
	}
	if len(out) > maxDecoded {
		return nil, errors.New("symbols: the file expands to more than 1 GiB")
	}
	return out, nil
}

// ParseTable reads what Encode wrote.
func ParseTable(body []byte) (*Table, error) {
	plain, err := decompress(body)
	if err != nil {
		return nil, err
	}
	s := bufio.NewScanner(bytes.NewReader(plain))
	s.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	if !s.Scan() || s.Text() != tableHeader {
		return nil, errors.New("symbols: not a symbol table made by openlog-symbols")
	}
	t := &Table{}
	for s.Scan() {
		line := s.Text()
		if len(line) < 2 {
			continue
		}
		rest := line[2:]
		switch line[0] {
		case 's':
			if _, path, ok := strings.Cut(rest, " "); ok {
				t.files = append(t.files, path)
			}
		case 'f':
			parts := strings.SplitN(rest, " ", 3)
			if len(parts) != 3 {
				return nil, fmt.Errorf("symbols: malformed function line %q", line)
			}
			start, err1 := strconv.ParseUint(parts[0], 16, 64)
			size, err2 := strconv.ParseUint(parts[1], 16, 64)
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("symbols: malformed function line %q", line)
			}
			t.funcs = append(t.funcs, function{start, start + size, parts[2]})
		case 'l':
			parts := strings.SplitN(rest, " ", 3)
			if len(parts) != 3 {
				return nil, fmt.Errorf("symbols: malformed line entry %q", line)
			}
			addr, err1 := strconv.ParseUint(parts[0], 16, 64)
			file, err2 := strconv.ParseInt(parts[1], 10, 32)
			num, err3 := strconv.ParseInt(parts[2], 10, 32)
			if err1 != nil || err2 != nil || err3 != nil {
				return nil, fmt.Errorf("symbols: malformed line entry %q", line)
			}
			t.lines = append(t.lines, lineEntry{addr, int32(file), int32(num)})
		}
	}
	if err := s.Err(); err != nil {
		return nil, fmt.Errorf("symbols: %w", err)
	}
	return t, nil
}
