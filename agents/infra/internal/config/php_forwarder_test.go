package config

import (
	"strings"
	"testing"
	"time"
)

func TestPHPForwarderConfig(t *testing.T) {
	d := DefaultFor("linux").PHPForwarder
	if d.Enabled != nil || d.Socket != DefaultPHPSocket || d.SocketGroup != "auto" || d.Mode() != 0o660 ||
		d.MaxPendingTraces != 10000 || d.ReassemblyTimeout.D() != 5*time.Second || d.UDPListen != "" {
		t.Errorf("defaults = %+v", d)
	}

	cfg := DefaultFor("linux")
	if err := Parse([]byte("php_forwarder:\n  enabled: false\n  socket_mode: \"0666\"\n  udp_listen: 127.0.0.1:18127\n"), cfg); err != nil {
		t.Fatal(err)
	}
	if p := cfg.PHPForwarder; p.Enabled == nil || *p.Enabled || p.Mode() != 0o666 || p.UDPListen != "127.0.0.1:18127" || p.Socket != DefaultPHPSocket {
		t.Errorf("parsed = %+v", p)
	}
	if err := cfg.Validate(false); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}

	cases := map[string]string{
		"socket: relative.sock":                   "absolute",
		"socket: /" + strings.Repeat("a", 120):    "107 bytes",
		"socket: \"\"":                            "socket or udp_listen",
		"socket_mode: \"0777\"":                   "socket_mode",
		"udp_listen: localhost":                   "udp_listen",
		"max_pending_traces: 0":                   "max_pending_traces",
		"reassembly_timeout: 10ms":                "reassembly_timeout",
		"socket: \"\"\n  udp_listen: 127.0.0.1:1": "",
	}
	for yml, want := range cases {
		cfg := Default()
		if err := Parse([]byte("php_forwarder:\n  "+yml+"\n"), cfg); err != nil {
			t.Fatalf("%s: %v", yml, err)
		}
		err := cfg.Validate(false)
		if want == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", yml, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", yml, err, want)
		}
	}
	if err := Parse([]byte("php_forwarder:\n  socket_owner: x\n"), Default()); err == nil {
		t.Error("unknown php_forwarder key must be rejected")
	}
}
