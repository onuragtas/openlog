package apm

import (
	"math"
	"strconv"
	"strings"
)

const minProbability = 1e-6

// SampleWeight returns 1/p for the span's sampling probability p (apm.md §4).
func SampleWeight(traceState string, spanAttrs, resAttrs map[string]string) float64 {
	if p, ok := probabilityFromTraceState(traceState); ok {
		if p == 0 {
			return 0
		}
		return 1 / clampP(p)
	}
	for _, attrs := range []map[string]string{spanAttrs, resAttrs} {
		if v := attrs["sampling.ratio"]; v != "" {
			if p, err := strconv.ParseFloat(v, 64); err == nil && p > 0 && p <= 1 && !math.IsNaN(p) {
				return 1 / clampP(p)
			}
		}
	}
	return 1
}

func clampP(p float64) float64 { return math.Min(1, math.Max(minProbability, p)) }

// probabilityFromTraceState reads the OTel `ot` tracestate entry: th:<hex> threshold
// (p = 1 - T/2^56) or legacy p:<n> (p = 2^-n, n = 63 means not sampled).
func probabilityFromTraceState(ts string) (float64, bool) {
	if ts == "" {
		return 0, false
	}
	for entry := range strings.SplitSeq(ts, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(entry), "=")
		if !ok || k != "ot" {
			continue
		}
		var p float64
		found := false
		for sub := range strings.SplitSeq(v, ";") {
			sk, sv, ok := strings.Cut(sub, ":")
			if !ok {
				continue
			}
			switch sk {
			case "th":
				if len(sv) == 0 || len(sv) > 14 {
					continue
				}
				t, err := strconv.ParseUint(sv+strings.Repeat("0", 14-len(sv)), 16, 64)
				if err != nil {
					continue
				}
				return 1 - float64(t)/float64(uint64(1)<<56), true
			case "p":
				n, err := strconv.Atoi(sv)
				if err != nil || n < 0 || n > 63 {
					continue
				}
				if n == 63 {
					p = 0
				} else {
					p = math.Ldexp(1, -n)
				}
				found = true
			}
		}
		if found {
			return p, true
		}
	}
	return 0, false
}
