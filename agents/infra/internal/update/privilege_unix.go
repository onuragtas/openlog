//go:build !windows

package update

import (
	"io/fs"
	"os"
)

func isPrivileged() bool { return os.Geteuid() == 0 }

// trustedAncestor: a directory above a trusted file is owned by root (or the trusted uid) and not writable by
// others unless sticky.
func (s *Sys) trustedAncestor(_ string, fi fs.FileInfo) bool {
	uid, _ := owner(fi)
	return (uid == s.RootUID || uid == 0) && (fi.Mode().Perm()&0o022 == 0 || fi.Mode()&os.ModeSticky != 0)
}

// SecureDir creates dir restricted to its owner (POSIX systems use modes; see reconcile).
func SecureDir(dir string) error { return os.MkdirAll(dir, 0o755) }

// SecureFile restricts a file to its owner (0600).
func SecureFile(path string) error { return os.Chmod(path, 0o600) }

// secureACL is a no-op on POSIX systems (modes are set by the callers).
func secureACL(string) error { return nil }

// platformInstallMethod reports installation methods recorded outside the file system (none on POSIX systems).
func platformInstallMethod() string { return "" }
