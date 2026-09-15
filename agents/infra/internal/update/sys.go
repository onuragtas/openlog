package update

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/osutil"
)

// maxStatusBytes bounds JSON files read by the privileged steps (state and status files).
const maxStatusBytes = 1 << 20

// Sys is the host the privileged steps ("-apply", "-reconcile") act on. Tests substitute a fixture:
// system paths are resolved below Root and "root" is the test user.
type Sys struct {
	// Root is prepended to system paths (/etc/passwd, /etc/group, systemd unit directories).
	Root string
	// RootUID and RootGID own trusted files (0 in production).
	RootUID, RootGID int
	IsRoot           func() bool
	Lchown           func(path string, uid, gid int) error
	// Run executes a system command (useradd, usermod, systemctl).
	Run      func(ctx context.Context, name string, args ...string) error
	LookPath func(string) (string, error)
	Getenv   func(string) string
	// SelfTestAsCurrent runs candidate self-tests with the identity of the privileged process instead of the agent
	// user: macOS and Windows services run as root / LocalSystem (D-104).
	SelfTestAsCurrent bool
	// TrustTree replaces the ownership check of TrustedTree when set. Only tests set it: on Windows the check reads
	// ACLs, which temporary directories of a test run cannot satisfy.
	TrustTree func(dir string) bool
}

// HostSys is the real host.
func HostSys() *Sys {
	return &Sys{
		Root: "/", IsRoot: isPrivileged, Lchown: os.Lchown,
		Run: runCommand, LookPath: exec.LookPath, Getenv: os.Getenv,
		SelfTestAsCurrent: runtime.GOOS != "linux",
	}
}

func runCommand(ctx context.Context, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *Sys) path(p string) string { return filepath.Join(s.Root, p) }

// trustedInfo reports whether the file at path (described by fi) belongs to the trusted owner and cannot be
// modified by anyone else (POSIX: not writable by group or others; Windows: ACL, see platform_windows.go).
func (s *Sys) trustedInfo(path string, fi fs.FileInfo) bool {
	return s.trustedOwnerAndMode(path, fi)
}

// trustedTree reports whether dir and everything below it are trusted directories and regular files
// (no symlinks, devices or files another user can modify).
func (s *Sys) trustedTree(dir string) bool {
	if s.TrustTree != nil {
		return s.TrustTree(dir)
	}
	ok := true
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if !(fi.IsDir() || fi.Mode().IsRegular()) || !s.trustedInfo(p, fi) {
			ok = false
			return filepath.SkipAll
		}
		return nil
	})
	return ok && err == nil
}

// TrustedFile checks that path (symlinks resolved), and every directory above it, can only be
// changed by root: a file the agent user could replace must not steer a root process.
func (s *Sys) TrustedFile(path string) error {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	fi, err := os.Stat(real)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || !s.trustedInfo(real, fi) {
		return fmt.Errorf("%s is not a regular file owned by root and writable only by its owner", real)
	}
	for d := filepath.Dir(real); ; d = filepath.Dir(d) {
		fi, err := os.Stat(d)
		if err != nil {
			return err
		}
		if !s.trustedAncestor(d, fi) {
			return fmt.Errorf("directory %s of %s is writable by a non-root user", d, real)
		}
		if filepath.Dir(d) == d {
			return nil
		}
	}
}

// fixDir makes path a directory owned by uid:gid with mode (created when missing; a symlink is an error).
func (s *Sys) fixDir(path string, uid, gid int, mode os.FileMode) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(path, mode); err != nil {
			return err
		}
		fi, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	if u, g := owner(fi); u != uid || g != gid {
		if err := s.Lchown(path, uid, gid); err != nil {
			return err
		}
	}
	if fi.Mode().Perm() != mode {
		return os.Chmod(path, mode)
	}
	return nil
}

// lookupUser reads uid and gid of name from <Root>/etc/passwd.
func (s *Sys) lookupUser(name string) (uid, gid int, err error) {
	f, err := s.colonFile("/etc/passwd", name)
	if err != nil {
		return -1, -1, err
	}
	if len(f) < 4 {
		return -1, -1, fmt.Errorf("user %s: malformed passwd entry", name)
	}
	uid, err1 := strconv.Atoi(f[2])
	gid, err2 := strconv.Atoi(f[3])
	if err1 != nil || err2 != nil {
		return -1, -1, fmt.Errorf("user %s: malformed passwd entry", name)
	}
	return uid, gid, nil
}

// groupMembers returns the members of group name from <Root>/etc/group; ok is false when it does not exist.
func (s *Sys) groupMembers(name string) (members []string, ok bool) {
	f, err := s.colonFile("/etc/group", name)
	if err != nil || len(f) < 4 {
		return nil, false
	}
	for _, m := range strings.Split(f[3], ",") {
		if m = strings.TrimSpace(m); m != "" {
			members = append(members, m)
		}
	}
	return members, true
}

func (s *Sys) colonFile(file, name string) ([]string, error) {
	b, err := os.ReadFile(s.path(file))
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if f := strings.Split(sc.Text(), ":"); len(f) > 0 && f[0] == name {
			return f, nil
		}
	}
	return nil, fmt.Errorf("%s: %w", name, fs.ErrNotExist)
}

// loadTrustedJSON decodes a root-owned status file; files another user could have written are refused.
func (s *Sys) loadTrustedJSON(path string, v any) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || !s.trustedInfo(path, fi) {
		return fmt.Errorf("%s is not a root-owned regular file", path)
	}
	b, err := readLimited(path, maxStatusBytes)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// readLimited reads a regular file of at most limit bytes without following a final symlink or
// blocking on a FIFO.
func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|osutil.ONofollow|osutil.ONonblock, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readOpened(f, path, limit)
}

func readOpened(f *os.File, name string, limit int64) ([]byte, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	if fi.Size() > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", name, limit)
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", name, limit)
	}
	return b, nil
}

// saveStatus writes a root-owned, world-readable status file.
func saveStatus(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'), 0o644)
}

// LoadApplyStatus and LoadReconcileStatus read the status files for the unprivileged agent.
func LoadApplyStatus(installRoot string) (*ApplyStatus, error) {
	var st ApplyStatus
	if err := loadJSON(filepath.Join(installRoot, ApplyStatusFile), &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func LoadReconcileStatus(installRoot string) (*ReconcileStatus, error) {
	var st ReconcileStatus
	if err := loadJSON(filepath.Join(installRoot, ReconcileStatusFile), &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func loadJSON(path string, v any) error {
	b, err := readLimited(path, maxStatusBytes)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// truncate shortens untrusted text copied into status files and reports.
func truncate(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
