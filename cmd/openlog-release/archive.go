package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// cmdArchive writes a reproducible tar.gz: sorted entries, fixed owner, one mtime for all entries
// ($SOURCE_DATE_EPOCH, or now), only regular files and directories (the agent rejects symlinks,
// docs/contracts/releases-updates.md §3 rule 6). It avoids platform tar differences (macOS xattrs).
func cmdArchive(args []string, stdout io.Writer) error {
	fset := newFlagSet("archive")
	out := fset.String("out", "", "output .tar.gz")
	prefix := fset.String("prefix", "", "single top-level directory name inside the archive")
	if err := fset.Parse(args); err != nil {
		return err
	}
	if fset.NArg() != 1 || *out == "" || *prefix == "" || strings.ContainsAny(*prefix, `/\`) {
		return usagef("want --out FILE --prefix NAME SRC_DIR")
	}
	mtime, err := releaseTime("")
	if err != nil {
		return err
	}
	src := fset.Arg(0)

	type entry struct {
		rel  string
		info fs.FileInfo
	}
	var entries []entry
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		base := d.Name()
		if base == ".DS_Store" || strings.HasPrefix(base, "._") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return fmt.Errorf("%s: only regular files and directories are allowed", p)
		}
		entries = append(entries, entry{filepath.ToSlash(rel), info})
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })

	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, _ := gzip.NewWriterLevel(f, gzip.BestCompression)
	tw := tar.NewWriter(gz)
	hdr := func(name string, mode int64, typ byte, size int64) *tar.Header {
		return &tar.Header{Name: name, Mode: mode, Typeflag: typ, Size: size, ModTime: mtime, Format: tar.FormatPAX,
			Uname: "root", Gname: "root"}
	}
	if err := tw.WriteHeader(hdr(*prefix+"/", 0o755, tar.TypeDir, 0)); err != nil {
		return err
	}
	for _, e := range entries {
		name := path.Join(*prefix, e.rel)
		if e.info.IsDir() {
			if err := tw.WriteHeader(hdr(name+"/", 0o755, tar.TypeDir, 0)); err != nil {
				return err
			}
			continue
		}
		mode := int64(0o644)
		if e.info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		if err := tw.WriteHeader(hdr(name, mode, tar.TypeReg, e.info.Size())); err != nil {
			return err
		}
		rf, err := os.Open(filepath.Join(src, filepath.FromSlash(e.rel)))
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, rf)
		rf.Close()
		if err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s (%d entries)\n", *out, len(entries)+1)
	return nil
}
