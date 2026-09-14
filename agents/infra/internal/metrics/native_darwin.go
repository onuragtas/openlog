//go:build darwin

package metrics

import "golang.org/x/sys/unix"

// Process states of struct extern_proc.p_stat (sys/proc.h).
const (
	sIDL  = 1
	sRUN  = 2
	sSLP  = 3
	sSTOP = 4
	sZOMB = 5
)

// DarwinProcessStatus maps p_stat to a process.status value.
func DarwinProcessStatus(stat int8) string {
	switch stat {
	case sRUN:
		return "running"
	case sSLP:
		return "sleeping"
	case sSTOP:
		return "stopped"
	case sZOMB:
		return "zombie"
	case sIDL:
		return "idle"
	}
	return "other"
}

func nativeProcessStatusCounts() (map[string]int64, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	for i := range procs {
		if procs[i].Proc.P_pid == 0 {
			continue // kernel_task
		}
		counts[DarwinProcessStatus(procs[i].Proc.P_stat)]++
	}
	return counts, nil
}
