package config

import (
	"strings"
	"testing"
)

func TestContainersCRIAndMultilineConfig(t *testing.T) {
	cfg := Default()
	if len(cfg.Containers.CRISockets) != 3 || cfg.Containers.CRISockets[0] != "/run/containerd/containerd.sock" {
		t.Errorf("cri_sockets default = %v", cfg.Containers.CRISockets)
	}
	yml := `
containers:
  cri_sockets: [/run/containerd/containerd.sock]
logs:
  containers:
    include:
      - { name: "billing*", multiline_start: '^\d{4}-' }
      - { name: "*" }
`
	if err := Parse([]byte(yml), cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(false); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Containers.CRISockets) != 1 || cfg.Logs.Containers.Include[0].MultilineStart != `^\d{4}-` {
		t.Errorf("parsed = %+v %+v", cfg.Containers, cfg.Logs.Containers.Include)
	}
	bad := Default()
	bad.Containers.CRISockets = []string{"run/crio.sock"}
	bad.Logs.Containers.Include = []ContainerMatch{{Name: "a", MultilineStart: "("}, {MultilineStart: "^x"}}
	bad.Logs.Containers.Exclude = []ContainerMatch{{Name: "b", MultilineStart: "^x"}}
	err := bad.Validate(false)
	for _, want := range []string{"containers.cri_sockets[0]", "include[0].multiline_start", "include[1] must set", "exclude[0]: multiline_start is only allowed"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}
