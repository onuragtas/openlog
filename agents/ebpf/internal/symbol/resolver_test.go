package symbol

import (
	"debug/elf"
	"errors"
	"strings"
	"testing"
)

func fakeResolver(t *testing.T, maps string, tab *Table, loadErr error) (*Resolver, *int) {
	t.Helper()
	parsed, err := ParseMaps(strings.NewReader(maps))
	if err != nil {
		t.Fatal(err)
	}
	loads := 0
	return &Resolver{
		readMaps:  func(int) ([]Mapping, error) { return parsed, nil },
		loadTable: func(string) (*Table, error) { loads++; return tab, loadErr },
		maps:      map[int][]Mapping{},
		tables:    map[string]*Table{},
	}, &loads
}

const oneMapping = "400000-401000 r-xp 00001000 fd:01 9 /usr/bin/app\n"

func symbolTable(t *testing.T) *Table {
	t.Helper()
	f := buildELF(t, []testSym{{Name: "main.work", Value: 0x401000, Size: 0x40, Type: elf.STT_FUNC}},
		0x1000, 0x401000, 0x1000)
	tab, err := NewTable(f)
	if err != nil {
		t.Fatal(err)
	}
	return tab
}

func TestResolveNamesAKnownSymbol(t *testing.T) {
	r, _ := fakeResolver(t, oneMapping, symbolTable(t), nil)
	// 0x400000 maps to file offset 0x1000, which PT_LOAD puts at vaddr 0x401000 — main.work.
	if got := r.Resolve(7, 0x400000); got.Function != "main.work" {
		t.Errorf("frame = %q, want main.work", got.Function)
	}
}

// A stripped binary still answers the question that matters most: which binary burned the CPU.
func TestStrippedBinaryFallsBackToTheFile(t *testing.T) {
	r, _ := fakeResolver(t, oneMapping, &Table{}, nil)
	got := r.Resolve(7, 0x400010)
	if got.Function != "app+0x1010" {
		t.Errorf("frame = %q, want app+0x1010", got.Function)
	}
}

// A file that cannot be opened is not a crash and not a silent drop.
func TestUnreadableFileStillNamesTheBinary(t *testing.T) {
	r, _ := fakeResolver(t, oneMapping, nil, errors.New("permission denied"))
	if got := r.Resolve(7, 0x400010); got.Function != "app+0x1010" {
		t.Errorf("frame = %q, want app+0x1010", got.Function)
	}
}

// A process that exited between the sample and the lookup has no mapping left. The address is still
// reported rather than the sample being thrown away.
func TestUnknownMappingReportsTheAddress(t *testing.T) {
	r, _ := fakeResolver(t, oneMapping, symbolTable(t), nil)
	if got := r.Resolve(7, 0x900000); got.Function != "0x900000" {
		t.Errorf("frame = %q, want 0x900000", got.Function)
	}
}

// Tens of thousands of lookups per interval: parsing the binary once per file is the difference between
// a profiler and a load generator.
func TestTableIsParsedOncePerFile(t *testing.T) {
	r, loads := fakeResolver(t, oneMapping, symbolTable(t), nil)
	for i := 0; i < 50; i++ {
		r.Resolve(7, 0x400000+uint64(i))
	}
	if *loads != 1 {
		t.Errorf("parsed the file %d times, want 1", *loads)
	}
}

// Pids are reused. A cache that outlived the process would name a new program's addresses after the old
// program's symbols — wrong, and wrong in a way that looks entirely plausible.
func TestForgetDropsAProcess(t *testing.T) {
	r, _ := fakeResolver(t, oneMapping, symbolTable(t), nil)
	r.Resolve(7, 0x400000)
	if _, ok := r.maps[7]; !ok {
		t.Fatal("the process was not cached")
	}
	r.Forget(7)
	if _, ok := r.maps[7]; ok {
		t.Error("the process is still cached after Forget")
	}
}

// The address travels on every frame, named or not: it is what a later, better symbolizer would need.
func TestAddressIsAlwaysCarried(t *testing.T) {
	r, _ := fakeResolver(t, oneMapping, symbolTable(t), nil)
	for _, addr := range []uint64{0x400000, 0x900000} {
		if got := r.Resolve(7, addr); got.Address != addr {
			t.Errorf("frame for %#x carries address %#x", addr, got.Address)
		}
	}
}
