package discovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/hostfs/hostfstest"
	"github.com/onuragtas/openlog/agents/infra/internal/inventory"
	"github.com/onuragtas/openlog/agents/infra/internal/testfixtures"
	"github.com/onuragtas/openlog/agents/infra/rules"

	"gopkg.in/yaml.v3"
)

func embedded(t *testing.T) []*Rule {
	t.Helper()
	rs, err := LoadFS(rules.FS, "rules")
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

// caseFile is a fixture describing inventory data and the expected services.
type caseFile struct {
	Name string `yaml:"name"`
	// Platform is inventory.Data.Platform ("" Linux, darwin, windows).
	Platform string `yaml:"platform"`
	Services []struct {
		Manager string `yaml:"manager"`
		Name    string `yaml:"name"`
		PID     int    `yaml:"pid"`
	} `yaml:"services"`
	Processes []struct {
		PID       int    `yaml:"pid"`
		PPID      int    `yaml:"ppid"`
		Exe       string `yaml:"exe"`
		Comm      string `yaml:"comm"`
		Cmdline   string `yaml:"cmdline"`
		Unit      string `yaml:"unit"`
		Container string `yaml:"container"`
	} `yaml:"processes"`
	Units []struct {
		Name  string `yaml:"name"`
		State string `yaml:"state"`
	} `yaml:"units"`
	Ports []struct {
		Protocol string `yaml:"protocol"`
		Address  string `yaml:"address"`
		Port     int    `yaml:"port"`
		PID      int    `yaml:"pid"`
	} `yaml:"ports"`
	Packages []struct {
		Manager string `yaml:"manager"`
		Name    string `yaml:"name"`
		Version string `yaml:"version"`
	} `yaml:"packages"`
	Containers []inventory.Container `yaml:"containers"`
	Expect     []struct {
		RuleID    string   `yaml:"rule_id"`
		Instance  string   `yaml:"instance"`
		Display   *string  `yaml:"display_instance"` // nil: not checked; "" must be omitted
		Command   string   `yaml:"command"`
		Version   string   `yaml:"version"`
		MatchedBy []string `yaml:"matched_by"`
		PIDs      []int    `yaml:"pids"`
		Ports     []int    `yaml:"ports"`
		Units     []string `yaml:"units"`
		Services  []string `yaml:"services"`
		LogPaths  []string `yaml:"log_paths"`
		Packages  []string `yaml:"packages"`
		Status    string   `yaml:"status"`
	} `yaml:"expect"`
	Forbid      []string `yaml:"forbid"`
	ExactlyKeys []string `yaml:"exactly_keys"` // when set, the full discovered key set
}

func (c *caseFile) data() *inventory.Data {
	d := &inventory.Data{Containers: c.Containers, Platform: c.Platform}
	for _, s := range c.Services {
		d.Services = append(d.Services, inventory.SystemService{Manager: s.Manager, Name: s.Name, PID: s.PID})
	}
	for _, p := range c.Processes {
		comm := p.Comm
		if comm == "" {
			comm = filepath.Base(p.Exe)
		}
		d.Instances = append(d.Instances, inventory.ProcessInstance{PID: p.PID, PPID: p.PPID, Exe: p.Exe, Comm: comm, Cmdline: p.Cmdline,
			SystemdUnit: p.Unit, ContainerID: p.Container})
	}
	for _, u := range c.Units {
		st := u.State
		if st == "" {
			st = "enabled"
		}
		d.Units = append(d.Units, inventory.SystemdUnit{Name: u.Name, EnabledState: st})
	}
	for _, p := range c.Ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		lp := inventory.ListeningPort{Protocol: proto, Address: p.Address, Port: p.Port, PID: p.PID}
		for _, in := range d.Instances {
			if in.PID == p.PID {
				lp.ProcessExe, lp.ProcessName = in.Exe, in.Comm
			}
		}
		d.Ports = append(d.Ports, lp)
	}
	for _, p := range c.Packages {
		m := p.Manager
		if m == "" {
			m = "dpkg"
		}
		d.Packages = append(d.Packages, inventory.Package{Manager: m, Name: p.Name, Version: p.Version})
	}
	return d
}

func TestRuleCases(t *testing.T) {
	engine := NewEngine(embedded(t))
	files, err := filepath.Glob("testdata/cases/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no case files: %v", err)
	}
	covered := map[string]bool{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var c caseFile
		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		dec.KnownFields(true)
		if err := dec.Decode(&c); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		t.Run(filepath.Base(f), func(t *testing.T) {
			got := engine.Discover(c.data())
			for _, e := range c.Expect {
				covered[e.RuleID] = true
				var s *Service
				for i := range got {
					if got[i].RuleID == e.RuleID && (e.Instance == "" || got[i].Instance == e.Instance) {
						s = &got[i]
						break
					}
				}
				if s == nil {
					t.Errorf("expected %s (instance %q) not discovered; got %v", e.RuleID, e.Instance, keys(got))
					continue
				}
				if e.Display != nil && s.DisplayInstance != *e.Display {
					t.Errorf("%s: display_instance = %q, want %q", s.Key(), s.DisplayInstance, *e.Display)
				}
				if e.Command != "" && s.Command != e.Command {
					t.Errorf("%s: command = %q, want %q", s.Key(), s.Command, e.Command)
				}
				if e.Version != "" && s.Version != e.Version {
					t.Errorf("%s: version = %q, want %q", s.Key(), s.Version, e.Version)
				}
				if e.MatchedBy != nil && !slices.Equal(s.MatchedBy, e.MatchedBy) {
					t.Errorf("%s: matched_by = %v, want %v", s.Key(), s.MatchedBy, e.MatchedBy)
				}
				if e.PIDs != nil && !slices.Equal(s.PIDs, e.PIDs) {
					t.Errorf("%s: pids = %v, want %v", s.Key(), s.PIDs, e.PIDs)
				}
				if e.Ports != nil {
					var ports []int
					for _, p := range s.Ports {
						ports = append(ports, p.Port)
					}
					if !slices.Equal(ports, e.Ports) {
						t.Errorf("%s: ports = %v, want %v", s.Key(), ports, e.Ports)
					}
				}
				if e.Units != nil && !slices.Equal(s.SystemdUnits, e.Units) {
					t.Errorf("%s: units = %v, want %v", s.Key(), s.SystemdUnits, e.Units)
				}
				if e.Services != nil && !slices.Equal(s.Services, e.Services) {
					t.Errorf("%s: services = %v, want %v", s.Key(), s.Services, e.Services)
				}
				if e.LogPaths != nil && !slices.Equal(s.LogPaths, e.LogPaths) {
					t.Errorf("%s: log_paths = %v, want %v", s.Key(), s.LogPaths, e.LogPaths)
				}
				if e.Packages != nil && !slices.Equal(s.Packages, e.Packages) {
					t.Errorf("%s: packages = %v, want %v", s.Key(), s.Packages, e.Packages)
				}
				if e.Status != "" && s.Integration.Status != e.Status {
					t.Errorf("%s: integration status = %q, want %q", s.Key(), s.Integration.Status, e.Status)
				}
			}
			if c.ExactlyKeys != nil && !slices.Equal(keys(got), c.ExactlyKeys) {
				t.Errorf("discovered keys = %v, want exactly %v", keys(got), c.ExactlyKeys)
			}
			for _, id := range c.Forbid {
				for _, s := range got {
					if s.RuleID == id {
						t.Errorf("rule %s must not match, got %s", id, s.Key())
					}
				}
			}
		})
	}
	for _, r := range engine.Rules() {
		if !covered[r.ID] {
			t.Errorf("rule %s has no positive test case in testdata/cases", r.ID)
		}
	}
}

func keys(ss []Service) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.Key())
	}
	return out
}

// A plain host (sshd, cron, chrony, client tools only) must not be reported
// as running any database, cache, search or queue service.
func TestPlainHostMatchesNoDatabases(t *testing.T) {
	rs := embedded(t)
	fs := hostfstest.Build(t, testfixtures.PlainHost())
	d := (&inventory.Collector{FS: fs, InterfaceAddrs: func(string) []string { return nil }}).Collect()
	got := NewEngine(rs).Discover(d)
	for _, s := range got {
		switch s.Category {
		case "database", "cache", "search", "queue", "web", "proxy", "container", "orchestration", "runtime":
			t.Errorf("plain host matched %s (%s) via %v", s.Key(), s.Category, s.MatchedBy)
		}
	}
	var ids []string
	for _, s := range got {
		ids = append(ids, s.RuleID)
	}
	sort.Strings(ids)
	if !slices.Equal(ids, []string{"chrony", "cron", "sshd"}) {
		t.Errorf("plain host services = %v", keys(got))
	}
}

// End-to-end: fixture host tree → inventory → discovery → snapshot items.
func TestServiceHostEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux fixture (POSIX paths, file modes, unix sockets or shell scripts); not portable to Windows")
	}
	fs := hostfstest.Build(t, testfixtures.ServiceHost())
	d := (&inventory.Collector{FS: fs, InterfaceAddrs: func(string) []string { return nil }}).Collect()
	got := NewEngine(embedded(t)).Discover(d)
	byKey := map[string]Service{}
	for _, s := range got {
		byKey[s.Key()] = s
	}
	redis, ok := byKey["redis:/usr/bin/redis-server"]
	if !ok {
		t.Fatalf("redis not found: %v", keys(got))
	}
	b, _ := json.Marshal(redis)
	want := `{"rule_id":"redis","name":"Redis","category":"database","instance":"/usr/bin/redis-server","command":"redis-server","version":"7.0.15",` +
		`"matched_by":["process","systemd_unit","listening_port","package"],"pids":[812],` +
		`"ports":[{"protocol":"tcp","address":"127.0.0.1","port":6379}],"systemd_units":["redis-server.service"],` +
		`"packages":["dpkg:redis-server"],"container_ids":[],"integration":{"id":"redis","status":"enabled"},"apm_hint":null,"log_paths":["/var/log/redis/*.log"]}`
	if string(b) != want {
		t.Errorf("redis body:\n got  %s\n want %s", b, want)
	}
	nginx := byKey["nginx:/usr/sbin/nginx"]
	if !slices.Equal(nginx.PIDs, []int{900, 901}) || len(nginx.Ports) != 2 || nginx.Version != "1.24.0" ||
		!slices.Equal(nginx.Packages, []string{"dpkg:nginx"}) {
		t.Errorf("nginx = %+v", nginx)
	}
	if len(got) != 5 {
		t.Errorf("services = %v", keys(got))
	}

	items := inventory.Normalize(append(d.Items(), Items(got)...))
	snap := &inventory.Snapshot{ID: "s1", Time: time.Unix(0, 0), Items: items}
	recs, err := snap.LogRecords(time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range recs {
		for _, kv := range r.Attributes {
			if kv.Key == inventory.AttrKey && kv.Value.GetStringValue() == "redis:/usr/bin/redis-server" {
				found = true
			}
		}
	}
	if !found {
		t.Error("discovered_service item missing from snapshot")
	}
}

func TestValidationErrors(t *testing.T) {
	cases := map[string]struct{ yaml, want string }{
		"bad id":         {"id: Bad ID\nname: x\ncategory: web\nmatch: [{process: {exe_basename: [x]}}]\n", "id: must match"},
		"no match":       {"id: x\nname: x\ncategory: web\n", "at least one matcher"},
		"two kinds":      {"id: x\nname: x\ncategory: web\nmatch: [{process: {exe_basename: [x]}, package: {name: [x]}}]\n", "exactly one of process"},
		"bad regex":      {"id: x\nname: x\ncategory: web\nmatch: [{systemd_unit: {name_regex: \"(\"}}]\n", "match[0].systemd_unit.name_regex: invalid regex"},
		"bad port":       {"id: x\nname: x\ncategory: web\nmatch: [{listening_port: {port: 70000}}]\n", "must be 1..65535"},
		"unknown field":  {"id: x\nname: x\ncategory: web\nmatch: [{process: {exe: [x]}}]\n", "field exe not found"},
		"empty process":  {"id: x\nname: x\ncategory: web\nmatch: [{process: {}}]\n", "one of exe_basename"},
		"version kinds":  {"id: x\nname: x\ncategory: web\nmatch: [{process: {exe_basename: [x]}}]\nversion: [{package: [a], cmdline_regex: b}]\n", "version[0]: exactly one"},
		"apm hint":       {"id: x\nname: x\ncategory: runtime\nmatch: [{process: {exe_basename: [x]}}]\napm_hint: {language: go}\n", "apm_hint"},
		"endpoint":       {"id: x\nname: x\ncategory: web\nmatch: [{process: {exe_basename: [x]}}]\nendpoints: [{from: dns}]\n", "endpoints[0].from"},
		"basename slash": {"id: x\nname: x\ncategory: web\nmatch: [{process: {exe_basename: [/usr/bin/x]}}]\n", "not a basename"},
	}
	for name, c := range cases {
		_, err := Parse("test.yaml", []byte(c.yaml))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want containing %q", name, err, c.want)
		}
	}
}

func TestLoadAndMerge(t *testing.T) {
	fsys := fstest.MapFS{
		"a.yaml": {Data: []byte("id: redis\nname: My Redis\ncategory: database\nmatch: [{process: {exe_basename: [keydb-server]}}]\n---\nid: myapp\nname: My App\ncategory: runtime\nmatch: [{process: {exe_basename: [myapp]}}]\n")},
	}
	extra, err := LoadFS(fsys, "extra")
	if err != nil || len(extra) != 2 {
		t.Fatalf("extra = %v %v", extra, err)
	}
	merged := Merge(embedded(t), extra)
	var redis *Rule
	count := 0
	for _, r := range merged {
		if r.ID == "redis" {
			redis = r
			count++
		}
	}
	if count != 1 || redis.Name != "My Redis" || merged[len(merged)-1].ID != "myapp" {
		t.Errorf("merge failed: redis=%+v", redis)
	}
	dup := fstest.MapFS{
		"a.yaml": {Data: []byte("id: x\nname: x\ncategory: web\nmatch: [{process: {exe_basename: [x]}}]\n")},
		"b.yaml": {Data: []byte("id: x\nname: y\ncategory: web\nmatch: [{process: {exe_basename: [y]}}]\n")},
	}
	if _, err := LoadFS(dup, "dup"); err == nil || !strings.Contains(err.Error(), "duplicate rule id") {
		t.Errorf("duplicate: %v", err)
	}
	if rs, err := LoadDir(filepath.Join(t.TempDir(), "missing")); err != nil || rs != nil {
		t.Errorf("missing dir: %v %v", rs, err)
	}
}

// A new service needs only a YAML rule: a user rule is discovered without code changes.
func TestUserRuleWithoutCode(t *testing.T) {
	rs, err := Parse("custom.yaml", []byte(`
id: acme-billing
name: ACME Billing
category: application
match:
  - process: { exe_basename: ["billingd"] }
  - listening_port: { port: 7443 }
version:
  - cmdline_regex: "--release=(\\S+)"
integration: { id: acme, requires: ["credentials"] }
`))
	if err != nil {
		t.Fatal(err)
	}
	d := &inventory.Data{
		Instances: []inventory.ProcessInstance{{PID: 10, Exe: "/opt/acme/billingd", Comm: "billingd", Cmdline: "/opt/acme/billingd --release=2.3.1"}},
		Ports:     []inventory.ListeningPort{{Protocol: "tcp", Address: "0.0.0.0", Port: 7443, PID: 10}, {Protocol: "tcp", Address: "::", Port: 7443}},
	}
	got := NewEngine(rs).Discover(d)
	// The ownerless [::]:7443 socket joins the process instance (a port key is
	// the lowest instance precedence, semantic-conventions §3.4).
	if len(got) != 1 {
		t.Fatalf("got %v", keys(got))
	}
	if got[0].Key() != "acme-billing:/opt/acme/billingd" || got[0].Version != "2.3.1" || len(got[0].Ports) != 2 ||
		!slices.Equal(got[0].MatchedBy, []string{"process", "listening_port"}) || got[0].Integration.Status != StatusNeedsConfiguration {
		t.Errorf("service = %+v", got[0])
	}

	// Precedence: an ownerless port joins a package instance instead of
	// becoming its own instance; with no other instance the port key is used.
	rs2, err := Parse("custom2.yaml", []byte(`
id: acme-gw
name: ACME Gateway
category: proxy
match:
  - listening_port: { port: 8443 }
  - package: { name: ["acme-gw"] }
`))
	if err != nil {
		t.Fatal(err)
	}
	d2 := &inventory.Data{
		Ports:    []inventory.ListeningPort{{Protocol: "tcp", Family: 6, Address: "::", Port: 8443}},
		Packages: []inventory.Package{{Manager: "dpkg", Name: "acme-gw", Version: "1.0-1"}},
	}
	got = NewEngine(rs2).Discover(d2)
	if len(got) != 1 || got[0].Key() != "acme-gw:dpkg:acme-gw" || len(got[0].Ports) != 1 ||
		!slices.Equal(got[0].MatchedBy, []string{"listening_port", "package"}) {
		t.Errorf("package+port = %v %+v", keys(got), got)
	}
	d2.Packages = nil
	if got = NewEngine(rs2).Discover(d2); len(got) != 1 || got[0].Key() != "acme-gw:tcp:[::]:8443" {
		t.Errorf("port only = %v", keys(got))
	}
}

func TestNormalizePackageVersion(t *testing.T) {
	cases := map[string]string{
		"5:7.0.15-1build2": "7.0.15", "1.24.0-2ubuntu7": "1.24.0", "7.2.4-r0": "7.2.4", "16+257build1": "16",
		"1:9.6p1-3ubuntu13": "9.6p1", "8.0.36-0ubuntu0.22.04.1": "8.0.36", "3.12": "3.12", "2.4.58~deb12": "2.4.58",
	}
	for in, want := range cases {
		if got := NormalizePackageVersion(in); got != want {
			t.Errorf("%s → %q, want %q", in, got, want)
		}
	}
}

func TestServiceIndexCommandAndLogPaths(t *testing.T) {
	d := &inventory.Data{
		Instances: []inventory.ProcessInstance{
			{PID: 50, PPID: 1, Exe: "/usr/bin/redis-check-rdb", Comm: "redis-server", Cmdline: "/usr/bin/redis-server 127.0.0.1:6379"},
			{PID: 60, PPID: 1, Exe: "/usr/lib/jvm/bin/java", Comm: "java", Cmdline: "/usr/lib/jvm/bin/java -cp kafka.jar kafka.Kafka"},
			{PID: 70, PPID: 1, Exe: "/usr/sbin/nginx", Comm: "nginx", Cmdline: "nginx: master process /usr/sbin/nginx"},
			{PID: 71, PPID: 70, Exe: "/usr/sbin/nginx", Comm: "nginx", Cmdline: "nginx: worker process"},
		},
		Containers: []inventory.Container{{ID: strings.Repeat("a", 64), Image: "nginx:1.25"}},
	}
	svcs := NewEngine(embedded(t)).Discover(d)
	byRule := map[string]Service{}
	for _, s := range svcs {
		byRule[s.RuleID+"|"+s.Instance] = s
	}
	redis := byRule["redis|/usr/bin/redis-check-rdb"]
	if redis.Command != "redis-server" || !slices.Equal(redis.LogPaths, []string{"/var/log/redis/*.log"}) {
		t.Errorf("redis = %+v (keys %v)", redis, keys(svcs))
	}
	if n := byRule["nginx|/usr/sbin/nginx"]; n.Command != "nginx" || len(n.LogPaths) != 1 {
		t.Errorf("nginx = %+v", n)
	}
	if c := byRule["nginx|"+strings.Repeat("a", 64)]; c.Command != "" || c.LogPaths == nil {
		t.Errorf("container instance = %+v", c)
	}
	ix := NewServiceIndex(svcs)
	cases := []struct {
		pid      int
		exe, ctr string
		want     string
	}{
		{50, "", "", "redis"},
		{60, "", "", "kafka"}, // kafka (queue) wins over jvm (runtime)
		{999, "/usr/sbin/nginx", "", "nginx"},
		{998, "/bin/sh", strings.Repeat("a", 64), "nginx"},
		{997, "/bin/sh", "", ""},
	}
	for _, c := range cases {
		if got := ix.Lookup(c.pid, c.exe, c.ctr); got != c.want {
			t.Errorf("Lookup(%d,%q,%q) = %q, want %q", c.pid, c.exe, c.ctr, got, c.want)
		}
	}
	if (*ServiceIndex)(nil).Lookup(1, "", "") != "" {
		t.Error("nil index")
	}
	if _, err := Parse("x.yaml", []byte("id: x\nname: x\ncategory: web\nmatch: [{process: {exe_basename: [x]}}]\nlogs: [{path: var/log/x.log}]\n")); err == nil ||
		!strings.Contains(err.Error(), "logs[0].path") {
		t.Errorf("relative log path: %v", err)
	}
}
