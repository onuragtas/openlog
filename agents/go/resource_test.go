package openlog

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

// fixture writes files below a temp root, like the infra agent's hostfstest.
func fixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, content := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func noPlatformID(t *testing.T) {
	old := platformHostID
	platformHostID = func(context.Context) string { return "" }
	t.Cleanup(func() { platformHostID = old })
}

// The fixtures mirror agents/infra/internal/resource TestHostIDChain so both agents resolve
// the same host.id on the same machine.
func TestHostIDChainMatchesInfraAgent(t *testing.T) {
	noPlatformID(t)
	ctx := context.Background()
	infra := "/var/lib/openlog-infra-agent"
	cases := []struct {
		name, want, source string
		files              map[string]string
	}{
		{"machine-id", "0123456789abcdef0123456789abcdef", "/etc/machine-id", map[string]string{
			"/etc/machine-id": "0123456789abcdef0123456789abcdef\n", "/var/lib/dbus/machine-id": "ffffffffffffffffffffffffffffffff\n"}},
		{"dbus fallback lower-cased", "abcdef0123456789abcdef0123456789", "/var/lib/dbus/machine-id", map[string]string{
			"/etc/machine-id": "\n", "/var/lib/dbus/machine-id": "ABCDEF0123456789abcdef0123456789\n",
			"/sys/class/dmi/id/product_uuid": "11111111-2222-3333-4444-555555555555\n"}},
		{"dmi fallback", "11111111-2222-3333-4444-555555555555", "/sys/class/dmi/id/product_uuid", map[string]string{
			"/sys/class/dmi/id/product_uuid": "11111111-2222-3333-4444-555555555555\n"}},
		{"too short machine-id ignored", "11111111-2222-3333-4444-555555555555", "/sys/class/dmi/id/product_uuid", map[string]string{
			"/etc/machine-id": "abc\n", "/sys/class/dmi/id/product_uuid": "11111111-2222-3333-4444-555555555555\n"}},
		{"zero dmi ignored, infra state file used", "9b2c7d7e-1f0a-4c1e-9a55-6a3f1c2b8d10", hostIDSourceInfra, map[string]string{
			"/sys/class/dmi/id/product_uuid":              "00000000-0000-0000-0000-000000000000\n",
			"/var/lib/openlog-infra-agent/host-id":        "9b2c7d7e-1f0a-4c1e-9a55-6a3f1c2b8d10\n",
			"/var/lib/openlog-infra-agent/host-id.tmp":    "garbage",
			"/var/lib/openlog-infra-agent/unrelated-file": "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := fixture(t, tc.files)
			id, src := resolveHostID(ctx, hostFS{root: root}, "", infra, t.TempDir())
			eq(t, id, tc.want)
			eq(t, src, tc.source)
		})
	}

	t.Run("generated and persisted", func(t *testing.T) {
		root := fixture(t, map[string]string{"/etc/machine-id": "\n"})
		state := t.TempDir()
		id, src := resolveHostID(ctx, hostFS{root: root}, "", infra, state)
		if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
			t.Fatalf("generated id %q", id)
		}
		eq(t, src, hostIDSourceGenerated)
		again, _ := resolveHostID(ctx, hostFS{root: root}, "", infra, state)
		eq(t, again, id)
	})
}

func TestContainerID(t *testing.T) {
	const id = "3f4b1a2c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f7a8"
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"cgroup v1 docker", map[string]string{"/proc/self/cgroup": "12:memory:/docker/" + id + "\n11:cpu:/docker/" + id + "\n"}, id},
		{"cgroup v2 systemd scope", map[string]string{"/proc/self/cgroup": "0::/system.slice/docker-" + id + ".scope\n"}, id},
		{"kubernetes containerd", map[string]string{"/proc/self/cgroup": "0::/kubepods.slice/kubepods-burstable.slice/kubepods-pod1.slice/cri-containerd-" + id + ".scope\n"}, id},
		{"cgroup namespace uses mountinfo", map[string]string{
			"/proc/self/cgroup": "0::/\n",
			"/proc/self/mountinfo": "600 580 0:50 / / rw,relatime - overlay overlay rw\n" +
				"612 600 254:1 /docker/containers/" + id + "/resolv.conf /etc/resolv.conf rw,relatime - ext4 /dev/vda1 rw\n"}, id},
		{"sandbox mount skipped", map[string]string{
			"/proc/self/cgroup":    "0::/\n",
			"/proc/self/mountinfo": "1 0 0:1 /var/lib/containerd/io.containerd.grpc.v1.cri/sandboxes/" + id + "/hostname /etc/hostname rw - ext4 /dev/sda rw\n"}, ""},
		{"not in a container", map[string]string{"/proc/self/cgroup": "0::/user.slice/user-1000.slice/session-2.scope\n"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eq(t, containerID(hostFS{root: fixture(t, tc.files)}), tc.want)
		})
	}
}

func TestK8sAttributes(t *testing.T) {
	root := fixture(t, map[string]string{"/var/run/secrets/kubernetes.io/serviceaccount/namespace": "shop\n"})
	got := k8sAttributes(hostFS{root: root}, env(map[string]string{
		"KUBERNETES_SERVICE_HOST": "10.0.0.1", "HOSTNAME": "api-7d9f-abc", "K8S_NODE_NAME": "node-1", "K8S_POD_UID": "uid-1",
	}))
	eq(t, got["k8s.pod.name"], "api-7d9f-abc")
	eq(t, got["k8s.namespace.name"], "shop")
	eq(t, got["k8s.node.name"], "node-1")
	eq(t, got["k8s.pod.uid"], "uid-1")
	if len(k8sAttributes(hostFS{root: root}, env(map[string]string{"HOSTNAME": "laptop"}))) != 0 {
		t.Error("k8s attributes outside a pod")
	}
}

func TestBuildResource(t *testing.T) {
	noPlatformID(t)
	root := fixture(t, map[string]string{
		"/etc/machine-id":            "0123456789abcdef0123456789abcdef\n",
		"/etc/os-release":            "ID=debian\nVERSION_ID=\"12\"\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n",
		"/proc/sys/kernel/hostname":  "web-01\n",
		"/proc/sys/kernel/osrelease": "6.1.0-18-amd64\n",
		"/proc/sys/kernel/arch":      "x86_64\n",
	})
	cfg, _, err := loadConfig(env(map[string]string{
		"OPENLOG_HOST_ROOT": root, "OPENLOG_SERVICE_NAME": "checkout", "OPENLOG_SERVICE_VERSION": "1.2.3",
		"OPENLOG_ENVIRONMENT": "production", "OPENLOG_SERVICE_NAMESPACE": "shop", "OPENLOG_STATE_DIR": t.TempDir(),
		"OPENLOG_RESOURCE_ATTRIBUTES": "team=payments,service.name=ignored,host.name=override-name",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	res, source, err := buildResource(context.Background(), cfg, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, kv := range res.Attributes() {
		got[string(kv.Key)] = kv.Value.Emit()
	}
	want := map[string]string{
		"service.name": "checkout", "service.version": "1.2.3", "service.namespace": "shop",
		"deployment.environment.name": "production", "telemetry.distro.name": "openlog", "telemetry.distro.version": Version,
		"telemetry.sdk.language": "go", "team": "payments", "host.name": "override-name", "process.runtime.name": "go",
	}
	if runtime.GOOS == "linux" {
		for k, v := range map[string]string{"host.id": "0123456789abcdef0123456789abcdef", "os.name": "debian", "os.version": "12",
			"os.description": "Debian GNU/Linux 12 (bookworm)", "host.arch": "amd64", "openlog.os.kernel_release": "6.1.0-18-amd64", "os.type": "linux"} {
			want[k] = v
		}
	}
	// host.id is resolved from the fixture on every OS (the file chain is not Linux-specific).
	want["host.id"] = "0123456789abcdef0123456789abcdef"
	eq(t, source, "/etc/machine-id")
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if got["process.pid"] == "" || got["process.executable.name"] == "" {
		t.Errorf("process attributes missing: %v", got)
	}

	// An explicit host id wins over detection and user attributes.
	cfg.HostID = "explicit"
	cfg.ResourceAttributes["host.id"] = "from-attrs"
	res, source, _ = buildResource(context.Background(), cfg, env(nil))
	v, _ := res.Set().Value("host.id")
	eq(t, v.AsString(), "explicit")
	eq(t, source, hostIDSourceConfig)
}
