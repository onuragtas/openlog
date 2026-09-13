package resource

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/onuragtas/openlog/agents/infra/internal/hostfs/hostfstest"
)

func TestHostIDChain(t *testing.T) {
	state := t.TempDir()
	fs := hostfstest.Build(t, map[string]string{
		"/etc/machine-id":                "\n",
		"/var/lib/dbus/machine-id":       "ABCDEF0123456789abcdef0123456789\n",
		"/sys/class/dmi/id/product_uuid": "11111111-2222-3333-4444-555555555555\n",
	})
	id, ok, err := HostID(fs, state)
	if err != nil || !ok || id != "abcdef0123456789abcdef0123456789" {
		t.Errorf("dbus fallback: %q %v %v", id, ok, err)
	}

	fs = hostfstest.Build(t, map[string]string{
		"/sys/class/dmi/id/product_uuid": "11111111-2222-3333-4444-555555555555\n",
	})
	if id, _, _ := HostID(fs, state); id != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("dmi fallback: %q", id)
	}

	// All-zero DMI UUIDs are placeholders and must be ignored.
	fs = hostfstest.Build(t, map[string]string{
		"/sys/class/dmi/id/product_uuid": "00000000-0000-0000-0000-000000000000\n",
	})
	gen, ok, err := HostID(fs, state)
	if err != nil || !ok || len(gen) != 36 {
		t.Fatalf("generated: %q %v %v", gen, ok, err)
	}
	again, _, _ := HostID(fs, state)
	if again != gen {
		t.Errorf("generated id not persisted: %q != %q", again, gen)
	}
	if _, err := os.Stat(filepath.Join(state, HostIDFile)); err != nil {
		t.Error(err)
	}
}

func TestDetectAndProto(t *testing.T) {
	fs := hostfstest.Build(t, map[string]string{
		"/etc/os-release":            "ID=debian\nVERSION_ID=\"12\"\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n",
		"/proc/sys/kernel/hostname":  "web-01\n",
		"/proc/sys/kernel/osrelease": "6.1.0-18-amd64\n",
		"/proc/sys/kernel/arch":      "x86_64\n",
	})
	info := Detect(fs, "hid", "0.1.0", map[string]string{"env": "prod", "host.id": "evil"})
	got := map[string]string{}
	for _, kv := range info.Proto().Attributes {
		if _, dup := got[kv.Key]; dup {
			t.Errorf("duplicate attribute %s", kv.Key)
		}
		got[kv.Key] = kv.Value.GetStringValue()
	}
	want := map[string]string{
		"host.id": "hid", "host.name": "web-01", "host.arch": "amd64", "os.type": "linux",
		"os.name": "debian", "os.version": "12", "os.description": "Debian GNU/Linux 12 (bookworm)",
		"openlog.os.kernel_release": "6.1.0-18-amd64", "openlog.entity.type": "host",
		"openlog.agent.name": "openlog-infra-agent", "openlog.agent.version": "0.1.0", "env": "prod",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("attributes = %v", got)
	}
}
