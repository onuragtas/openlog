// Turning a sampled address into a frame: which file, where in it, and what it is called.
//
// Untagged and seam-driven on purpose. Reading /proc and opening files are injected, so every branch —
// unknown module, stripped binary, real name — is exercised here rather than only on a host that can run
// BPF.
package symbol

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/onuragtas/openlog/agents/ebpf/internal/otlpprofiles"
)

// MaxSymbolBytes is what all cached symbol tables together may cost. The cache used to be unbounded, and
// on a host running many distinct binaries — every container image brings its own — it grew past the
// unit's MemoryMax within one interval and the profiler was killed by the OOM killer while it was
// working correctly. Past the budget, files are named "<binary>+0x<offset>" instead (contract §7):
// fewer names, and a profiler that stays alive to produce them.
var MaxSymbolBytes = 64 << 20

// Resolver names addresses, caching what it learns. A profiler asks it tens of thousands of times per
// interval, so re-reading /proc/<pid>/maps or re-parsing a binary for every sample is not an option.
type Resolver struct {
	readMaps  func(pid int) ([]Mapping, error)
	loadTable func(path string) (*Table, error)

	maps   map[int][]Mapping
	tables map[string]*Table
	// used is what the cached tables cost so far. Counting up rather than down keeps the zero value
	// usable: a Resolver built as a struct literal gets the whole budget, not none of it.
	used int
}

// Used is what the cached symbol tables cost, for tests and diagnostics.
func (r *Resolver) Used() int { return r.used }

// NewResolver reads the real /proc and the real files, under hostRoot when the profiler runs in a
// container with the host mounted.
func NewResolver(hostRoot string) *Resolver {
	return &Resolver{
		readMaps: func(pid int) ([]Mapping, error) {
			f, err := os.Open(filepath.Join(hostRoot, fmt.Sprintf("/proc/%d/maps", pid)))
			if err != nil {
				return nil, err
			}
			defer f.Close()
			return ParseMaps(f)
		},
		loadTable: func(path string) (*Table, error) { return LoadTable(filepath.Join(hostRoot, path)) },
		maps:      map[int][]Mapping{},
		tables:    map[string]*Table{},
	}
}

// Resolve names one address.
//
// Three answers, in order of how much is known. A name when the symbol is there; `<binary>+0x<offset>`
// when the file is known but stripped, because which binary burned the CPU is still an answer; and the
// bare address when even the mapping is gone — a process that exited between the sample and the lookup.
// Nothing is invented at any step.
func (r *Resolver) Resolve(pid int, addr uint64) otlpprofiles.Frame {
	ms, ok := r.maps[pid]
	if !ok {
		ms, _ = r.readMaps(pid)
		// Cached even when empty: a vanished process must not be re-read once per sample.
		r.maps[pid] = ms
	}
	m, found := Find(ms, addr)
	if !found {
		return otlpprofiles.Frame{Function: fmt.Sprintf("0x%x", addr), Address: addr}
	}

	off := m.FileOffset(addr)
	tab, cached := r.tables[m.Path]
	if !cached {
		tab, _ = r.loadTable(m.Path)
		if tab == nil {
			tab = &Table{} // a file that could not be read is remembered as having no symbols
		}
		if r.used+tab.Bytes() > MaxSymbolBytes {
			// Out of budget: remember the file as having no symbols rather than holding the table. It is
			// remembered so the next sample in the same file does not parse it again.
			tab = &Table{}
		}
		r.used += tab.Bytes()
		r.tables[m.Path] = tab
	}
	if !tab.Empty() {
		if vaddr, ok := tab.VaddrFor(off); ok {
			if name, ok := tab.Lookup(vaddr); ok {
				return otlpprofiles.Frame{Function: name, Address: addr}
			}
		}
	}
	return otlpprofiles.Frame{Function: fmt.Sprintf("%s+0x%x", filepath.Base(m.Path), off), Address: addr}
}

// Forget drops what was learned about a process. Pids are reused, and a cache that outlived the process
// would name a new program's addresses after the old one's symbols.
func (r *Resolver) Forget(pid int) { delete(r.maps, pid) }
