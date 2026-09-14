//go:build !windows

package osutil

import (
	"os"
	"syscall"
)

const (
	// ONonblock does not block opening a FIFO.
	ONonblock = syscall.O_NONBLOCK
	// ONofollow fails on a final symlink.
	ONofollow = syscall.O_NOFOLLOW
)

// FileIdentity returns the device and inode of fi (path is unused on POSIX systems).
func FileIdentity(_ string, fi os.FileInfo) (dev, ino uint64, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return uint64(st.Dev), uint64(st.Ino), true //nolint:unconvert // Dev is int32 on darwin
}

// OpenShared opens a file for reading; on POSIX systems other processes may always rename or delete it.
func OpenShared(path string) (*os.File, error) { return os.Open(path) }
