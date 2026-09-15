package update

import (
	"fmt"
	"os"
	"path/filepath"
)

// Helpers of the privileged steps exported for managing the Java agent jar (internal/javaagent,
// docs/contracts/java-agent.md §2), which follows the same trust rules as the PHP agent installation.

// TrustedDir checks that dir and every directory above it can only be changed by root (a directory the agent user
// could rename entries in must not receive a link written by root).
func (s *Sys) TrustedDir(dir string) error {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	for d := real; ; d = filepath.Dir(d) {
		fi, err := os.Stat(d)
		if err != nil {
			return err
		}
		if !fi.IsDir() || !s.trustedAncestor(d, fi) {
			return fmt.Errorf("directory %s is writable by a non-root user", d)
		}
		if filepath.Dir(d) == d {
			return nil
		}
	}
}

// TrustedRegular reports whether path (not following a final symlink) is a regular file only root can change.
func (s *Sys) TrustedRegular(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode().IsRegular() && s.trustedInfo(path, fi)
}

// ReplaceLink atomically replaces the symbolic link link by the symbolic link tmp (Windows: directory links are
// moved aside first).
func ReplaceLink(tmp, link string) error { return replaceLink(tmp, link) }
