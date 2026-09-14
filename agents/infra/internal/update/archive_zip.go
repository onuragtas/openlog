package update

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	lib "github.com/onuragtas/openlog/libs/release"
)

// ArtifactFormat is the release archive format the agent of goos updates from: zip on Windows, tar.gz
// elsewhere (releases-updates.md §2).
func ArtifactFormat(goos string) string {
	if goos == "windows" {
		return lib.FormatZip
	}
	return lib.FormatTarGz
}

// ExtractArchive extracts a tar.gz or zip release archive with the rules of ExtractTarGz.
func ExtractArchive(format, archive, destDir, topDir string, maxTotal int64) error {
	if format == lib.FormatZip {
		return ExtractZip(archive, destDir, topDir, maxTotal)
	}
	return ExtractTarGz(archive, destDir, topDir, maxTotal)
}

// ExtractZip is ExtractTarGz for zip archives: every entry below the single top-level directory topDir,
// no absolute paths, "..", backslashes, symlinks or special files, at most maxTotal bytes (rule 6).
func ExtractZip(archive, destDir, topDir string, maxTotal int64) (err error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return ruleErr(6, "archive: %v", err)
	}
	defer zr.Close()
	if len(zr.File) > 10000 {
		return ruleErr(6, "archive: too many entries")
	}
	if err := os.Mkdir(destDir, 0o755); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(destDir)
		}
	}()
	var total int64
	for _, f := range zr.File {
		name := f.Name
		isDir := strings.HasSuffix(name, "/")
		rel, err := archiveRelPath(strings.TrimSuffix(name, "/"), topDir)
		if err != nil {
			return err
		}
		target := filepath.Join(destDir, filepath.FromSlash(rel))
		mode := f.Mode()
		switch {
		case isDir || mode.IsDir():
			if rel != "." {
				if err := os.MkdirAll(target, 0o755); err != nil {
					return err
				}
			}
			continue
		case mode&os.ModeSymlink != 0:
			return ruleErr(6, "archive: symlink %q rejected", name)
		case !mode.IsRegular():
			return ruleErr(6, "archive: special file %q rejected", name)
		case rel == ".":
			return ruleErr(6, "archive: top-level entry %q is not a directory", name)
		}
		size := int64(f.UncompressedSize64)
		if size < 0 || f.UncompressedSize64 > uint64(maxTotal) {
			return ruleErr(6, "archive: %q is too large", name)
		}
		if total += size; total > maxTotal {
			return ruleErr(6, "archive: extracted size exceeds %d bytes", maxTotal)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		perm := os.FileMode(0o644)
		if mode&0o111 != 0 {
			perm = 0o755
		}
		if err := extractZipFile(f, target, perm, size); err != nil {
			return err
		}
	}
	return nil
}

func extractZipFile(f *zip.File, target string, perm os.FileMode, size int64) error {
	in, err := f.Open()
	if err != nil {
		return ruleErr(6, "archive: %q: %v", f.Name, err)
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return ruleErr(6, "archive: %q: %v", f.Name, err)
	}
	n, err := io.Copy(out, io.LimitReader(in, size+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("archive: %q: %w", f.Name, err)
	}
	if n != size {
		return ruleErr(6, "archive: %q size mismatch", f.Name)
	}
	return nil
}
