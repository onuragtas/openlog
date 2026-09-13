package clickhouse

import (
	"strings"
	"testing"
)

// Golden values from ClickHouse 25.8 (SELECT cityHash64(...)). They cover every
// CityHash64 length class (0-16, 17-32, 33-64, > 64 bytes) and the multi-argument
// combination used by the Distributed sharding keys. LowCardinality(String)
// arguments hash identically (verified on the server as well).
func TestCityHash64MatchesClickHouse(t *testing.T) {
	cases := []struct {
		in   []string
		want uint64
	}{
		{[]string{""}, 11160318154034397263},
		{[]string{"a"}, 2603192927274642682},
		{[]string{"tenant-1"}, 4190544471127295606},
		{[]string{"0123456789abcdef0123"}, 8321472320692844101},
		{[]string{"0123456789abcdef0123456789abcdef0123456789"}, 2411686149307240556},
		{[]string{strings.Repeat("xyz", 40)}, 2991624373005869627},
		{[]string{"t1", "host-1"}, 15792450785286982464},
		{[]string{"default", "4bf92f3577b34da6a3ce929d0e0e4736"}, 12373266430869101994},
		{[]string{"a", "b", "c"}, 9684406651005280037},
	}
	for _, c := range cases {
		if got := CityHash64(c.in...); got != c.want {
			t.Errorf("CityHash64(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
