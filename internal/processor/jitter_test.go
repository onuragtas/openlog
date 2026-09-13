package processor

import (
	"testing"
	"time"
)

func TestJitterBoundsAndSpread(t *testing.T) {
	for _, d := range []time.Duration{0, 1, 200 * time.Millisecond, 10 * time.Second} {
		seen := map[time.Duration]bool{}
		for range 200 {
			j := jitter(d)
			if j < d/2 || j > d {
				t.Fatalf("jitter(%v) = %v, want within [%v, %v]", d, j, d/2, d)
			}
			seen[j] = true
		}
		// Retries of different processors must not stay in lockstep.
		if d >= 200*time.Millisecond && len(seen) < 50 {
			t.Errorf("jitter(%v) produced only %d distinct values in 200 draws", d, len(seen))
		}
	}
}
