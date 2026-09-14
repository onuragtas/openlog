package plist

import (
	"bytes"
	"encoding/binary"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func load(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// testdata/receipt.binary.plist is `plutil -convert binary1` of receipt.xml.plist.
func TestDecodeXMLAndBinaryAgree(t *testing.T) {
	want := map[string]any{
		"PackageIdentifier": "org.openlog.infra-agent",
		"PackageVersion":    "1.2.3",
		"InstallDate":       time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC),
		"InstallPrefixPath": "/",
		"Size":              int64(123456789),
		"Ratio":             0.25,
		"Signed":            true,
		"Beta":              false,
		"ProgramArguments":  []any{"/opt/openlog/infra-agent/current/openlog-infra-agent", "-config"},
		"Blob":              []byte("hello"),
		"Unicode":           "Grüße 👋",
	}
	for _, name := range []string{"receipt.xml.plist", "receipt.binary.plist"} {
		m, err := Dict(load(t, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if d, ok := m["InstallDate"].(time.Time); ok {
			m["InstallDate"] = d.UTC()
		}
		if !reflect.DeepEqual(m, want) {
			t.Errorf("%s:\n got %#v\nwant %#v", name, m, want)
		}
		if String(m, "PackageVersion") != "1.2.3" || String(m, "Size") != "" {
			t.Errorf("%s: String helper", name)
		}
	}
}

func TestDecodeRejectsMalformed(t *testing.T) {
	bin := load(t, "receipt.binary.plist")
	cases := map[string][]byte{
		"empty":           nil,
		"short binary":    []byte("bplist00"),
		"truncated":       bin[:len(bin)-10],
		"not a plist":     []byte("hello"),
		"unterminated":    []byte(`<plist><dict><key>a</key><string>b</string>`),
		"bad integer":     []byte(`<plist><integer>x</integer></plist>`),
		"top-level array": []byte(`<plist><array><string>a</string></array></plist>`),
	}
	for name, data := range cases {
		if _, err := Dict(data); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// Trailer pointing the top object at an offset table entry beyond the data.
	bad := bytes.Clone(bin)
	binary.BigEndian.PutUint64(bad[len(bad)-16:len(bad)-8], 1<<40)
	if _, err := Decode(bad); err == nil {
		t.Error("offset table beyond the data accepted")
	}

	// A one-object array that contains itself must not recurse forever.
	cyclic := []byte("bplist00")
	cyclic = append(cyclic, 0xA1, 0x00) // array of 1 element: object 0 (itself)
	table := len(cyclic)
	cyclic = append(cyclic, 8) // offset of object 0
	trailer := make([]byte, 32)
	trailer[6], trailer[7] = 1, 1
	binary.BigEndian.PutUint64(trailer[8:], 1)
	binary.BigEndian.PutUint64(trailer[24:], uint64(table))
	cyclic = append(cyclic, trailer...)
	if _, err := Decode(cyclic); err == nil {
		t.Error("cyclic reference accepted")
	}

	if _, err := Decode([]byte(strings.Repeat(" ", MaxBytes+1))); err == nil {
		t.Error("oversized input accepted")
	}
}
