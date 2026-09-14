package main

// "-configure": create the configuration file when missing and set license_key and endpoint, keeping everything
// else (comments included). Used by install.sh (macOS), install.ps1 and the MSI (D-104).

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/update"
	"github.com/onuragtas/openlog/agents/infra/packaging"
)

// minimalConfig is the new configuration of macOS and Windows hosts: every other setting keeps the OS default
// (packaging/config.example.yaml documents all keys with Linux paths).
const minimalConfig = `# openlog infrastructure agent configuration. Settings not listed here use the defaults for this
# operating system; every key is documented in packaging/config.example.yaml and agents/infra/README.md.
license_key: ""
endpoint: ""
`

func runConfigure(path, licenseKey, endpoint string, setKey, setEndpoint bool) int {
	fail := func(format string, a ...any) int {
		fmt.Fprintf(os.Stderr, "configure: "+format+"\n", a...)
		return 1
	}
	if setKey && (licenseKey == "" || strings.ContainsAny(licenseKey, "\"\\\r\n\t ")) {
		return fail("-license-key is empty or contains quotes, backslashes or whitespace")
	}
	if setEndpoint {
		u, err := url.Parse(endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsAny(endpoint, "\"\\\r\n\t ") {
			return fail("-endpoint must be an http(s) URL without quotes or whitespace")
		}
	}
	dir := filepath.Dir(path)
	if err := update.SecureDir(dir); err != nil {
		return fail("%s: %v", dir, err)
	}
	data, err := os.ReadFile(path)
	created := false
	mode := os.FileMode(0o600)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		created = true
		data = []byte(minimalConfig)
		if runtime.GOOS == "linux" {
			data, mode = packaging.ConfigExample, 0o640
		}
	case err != nil:
		return fail("%v", err)
	default:
		if fi, err := os.Stat(path); err == nil {
			mode = fi.Mode().Perm()
		}
	}
	if setKey {
		data = SetTopLevelKey(data, "license_key", licenseKey)
	}
	if setEndpoint {
		data = SetTopLevelKey(data, "endpoint", endpoint)
	}
	var probe config.Config
	if err := config.Parse(data, &probe); err != nil {
		return fail("the resulting configuration would be invalid: %v", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return fail("%v", err)
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return fail("%v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		// Windows cannot rename over a file another process holds open without FILE_SHARE_DELETE.
		if err := os.WriteFile(path, data, mode); err != nil {
			os.Remove(tmp)
			return fail("%v", err)
		}
		os.Remove(tmp)
	}
	if runtime.GOOS == "linux" && created {
		// Like install.sh: readable by the agent group, writable by root only.
		if g, err := user.LookupGroup(update.AgentUser); err == nil {
			if gid, err := strconv.Atoi(g.Gid); err == nil {
				_ = os.Chown(path, 0, gid)
			}
		}
	} else if err := update.SecureFile(path); err != nil {
		return fail("restricting %s: %v", path, err)
	}
	verb := "updated"
	if created {
		verb = "created"
	}
	fmt.Printf("configuration %s %s\n", path, verb)
	if cfg, err := config.Load(path, false); err == nil && cfg.LicenseKey == "" {
		fmt.Println("license_key is empty: pass -license-key")
	}
	return 0
}

// SetTopLevelKey sets a top-level scalar key of a YAML document line by line ("key: \"value\""), replacing the first
// line that starts with "key:" or appending one. Comments and everything else stay as they are.
func SetTopLevelKey(data []byte, key, value string) []byte {
	line := key + `: "` + value + `"`
	eol := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		eol = "\r\n"
	}
	lines := strings.SplitAfter(string(data), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, key+":") {
			lines[i] = line + eol
			if !strings.HasSuffix(l, "\n") {
				lines[i] = line
			}
			return []byte(strings.Join(lines, ""))
		}
	}
	s := string(data)
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += eol
	}
	return []byte(s + line + eol)
}
