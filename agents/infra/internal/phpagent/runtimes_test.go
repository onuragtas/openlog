package phpagent

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	lib "github.com/onuragtas/openlog/libs/release"
)

func TestParsePHPInfo(t *testing.T) {
	text := "phpinfo()\nPHP Version => 8.3.11\n\nSystem => Linux\nPHP Extension => 20230831\nThread Safety => enabled\n" +
		"Debug Build => no\nScan this dir for additional .ini files => /usr/local/etc/php/conf.d\n" +
		"memory_limit => -1 => -1\nPHP Version => ignored duplicate\n"
	kv := ParsePHPInfo([]byte(text))
	want := map[string]string{"PHP Version": "8.3.11", "PHP Extension": "20230831", "Thread Safety": "enabled",
		"Debug Build": "no", "Scan this dir for additional .ini files": "/usr/local/etc/php/conf.d", "memory_limit": "-1"}
	for k, v := range want {
		if kv[k] != v {
			t.Errorf("%s = %q, want %q", k, kv[k], v)
		}
	}
	html := `<tr><td class="e">PHP Extension </td><td class="v">20220829 </td></tr>` + "\n" +
		`<tr><td class="e">Scan this dir for additional .ini files </td><td class="v">/etc/php/8.2/cgi/conf.d </td></tr>`
	kv = ParsePHPInfo([]byte(html))
	if kv["PHP Extension"] != "20220829" || kv["Scan this dir for additional .ini files"] != "/etc/php/8.2/cgi/conf.d" {
		t.Errorf("html = %v", kv)
	}
}

func TestDetectRuntimes(t *testing.T) {
	dir := t.TempDir()
	scan := filepath.Join(dir, "conf.d")
	os.MkdirAll(scan, 0o755)
	os.WriteFile(filepath.Join(scan, "90-openlog.ini"), []byte(iniMarker+" (openlog PHP agent 0.9.1)\nextension=x\n"), 0o644)
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name + " " + args[0] {
		case "/opt/php82/bin/php -i":
			return []byte("PHP Version => 8.2.1\nPHP Extension => 20220829\nThread Safety => disabled\nDebug Build => no\n" +
				"Scan this dir for additional .ini files => " + scan + "\n"), nil
		case "/opt/php82/bin/php -m":
			return []byte("[PHP Modules]\nopenlog\n"), nil
		case "/opt/php70/bin/php -i":
			return []byte("PHP Version => 7.0.33\nPHP Extension => 20151012\nThread Safety => enabled\nDebug Build => no\n" +
				"Scan this dir for additional .ini files => (none)\n"), nil
		case "/opt/debug/bin/php -i":
			return []byte("PHP Version => 8.3.0\nPHP Extension => 20230831\nDebug Build => yes\n"), nil
		}
		return nil, fmt.Errorf("exit 1")
	}
	rts := DetectRuntimes(context.Background(), run, []string{"/opt/php82/bin/php", "/opt/php70/bin/php", "/opt/debug/bin/php", "/bin/false"})
	if len(rts) != 3 {
		t.Fatalf("runtimes = %+v", rts)
	}
	libc := Libc()
	if r := rts[0]; r.Module != "20220829-nts-"+libc || !r.Supported || !r.Enabled || !r.Loaded || r.ZTS {
		t.Errorf("php 8.2 = %+v", r)
	}
	if r := rts[1]; r.Module != "20151012-zts-"+libc || r.Supported || r.ScanDir != "" || r.Loaded {
		t.Errorf("php 7.0 = %+v", r)
	}
	if r := rts[2]; r.Supported || !r.Debug {
		t.Errorf("debug build = %+v", r)
	}
	MarkExcluded(rts, []string{"/opt/php7*/bin/php"})
	if rts[0].Excluded || !rts[1].Excluded {
		t.Errorf("excluded = %v %v", rts[0].Excluded, rts[1].Excluded)
	}
}

func TestInstallerStatus(t *testing.T) {
	out := "openlog-php-install: no noise expected\n" +
		`{"bin":"/usr/sbin/php-fpm8.2","version":"8.2.29","api":"20220829","zts":false,"debug":false,"libc":"glibc","scan_dir":"/etc/php/8.2/fpm/conf.d","module":"20220829-nts-glibc","supported":true,"enabled":true,"loaded":true}` + "\n"
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if strings.Join(args, " ") != "status --json" {
			t.Errorf("args = %v", args)
		}
		return []byte(out), nil
	}
	rts, err := InstallerStatus(context.Background(), run, "/x/bin/openlog-php-install")
	if err != nil || len(rts) != 1 || !rts[0].Loaded || rts[0].ScanDir != "/etc/php/8.2/fpm/conf.d" {
		t.Fatalf("status = %+v, %v", rts, err)
	}
}

func TestInstalledAndManagedBy(t *testing.T) {
	root := filepath.Join(t.TempDir(), "php-agent")
	noPkg := func(context.Context, string, ...string) ([]byte, error) { return nil, fmt.Errorf("not owned") }
	if InstalledVersion(root) != "" || ManagedBy(context.Background(), noPkg, root) != ManagedNone {
		t.Error("missing root")
	}
	os.MkdirAll(filepath.Join(root, "versions", "0.9.1"), 0o755)
	os.Symlink("versions/0.9.1", filepath.Join(root, "current"))
	if InstalledVersion(root) != "0.9.1" || ManagedBy(context.Background(), noPkg, root) != ManagedManual {
		t.Error("manual install")
	}
	os.WriteFile(filepath.Join(root, MarkerFile), nil, 0o644)
	if ManagedBy(context.Background(), noPkg, root) != ManagedFleet {
		t.Error("fleet install")
	}
	os.Remove(filepath.Join(root, "current"))
	os.Symlink("versions/not-a-version", filepath.Join(root, "current"))
	if InstalledVersion(root) != "" {
		t.Error("invalid current accepted")
	}
}

func TestVerify(t *testing.T) {
	k := newKey(t)
	archive := phpTarball(t, "0.9.2", goodSO)
	manifest := phpManifest(t, "0.9.2", "https://example.com", archive, "0.9.0")
	sig := signLine(manifest, k)
	keys := []ed25519.PublicKey{k.pub}
	none := func() (*lib.Manifest, error) { return nil, nil }
	if v, err := Verify(manifest, sig, keys, "0.9.2", "linux", testArch, none); err != nil || v.Artifact.Component != lib.ComponentPHPAgent {
		t.Fatalf("verify = %+v, %v", v, err)
	}
	installed := func(floor string) func() (*lib.Manifest, error) {
		return func() (*lib.Manifest, error) {
			return lib.ParseManifest(phpManifest(t, "0.9.4", "https://example.com", archive, floor))
		}
	}
	for name, c := range map[string]struct {
		keys         []ed25519.PublicKey
		target, arch string
		installed    func() (*lib.Manifest, error)
		want         string
	}{
		"no keys":       {nil, "0.9.2", testArch, none, "no trusted release keys"},
		"other version": {keys, "0.9.3", testArch, none, "does not match"},
		"other arch":    {keys, "0.9.2", "arm64", none, "no php-agent linux/arm64 tar.gz"},
		"floor unknown": {keys, "0.9.2", testArch, installed(""), "rollback_floor of installed 0.9.4 unknown"},
		"below floor":   {keys, "0.9.2", testArch, installed("0.9.3"), "below rollback_floor 0.9.3"},
	} {
		if _, err := Verify(manifest, sig, c.keys, c.target, "linux", c.arch, c.installed); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	if _, err := Verify(manifest, sig, keys, "0.9.2", "linux", testArch, installed("0.9.1")); err != nil {
		t.Errorf("downgrade above the floor: %v", err)
	}
}
