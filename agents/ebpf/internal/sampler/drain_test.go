package sampler

import "testing"

// Reading one CPU's slot instead of the sum reports a fraction of the samples: a profile that looks
// plausible and is wrong by roughly the core count.
func TestSumPerCPUAddsEveryCPU(t *testing.T) {
	if got := SumPerCPU([]uint64{3, 0, 7, 1}); got != 11 {
		t.Errorf("sum = %d, want 11", got)
	}
	if got := SumPerCPU(nil); got != 0 {
		t.Errorf("empty sum = %d, want 0", got)
	}
}

// The map value is a fixed-width array, so the padding after the last frame has to go — otherwise every
// flame graph gets a 0x0 at its root.
func TestTrimStackDropsTrailingPadding(t *testing.T) {
	got := TrimStack([]uint64{0x1000, 0x2000, 0x3000, 0, 0, 0})
	if len(got) != 3 {
		t.Fatalf("kept %d frames, want 3: %v", len(got), got)
	}
	if got[2] != 0x3000 {
		t.Errorf("last frame = %#x, want 0x3000", got[2])
	}
}

// A zero between real frames is not padding. Stopping at the first zero would truncate a stack that the
// kernel walked perfectly well.
func TestTrimStackKeepsInteriorZeros(t *testing.T) {
	got := TrimStack([]uint64{0x1000, 0, 0x3000, 0, 0})
	if len(got) != 3 {
		t.Errorf("kept %d frames, want 3: %v", len(got), got)
	}
}

func TestTrimStackHandlesAllZeroAndFull(t *testing.T) {
	if got := TrimStack([]uint64{0, 0, 0}); len(got) != 0 {
		t.Errorf("an empty stack kept %d frames", len(got))
	}
	full := []uint64{1, 2, 3}
	if got := TrimStack(full); len(got) != 3 {
		t.Errorf("a full stack kept %d frames", len(got))
	}
}
