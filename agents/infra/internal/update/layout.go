package update

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/osutil"
)

// secureLayout makes the install root and versions/ root-owned (0755) and ensures every version
// directory is a root-owned tree nobody else can modify, so root may execute and switch to it.
//
// Installs from before the privileged pre-start step kept versions/ writable by the agent user.
// Their contents cannot be verified (the signed manifest only covers the archive): the version
// "current" points at is copied into a fresh root-owned directory (root already executes it through
// the unit), every other untrusted version directory is removed and can no longer be a rollback target.
func (s *Sys) secureLayout(root string, log *slog.Logger) (notes []string, err error) {
	versions := filepath.Join(root, "versions")
	for _, d := range []string{root, versions} {
		if err := s.fixDir(d, s.RootUID, s.RootGID, 0o755); err != nil {
			return notes, fmt.Errorf("install root: %w", err)
		}
	}
	cur, _ := CurrentDir(root)
	entries, err := os.ReadDir(versions)
	if err != nil {
		return notes, err
	}
	var errs []error
	for _, e := range entries {
		name := e.Name()
		p := filepath.Join(versions, name)
		if strings.HasPrefix(name, ".") {
			// Leftovers of an interrupted install, staging or migration.
			if err := os.RemoveAll(p); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if s.trustedTree(p) {
			continue
		}
		fi, lerr := os.Lstat(p)
		if name == cur && lerr == nil && fi.IsDir() {
			if err := s.migrateVersion(versions, name); err != nil {
				errs = append(errs, fmt.Errorf("securing current version %s: %w", name, err))
				continue
			}
			notes = append(notes, fmt.Sprintf("current version %s was writable by a non-root user; copied into a root-owned directory", name))
			log.Warn("current version directory was writable by a non-root user (legacy self-update); copied into a root-owned directory", "version", name)
			continue
		}
		if err := os.RemoveAll(p); err != nil {
			errs = append(errs, err)
			continue
		}
		notes = append(notes, fmt.Sprintf("removed version %s: writable by a non-root user, not trusted as a rollback target", name))
		log.Warn("removed a version directory writable by a non-root user; it is no longer a rollback target", "version", name)
	}
	// Status files another user could have written are dropped; the privileged steps write new ones.
	for _, name := range []string{ApplyStatusFile, ReconcileStatusFile} {
		p := filepath.Join(root, name)
		if fi, err := os.Lstat(p); err == nil && (!fi.Mode().IsRegular() || !s.trustedInfo(p, fi)) {
			if err := os.Remove(p); err != nil {
				errs = append(errs, err)
			}
		}
	}
	link := filepath.Join(root, "current")
	if fi, err := os.Lstat(link); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if u, g := owner(fi); u != s.RootUID || g != s.RootGID {
			if err := s.Lchown(link, s.RootUID, s.RootGID); err != nil {
				errs = append(errs, err)
			}
		}
	}
	syncDir(versions)
	return notes, errors.Join(errs...)
}

// migrateVersion replaces versions/<name> by a root-owned copy of its directories and regular files.
func (s *Sys) migrateVersion(versions, name string) error {
	src := filepath.Join(versions, name)
	tmp := filepath.Join(versions, "."+name+".migrate")
	old := filepath.Join(versions, "."+name+".old")
	os.RemoveAll(tmp)
	os.RemoveAll(old)
	if err := s.copyTree(src, tmp, MaxExtractBytes); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	if err := os.Rename(src, old); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	if err := os.Rename(tmp, src); err != nil {
		os.Rename(old, src)
		return err
	}
	return os.RemoveAll(old)
}

// copyTree copies the directories and regular files below src into the new directory dst. Reads go
// through os.Root, so symlinks swapped in by another user cannot reach outside src; symlinks and
// special files are skipped; at most maxTotal bytes are copied.
func (s *Sys) copyTree(src, dst string, maxTotal int64) error {
	r, err := os.OpenRoot(src)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := os.Mkdir(dst, 0o755); err != nil {
		return err
	}
	var total int64
	return fs.WalkDir(r.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dst, filepath.FromSlash(p))
		switch {
		case p == ".":
			return s.lchownRoot(dst)
		case d.IsDir():
			if err := os.Mkdir(target, 0o755); err != nil {
				return err
			}
			return s.lchownRoot(target)
		case !d.Type().IsRegular():
			return nil
		}
		in, err := r.OpenFile(p, os.O_RDONLY|osutil.ONofollow|osutil.ONonblock, 0)
		if err != nil {
			return err
		}
		defer in.Close()
		fi, err := in.Stat()
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		if total += fi.Size(); total > maxTotal {
			return fmt.Errorf("%s exceeds %d bytes", src, maxTotal)
		}
		mode := os.FileMode(0o644)
		if fi.Mode()&0o111 != 0 {
			mode = 0o755
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, io.LimitReader(in, fi.Size()))
		if err == nil {
			err = out.Chmod(mode)
		}
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		return s.lchownRoot(target)
	})
}

func (s *Sys) lchownRoot(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if u, g := owner(fi); u == s.RootUID && g == s.RootGID {
		return nil
	}
	return s.Lchown(path, s.RootUID, s.RootGID)
}
