//go:build windows

package metrics

import "github.com/shirou/gopsutil/v4/process"

// nativeProcessStatusCounts: Windows has no process run states; every process counts as running
// (semantic-conventions §2, platform notes).
func nativeProcessStatusCounts() (map[string]int64, error) {
	pids, err := process.Pids()
	if err != nil {
		return nil, err
	}
	n := int64(0)
	for _, pid := range pids {
		if pid != 0 { // System Idle Process
			n++
		}
	}
	return map[string]int64{"running": n}, nil
}
