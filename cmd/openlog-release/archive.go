package main

import (
	"archive/tar"
	"archive/zip"
	"compress/flate"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// cmdArchive writes a reproducible tar.gz or zip: sorted entries, fixed owner, one mtime for all entries
// ($SOURCE_DATE_EPOCH, or now), only regular files and directories (the agent rejects symlinks,
// docs/contracts/releases-updates.md §3 rule 6). It avoids platform tar differences (macOS xattrs).
// The format is --format, or inferred from --out (.zip: zip, otherwise tar.gz). Zip entries keep the
// executable bit in their Unix external attributes (the Windows agent archive).
func cmdArchive(args []string, stdout io.Writer) error {
	fset := newFlagSet("archive")
	out := fset.String("out", "", "output .tar.gz or .zip")
	prefix := fset.String("prefix", "", "single top-level directory name inside the archive")
	format := fset.String("format", "", "tar.gz or zip (default: from the --out suffix)")
	if err := fset.Parse(args); err != nil {
		return err
	}
	if fset.NArg() != 1 || *out == "" || *prefix == "" || strings.ContainsAny(*prefix, `/\`) {
		return usagef("want --out FILE --prefix NAME [--format tar.gz|zip] SRC_DIR")
	}
	if *format == "" {
		*format = "tar.gz"
		if strings.HasSuffix(strings.ToLower(*out), ".zip") {
			*format = "zip"
		}
	}
	if *format != "tar.gz" && *format != "zip" {
		return usagef("--format %q: want tar.gz or zip", *format)
	}
	mtime, err := releaseTime("")
	if err != nil {
		return err
	}
	src := fset.Arg(0)

	var entries []archiveEntry
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
		entries = append(entries, archiveEntry{filepath.ToSlash(rel), info})
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
	if *format == "zip" {
		err = writeZip(f, src, *prefix, mtime, entries)
	} else {
		err = writeTarGz(f, src, *prefix, mtime, entries)
	}
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s (%d entries)\n", *out, len(entries)+1)
	return nil
}

type archiveEntry struct {
	rel  string
	info fs.FileInfo
}

func (e archiveEntry) mode() int64 {
	if e.info.IsDir() || e.info.Mode()&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

func copyEntry(w io.Writer, src string, e archiveEntry) error {
	rf, err := os.Open(filepath.Join(src, filepath.FromSlash(e.rel)))
	if err != nil {
		return err
	}
	defer rf.Close()
	_, err = io.Copy(w, rf)
	return err
}

func writeTarGz(f io.Writer, src, prefix string, mtime time.Time, entries []archiveEntry) error {
	gz, _ := gzip.NewWriterLevel(f, gzip.BestCompression)
	tw := tar.NewWriter(gz)
	hdr := func(name string, mode int64, typ byte, size int64) *tar.Header {
		return &tar.Header{Name: name, Mode: mode, Typeflag: typ, Size: size, ModTime: mtime, Format: tar.FormatPAX,
			Uname: "root", Gname: "root"}
	}
	if err := tw.WriteHeader(hdr(prefix+"/", 0o755, tar.TypeDir, 0)); err != nil {
		return err
	}
	for _, e := range entries {
		name := path.Join(prefix, e.rel)
		if e.info.IsDir() {
			if err := tw.WriteHeader(hdr(name+"/", 0o755, tar.TypeDir, 0)); err != nil {
				return err
			}
			continue
		}
		if err := tw.WriteHeader(hdr(name, e.mode(), tar.TypeReg, e.info.Size())); err != nil {
			return err
		}
		if err := copyEntry(tw, src, e); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func writeZip(f io.Writer, src, prefix string, mtime time.Time, entries []archiveEntry) error {
	zw := zip.NewWriter(f)
	zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(w, flate.BestCompression)
	})
	header := func(name string, mode fs.FileMode) *zip.FileHeader {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: mtime.UTC()}
		h.SetMode(mode)
		if mode.IsDir() {
			h.Method = zip.Store
		}
		return h
	}
	if _, err := zw.CreateHeader(header(prefix+"/", fs.ModeDir|0o755)); err != nil {
		return err
	}
	for _, e := range entries {
		name := path.Join(prefix, e.rel)
		if e.info.IsDir() {
			if _, err := zw.CreateHeader(header(name+"/", fs.ModeDir|0o755)); err != nil {
				return err
			}
			continue
		}
		w, err := zw.CreateHeader(header(name, fs.FileMode(e.mode())))
		if err != nil {
			return err
		}
		if err := copyEntry(w, src, e); err != nil {
			return err
		}
	}
	return zw.Close()
}
