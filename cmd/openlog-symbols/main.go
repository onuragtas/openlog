// Command openlog-symbols uploads what turns a mobile crash's stack back into source (docs/contracts/mobile-crash.md §3).
//
// It runs on the build machine, as the last step of a release build:
//
//	openlog-symbols upload -endpoint https://apm.resoft.org -api-key ola_... -app my-game path...
//
// A path may be a dSYM, a directory of them, an Android .so or a directory of those, which are read here and reduced to a small
// table, or with -version an R8 mapping.txt. A dSYM can run to hundreds of megabytes and most of it is type information a stack
// does not need, so what is uploaded is usually a few percent of that.
package main

import (
	"bytes"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/symbols"
)

const usage = `usage:
  openlog-symbols upload -endpoint URL -api-key KEY -app APP path...          native symbols: dSYMs and Android .so files
  openlog-symbols upload -endpoint URL -api-key KEY -app APP -version V file  an R8 or ProGuard mapping.txt

The API key needs the admin role. OPENLOG_API_KEY is read when -api-key is not given.
A file with no symbols of its own is skipped, and it is an error only when nothing could be uploaded.
`

func main() {
	if len(os.Args) < 2 || os.Args[1] != "upload" {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	fs := flag.NewFlagSet("upload", flag.ExitOnError)
	endpoint := fs.String("endpoint", "", "base URL of the openlog API")
	apiKey := fs.String("api-key", os.Getenv("OPENLOG_API_KEY"), "API key with the admin role")
	app := fs.String("app", "", "the application name on its mobile key")
	version := fs.String("version", "", "the build a mapping belongs to, which makes the paths mapping files")
	dryRun := fs.Bool("dry-run", false, "read the files and report, and upload nothing")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	_ = fs.Parse(os.Args[2:])
	if *app == "" || fs.NArg() == 0 || (!*dryRun && (*endpoint == "" || *apiKey == "")) {
		fs.Usage()
		os.Exit(2)
	}
	up := uploader{endpoint: strings.TrimRight(*endpoint, "/"), key: *apiKey, app: *app, dryRun: *dryRun}
	if err := up.run(fs.Args(), *version); err != nil {
		fmt.Fprintln(os.Stderr, "openlog-symbols:", err)
		os.Exit(1)
	}
}

type uploader struct {
	endpoint, key, app string
	dryRun             bool
	sent               int
	client             http.Client
}

type found struct {
	path  string
	table *symbols.Table
}

func (u *uploader) run(paths []string, version string) error {
	u.client.Timeout = 10 * time.Minute
	best := map[string]found{}
	for _, p := range paths {
		if version != "" {
			if err := u.mapping(p, version); err != nil {
				return err
			}
			continue
		}
		if err := u.native(p, best); err != nil {
			return err
		}
	}
	for id, f := range best {
		var b bytes.Buffer
		if err := f.table.Encode(&b); err != nil {
			return err
		}
		if err := u.post(f.path, symbols.KindNative, id, b.Bytes()); err != nil {
			return err
		}
	}
	if u.sent == 0 {
		return fmt.Errorf("nothing to upload in %s", strings.Join(paths, ", "))
	}
	return nil
}

func (u *uploader) mapping(path, version string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if _, err := symbols.ParseMapping(raw); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	if _, err := gz.Write(raw); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return u.post(path, symbols.KindR8, version, b.Bytes())
}

// native collects every binary with symbols under a path. A build directory holds the same image more than once, a stripped
// libil2cpp.so beside its libil2cpp.sym.so and an app beside its dSYM, under one id. So nothing is sent until all are read, and
// the copy that knows the most functions is the one sent, or the stripped one could replace the real one.
func (u *uploader) native(path string, best map[string]found) error {
	return filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		var images []symbols.Image
		switch {
		case symbols.IsELF(p):
			img, err := symbols.FromELF(p)
			if err != nil {
				fmt.Fprintf(os.Stderr, "skipping %s: %v\n", p, err)
				return nil
			}
			images = []symbols.Image{img}
		case symbols.IsMachO(p):
			if images, err = symbols.FromMachO(p); err != nil {
				fmt.Fprintf(os.Stderr, "skipping %s: %v\n", p, err)
				return nil
			}
		default:
			return nil
		}
		for _, img := range images {
			if img.Table.Functions() == 0 {
				fmt.Fprintf(os.Stderr, "skipping %s: no symbols in it\n", p)
				continue
			}
			if cur, ok := best[img.ID]; !ok || img.Table.Functions() > cur.table.Functions() {
				best[img.ID] = found{p, img.Table}
			}
		}
		return nil
	})
}

func (u *uploader) post(path, kind, key string, body []byte) error {
	fmt.Printf("%s %s %s (%.1f MB)\n", kind, key, filepath.Base(path), float64(len(body))/(1<<20))
	u.sent++
	if u.dryRun {
		return nil
	}
	q := url.Values{"app": {u.app}, "kind": {kind}, "key": {key}}
	req, err := http.NewRequest(http.MethodPost, u.endpoint+"/api/v1/symbols?"+q.Encode(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+u.key)
	req.Header.Set("Content-Type", "application/octet-stream")
	res, err := u.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return fmt.Errorf("%s: %s: %s", path, res.Status, msg)
	}
	return nil
}
