package update

import "os"

// Helpers of the privileged steps exported for the PHP agent installation (internal/phpagent, php-agent.md §7.3),
// which follows the same trust rules as "-apply": everything under state_dir is untrusted, root only executes and
// switches to root-owned trees.

// TrustedTree reports whether dir and everything below it are root-owned directories and regular files that nobody
// else can modify.
func (s *Sys) TrustedTree(dir string) bool { return s.trustedTree(dir) }

// LoadTrustedJSON decodes a root-owned status file; files another user could have written are refused.
func (s *Sys) LoadTrustedJSON(path string, v any) error { return s.loadTrustedJSON(path, v) }

// FixDir makes path a root-owned directory with mode (created when missing; a symlink is an error).
func (s *Sys) FixDir(path string, mode os.FileMode) error {
	return s.fixDir(path, s.RootUID, s.RootGID, mode)
}

// CopyVerified copies a staged file opened through r into the new file dest and checks size and sha256 on the copy.
func CopyVerified(r *os.Root, name, dest string, size int64, sha string) error {
	return copyVerified(r, name, dest, size, sha)
}

// ReadRootFile reads a regular file of at most limit bytes through r (no symlink escape, no FIFO).
func ReadRootFile(r *os.Root, name string, limit int64) ([]byte, error) {
	return readRootFile(r, name, limit)
}

// ReadLimited reads a regular file of at most limit bytes without following a final symlink.
func ReadLimited(path string, limit int64) ([]byte, error) { return readLimited(path, limit) }

// WriteFileAtomic writes data durably (temp file, fsync, rename, fsync dir).
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	return writeFileAtomic(path, data, mode)
}

// SyncDir fsyncs a directory (best effort).
func SyncDir(dir string) { syncDir(dir) }

// Truncate shortens untrusted text for status files and reports (control characters replaced).
func Truncate(s string, n int) string { return truncate(s, n) }
