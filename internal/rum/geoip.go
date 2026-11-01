package rum

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
)

// The visitor's country from an address, for an installation with no CDN to say it (rum.md §3.7).
//
// The operator supplies the database (OPENLOG_RUM_GEOIP_DB), so openlog ships no data file and accepts no licence on
// anyone's behalf. **The address still goes nowhere:** it is looked up in memory while the request is handled and only the
// country is kept, on the span, exactly as if a proxy had declared it. No address reaches a log, a span or a table.

// geoIPCheckEvery is how often the file is looked at. A monthly database is replaced in place, and a restart of ingest
// should not be the price of that.
const geoIPCheckEvery = time.Minute

// GeoIP answers countries from an MMDB file in the GeoLite2-Country shape, which DB-IP's country database shares.
type GeoIP struct {
	path      string
	log       *slog.Logger
	reader    atomic.Pointer[maxminddb.Reader]
	modified  atomic.Int64
	nextCheck atomic.Int64
	now       func() time.Time
}

// OpenGeoIP reads the database. An error says the file cannot be used, for the caller to report.
func OpenGeoIP(path string, log *slog.Logger) (*GeoIP, error) {
	g := &GeoIP{path: path, log: log, now: time.Now}
	if err := g.load(); err != nil {
		return nil, err
	}
	g.nextCheck.Store(g.now().Add(geoIPCheckEvery).UnixNano())
	return g, nil
}

// load reads the whole file rather than mapping it: a reader replaced while a request is using it then needs nothing but
// the garbage collector, and a country database is a few megabytes.
func (g *GeoIP) load() error {
	info, err := os.Stat(g.path)
	if err != nil {
		return fmt.Errorf("geoip: %w", err)
	}
	body, err := os.ReadFile(g.path)
	if err != nil {
		return fmt.Errorf("geoip: %w", err)
	}
	r, err := maxminddb.OpenBytes(body)
	if err != nil {
		return fmt.Errorf("geoip: %s: %w", g.path, err)
	}
	g.reader.Store(r)
	g.modified.Store(info.ModTime().UnixNano())
	return nil
}

// reload looks for a replaced file, at most once a minute and off the request's path. A file that cannot be read
// leaves the one in use: it may only be half copied, and a stale country beats none.
func (g *GeoIP) reload() {
	now := g.now().UnixNano()
	next := g.nextCheck.Load()
	if now < next || !g.nextCheck.CompareAndSwap(next, now+int64(geoIPCheckEvery)) {
		return
	}
	go func() {
		info, err := os.Stat(g.path)
		if err != nil || info.ModTime().UnixNano() == g.modified.Load() {
			return
		}
		if err := g.load(); err != nil {
			if g.log != nil {
				g.log.Warn("geoip database not replaced", "err", err)
			}
			return
		}
		if g.log != nil {
			g.log.Info("geoip database replaced", "path", g.path)
		}
	}()
}

// Country returns the ISO 3166-1 alpha-2 code of an address, or "" when the database has no answer.
func (g *GeoIP) Country(addr netip.Addr) string {
	if g == nil || !addr.IsValid() {
		return ""
	}
	g.reload()
	var rec struct {
		Country struct {
			ISOCode string `maxminddb:"iso_code"`
		} `maxminddb:"country"`
	}
	if err := g.reader.Load().Lookup(addr.Unmap()).Decode(&rec); err != nil {
		return ""
	}
	return ParseCountry(rec.Country.ISOCode)
}

// ParseTrustedProxies reads OPENLOG_RUM_TRUSTED_PROXIES: comma-separated CIDR ranges or single addresses.
func ParseTrustedProxies(csv string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if p, err := netip.ParsePrefix(part); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil {
			return nil, errors.New("trusted proxies: " + part + " is neither an address nor a CIDR range")
		}
		out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
	}
	return out, nil
}

// ClientAddr finds the address a request came from. X-Forwarded-For is anyone's to write, so it counts only as far as
// proxies the operator named vouch for it: walking from the right, the first address that is not one of them is the
// client, and what stands to the left of it may be invented. With no proxies named the header is never read.
func ClientAddr(remoteAddr string, forwardedFor []string, trusted []netip.Prefix) netip.Addr {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	addr = addr.Unmap()
	if !containsAddr(trusted, addr) {
		return addr
	}
	hops := strings.Split(strings.Join(forwardedFor, ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return netip.Addr{} // a malformed hop means nothing further left can be believed
		}
		hop = hop.Unmap()
		addr = hop
		if !containsAddr(trusted, hop) {
			return hop
		}
	}
	return addr
}

func containsAddr(prefixes []netip.Prefix, a netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
