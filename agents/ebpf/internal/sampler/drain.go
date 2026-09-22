// Turning what the kernel left in the maps into samples. No build tag: this is arithmetic over numbers the
// kernel wrote, so it is tested here rather than only on a machine that can load BPF.
package sampler

// SumPerCPU adds a per-CPU counter across CPUs.
//
// The counter map is PerCPUHash, so one key holds one value per possible CPU and the total is userspace's
// job. Reading only one CPU's slot would quietly report a fraction of the samples — a profile that looks
// plausible and is wrong by a factor of the core count.
func SumPerCPU(values []uint64) int64 {
	var total uint64
	for _, v := range values {
		total += v
	}
	return int64(total)
}

// TrimStack drops the zero padding a stack-trace map leaves after the last frame.
//
// The map's value is a fixed-width array, so a stack of three frames comes back with the depth minus three
// trailing zeros. They are not frames: keeping them would put a `0x0` at the root of every flame graph.
// A zero *inside* the walked frames is not padding and is kept, which is why this trims from the end
// rather than stopping at the first zero.
func TrimStack(addrs []uint64) []uint64 {
	end := len(addrs)
	for end > 0 && addrs[end-1] == 0 {
		end--
	}
	return addrs[:end]
}
