package symbol

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"testing"
)

// buildELF lays out a minimal but genuinely valid ELF64: one PT_LOAD segment and a symbol table.
//
// Built in memory rather than committed as a fixture (this repository carries no binary test data) or
// produced by invoking the toolchain from a test (slow, and fragile in a sandbox). debug/elf is a strict
// parser, so if it accepts these bytes the code under test is exercised for real.
func buildELF(t *testing.T, syms []testSym, loadOff, loadVaddr, loadSize uint64) *elf.File {
	t.Helper()
	const (
		ehSize = 64
		phSize = 56
		shSize = 64
		symSz  = 24
	)
	// Section name table: \0 .shstrtab \0 .strtab \0 .symtab \0
	shstr := []byte("\x00.shstrtab\x00.strtab\x00.symtab\x00")
	offShstrtab := 1
	offStrtab := 1 + len(".shstrtab") + 1
	offSymtab := offStrtab + len(".strtab") + 1

	// Symbol name table and the symbol entries (index 0 is the reserved null symbol).
	var strtab bytes.Buffer
	strtab.WriteByte(0)
	symData := make([]byte, symSz) // null symbol
	for _, s := range syms {
		nameOff := strtab.Len()
		strtab.WriteString(s.Name)
		strtab.WriteByte(0)
		e := make([]byte, symSz)
		binary.LittleEndian.PutUint32(e[0:], uint32(nameOff))
		e[4] = byte(elf.ST_INFO(elf.STB_GLOBAL, s.Type))
		binary.LittleEndian.PutUint16(e[6:], 1) // a defined section
		binary.LittleEndian.PutUint64(e[8:], s.Value)
		binary.LittleEndian.PutUint64(e[16:], s.Size)
		symData = append(symData, e...)
	}

	phOff := uint64(ehSize)
	symOff := phOff + phSize
	strOff := symOff + uint64(len(symData))
	shstrOff := strOff + uint64(strtab.Len())
	shOff := shstrOff + uint64(len(shstr))

	buf := &bytes.Buffer{}
	// ---- ELF header ----
	hdr := make([]byte, ehSize)
	copy(hdr, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0}) // ELF64, little endian, current version
	binary.LittleEndian.PutUint16(hdr[16:], uint16(elf.ET_EXEC))
	binary.LittleEndian.PutUint16(hdr[18:], uint16(elf.EM_X86_64))
	binary.LittleEndian.PutUint32(hdr[20:], 1)
	binary.LittleEndian.PutUint64(hdr[32:], phOff)
	binary.LittleEndian.PutUint64(hdr[40:], shOff)
	binary.LittleEndian.PutUint16(hdr[52:], ehSize)
	binary.LittleEndian.PutUint16(hdr[54:], phSize)
	binary.LittleEndian.PutUint16(hdr[56:], 1) // one program header
	binary.LittleEndian.PutUint16(hdr[58:], shSize)
	binary.LittleEndian.PutUint16(hdr[60:], 4) // null, .symtab, .strtab, .shstrtab
	binary.LittleEndian.PutUint16(hdr[62:], 3) // .shstrtab is section 3
	buf.Write(hdr)

	// ---- program header: one PT_LOAD ----
	ph := make([]byte, phSize)
	binary.LittleEndian.PutUint32(ph[0:], uint32(elf.PT_LOAD))
	binary.LittleEndian.PutUint32(ph[4:], uint32(elf.PF_X|elf.PF_R))
	binary.LittleEndian.PutUint64(ph[8:], loadOff)
	binary.LittleEndian.PutUint64(ph[16:], loadVaddr)
	binary.LittleEndian.PutUint64(ph[24:], loadVaddr)
	binary.LittleEndian.PutUint64(ph[32:], loadSize)
	binary.LittleEndian.PutUint64(ph[40:], loadSize)
	binary.LittleEndian.PutUint64(ph[48:], 0x1000)
	buf.Write(ph)

	buf.Write(symData)
	buf.Write(strtab.Bytes())
	buf.Write(shstr)

	// ---- section headers ----
	sh := func(nameOff, typ uint32, off, size uint64, link, info uint32, entsize uint64) []byte {
		b := make([]byte, shSize)
		binary.LittleEndian.PutUint32(b[0:], nameOff)
		binary.LittleEndian.PutUint32(b[4:], typ)
		binary.LittleEndian.PutUint64(b[24:], off)
		binary.LittleEndian.PutUint64(b[32:], size)
		binary.LittleEndian.PutUint32(b[40:], link)
		binary.LittleEndian.PutUint32(b[44:], info)
		binary.LittleEndian.PutUint64(b[48:], 1)
		binary.LittleEndian.PutUint64(b[56:], entsize)
		return b
	}
	buf.Write(make([]byte, shSize)) // [0] null
	buf.Write(sh(uint32(offSymtab), uint32(elf.SHT_SYMTAB), symOff, uint64(len(symData)), 2, 1, symSz))
	buf.Write(sh(uint32(offStrtab), uint32(elf.SHT_STRTAB), strOff, uint64(strtab.Len()), 0, 0, 0))
	buf.Write(sh(uint32(offShstrtab), uint32(elf.SHT_STRTAB), shstrOff, uint64(len(shstr)), 0, 0, 0))

	f, err := elf.NewFile(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("the synthetic ELF is not valid: %v", err)
	}
	return f
}

// testSym describes one symbol for the synthetic file, including its type: a data symbol has to be
// representable, or the filter that drops it could not be tested at all.
type testSym struct {
	Name  string
	Value uint64
	Size  uint64
	Type  elf.SymType
}

func table(t *testing.T) *Table {
	t.Helper()
	f := buildELF(t, []testSym{
		{Name: "main.work", Value: 0x401000, Size: 0x40, Type: elf.STT_FUNC},
		{Name: "main.idle", Value: 0x401100, Size: 0, Type: elf.STT_FUNC},
		{Name: "main.main", Value: 0x401200, Size: 0x20, Type: elf.STT_FUNC},
	}, 0x1000, 0x401000, 0x1000)
	tab, err := NewTable(f)
	if err != nil {
		t.Fatal(err)
	}
	return tab
}

func TestLookupNamesTheContainingFunction(t *testing.T) {
	tab := table(t)
	for addr, want := range map[uint64]string{
		0x401000: "main.work",
		0x401020: "main.work",
		0x401200: "main.main",
		0x40121f: "main.main",
	} {
		got, ok := tab.Lookup(addr)
		if !ok || got != want {
			t.Errorf("Lookup(%#x) = %q,%v, want %q", addr, got, ok, want)
		}
	}
}

// A sized symbol does not own the bytes after it. Crediting them to it would attribute another function's
// time to whichever symbol happened to come before the gap.
func TestLookupRefusesTheGapAfterASizedSymbol(t *testing.T) {
	tab := table(t)
	if got, ok := tab.Lookup(0x401050); ok {
		t.Errorf("Lookup in the gap returned %q, want no name", got)
	}
}

// Hand-written assembly often has no size. Such a symbol is credited up to the next one, which is the
// best answer available and the reason the table is sorted.
func TestUnsizedSymbolRunsToTheNextOne(t *testing.T) {
	tab := table(t)
	if got, ok := tab.Lookup(0x4011ff); !ok || got != "main.idle" {
		t.Errorf("Lookup(0x4011ff) = %q,%v, want main.idle", got, ok)
	}
}

func TestLookupBelowTheFirstSymbol(t *testing.T) {
	if _, ok := table(t).Lookup(0x400000); ok {
		t.Error("an address below every symbol was named")
	}
}

// The conversion that makes every other number mean something: /proc gives a file offset, symbols carry
// virtual addresses, and only PT_LOAD relates them.
func TestVaddrForUsesTheLoadSegment(t *testing.T) {
	tab := table(t)
	got, ok := tab.VaddrFor(0x1000)
	if !ok || got != 0x401000 {
		t.Errorf("VaddrFor(0x1000) = %#x,%v, want 0x401000", got, ok)
	}
	got, ok = tab.VaddrFor(0x1234)
	if !ok || got != 0x401234 {
		t.Errorf("VaddrFor(0x1234) = %#x,%v, want 0x401234", got, ok)
	}
	if _, ok := tab.VaddrFor(0x9999); ok {
		t.Error("an offset outside every PT_LOAD was converted")
	}
}

// A stripped binary is normal, not an error: the caller falls back to reporting the address.
func TestStrippedFileYieldsAnEmptyTable(t *testing.T) {
	f := buildELF(t, nil, 0x1000, 0x401000, 0x1000)
	tab, err := NewTable(f)
	if err != nil {
		t.Fatalf("a stripped file was an error: %v", err)
	}
	if !tab.Empty() {
		t.Error("a file with no symbols produced a non-empty table")
	}
	if _, ok := tab.Lookup(0x401000); ok {
		t.Error("an empty table named an address")
	}
}

// A data symbol never appears in a call stack, and one sitting between two functions would otherwise
// swallow every address after it — naming CPU time after a variable.
func TestDataSymbolsAreNotUsedForNames(t *testing.T) {
	f := buildELF(t, []testSym{
		{Name: "main.work", Value: 0x401000, Size: 0x40, Type: elf.STT_FUNC},
		{Name: "runtime.buffer", Value: 0x401050, Size: 0x100, Type: elf.STT_OBJECT},
		{Name: "main.main", Value: 0x401200, Size: 0x20, Type: elf.STT_FUNC},
	}, 0x1000, 0x401000, 0x1000)
	tab, err := NewTable(f)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := tab.Lookup(0x401060); ok {
		t.Errorf("an address inside a data symbol was named %q", got)
	}
	if got, ok := tab.Lookup(0x401200); !ok || got != "main.main" {
		t.Errorf("Lookup(0x401200) = %q,%v, want main.main", got, ok)
	}
}
