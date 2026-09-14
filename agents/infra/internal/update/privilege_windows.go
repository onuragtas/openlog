//go:build windows

package update

import (
	"io/fs"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// isPrivileged: an elevated administrator or LocalSystem.
func isPrivileged() bool {
	t := windows.GetCurrentProcessToken()
	return t.IsElevated()
}

// Rights that let a principal replace or re-permission an entry below a directory. FILE_ADD_FILE and
// FILE_ADD_SUBDIRECTORY (granted to Users on C:\ and C:\ProgramData) only create new entries.
const ancestorWriteMask = windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | windows.GENERIC_ALL | fileDeleteChild

// trustedAncestor: a directory above a trusted file is owned by a trusted principal and grants nobody else rights
// to delete, rename or re-permission its entries.
func (s *Sys) trustedAncestor(path string, _ fs.FileInfo) bool {
	return aclTrusted(path, ancestorWriteMask)
}

func aclTrusted(path string, mask windows.ACCESS_MASK) bool {
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
	if err != nil || dacl == nil {
		return false
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || ace.Mask&mask == 0 {
			continue
		}
		if !isTrustedSID((*windows.SID)(unsafe.Pointer(&ace.SidStart))) {
			return false
		}
	}
	return true
}

// protectedSDDL grants full control to LocalSystem and Administrators only and blocks inheritance from the parent.
const protectedSDDL = "D:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

func secureACL(path string) error {
	sd, err := windows.SecurityDescriptorFromString(protectedSDDL)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// SecureFile restricts a file to SYSTEM and Administrators.
func SecureFile(path string) error { return secureACL(path) }

// SecureDir creates dir (and missing parents) and restricts it and every parent below %ProgramData% that belongs to
// openlog to SYSTEM and Administrators: ProgramData lets users create folders, so a pre-created
// C:\ProgramData\openlog must not keep its creator's access.
func SecureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	pd := os.Getenv("ProgramData")
	for d := dir; ; d = parentDir(d) {
		if err := secureACL(d); err != nil {
			return err
		}
		p := parentDir(d)
		if p == d || pd == "" || !strings.HasPrefix(strings.ToLower(p), strings.ToLower(pd)+`\`) {
			return nil
		}
	}
}

func parentDir(p string) string {
	i := strings.LastIndexAny(strings.TrimRight(p, `\/`), `\/`)
	if i <= 2 {
		return p
	}
	return p[:i]
}

// InstallRegistryKey holds InstallMethod=msi for MSI installations.
const InstallRegistryKey = `SOFTWARE\openlog\infra-agent`

// platformInstallMethod reports MethodMSI when the MSI recorded itself, else MethodZip (install.ps1).
func platformInstallMethod() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, InstallRegistryKey, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err == nil {
		defer k.Close()
		if v, _, err := k.GetStringValue("InstallMethod"); err == nil && strings.EqualFold(v, MethodMSI) {
			return MethodMSI
		}
	}
	return MethodZip
}
