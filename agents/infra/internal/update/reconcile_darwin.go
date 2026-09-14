//go:build darwin

package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// newsyslogConf rotates the LaunchDaemon's log (10 MiB, 5 compressed generations).
const newsyslogConf = "# openlog infrastructure agent (installed by openlog-infra-agent -reconcile)\n" +
	"# logfilename                          [owner:group]  mode count size(KiB) when flags\n" +
	LaunchdLogPath + "   root:wheel     640  5     10240     *    NJ\n"

func (r *reconciler) native(ctx context.Context, prev *ReconcileStatus) {
	s := r.o.Sys
	// State (host id, buffer, staged updates) and configuration (license key): root only.
	if r.o.StateDir != "" {
		if err := s.fixDir(r.o.StateDir, s.RootUID, s.RootGID, 0o700); err != nil {
			r.fail("state dir", err)
		}
	}
	if r.o.ConfigPath != "" {
		if fi, err := os.Lstat(filepath.Dir(r.o.ConfigPath)); err == nil && fi.IsDir() {
			if err := s.fixDir(filepath.Dir(r.o.ConfigPath), s.RootUID, s.RootGID, fi.Mode().Perm()&^0o022); err != nil {
				r.fail("config dir", err)
			}
		}
		if fi, err := os.Lstat(r.o.ConfigPath); err == nil && fi.Mode().IsRegular() {
			if u, g := owner(fi); u != s.RootUID || g != s.RootGID {
				if err := s.Lchown(r.o.ConfigPath, s.RootUID, s.RootGID); err != nil {
					r.fail("config", err)
				}
			}
			if fi.Mode().Perm() != 0o600 {
				if err := os.Chmod(r.o.ConfigPath, 0o600); err != nil {
					r.fail("config", err)
				}
			}
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			r.fail("config", err)
		}
	}

	plist := RenderLaunchdPlist(r.o.Install.InstallRoot, r.o.ConfigPath)
	sum := sha256.Sum256(plist)
	r.st.UnitPath, r.st.UnitSHA256 = LaunchdPlistPath, hex.EncodeToString(sum[:])
	changed, err := writeIfChanged(s.path(LaunchdPlistPath), plist, 0o644)
	if err != nil {
		r.fail("launchd plist", err)
	} else if changed {
		if err := s.lchownRoot(s.path(LaunchdPlistPath)); err != nil {
			r.fail("launchd plist", err)
		}
		r.st.UnitChanged = true
		r.log.Info("launchd job definition updated", "path", LaunchdPlistPath)
		if r.o.Context == ReconcileApply {
			// A running job cannot re-bootstrap itself: the new definition applies at the next bootstrap (reboot or install.sh).
			r.note("launchd job definition changed; it takes effect at the next boot or install.sh run")
		} else if !(prev.UnitSHA256 == r.st.UnitSHA256 && prev.RestartRequired) {
			r.st.RestartRequired = true
		}
	}
	if _, err := writeIfChanged(s.path("/etc/newsyslog.d/openlog-infra-agent.conf"), []byte(newsyslogConf), 0o644); err != nil {
		r.fail("newsyslog", err)
	}
	r.cliLink("/usr/local/bin")
	_ = ctx
}

// cliLink keeps <dir>/openlog-infra-agent -> <install root>/current/openlog-infra-agent.
func (r *reconciler) cliLink(dir string) {
	link := r.o.Sys.path(filepath.Join(dir, BinaryName))
	want := filepath.Join(r.o.Install.InstallRoot, "current", BinaryName)
	if t, err := os.Readlink(link); err == nil && t == want {
		return
	}
	if fi, err := os.Lstat(link); err == nil && fi.Mode()&os.ModeSymlink == 0 {
		return // a real file: not ours
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		r.fail("cli link", err)
		return
	}
	tmp := link + ".openlog-tmp"
	os.Remove(tmp)
	if err := os.Symlink(want, tmp); err != nil {
		r.fail("cli link", err)
		return
	}
	if err := os.Rename(tmp, link); err != nil {
		os.Remove(tmp)
		r.fail("cli link", err)
	}
}
