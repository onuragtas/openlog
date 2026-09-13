package clickhouse

import (
	"unsafe"

	"github.com/go-faster/city"
)

// CityHash64 returns ClickHouse's cityHash64(s1, s2, ...) for String (or
// LowCardinality(String)) arguments.
//
// ClickHouse hashes each argument with CityHash64 v1.0.2 (city.CH64) and, for
// every argument after the first, combines the running hash h with the
// argument's hash a as Hash128to64(uint128{low: h, high: a}). The processor uses
// this to place rows on the same shard the Distributed engine would choose; the
// result is verified against the server in the integration test.
func CityHash64(parts ...string) uint64 {
	var h uint64
	for i, s := range parts {
		a := city.CH64(unsafe.Slice(unsafe.StringData(s), len(s)))
		if i == 0 {
			h = a
			continue
		}
		h = hash128to64(h, a)
	}
	return h
}

// hash128to64 is CityHash's Hash128to64 (Murmur-inspired) for uint128{low, high}.
func hash128to64(low, high uint64) uint64 {
	const mul = uint64(0x9ddfea08eb382d69)
	a := (low ^ high) * mul
	a ^= a >> 47
	b := (high ^ a) * mul
	b ^= b >> 47
	b *= mul
	return b
}
