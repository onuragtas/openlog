//go:build windows

package update

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// owner has no uid/gid meaning on Windows: the trust decision is made from the ACL
// (trustedOwnerAndMode). It reports the trusted owner so that ownership fixes are skipped.
func owner(fs.FileInfo) (uid, gid int) { return 0, 0 }

// fileDeleteChild is FILE_DELETE_CHILD (not defined by x/sys/windows).
const fileDeleteChild = 0x40

// Write-like rights that must only be granted to trusted principals.
const untrustedWriteMask = windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA |
	windows.FILE_WRITE_ATTRIBUTES | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER |
	windows.GENERIC_WRITE | windows.GENERIC_ALL | fileDeleteChild

// trustedSIDs are the principals that may own or modify install files: LocalSystem, BUILTIN\Administrators
// and TrustedInstaller (Windows servicing), plus CREATOR OWNER (inherit-only template ACEs of Program Files).
func isTrustedSID(sid *windows.SID) bool {
	for _, t := range []windows.WELL_KNOWN_SID_TYPE{windows.WinLocalSystemSid, windows.WinBuiltinAdministratorsSid, windows.WinCreatorOwnerSid} {
		if sid.IsWellKnown(t) {
			return true
		}
	}
	s := sid.String()
	return strings.HasPrefix(s, "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464") // NT SERVICE\TrustedInstaller
}

// trustedOwnerAndMode: the file is owned by a trusted principal and its DACL grants no write-like right to
// anyone else (the Windows counterpart of "root-owned, not writable by group or others").
func (s *Sys) trustedOwnerAndMode(path string, _ fs.FileInfo) bool {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	own, _, err := sd.Owner()
	if err != nil || own == nil || !isTrustedSID(own) {
		return false
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil { // a NULL DACL grants everyone full access
		return false
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue // deny ACEs only restrict; object ACEs are not used on files
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if ace.Mask&untrustedWriteMask == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !isTrustedSID(sid) {
			return false
		}
	}
	return true
}

// exeSuffix is appended to executable names.
const exeSuffix = ".exe"

// runAs is a no-op on Windows: the service runs as LocalSystem and self-tests run with the same identity.
func runAs(*exec.Cmd, int, int) {}

// replaceLink replaces the directory symbolic link link by tmp. Windows cannot rename over an existing
// directory entry, so the old link is moved aside first and restored when the second rename fails.
func replaceLink(tmp, link string) error {
	old := link + ".old"
	_ = os.Remove(old)
	if err := os.Rename(link, old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Rename(old, link)
		return err
	}
	_ = os.Remove(old) // removes the link only, never the target directory
	return nil
}
