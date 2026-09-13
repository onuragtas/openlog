//go:build linux

package hostfs

import "syscall"

func statfs(path string) (Statfs, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Statfs{}, err
	}
	bs := uint64(st.Frsize)
	if bs == 0 {
		bs = uint64(st.Bsize)
	}
	return Statfs{
		BlockSize:   bs,
		Blocks:      uint64(st.Blocks),
		BlocksFree:  uint64(st.Bfree),
		BlocksAvail: uint64(st.Bavail),
	}, nil
}
