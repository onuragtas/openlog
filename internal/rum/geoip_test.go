package rum

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testdata/country-test.mmdb is generated with mmdbwriter and holds invented answers on documentation ranges:
// 198.51.100.0/24 is TR, 203.0.113.0/24 is DE and 2001:db8::/32 is GB.
const geoIPFixture = "testdata/country-test.mmdb"

func TestCountry(t *testing.T) {
	g, err := OpenGeoIP(geoIPFixture, nil)
	if err != nil {
		t.Fatal(err)
	}
	for addr, want := range map[string]string{
		"198.51.100.7":       "TR",
		"::ffff:203.0.113.9": "DE",
		"2001:db8::1":        "GB",
		"192.0.2.1":          "",
		"10.0.0.1":           "",
	} {
		if got := g.Country(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: %q, want %q", addr, got, want)
		}
	}
	if got := g.Country(netip.Addr{}); got != "" {
		t.Errorf("no address: %q", got)
	}
	var none *GeoIP
	if got := none.Country(netip.MustParseAddr("198.51.100.7")); got != "" {
		t.Errorf("no database: %q", got)
	}
}

func TestOpenRefusesWhatIsNotADatabase(t *testing.T) {
	if _, err := OpenGeoIP("testdata/missing.mmdb", nil); err == nil {
		t.Error("a missing file was accepted")
	}
	p := filepath.Join(t.TempDir(), "bad.mmdb")
	if err := os.WriteFile(p, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenGeoIP(p, nil); err == nil {
		t.Error("a corrupt file was accepted")
	}
}

func TestAReplacedFileIsPickedUpAndABrokenOneIsNot(t *testing.T) {
	body, err := os.ReadFile(geoIPFixture)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "c.mmdb")
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := OpenGeoIP(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	first := g.reader.Load()
	clock := time.Now()
	g.now = func() time.Time { return clock }
	wait := func(cond func() bool) bool {
		for i := 0; i < 200; i++ {
			if cond() {
				return true
			}
			time.Sleep(5 * time.Millisecond)
		}
		return false
	}
	addr := netip.MustParseAddr("198.51.100.7")

	// Broken first: the one in use stays, because the file may only be half copied.
	if err := os.WriteFile(p, []byte("half a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, clock, clock.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(2 * geoIPCheckEvery)
	g.Country(addr)
	time.Sleep(50 * time.Millisecond)
	if g.reader.Load() != first || g.Country(addr) != "TR" {
		t.Error("a broken file replaced the database in use")
	}

	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, clock, clock.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(2 * geoIPCheckEvery)
	g.Country(addr)
	if !wait(func() bool { return g.reader.Load() != first }) {
		t.Error("a replaced file was not picked up")
	}
}

func TestClientAddr(t *testing.T) {
	trusted, err := ParseTrustedProxies("10.0.0.0/8, 172.16.0.1")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, remote string
		xff          []string
		trusted      []netip.Prefix
		want         string
	}{
		{"no proxies named: the header is not read", "198.51.100.7:1234", []string{"203.0.113.9"}, nil, "198.51.100.7"},
		{"not from a proxy: the header is a claim", "198.51.100.7:1234", []string{"203.0.113.9"}, trusted, "198.51.100.7"},
		{"through a proxy", "10.1.1.1:1234", []string{"203.0.113.9"}, trusted, "203.0.113.9"},
		{"a forged hop to the left is ignored", "10.1.1.1:1234", []string{"8.8.8.8, 203.0.113.9"}, trusted, "203.0.113.9"},
		{"through two proxies", "10.1.1.1:1234", []string{"203.0.113.9, 172.16.0.1"}, trusted, "203.0.113.9"},
		{"split over two headers", "10.1.1.1:1234", []string{"203.0.113.9", "10.2.2.2"}, trusted, "203.0.113.9"},
		{"mapped addresses", "[::ffff:10.1.1.1]:1234", []string{"::ffff:203.0.113.9"}, trusted, "203.0.113.9"},
		{"ipv6", "[2001:db8::1]:1234", nil, nil, "2001:db8::1"},
	} {
		if got := ClientAddr(tc.remote, tc.xff, tc.trusted); got != netip.MustParseAddr(tc.want) {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
	if got := ClientAddr("10.1.1.1:1", []string{"not an address"}, trusted); got.IsValid() {
		t.Errorf("a malformed hop was believed: %s", got)
	}
	if got := ClientAddr("nonsense", nil, nil); got.IsValid() {
		t.Errorf("%s", got)
	}
	if _, err := ParseTrustedProxies("10.0.0.0/8,nonsense"); err == nil {
		t.Error("a bad range was accepted")
	}
}
