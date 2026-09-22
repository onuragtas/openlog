// The second half of symbolication: the symbol table of the file a mapping points at.
//
// No build tag — debug/elf is a parser, so this is tested here rather than only on a machine that can run
// BPF. The awkward part is that /proc/<pid>/maps gives a **file offset** while ELF symbols carry **virtual
// addresses**, and the two are only related through the PT_LOAD program headers. Skipping that conversion
// does not fail: it names every frame after whichever symbol sits at the wrong address.
package symbol

import (
	"debug/elf"
	"fmt"
	"sort"
)

// Table is one file's symbols, sorted by address so a lookup is a binary search.
type Table struct {
	addrs []uint64
	sizes []uint64
	names []string
	// loads are the PT_LOAD segments, kept to convert a file offset into a virtual address.
	loads []load
}

type load struct{ off, vaddr, filesz uint64 }

// LoadTable reads the symbols of an on-disk file.
func LoadTable(path string) (*Table, error) {
	f, err := elf.Open(path)
	if err != nil {
		return nil, fmt.Errorf("symbol: open %s: %w", path, err)
	}
	defer f.Close()
	return NewTable(f)
}

// NewTable builds a table from an already-parsed file.
//
// Both .symtab and .dynsym are read. A stripped binary has only the dynamic table, which holds far fewer
// names but still enough to tell one shared library's exported function from another.
func NewTable(f *elf.File) (*Table, error) {
	t := &Table{}
	for _, p := range f.Progs {
		if p.Type == elf.PT_LOAD {
			t.loads = append(t.loads, load{off: p.Off, vaddr: p.Vaddr, filesz: p.Filesz})
		}
	}

	type entry struct {
		addr, size uint64
		name       string
	}
	var all []entry
	add := func(syms []elf.Symbol) {
		for _, s := range syms {
			// Only functions with a real address are useful here; a data symbol never appears in a stack.
			if s.Value == 0 || s.Name == "" || elf.ST_TYPE(s.Info) != elf.STT_FUNC {
				continue
			}
			all = append(all, entry{addr: s.Value, size: s.Size, name: s.Name})
		}
	}
	syms, err := f.Symbols()
	if err == nil {
		add(syms)
	}
	dyn, err := f.DynamicSymbols()
	if err == nil {
		add(dyn)
	}
	if len(all) == 0 {
		// Not an error: a stripped binary is normal, and the caller falls back to the address.
		return t, nil
	}
	sort.Slice(all, func(i, j int) bool { return all[i].addr < all[j].addr })
	for _, e := range all {
		t.addrs = append(t.addrs, e.addr)
		t.sizes = append(t.sizes, e.size)
		t.names = append(t.names, e.name)
	}
	return t, nil
}

// VaddrFor converts a file offset into the virtual address the symbols are expressed in.
//
// This is the conversion that makes every other number mean something. A file offset is only inside one
// PT_LOAD segment, and the shift between the two is that segment's own.
func (t *Table) VaddrFor(fileOffset uint64) (uint64, bool) {
	for _, l := range t.loads {
		if fileOffset >= l.off && fileOffset < l.off+l.filesz {
			return fileOffset - l.off + l.vaddr, true
		}
	}
	return 0, false
}

// Lookup names the function containing a virtual address.
//
// A symbol with a size covers [addr, addr+size); one without a size — common in hand-written assembly —
// is credited with everything up to the next symbol, which is the best available answer and is why the
// table is kept sorted.
func (t *Table) Lookup(vaddr uint64) (string, bool) {
	if len(t.addrs) == 0 {
		return "", false
	}
	i := sort.Search(len(t.addrs), func(i int) bool { return t.addrs[i] > vaddr })
	if i == 0 {
		return "", false // below the first symbol
	}
	i--
	if s := t.sizes[i]; s > 0 && vaddr >= t.addrs[i]+s {
		return "", false // in the gap after a sized symbol
	}
	return t.names[i], true
}

// Empty reports whether the file carried no usable symbols.
func (t *Table) Empty() bool { return len(t.addrs) == 0 }
