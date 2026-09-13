// Command gomodproxy writes a GOPROXY=file:// directory that serves Go modules of this
// repository at one version, built from the git-tracked files of the working tree. The Go
// agent release check uses it to build modules without their local replace directives,
// exactly as consumers resolve the tags a release is about to push
// (scripts/go-agent-release.sh verify, docs/operations/releasing.md).
//
//	go run ./scripts/gomodproxy -out /tmp/proxy -version v0.4.0 agents/go agents/go/instrumentation/grpc
package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	out := flag.String("out", "", "proxy directory to write")
	version := flag.String("version", "", "module version, e.g. v0.4.0")
	repo := flag.String("repo", ".", "repository root")
	flag.Parse()
	if *out == "" || !strings.HasPrefix(*version, "v") || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: gomodproxy -out DIR -version vX.Y.Z [-repo ROOT] MODULE_DIR...")
		os.Exit(2)
	}
	for _, dir := range flag.Args() {
		if err := writeModule(*out, *repo, filepath.ToSlash(dir), *version); err != nil {
			fmt.Fprintf(os.Stderr, "gomodproxy: %s: %v\n", dir, err)
			os.Exit(1)
		}
	}
}

func writeModule(out, repo, dir, version string) error {
	modFile, err := os.ReadFile(filepath.Join(repo, dir, "go.mod"))
	if err != nil {
		return err
	}
	modPath := modulePath(modFile)
	if modPath == "" {
		return errors.New("go.mod has no module line")
	}
	files, err := moduleFiles(repo, dir)
	if err != nil {
		return err
	}
	esc, err := escapePath(modPath)
	if err != nil {
		return err
	}
	vdir := filepath.Join(out, filepath.FromSlash(esc), "@v")
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	prefix := modPath + "@" + version + "/"
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(repo, dir, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		w, err := zw.Create(prefix + rel)
		if err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	info, _ := json.Marshal(map[string]string{"Version": version, "Time": time.Now().UTC().Format(time.RFC3339)})
	for name, data := range map[string][]byte{
		version + ".zip":  buf.Bytes(),
		version + ".mod":  modFile,
		version + ".info": info,
	} {
		if err := os.WriteFile(filepath.Join(vdir, name), data, 0o644); err != nil {
			return err
		}
	}
	list := filepath.Join(vdir, "list")
	prev, _ := os.ReadFile(list)
	if !bytes.Contains(prev, []byte(version+"\n")) {
		prev = append(prev, version+"\n"...)
	}
	fmt.Printf("%s@%s (%d files)\n", modPath, version, len(files))
	return os.WriteFile(list, prev, 0o644)
}

func modulePath(modFile []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(modFile))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) >= 2 && f[0] == "module" {
			return strings.Trim(f[1], `"`)
		}
	}
	return ""
}

// moduleFiles lists the module's files relative to dir: git-tracked files when dir is in a
// git work tree (else every regular file), without nested modules and vendor directories,
// like the zip the Go module proxy builds from a tag.
func moduleFiles(repo, dir string) ([]string, error) {
	root := filepath.Join(repo, dir)
	var all []string
	if b, err := exec.Command("git", "-C", root, "ls-files", "-z", "--", ".").Output(); err == nil {
		for f := range strings.SplitSeq(string(b), "\x00") {
			if f != "" {
				all = append(all, f)
			}
		}
	} else {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			all = append(all, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	nested := map[string]bool{}
	var files []string
	for _, f := range all {
		if d := path.Dir(f); d != "." && path.Base(f) == "go.mod" {
			nested[d] = true
		}
	}
	for _, f := range all {
		skip := false
		for d := path.Dir(f); d != "."; d = path.Dir(d) {
			if nested[d] || path.Base(d) == "vendor" {
				skip = true
				break
			}
		}
		if st, err := os.Lstat(filepath.Join(root, filepath.FromSlash(f))); skip || err != nil || !st.Mode().IsRegular() {
			continue
		}
		files = append(files, f)
	}
	return files, nil
}

// escapePath applies the module proxy case encoding (upper-case letters → "!" + lower case).
func escapePath(p string) (string, error) {
	var b strings.Builder
	for _, r := range p {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteByte('!')
			b.WriteRune(r + ('a' - 'A'))
		case r == '!' || r > 0x7e:
			return "", fmt.Errorf("unsupported character %q in module path", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String(), nil
}
