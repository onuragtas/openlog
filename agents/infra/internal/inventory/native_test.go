package inventory

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestWindowsBaseName(t *testing.T) {
	for in, want := range map[string]string{
		`C:\nginx\nginx.exe`:                              "nginx",
		`"C:\Program Files\Redis\redis-server.EXE"`:       "redis-server",
		`C:/Program Files/PostgreSQL/16/bin/postgres.exe`: "postgres",
		"sqlservr.exe":                                    "sqlservr",
		"w3wp":                                            "w3wp",
		`C:\tools\.exe`:                                   ".exe",
		"":                                                ".",
	} {
		if got := WindowsBaseName(in); got != want {
			t.Errorf("WindowsBaseName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseLsof(t *testing.T) {
	out := "p501\ncnginx\nf6\ntIPv4\nPTCP\nn*:8080\nf7\ntIPv6\nPTCP\nn*:8080\n" +
		"p610\ncredis-server\nf6\ntIPv4\nPTCP\nn127.0.0.1:6379\nf7\ntIPv6\nPTCP\nn[::1]:6379\n" +
		"p700\ncmDNSResponder\nf10\ntIPv4\nPUDP\nn*:5353\nf11\ntIPv6\nPUDP\nn[fe80::1%lo0]:123\nf12\ntIPv4\nPUDP\nn10.0.0.2:5000->10.0.0.9:6000\n" +
		"p800\ncweird\nf3\ntIPv4\nPTCP\nnno-port\n"
	got := ParseLsof(out)
	var keys []string
	for _, p := range got {
		keys = append(keys, p.Key())
	}
	want := []string{"tcp:0.0.0.0:8080", "tcp:127.0.0.1:6379", "tcp:[::1]:6379", "tcp:[::]:8080", "udp:0.0.0.0:5353", "udp:[fe80::1]:123"}
	if !slices.Equal(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	for _, p := range got {
		if p.Key() == "tcp:127.0.0.1:6379" && (p.PID != 610 || p.ProcessName != "redis-server" || p.Family != 4) {
			t.Errorf("redis port = %+v", p)
		}
		if p.Key() == "tcp:[::]:8080" && p.Family != 6 {
			t.Errorf("ipv6 wildcard = %+v", p)
		}
	}
}

func TestParseLaunchctlList(t *testing.T) {
	out := "PID\tStatus\tLabel\n-\t0\tcom.apple.sshd\n501\t0\thomebrew.mxcl.nginx\n-\t-9\thomebrew.mxcl.redis\n612\t-\torg.openlog.infra-agent\nbroken line\n"
	got := ParseLaunchctlList(out)
	if len(got) != 4 {
		t.Fatalf("got %d jobs: %+v", len(got), got)
	}
	byLabel := map[string]LaunchdService{}
	for _, s := range got {
		byLabel[s.Label] = s
	}
	if s := byLabel["homebrew.mxcl.nginx"]; s.PID == nil || *s.PID != 501 || s.LastExitStatus == nil || *s.LastExitStatus != 0 {
		t.Errorf("nginx = %+v", s)
	}
	if s := byLabel["homebrew.mxcl.redis"]; s.PID != nil || s.LastExitStatus == nil || *s.LastExitStatus != -9 {
		t.Errorf("redis = %+v", s)
	}
	if s := byLabel["org.openlog.infra-agent"]; s.PID == nil || s.LastExitStatus != nil {
		t.Errorf("agent = %+v", s)
	}
}

func TestParseDscacheutilUsers(t *testing.T) {
	out := "name: _www\npassword: *\nuid: 70\ngid: 70\ndir: /Library/WebServer\nshell: /usr/bin/false\ngecos: World Wide Web Server\n\n" +
		"name: alice\npassword: ********\nuid: 501\ngid: 20\ndir: /Users/alice\nshell: /bin/zsh\ngecos: Alice\n\nname: broken\nuid: x\n"
	got := ParseDscacheutilUsers(out)
	want := []User{{Name: "_www", UID: 70, GID: 70, Home: "/Library/WebServer", Shell: "/usr/bin/false"},
		{Name: "alice", UID: 501, GID: 20, Home: "/Users/alice", Shell: "/bin/zsh"}}
	if !slices.Equal(got, want) {
		t.Errorf("users = %+v", got)
	}
}

func TestHomebrewPackages(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"Cellar/nginx/1.25.3", "Cellar/nginx/1.27.4", "Cellar/postgresql@16/16.8", "Cellar/redis/7.2.10",
		"Cellar/redis/7.2.9", "Caskroom/docker/4.38.0,181591", "Cellar/.keep", "Cellar/empty"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := HomebrewPackages([]string{root, filepath.Join(root, "missing")})
	want := []Package{
		{Manager: "homebrew", Name: "docker", Version: "4.38.0,181591"},
		{Manager: "homebrew", Name: "nginx", Version: "1.27.4"},
		{Manager: "homebrew", Name: "postgresql@16", Version: "16.8"},
		{Manager: "homebrew", Name: "redis", Version: "7.2.10"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("packages = %+v", got)
	}
}

func TestSystemServiceItems(t *testing.T) {
	pid := 42
	d := &Data{Services: []SystemService{
		{Manager: ServiceManagerLaunchd, Name: "homebrew.mxcl.nginx", PID: 42, Body: LaunchdService{Label: "homebrew.mxcl.nginx", PID: &pid, Domain: "system"}},
		{Manager: ServiceManagerWindows, Name: "W3SVC", Body: WindowsService{Name: "W3SVC", State: "running", StartType: "automatic"}},
	}}
	items := Normalize(d.Items())
	if len(items) != 2 || items[0].Category != CategoryLaunchdService || items[1].Category != CategoryWindowsService || items[1].Key != "W3SVC" {
		t.Errorf("items = %+v", items)
	}
}
