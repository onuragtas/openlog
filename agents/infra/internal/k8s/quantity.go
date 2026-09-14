package k8s

import (
	"math"
	"strconv"
	"strings"
)

// ParseQuantity converts a Kubernetes resource quantity ("100m", "1.5", "512Mi", "2Gi", "1e3", "10k") to a float:
// cores for CPU, bytes for memory and storage, a count otherwise.
func ParseQuantity(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	type suffix struct {
		s string
		m float64
	}
	// Longest suffixes first.
	suffixes := []suffix{
		{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40}, {"Pi", 1 << 50}, {"Ei", 1 << 60},
		{"n", 1e-9}, {"u", 1e-6}, {"m", 1e-3}, {"k", 1e3}, {"M", 1e6}, {"G", 1e9}, {"T", 1e12}, {"P", 1e15}, {"E", 1e18},
	}
	mult := 1.0
	num := s
	for _, sf := range suffixes {
		if strings.HasSuffix(s, sf.s) {
			// "1e3" ends in a digit; "E" alone is exa only when the rest is a plain number.
			rest := strings.TrimSuffix(s, sf.s)
			if _, err := strconv.ParseFloat(rest, 64); err == nil {
				num, mult = rest, sf.m
				break
			}
		}
	}
	v, err := strconv.ParseFloat(num, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v * mult, true
}
