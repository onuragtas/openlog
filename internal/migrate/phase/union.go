package phase

import (
	"errors"
	"io/fs"
	"sort"
)

// Union overlays file systems: Open returns the first match, ReadDir merges directory entries
// (the first file system wins on duplicate names). The embedded migration packages use it to add
// build-tag gated test migrations (test/mixedversion) to the real ones.
func Union(fss ...fs.FS) fs.FS {
	if len(fss) == 1 {
		return fss[0]
	}
	return union(fss)
}

type union []fs.FS

func (u union) Open(name string) (fs.File, error) {
	var firstErr error
	for _, f := range u {
		file, err := f.Open(name)
		if err == nil {
			return file, nil
		}
		if firstErr == nil || !errors.Is(err, fs.ErrNotExist) {
			firstErr = err
		}
	}
	if firstErr == nil {
		firstErr = &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return nil, firstErr
}

func (u union) ReadDir(name string) ([]fs.DirEntry, error) {
	seen := map[string]bool{}
	var (
		out   []fs.DirEntry
		found bool
	)
	for _, f := range u {
		entries, err := fs.ReadDir(f, name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		found = true
		for _, e := range entries {
			if !seen[e.Name()] {
				seen[e.Name()] = true
				out = append(out, e)
			}
		}
	}
	if !found {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}
