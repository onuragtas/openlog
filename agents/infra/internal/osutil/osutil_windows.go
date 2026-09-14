//go:build windows

package osutil

import (
	"os"

	"golang.org/x/sys/windows"
)

const (
	// ONonblock has no Windows equivalent (no FIFOs in the file system namespace).
	ONonblock = 0
	// ONofollow has no Windows equivalent; callers check the file type after opening.
	ONofollow = 0
)

// FileIdentity returns the volume serial number and the 64-bit file index of path, the Windows
// equivalent of device and inode. fi is unused.
func FileIdentity(path string, _ os.FileInfo) (dev, ino uint64, ok bool) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, false
	}
	h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return 0, 0, false
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return 0, 0, false
	}
	return uint64(info.VolumeSerialNumber), uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow), true
}

// OpenShared opens a file for reading with FILE_SHARE_DELETE, so that applications can still rotate
// (rename or delete) a log file the agent is tailing. os.Open does not share delete access.
func OpenShared(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}
