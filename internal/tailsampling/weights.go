package tailsampling

import (
	"encoding/binary"
	"math"
	"strconv"
	"strings"

	"github.com/cespare/xxhash/v2"
)

// maxThreshold is 2^56, the OTel probability sampling threshold/randomness range.
const maxThreshold = uint64(1) << 56

// Threshold returns the OTel rejection threshold T of probability p: a trace with randomness
// R >= T is kept, so p = 1 - T/2^56. p >= 1 gives 0, p <= 0 gives 2^56 (never kept).
func Threshold(p float64) uint64 {
	switch {
	case p >= 1 || math.IsNaN(p):
		return 0
	case p <= 0:
		return maxThreshold
	}
	t := math.Round((1 - p) * float64(maxThreshold))
	if t >= float64(maxThreshold) {
		return maxThreshold - 1
	}
	return uint64(t)
}

// EncodeThreshold formats T as the tracestate `th` value: 14 hex digits without trailing zeros
// ("0" for T = 0).
func EncodeThreshold(t uint64) string {
	if t == 0 {
		return "0"
	}
	if t >= maxThreshold {
		t = maxThreshold - 1
	}
	s := strconv.FormatUint(t, 16)
	s = strings.Repeat("0", 14-len(s)) + s
	return strings.TrimRight(s, "0")
}

// traceRandomness returns the 56-bit randomness of a trace: the tracestate `ot=rv` value when present,
// else the least significant 7 bytes of the trace id (W3C random trace id flag, OTel consistent sampling).
func traceRandomness(traceID []byte, traceState string) uint64 {
	if rv, ok := otSubKey(traceState, "rv"); ok && len(rv) == 14 {
		if v, err := strconv.ParseUint(rv, 16, 64); err == nil {
			return v
		}
	}
	if len(traceID) != 16 {
		return hashRandomness(traceID)
	}
	return binary.BigEndian.Uint64(traceID[8:]) & (maxThreshold - 1)
}

// hashRandomness derives 56 bits from the trace id that are independent of any head sampler's use
// of the trace id bytes (used when no span carries a consistent `ot` threshold).
func hashRandomness(traceID []byte) uint64 {
	d := xxhash.New()
	_, _ = d.WriteString("openlog-tail-sampling/v1:")
	_, _ = d.Write(traceID)
	return d.Sum64() >> 8
}

// otSubKey returns the value of key inside the tracestate `ot` entry.
func otSubKey(traceState, key string) (string, bool) {
	for entry := range strings.SplitSeq(traceState, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(entry), "=")
		if !ok || k != "ot" {
			continue
		}
		for sub := range strings.SplitSeq(v, ";") {
			if sk, sv, ok := strings.Cut(sub, ":"); ok && sk == key {
				return sv, true
			}
		}
	}
	return "", false
}

// WithThreshold returns traceState with the `ot` entry's threshold set to th: `th` and the legacy
// `p` sub-keys are replaced, other `ot` sub-keys (rv, …) are kept, and the modified entry moves to
// the front as W3C Trace Context requires. Other vendors' entries are unchanged.
func WithThreshold(traceState, th string) string {
	var others []string
	subs := []string{"th:" + th}
	for entry := range strings.SplitSeq(traceState, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		k, v, ok := strings.Cut(entry, "=")
		if !ok || k != "ot" {
			others = append(others, entry)
			continue
		}
		for sub := range strings.SplitSeq(v, ";") {
			sk, _, ok := strings.Cut(sub, ":")
			if !ok || sk == "th" || sk == "p" {
				continue
			}
			subs = append(subs, sub)
		}
	}
	out := "ot=" + strings.Join(subs, ";")
	if len(others) > 31 { // at most 32 list members
		others = others[:31]
	}
	if len(others) > 0 {
		out += "," + strings.Join(others, ",")
	}
	return out
}
