package symbols

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const mapping = `# compiler: R8
com.example.game.Save -> a.b:
# {"id":"sourceFile","fileName":"Save.kt"}
    int field -> a
    1:5:void load(java.lang.String):10:14 -> c
    6:6:void parse():40:40 -> c
    void plain() -> d
com.example.game.Other$Inner -> a.c:
    1:1:void run():7:7 -> a
`

func TestRetracesAnObfuscatedStack(t *testing.T) {
	m, err := ParseMapping([]byte(mapping))
	if err != nil {
		t.Fatal(err)
	}
	r := Symbolicate("java.lang.IllegalStateException: boom\n\tat a.b.c(SourceFile:3)\n\tat a.b.c(SourceFile:6)\n\tat a.c.a(SourceFile:1)\n\tat android.os.Looper.loop(Looper.java:288)",
		Resolver{Mapping: func() *Mapping { return m }})
	for _, want := range []string{
		"at com.example.game.Save.load(Save.kt:12)",
		"at com.example.game.Save.parse(Save.kt:40)",
		"at com.example.game.Other$Inner.run(Other.java:7)",
		"at android.os.Looper.loop(Looper.java:288)",
	} {
		if !strings.Contains(r.Stack, want) {
			t.Errorf("missing %q in:\n%s", want, r.Stack)
		}
	}
	if r.Frame != "com.example.game.Save.load" || r.Resolved != 3 {
		t.Errorf("frame %q, resolved %d", r.Frame, r.Resolved)
	}
	if m.Class("a.b") != "com.example.game.Save" || m.Class("java.lang.Error") != "java.lang.Error" {
		t.Errorf("class names")
	}
}

func TestSaysWhenAMappingIsMissingOnlyForAnObfuscatedStack(t *testing.T) {
	if r := Symbolicate("\tat a.b.c(SourceFile:3)", Resolver{}); !r.MissingMapping || r.Resolved != 0 {
		t.Errorf("%+v", r)
	}
	if r := Symbolicate("\tat com.example.Save.load(Save.kt:3)\n\tat android.app.ActivityThread.main(Unknown Source:0)", Resolver{}); r.MissingMapping {
		t.Errorf("a readable stack asked for a mapping: %+v", r)
	}
}

func TestRefusesWhatIsNotAMapping(t *testing.T) {
	if _, err := ParseMapping([]byte("not a mapping")); err == nil {
		t.Error("accepted")
	}
}

func table() *Table {
	tb := &Table{files: []string{"a.cpp"}, funcs: []function{{0x1000, 0x1100, "first"}, {0x2000, 0, "noSize"}, {0x3000, 0x3010, "third"}},
		lines: []lineEntry{{0x1000, 0, 10}, {0x1080, 0, 20}}}
	tb.finish()
	return tb
}

func TestLookup(t *testing.T) {
	tb := table()
	if l, ok := tb.Lookup(0x1090); !ok || l.Function != "first" || l.File != "a.cpp" || l.Line != 20 {
		t.Errorf("%+v %v", l, ok)
	}
	if _, ok := tb.Lookup(0x1200); ok {
		t.Error("an address between functions resolved")
	}
	if l, ok := tb.Lookup(0x2fff); !ok || l.Function != "noSize" || l.File != "" {
		t.Errorf("a function of unknown size ends at the next, and has no lines of another: %+v %v", l, ok)
	}
	if _, ok := tb.Lookup(0x10); ok {
		t.Error("an address before everything resolved")
	}
}

func TestTableRoundTrips(t *testing.T) {
	var b bytes.Buffer
	if err := table().Encode(&b); err != nil {
		t.Fatal(err)
	}
	got, err := ParseTable(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if l, ok := got.Lookup(0x1090); !ok || l.Function != "first" || l.Line != 20 {
		t.Errorf("%+v %v", l, ok)
	}
	if got.Functions() != 3 {
		t.Errorf("%d functions", got.Functions())
	}
	if _, err := ParseTable([]byte("something else")); err == nil {
		t.Error("accepted a file that is not a table")
	}
}

func TestSymbolicatesANativeStack(t *testing.T) {
	const id = "a1b2c3d4e5f60718a1b2c3d4e5f60718"
	stack := "#00 libil2cpp.so A1B2C3D4-E5F6-0718-A1B2-C3D4E5F60718 +0x1090\n#01 libc.so ffffffffffffffffffffffffffffffff +0x50 __libc_init\n#02 libil2cpp.so a1b2c3d4e5f60718a1b2c3d4e5f60718 +0x9999"
	r := Symbolicate(stack, Resolver{Image: func(got string) *Table {
		if got == id {
			return table()
		}
		return nil
	}})
	if !strings.Contains(r.Stack, "#00 libil2cpp.so first (a.cpp:20)") {
		t.Errorf("%s", r.Stack)
	}
	if !strings.Contains(r.Stack, "#01 libc.so ffffffffffffffffffffffffffffffff +0x50 __libc_init") {
		t.Errorf("an unresolved frame must stay as the device sent it: %s", r.Stack)
	}
	if r.Frame != "first" || r.Resolved != 1 || len(r.Missing) != 0 {
		t.Errorf("%+v", r)
	}
}

func TestAsksOnlyForSymbolsSomeoneCanUpload(t *testing.T) {
	r := Symbolicate("#00 libc.so ffffffffffffffffffffffffffffffff +0x50 abort\n#01 libsystem_c.dylib eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee +0x10\n#02 libil2cpp.so 0123456789abcdef0123456789abcdef +0x10\n#03 libil2cpp.so 0123456789abcdef0123456789abcdef +0x20", Resolver{})
	if len(r.Missing) != 1 || r.Missing[0] != "libil2cpp.so 0123456789abcdef0123456789abcdef" {
		t.Errorf("%+v", r.Missing)
	}
}

func TestAFrameSymbolsResolvedOutranksOneTheDeviceNamed(t *testing.T) {
	const id = "a1b2c3d4e5f60718a1b2c3d4e5f60718"
	r := Symbolicate("#00 libc.so ffffffffffffffffffffffffffffffff +0x50 abort\n#01 libgame.so "+id+" +0x1090", Resolver{Image: func(got string) *Table {
		if got == id {
			return table()
		}
		return nil
	}})
	if r.Frame != "first" {
		t.Errorf("grouped on %q", r.Frame)
	}
}

func TestDemanglesWhatItBuilds(t *testing.T) {
	tb := &Table{funcs: []function{{0x10, 0x20, "_ZN4Game6UpdateEv"}, {0x20, 0x30, "plain_c"}}}
	tb.finish()
	if tb.funcs[0].name != "Game::Update()" || tb.funcs[1].name != "plain_c" {
		t.Errorf("%+v", tb.funcs)
	}
}

func TestFallsBackToWhatTheDeviceNamedForAGroup(t *testing.T) {
	r := Symbolicate("#00 libgame.so 0123456789abcdef0123456789abcdef +0x10 Game::Update()", Resolver{})
	if r.Frame != "Game::Update()" || r.Resolved != 0 {
		t.Errorf("%+v", r)
	}
}

// Real binaries from a real compiler, because the format is what is being tested: this is what failed to read would look like.
func TestReadsARealDSYM(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("needs clang and dsymutil")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "crash.c")
	if err := os.WriteFile(src, []byte("int target(int a) {\n  return a * 3;\n}\nint main(void) { return target(2); }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "crash")
	// In two steps, because dsymutil reads the debug info from the object file and clang deletes a temporary one.
	obj := bin + ".o"
	if out, err := exec.Command("clang", "-g", "-O0", "-arch", "arm64", "-c", src, "-o", obj).CombinedOutput(); err != nil {
		t.Skipf("clang: %v %s", err, out)
	}
	if out, err := exec.Command("clang", "-arch", "arm64", obj, "-o", bin).CombinedOutput(); err != nil {
		t.Skipf("clang: %v %s", err, out)
	}
	if out, err := exec.Command("dsymutil", bin).CombinedOutput(); err != nil {
		t.Skipf("dsymutil: %v %s", err, out)
	}
	images, err := FromMachO(filepath.Join(dir, "crash.dSYM/Contents/Resources/DWARF/crash"))
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || len(images[0].ID) != 32 {
		t.Fatalf("%+v", images)
	}
	var offset uint64
	for _, f := range images[0].Table.funcs {
		if strings.Contains(f.name, "target") {
			offset = f.start
		}
	}
	if offset == 0 {
		t.Fatalf("no target in %+v", images[0].Table.funcs)
	}
	l, ok := images[0].Table.Lookup(offset + 4)
	if !ok || !strings.Contains(l.Function, "target") || !strings.HasSuffix(l.File, "crash.c") || l.Line < 1 || l.Line > 2 {
		t.Errorf("%+v %v", l, ok)
	}
	// What MetricKit reports is an offset into __TEXT, so the id must match what the binary itself says.
	out, err := exec.Command("dwarfdump", "--uuid", bin).Output()
	if err == nil && !strings.Contains(NormalizeImageID(string(out)), images[0].ID) {
		t.Errorf("uuid %s not in %s", images[0].ID, out)
	}
}
