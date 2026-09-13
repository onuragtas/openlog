package integrations

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/onuragtas/openlog/agents/infra/internal/containers"
	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
	"github.com/onuragtas/openlog/agents/infra/internal/inventory"
)

const tcpHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

func tcpLine(addrPort, inode string) string {
	return "   0: " + addrPort + " 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 " + inode + "\n"
}

// Recorded from a Docker bridge container (trimmed).
const fibTrie = `Main:
  +-- 0.0.0.0/0 3 0 5
     |-- 0.0.0.0
        /0 universe UNICAST
     +-- 127.0.0.0/8 2 0 2
        +-- 127.0.0.0/31 1 0 0
           |-- 127.0.0.0
              /8 host LOCAL
           |-- 127.0.0.1
              /32 host LOCAL
        |-- 127.255.255.255
           /32 link BROADCAST
     +-- 172.17.0.0/16 2 0 2
        +-- 172.17.0.0/30 2 0 2
           |-- 172.17.0.0
              /16 link UNICAST
           |-- 172.17.0.2
              /32 host LOCAL
        |-- 172.17.255.255
           /32 link BROADCAST
Local:
  +-- 0.0.0.0/0 3 0 5
           |-- 172.17.0.2
              /32 host LOCAL
`

func writeTree(t *testing.T, root string, files map[string]string, links map[string]string) {
	t.Helper()
	for p, content := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for p, target := range links {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, full); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProcessContainersWithoutRuntimeAPI(t *testing.T) {
	root := t.TempDir()
	nginxID := strings.Repeat("a", 64)
	redisID := strings.Repeat("b", 64)
	hostNetID := strings.Repeat("c", 64)
	writeTree(t, root, map[string]string{
		// Host (PID 1 under a root path): the docker-proxy socket of the published port.
		"proc/1/net/tcp": tcpHeader + tcpLine("00000000:1F90", "900"),
		// nginx container: 0.0.0.0:80 plus Docker's embedded DNS on 127.0.0.11.
		"proc/200/net/tcp":      tcpHeader + tcpLine("00000000:0050", "500") + tcpLine("0B00007F:9C40", "501"),
		"proc/200/net/fib_trie": fibTrie,
		// redis container: fd links unreadable, no fib_trie, no docker-proxy → only the private port.
		"proc/300/net/tcp": tcpHeader + tcpLine("00000000:18EB", "600"),
		// host-network container: shares the host namespace.
		"proc/400/net/tcp": tcpHeader + tcpLine("00000000:1F90", "900"),
	}, map[string]string{
		"proc/1/ns/net":   "net:[4026531840]",
		"proc/200/ns/net": "net:[4026532001]",
		"proc/200/fd/3":   "socket:[500]",
		"proc/200/fd/4":   "socket:[501]",
		"proc/201/fd/3":   "socket:[500]",
		"proc/300/ns/net": "net:[4026532002]",
		"proc/400/ns/net": "net:[4026531840]",
	})
	fs := hostfs.New(root)
	instances := []inventory.ProcessInstance{
		{PID: 150, Exe: "/usr/bin/docker-proxy", Cmdline: "/usr/bin/docker-proxy -proto tcp -host-ip 0.0.0.0 -host-port 8080 -container-ip 172.17.0.2 -container-port 80"},
		{PID: 151, Exe: "/usr/bin/docker-proxy", Cmdline: "/usr/bin/docker-proxy -proto tcp -host-ip :: -host-port 8080 -container-ip 172.17.0.2 -container-port 80"},
		{PID: 152, Exe: "/usr/bin/docker-proxy", Cmdline: "/usr/bin/docker-proxy -proto udp -host-ip 0.0.0.0 -host-port 53 -container-ip 172.17.0.9 -container-port 53"},
		{PID: 200, Exe: "/usr/sbin/nginx", ContainerID: nginxID},
		{PID: 201, Exe: "", Comm: "nginx", ContainerID: nginxID},
		{PID: 300, Exe: "/usr/local/bin/redis-server", ContainerID: redisID},
		{PID: 400, Exe: "/usr/bin/app", ContainerID: hostNetID},
	}
	known := []containers.Container{{ID: "d" + strings.Repeat("0", 63), Name: "described", IPs: []string{"172.18.0.5"}}}
	got := ProcessContainers(fs, instances, known)

	want := []containers.Container{
		known[0],
		{ID: nginxID, IPs: []string{"172.17.0.2"}, Ports: []containers.Port{
			{PrivatePort: 80, Protocol: "tcp"}, {IP: "0.0.0.0", PrivatePort: 80, PublicPort: 8080, Protocol: "tcp"},
		}},
		{ID: redisID, Ports: []containers.Port{{PrivatePort: 6379, Protocol: "tcp"}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("containers =\n%+v\nwant\n%+v", got, want)
	}

	// Endpoints of the containerized nginx: published port, then the container address.
	eps := displays(DeriveEndpoints(Target{Containers: got[1:2]}, EndpointSpec{DefaultPort: 80}, fs))
	if strings.Join(eps, ",") != "127.0.0.1:8080,172.17.0.2:80" {
		t.Errorf("nginx endpoints = %v", eps)
	}
	// redis: no address known → loopback default port.
	eps = displays(DeriveEndpoints(Target{Containers: got[2:3]}, EndpointSpec{DefaultPort: 6379}, fs))
	if strings.Join(eps, ",") != "127.0.0.1:6379" {
		t.Errorf("redis endpoints = %v", eps)
	}

	// Everything described by the runtime API: nothing to add.
	if n := len(ProcessContainers(fs, instances[:3], known)); n != 1 {
		t.Errorf("no container processes: %d containers", n)
	}
}

func TestLocalAddresses(t *testing.T) {
	if got := LocalAddresses(fibTrie); !reflect.DeepEqual(got, []string{"172.17.0.2"}) {
		t.Errorf("LocalAddresses = %v", got)
	}
	if got := LocalAddresses(""); got != nil {
		t.Errorf("empty = %v", got)
	}
}
