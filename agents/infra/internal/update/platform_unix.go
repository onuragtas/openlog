//go:build !windows

package update

import (
	"io/fs"
	"os"
	"os/exec"
	"syscall"
)

// owner returns the uid and gid of fi (-1 when unknown).
func owner(fi fs.FileInfo) (uid, gid int) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid), int(st.Gid)
	}
	return -1, -1
}

// trustedOwnerAndMode: owned by the trusted uid and not writable by group or others.
func (s *Sys) trustedOwnerAndMode(_ string, fi fs.FileInfo) bool {
	uid, _ := owner(fi)
	return uid == s.RootUID && fi.Mode().Perm()&0o022 == 0
}

// exeSuffix is appended to executable names.
const exeSuffix = ""

// runAs makes cmd run with uid:gid and no supplementary groups (uid < 0: unchanged).
func runAs(cmd *exec.Cmd, uid, gid int) {
	if uid < 0 {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: []uint32{}}}
	cmd.Dir = "/"
}

// replaceLink atomically replaces link by the symlink tmp.
func replaceLink(tmp, link string) error { return os.Rename(tmp, link) }
