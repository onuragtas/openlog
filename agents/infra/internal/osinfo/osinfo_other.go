//go:build !darwin && !windows

package osinfo

import "time"

// Linux and other systems are described by /etc/os-release and procfs elsewhere.
func read() Info { return Info{} }

func bootTime() time.Time { return time.Time{} }
