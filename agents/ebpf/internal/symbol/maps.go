// Package symbol turns a sampled address into a name (docs/contracts/ebpf-profiler.md §7).
//
// An address from the kernel means nothing on its own: it is only meaningful inside the mapping it fell
// in. This file is the first half — which file is mapped at that address, and where in that file the
// address lands. No build tag: it parses text the kernel wrote, so it is tested here rather than only on
// a machine that can run BPF.
package symbol

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// Mapping is one executable, file-backed region of a process's address space.
type Mapping struct {
	// Start is inclusive, End exclusive, matching how /proc/<pid>/maps prints them.
	Start, End uint64
	// Offset is where this region begins inside the file.
	Offset uint64
	Path   string
}

// FileOffset converts a runtime address into an offset inside the mapped file. Getting this wrong does
// not fail: it names every frame after whichever symbol happens to sit at the wrong offset, which reads
// as a plausible profile of a program nobody ran.
func (m Mapping) FileOffset(addr uint64) uint64 {
	return addr - m.Start + m.Offset
}

// Contains reports whether addr falls in this mapping.
func (m Mapping) Contains(addr uint64) bool { return addr >= m.Start && addr < m.End }

// ParseMaps reads /proc/<pid>/maps and keeps the mappings an instruction pointer can land in: executable
// and backed by a file.
//
// Everything else is dropped on purpose. [heap], [stack] and anonymous regions have no file to read
// symbols from, and a non-executable mapping cannot contain the address a CPU sample interrupted.
func ParseMaps(r io.Reader) ([]Mapping, error) {
	var out []Mapping
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		m, ok := parseMapLine(sc.Text())
		if ok {
			out = append(out, m)
		}
	}
	return out, sc.Err()
}

// parseMapLine reads one line: "<start>-<end> <perms> <offset> <dev> <inode> <path>".
func parseMapLine(line string) (Mapping, bool) {
	f := strings.Fields(line)
	if len(f) < 6 {
		// No path: anonymous memory, nothing to symbolize against.
		return Mapping{}, false
	}
	if len(f[1]) < 4 || f[1][2] != 'x' {
		return Mapping{}, false
	}
	dash := strings.IndexByte(f[0], '-')
	if dash < 0 {
		return Mapping{}, false
	}
	start, err := strconv.ParseUint(f[0][:dash], 16, 64)
	if err != nil {
		return Mapping{}, false
	}
	end, err := strconv.ParseUint(f[0][dash+1:], 16, 64)
	if err != nil || end <= start {
		return Mapping{}, false
	}
	off, err := strconv.ParseUint(f[2], 16, 64)
	if err != nil {
		return Mapping{}, false
	}
	// The path is the rest of the line: a file name may contain spaces.
	path := strings.Join(f[5:], " ")
	if !strings.HasPrefix(path, "/") {
		// [vdso], [heap] and friends: executable, but no file on disk to read symbols from.
		return Mapping{}, false
	}
	// An upgraded or replaced file keeps its mapping with a suffix. The bytes are still the ones that
	// were loaded, so the mapping is kept and the path is reported as it really is.
	path = strings.TrimSuffix(path, " (deleted)")
	return Mapping{Start: start, End: end, Offset: off, Path: path}, true
}

// Find returns the mapping containing addr. Mappings are few per process, so a scan is cheaper than the
// bookkeeping a sorted search would need on a slice that is rebuilt whenever a process re-execs.
func Find(ms []Mapping, addr uint64) (Mapping, bool) {
	for _, m := range ms {
		if m.Contains(addr) {
			return m, true
		}
	}
	return Mapping{}, false
}
