package update

import (
	"bytes"
	"encoding/xml"
	"runtime"
	"testing"

	"github.com/onuragtas/openlog/agents/infra/packaging"
)

func TestRenderLaunchdPlistMatchesPackaging(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("BinaryName has .exe on Windows")
	}
	got := RenderLaunchdPlist("/opt/openlog/infra-agent", "/etc/openlog-infra-agent/config.yaml")
	if !bytes.Equal(got, packaging.LaunchdPlist) {
		t.Errorf("packaging/launchd/org.openlog.infra-agent.plist is out of date; rendered:\n%s", got)
	}
	// Paths are XML-escaped.
	odd := RenderLaunchdPlist("/opt/a&b", "/etc/<x>.yaml")
	if err := xml.Unmarshal(odd, new(struct{})); err != nil {
		t.Errorf("rendered plist is not well-formed: %v", err)
	}
	if !bytes.Contains(odd, []byte("/opt/a&amp;b/current/")) || !bytes.Contains(odd, []byte("/etc/&lt;x&gt;.yaml")) {
		t.Errorf("paths not escaped:\n%s", odd)
	}
}
