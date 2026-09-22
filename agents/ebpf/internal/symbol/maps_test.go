package symbol

import (
	"strings"
	"testing"
)

const sample = `` +
	"55a4c0e00000-55a4c0e21000 r--p 00000000 fd:01 1234  /usr/bin/redis-server\n" +
	"55a4c0e21000-55a4c0f50000 r-xp 00021000 fd:01 1234  /usr/bin/redis-server\n" +
	"55a4c0f50000-55a4c0f90000 r--p 00150000 fd:01 1234  /usr/bin/redis-server\n" +
	"7f2b1c000000-7f2b1c028000 r-xp 00000000 fd:01 5678  /usr/lib/x86_64-linux-gnu/libc.so.6\n" +
	"7ffd4e7e1000-7ffd4e802000 rw-p 00000000 00:00 0     [stack]\n" +
	"7ffd4e8f0000-7ffd4e8f2000 r-xp 00000000 00:00 0     [vdso]\n" +
	"55a4c1000000-55a4c1010000 rw-p 00000000 00:00 0 \n"

func parse(t *testing.T) []Mapping {
	t.Helper()
	ms, err := ParseMaps(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

// Only executable, file-backed regions can hold a sampled instruction pointer and have symbols to read.
func TestParseKeepsOnlyExecutableFileMappings(t *testing.T) {
	ms := parse(t)
	if len(ms) != 2 {
		t.Fatalf("kept %d mappings, want 2: %+v", len(ms), ms)
	}
	if ms[0].Path != "/usr/bin/redis-server" || ms[1].Path != "/usr/lib/x86_64-linux-gnu/libc.so.6" {
		t.Errorf("kept the wrong mappings: %+v", ms)
	}
	// The read-only segments of the same file are not executable and must not be kept: an address can
	// never land in them, and keeping them would shadow the executable one in a scan.
	for _, m := range ms {
		if m.Offset == 0 && m.Path == "/usr/bin/redis-server" {
			t.Error("a non-executable segment of redis-server was kept")
		}
	}
}

// This is the arithmetic that decides every name. Off by a page and the profile is plausible and wrong.
func TestFileOffsetMapsAddressIntoTheFile(t *testing.T) {
	ms := parse(t)
	m := ms[0] // r-xp at 0x55a4c0e21000, file offset 0x21000
	if got := m.FileOffset(0x55a4c0e21000); got != 0x21000 {
		t.Errorf("start of the mapping = %#x, want 0x21000", got)
	}
	if got := m.FileOffset(0x55a4c0e21abc); got != 0x21abc {
		t.Errorf("offset inside the mapping = %#x, want 0x21abc", got)
	}
}

// End is exclusive, the way /proc prints it: the first address of the next mapping is not in this one.
func TestContainsTreatsEndAsExclusive(t *testing.T) {
	m := Mapping{Start: 0x1000, End: 0x2000}
	if !m.Contains(0x1000) {
		t.Error("the first address is not contained")
	}
	if !m.Contains(0x1fff) {
		t.Error("the last address is not contained")
	}
	if m.Contains(0x2000) {
		t.Error("the end address is contained, but it belongs to the next mapping")
	}
	if m.Contains(0xfff) {
		t.Error("an address below the start is contained")
	}
}

func TestFindPicksTheRightModule(t *testing.T) {
	ms := parse(t)
	m, ok := Find(ms, 0x7f2b1c000100)
	if !ok || m.Path != "/usr/lib/x86_64-linux-gnu/libc.so.6" {
		t.Errorf("found %+v (%v), want libc", m, ok)
	}
	if _, ok := Find(ms, 0x10); ok {
		t.Error("an address in no mapping was matched")
	}
}

// An upgraded package leaves its mapping behind with a suffix. The bytes loaded are still those bytes, so
// the mapping is kept — but the path must not carry the suffix into an open() call.
func TestDeletedMappingKeepsACleanPath(t *testing.T) {
	ms, err := ParseMaps(strings.NewReader(
		"400000-401000 r-xp 00000000 fd:01 99  /usr/bin/app (deleted)\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].Path != "/usr/bin/app" {
		t.Errorf("parsed %+v, want one mapping of /usr/bin/app", ms)
	}
}

// A file name may contain spaces, and the path is the rest of the line rather than one field.
func TestPathWithSpacesSurvives(t *testing.T) {
	ms, _ := ParseMaps(strings.NewReader(
		"400000-401000 r-xp 00000000 fd:01 99  /opt/my app/bin/server\n"))
	if len(ms) != 1 || ms[0].Path != "/opt/my app/bin/server" {
		t.Errorf("parsed %+v, want the full path", ms)
	}
}

func TestMalformedLinesAreSkipped(t *testing.T) {
	ms, err := ParseMaps(strings.NewReader("" +
		"garbage\n" +
		"zzzz-yyyy r-xp 00000000 fd:01 1 /usr/bin/x\n" +
		"400000 r-xp 00000000 fd:01 1 /usr/bin/y\n" +
		"402000-401000 r-xp 00000000 fd:01 1 /usr/bin/backwards\n" +
		"500000-501000 r-xp 00000000 fd:01 1 /usr/bin/good\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].Path != "/usr/bin/good" {
		t.Errorf("parsed %+v, want only the well-formed line", ms)
	}
}
