package rpmdb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// testdata/rpmdb.sqlite was generated with Python's sqlite3 module: a
// Packages table in WAL mode (page size 4096) with synthetic rpm headers,
// several of them larger than a page (overflow chains). The last insert
// ("redis") exists only in rpmdb.sqlite-wal.
func TestReadSQLiteWithWAL(t *testing.T) {
	pkgs, err := ReadSQLite("testdata/rpmdb.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 64 {
		t.Fatalf("packages = %d, want 64 (gpg-pubkey skipped)", len(pkgs))
	}
	want := []Package{
		{"bash", "5.1.8-9.el9", "x86_64"},
		{"openssl", "1:3.0.7-27.el9", "x86_64"},
		{"kernel-core", "5.14.0-427.13.1.el9_4", "x86_64"},
	}
	if !reflect.DeepEqual(pkgs[:3], want) {
		t.Errorf("first packages = %+v", pkgs[:3])
	}
	if last := pkgs[len(pkgs)-1]; last != (Package{"redis", "6.2.7-1.el9", "aarch64"}) {
		t.Errorf("WAL package = %+v", last)
	}

	// Without the WAL file the uncheckpointed transaction is invisible.
	dir := t.TempDir()
	b, _ := os.ReadFile("testdata/rpmdb.sqlite")
	if err := os.WriteFile(filepath.Join(dir, "rpmdb.sqlite"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	pkgs, err = ReadSQLite(filepath.Join(dir, "rpmdb.sqlite"))
	if err != nil || len(pkgs) != 63 {
		t.Errorf("without wal: %d %v", len(pkgs), err)
	}
}

func TestCorruptInput(t *testing.T) {
	if _, err := parseSQLite([]byte("not a database"), nil); err == nil {
		t.Error("expected error")
	}
	b, _ := os.ReadFile("testdata/rpmdb.sqlite")
	broken := append([]byte(nil), b[:4096*2]...) // truncated file
	db, err := parseSQLite(broken, nil)
	if err == nil {
		if _, err = readPackages(db); err == nil {
			t.Error("truncated database must fail, not loop or panic")
		}
	}
	if _, err := ParseHeader([]byte{0, 0, 0, 9, 0, 0, 0, 1}); err == nil {
		t.Error("bad header sizes")
	}
}

func TestParseHeaderI18N(t *testing.T) {
	// NAME as I18NSTRING, no release, epoch 0 is not printed.
	store := []byte("curl\x00" + "7.76.1\x00\x00\x00\x00\x00\x00\x00\x00")
	idx := func(tag, typ, off uint32) []byte {
		e := make([]byte, 16)
		binary.BigEndian.PutUint32(e[0:], tag)
		binary.BigEndian.PutUint32(e[4:], typ)
		binary.BigEndian.PutUint32(e[8:], off)
		binary.BigEndian.PutUint32(e[12:], 1)
		return e
	}
	blob := binary.BigEndian.AppendUint32(nil, 3)
	blob = binary.BigEndian.AppendUint32(blob, uint32(len(store)))
	blob = append(blob, idx(tagName, typeI18NString, 0)...)
	blob = append(blob, idx(tagVersion, typeString, 5)...)
	blob = append(blob, idx(tagEpoch, typeInt32, 16)...)
	blob = append(blob, store...)
	p, err := ParseHeader(blob)
	if err != nil || p != (Package{Name: "curl", Version: "7.76.1"}) {
		t.Errorf("header = %+v %v", p, err)
	}
}

func TestParseQueryOutput(t *testing.T) {
	out := "bash\t5.1.8-9.el9\tx86_64\ngpg-pubkey\t8483c65d-5ccc5b19\t(none)\nopenssl\t1:3.0.7-27.el9\tx86_64\nbroken line\n"
	got := ParseQueryOutput([]byte(out))
	want := []Package{{"bash", "5.1.8-9.el9", "x86_64"}, {"openssl", "1:3.0.7-27.el9", "x86_64"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
}
