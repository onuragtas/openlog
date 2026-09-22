package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// downloadErr keeps the reason instead of the address. A *url.Error renders as `Get "<final URL>": reason`,
// and after a redirect that URL is the storage provider's presigned one — GitHub's release assets carry a
// signature and a JWT and run well over a kilobyte. Reported verbatim it pushed the reason past every
// truncation boundary: a host showed `download …: Get "https://release-assets…&sig=…&jwt=…"` and nobody
// could tell a timeout from a refused connection.
func downloadErr(url string, err error) error {
	var ue *neturl.Error
	if errors.As(err, &ue) && ue.Err != nil {
		err = ue.Err
	}
	return fmt.Errorf("download %s: %w", url, err)
}

// Download fetches url into dest and checks size and sha256 against the signed manifest
// (rule 6). At most size+1 bytes are read. dest is removed on failure.
func Download(ctx context.Context, client *http.Client, url string, header http.Header, dest string, size int64, sha string) (err error) {
	if size <= 0 || size > MaxArchiveBytes {
		return ruleErr(6, "archive size %d outside (0, %d]", size, MaxArchiveBytes)
	}
	ctx, cancel := context.WithTimeout(ctx, DownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ruleErr(6, "download: %v", err)
	}
	for k, vs := range header {
		req.Header[k] = vs
	}
	resp, err := client.Do(req)
	if err != nil {
		return downloadErr(url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != size {
		return ruleErr(6, "archive size mismatch: server announces %d bytes, manifest says %d", resp.ContentLength, size)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer func() {
		if cerr := f.Close(); err == nil && cerr != nil {
			err = cerr
		}
		if err != nil {
			os.Remove(dest)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, size+1))
	if err != nil {
		return downloadErr(url, err)
	}
	if n != size {
		return ruleErr(6, "archive size mismatch: got %d bytes, manifest says %d", n, size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sha {
		return ruleErr(6, "archive sha256 mismatch: got %s, manifest says %s", got, sha)
	}
	return f.Sync()
}

// ExtractTarGz extracts archive into destDir (which must not exist). Every entry must live under
// the single top-level directory topDir, whose contents become destDir's contents. Absolute
// paths, "..", symlinks, hard links, device files and other special entries are rejected, as are
// archives whose files exceed maxTotal bytes in total (rule 6).
func ExtractTarGz(archive, destDir, topDir string, maxTotal int64) (err error) {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return ruleErr(6, "archive: %v", err)
	}
	defer zr.Close()
	if err := os.Mkdir(destDir, 0o755); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(destDir)
		}
	}()

	tr := tar.NewReader(zr)
	var total int64
	entries := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ruleErr(6, "archive: %v", err)
		}
		if entries++; entries > 10000 {
			return ruleErr(6, "archive: too many entries")
		}
		rel, err := archiveRelPath(hdr.Name, topDir)
		if err != nil {
			return err
		}
		target := filepath.Join(destDir, filepath.FromSlash(rel))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if rel == "." {
				continue
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if rel == "." {
				return ruleErr(6, "archive: top-level entry %q is not a directory", hdr.Name)
			}
			if hdr.Size < 0 {
				return ruleErr(6, "archive: %q has a negative size", hdr.Name)
			}
			if total += hdr.Size; total > maxTotal {
				return ruleErr(6, "archive: extracted size exceeds %d bytes", maxTotal)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if hdr.Mode&0o111 != 0 {
				mode = 0o755
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return ruleErr(6, "archive: %q: %v", hdr.Name, err)
			}
			n, err := io.Copy(out, io.LimitReader(tr, hdr.Size))
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return fmt.Errorf("archive: %q: %w", hdr.Name, err)
			}
			if n != hdr.Size {
				return ruleErr(6, "archive: %q truncated", hdr.Name)
			}
		case tar.TypeSymlink:
			return ruleErr(6, "archive: symlink %q rejected", hdr.Name)
		case tar.TypeLink:
			return ruleErr(6, "archive: hard link %q rejected", hdr.Name)
		case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			return ruleErr(6, "archive: device file %q rejected", hdr.Name)
		default:
			return ruleErr(6, "archive: unsupported entry type %q for %q", hdr.Typeflag, hdr.Name)
		}
	}
	return nil
}

// archiveRelPath validates an entry name and returns its path relative to topDir ("." for topDir).
func archiveRelPath(name, topDir string) (string, error) {
	if name == "" || strings.ContainsAny(name, "\\\x00") {
		return "", ruleErr(6, "archive: invalid path %q", name)
	}
	if strings.HasPrefix(name, "/") {
		return "", ruleErr(6, "archive: absolute path %q rejected", name)
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return "", ruleErr(6, "archive: path %q contains ..", name)
		}
	}
	clean := path.Clean(name)
	if clean == topDir {
		return ".", nil
	}
	if !strings.HasPrefix(clean, topDir+"/") {
		return "", ruleErr(6, "archive: %q is outside the expected directory %s/", name, topDir)
	}
	return strings.TrimPrefix(clean, topDir+"/"), nil
}

// TopDir is the tarball's top-level directory name (releases-updates.md §2).
func TopDir(version, os, arch string) string {
	return fmt.Sprintf("%s_%s_%s_%s", AgentName, version, os, arch)
}

// RunSelfTest runs "<binary> -self-test [-config <configPath>]" and requires exit 0 within
// timeout (rule 7).
func RunSelfTest(ctx context.Context, binary, configPath string, timeout time.Duration) error {
	return runSelfTest(ctx, binary, configPath, timeout, -1, -1)
}

// RunSelfTestAs is RunSelfTest with the privileges of uid:gid (no supplementary groups): "-apply"
// runs as root but tests the candidate the way the service runs it.
func RunSelfTestAs(ctx context.Context, binary, configPath string, timeout time.Duration, uid, gid int) error {
	return runSelfTest(ctx, binary, configPath, timeout, uid, gid)
}

func runSelfTest(ctx context.Context, binary, configPath string, timeout time.Duration, uid, gid int) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{"-self-test"}
	if configPath != "" {
		args = append(args, "-config", configPath)
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	runAs(cmd, uid, gid)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return ruleErr(7, "self-test of %s timed out after %s", binary, timeout)
	}
	if err != nil {
		msg := strings.TrimSpace(out.String())
		if len(msg) > 512 {
			msg = "…" + msg[len(msg)-512:]
		}
		return ruleErr(7, "self-test of %s failed: %v: %s", binary, err, msg)
	}
	return nil
}

// SwitchCurrent atomically points <root>/current at versions/<dir> (temp symlink + rename).
func SwitchCurrent(root, dir string) error {
	if _, err := os.Stat(filepath.Join(root, "versions", dir, BinaryName)); err != nil {
		return fmt.Errorf("switch to %s: %w", dir, err)
	}
	tmp := filepath.Join(root, fmt.Sprintf(".current.%d.tmp", os.Getpid()))
	os.Remove(tmp)
	if err := os.Symlink(filepath.Join("versions", dir), tmp); err != nil {
		return fmt.Errorf("switch to %s: %w", dir, err)
	}
	if err := replaceLink(tmp, filepath.Join(root, "current")); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("switch to %s: %w", dir, err)
	}
	syncDir(root)
	return nil
}

// CurrentDir returns the versions/<dir> name the current symlink points at.
func CurrentDir(root string) (string, error) {
	t, err := os.Readlink(filepath.Join(root, "current"))
	if err != nil {
		return "", err
	}
	return filepath.Base(filepath.Clean(t)), nil
}

// Prune removes everything under <root>/versions except the named directories.
func Prune(root string, keep ...string) ([]string, error) {
	dir := filepath.Join(root, "versions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	keepSet := map[string]bool{}
	for _, k := range keep {
		if k != "" {
			keepSet[k] = true
		}
	}
	var removed []string
	var errs []error
	for _, e := range entries {
		if keepSet[e.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, e.Name())
	}
	return removed, errors.Join(errs...)
}
