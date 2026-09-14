// Package hostfs resolves every host file access against a configurable root
// path (host.root_path). When the agent runs inside a container the host file
// system is mounted at e.g. /host; tests point the root at fixture trees.
package hostfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// PermissionRecorder is notified about permission-denied errors so that
// coverage gaps can be reported as openlog.agent.permission_denied.
type PermissionRecorder interface {
	PermissionDenied(collector string)
}

// FS is a read-only view of the host file system rooted at Root.
type FS struct {
	root      string
	collector string
	rec       PermissionRecorder
}

// New returns an FS rooted at root ("" means "/").
func New(root string) *FS {
	if root == "" {
		root = "/"
	}
	root = filepath.Clean(root)
	if filepath.ToSlash(root) == "/" {
		root = "/" // filepath.Clean("/") is `\` on Windows: IsHostRoot and NativeOS compare with "/"
	}
	return &FS{root: root}
}

// WithRecorder returns a copy that reports permission errors to rec.
func (f *FS) WithRecorder(rec PermissionRecorder) *FS {
	c := *f
	c.rec = rec
	return &c
}

// ForCollector returns a copy whose permission errors are attributed to name.
func (f *FS) ForCollector(name string) *FS {
	c := *f
	c.collector = name
	return &c
}

// Root returns the configured root path.
func (f *FS) Root() string { return f.root }

// IsHostRoot reports whether the root is "/" (the agent sees the host directly).
func (f *FS) IsHostRoot() bool { return f.root == "/" }

// NativeOS reports whether collectors must use the native APIs of a non-Linux host (macOS, Windows;
// D-104) instead of procfs/sysfs: the agent runs on such an OS and looks at the host directly. A
// host.root_path other than "/" always means a mounted Linux file system (containers, test fixtures).
func (f *FS) NativeOS() bool { return runtime.GOOS != "linux" && f.IsHostRoot() }

// Path maps an absolute host path to the local path under the root.
func (f *FS) Path(p string) string {
	if f.root == "/" && filepath.VolumeName(p) != "" {
		return filepath.Clean(p) // Windows drive or UNC path
	}
	if f.root == "/" {
		return filepath.Clean("/" + p)
	}
	return filepath.Join(f.root, filepath.Clean("/"+p))
}

// ProcSelf returns the procfs directory describing the host's own namespaces.
// Inside a container /host/proc/self would describe the agent itself, so PID 1
// of the host is used instead.
func (f *FS) ProcSelf() string {
	if f.IsHostRoot() {
		return "/proc/self"
	}
	return "/proc/1"
}

func (f *FS) note(err error) error {
	if err != nil && f.rec != nil && errors.Is(err, fs.ErrPermission) {
		f.rec.PermissionDenied(f.collectorName())
	}
	return err
}

func (f *FS) collectorName() string {
	if f.collector == "" {
		return "unknown"
	}
	return f.collector
}

// ReadFile reads a whole host file.
func (f *FS) ReadFile(p string) ([]byte, error) {
	b, err := os.ReadFile(f.Path(p))
	return b, f.note(err)
}

// ReadString reads a host file and trims surrounding whitespace.
func (f *FS) ReadString(p string) (string, error) {
	b, err := f.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// ReadDir lists a host directory.
func (f *FS) ReadDir(p string) ([]os.DirEntry, error) {
	e, err := os.ReadDir(f.Path(p))
	return e, f.note(err)
}

// Readlink returns the raw link target (host-absolute paths are not re-rooted).
func (f *FS) Readlink(p string) (string, error) {
	s, err := os.Readlink(f.Path(p))
	return s, f.note(err)
}

// Stat stats a host path following symlinks. Absolute symlink targets are
// resolved by the local OS, so use Lstat+Readlink for links that may point
// outside the root.
func (f *FS) Stat(p string) (os.FileInfo, error) {
	fi, err := os.Stat(f.Path(p))
	return fi, f.note(err)
}

// Lstat stats a host path without following symlinks.
func (f *FS) Lstat(p string) (os.FileInfo, error) {
	fi, err := os.Lstat(f.Path(p))
	return fi, f.note(err)
}

// Open opens a host file.
func (f *FS) Open(p string) (*os.File, error) {
	fh, err := os.Open(f.Path(p))
	return fh, f.note(err)
}

// Statfs holds the subset of statfs(2) results the agent needs.
type Statfs struct {
	BlockSize   uint64 // fragment size used for block counts
	Blocks      uint64
	BlocksFree  uint64 // free blocks including reserved ones
	BlocksAvail uint64 // blocks available to unprivileged users
}

// Statfs returns file system statistics for a host mount point.
func (f *FS) Statfs(mountpoint string) (Statfs, error) {
	st, err := statfs(f.Path(mountpoint))
	return st, f.note(err)
}

// ErrUnsupported is returned by platform-specific operations on other OSes.
var ErrUnsupported = errors.New("hostfs: operation not supported on this platform")
