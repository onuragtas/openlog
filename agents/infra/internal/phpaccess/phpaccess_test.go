package phpaccess

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func write(t *testing.T, root, p, content string) {
	t.Helper()
	full := filepath.Join(root, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParsePoolFile(t *testing.T) {
	src := `; HestiaCP style
[global]
user = nobody

[example.com]
listen = /run/php/php8.2-fpm-example.com.sock
user = admin ; site owner
group = admin

[second]
;user = commented
# user = hash-commented
user = "Projects"

[no-user]
listen = 127.0.0.1:9001

[$pool-var]
user = $pool

[env]
user = ${FPM_USER}

[bad]
user = -rf

[root-pool]
user = root
`
	got := ParsePoolFile([]byte(src))
	want := []Pool{{Name: "example.com", User: "admin"}, {Name: "second", User: "Projects"},
		{Name: "$pool-var", User: ""}, {Name: "root-pool", User: "root"}}
	// "$pool-var" expands to a name with "$": rejected, so it is not listed at all.
	want = []Pool{want[0], want[1], want[3]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pools = %+v, want %+v", got, want)
	}

	if got := ParsePoolFile([]byte("[www]\nuser = $pool\n")); len(got) != 1 || got[0].User != "www" {
		t.Errorf("$pool expansion: %+v", got)
	}
}

func TestUnitForPoolDir(t *testing.T) {
	cases := map[string][2]string{
		"/etc/php/8.2/fpm/pool.d":                 {"php8.2-fpm.service", "8.2"},
		"/etc/php/7.2/fpm/pool.d/":                {"php7.2-fpm.service", "7.2"},
		"/etc/php-fpm.d":                          {"php-fpm.service", ""},
		"/etc/opt/remi/php74/php-fpm.d":           {"php74-php-fpm.service", "7.4"},
		"/opt/cpanel/ea-php81/root/etc/php-fpm.d": {"ea-php81-php-fpm.service", "8.1"},
		"/opt/plesk/php/8.3/etc/php-fpm.d":        {"plesk-php83-fpm.service", "8.3"},
		"/etc/php82/php-fpm.d":                    {"php-fpm82.service", "8.2"},
	}
	for dir, want := range cases {
		unit, ver, ok := UnitForPoolDir(dir)
		if !ok || unit != want[0] || ver != want[1] {
			t.Errorf("%s: %q %q %v, want %v", dir, unit, ver, ok, want)
		}
	}
	for _, dir := range []string{"/etc/php/8.2/cli", "/tmp/pool.d", "/etc/php/../../tmp/fpm/pool.d"} {
		if _, _, ok := UnitForPoolDir(dir); ok {
			t.Errorf("%s accepted", dir)
		}
	}
}

func TestDiscoverPoolsLayouts(t *testing.T) {
	root := t.TempDir()
	write(t, root, "/etc/php/7.2/fpm/pool.d/example.com.conf", "[example.com]\nuser = admin\n[shop]\nuser = semihyurudu\n")
	write(t, root, "/etc/php/8.2/fpm/pool.d/www.conf", "[www]\nuser = www-data\n")
	write(t, root, "/etc/php/8.2/fpm/pool.d/readme.txt", "[x]\nuser = ignored\n")
	write(t, root, "/etc/php-fpm.d/www.conf", "[www]\nuser = apache\n")
	write(t, root, "/opt/cpanel/ea-php81/root/etc/php-fpm.d/site.conf", "[site_tld]\nuser = cpuser\n")
	write(t, root, "/opt/plesk/php/8.3/etc/php-fpm.d/dom.conf", "[dom.tld]\nuser = plesku\n")
	write(t, root, "/etc/opt/remi/php74/php-fpm.d/www.conf", "[www]\nuser = remiu\n")
	if err := os.MkdirAll(filepath.Join(root, "etc/php/8.2/fpm/pool.d/dir.conf"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := DiscoverPools(root)
	type row struct{ file, pool, user, unit, ver string }
	var rows []row
	for _, p := range got {
		rows = append(rows, row{p.File, p.Name, p.User, p.Unit, p.PHPVersion})
	}
	want := []row{
		{"/etc/php/7.2/fpm/pool.d/example.com.conf", "example.com", "admin", "php7.2-fpm.service", "7.2"},
		{"/etc/php/7.2/fpm/pool.d/example.com.conf", "shop", "semihyurudu", "php7.2-fpm.service", "7.2"},
		{"/etc/php/8.2/fpm/pool.d/www.conf", "www", "www-data", "php8.2-fpm.service", "8.2"},
		{"/etc/php-fpm.d/www.conf", "www", "apache", "php-fpm.service", ""},
		{"/etc/opt/remi/php74/php-fpm.d/www.conf", "www", "remiu", "php74-php-fpm.service", "7.4"},
		{"/opt/cpanel/ea-php81/root/etc/php-fpm.d/site.conf", "site_tld", "cpuser", "ea-php81-php-fpm.service", "8.1"},
		{"/opt/plesk/php/8.3/etc/php-fpm.d/dom.conf", "dom.tld", "plesku", "plesk-php83-fpm.service", "8.3"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("pools:\n got %+v\nwant %+v", rows, want)
	}
}

func accountsFixture(t *testing.T) string {
	root := t.TempDir()
	write(t, root, "/etc/passwd", "root:x:0:0::/root:/bin/sh\nwww-data:x:33:33::/var/www:/usr/sbin/nologin\n"+
		"admin:x:1000:1000::/home/admin:/bin/bash\nsemihyurudu:x:1001:1001::/home/s:/bin/bash\n"+
		"openlog-agent:x:998:998::/var/lib/openlog-infra-agent:/usr/sbin/nologin\ntoor:x:0:0::/root:/bin/sh\n")
	write(t, root, "/etc/group", "root:x:0:\nwww-data:x:33:\nadmin:x:1000:\nopenlog-agent:x:998:\nopenlog-php:x:997:openlog-agent,admin\n")
	return root
}

func TestAccountsAndCandidates(t *testing.T) {
	acc := ReadAccounts(accountsFixture(t))
	if !acc.GroupExists(Group) || !acc.InGroup("admin", Group) || acc.InGroup("semihyurudu", Group) {
		t.Fatal("group membership parsing")
	}
	if !acc.InGroup("admin", "admin") { // primary group
		t.Error("primary group not honored")
	}
	if acc.Grantable("root") || acc.Grantable("toor") || acc.Grantable("ghost") || !acc.Grantable("admin") {
		t.Error("grantable: root/uid 0/missing users must be refused")
	}
	pools := []Pool{{User: "admin"}, {User: "semihyurudu"}, {User: "root"}, {User: "ghost"}, {User: "admin"}, {User: "openlog-agent"}}
	if got := Candidates(pools, true, acc, "openlog-agent"); !reflect.DeepEqual(got, []string{"admin", "semihyurudu", "www-data"}) {
		t.Errorf("candidates with web server = %v", got)
	}
	if got := Candidates(pools, false, acc, "openlog-agent"); !reflect.DeepEqual(got, []string{"admin", "semihyurudu"}) {
		t.Errorf("candidates without web server = %v", got)
	}
}

func TestWebServerPresent(t *testing.T) {
	root := t.TempDir()
	if WebServerPresent(root) {
		t.Fatal("empty root")
	}
	write(t, root, "/proc/123/comm", "nginx\n")
	if !WebServerPresent(root) {
		t.Error("nginx process not detected")
	}
	root = t.TempDir()
	write(t, root, "/lib/systemd/system/apache2.service", "[Unit]\n")
	if !WebServerPresent(root) {
		t.Error("apache2 unit not detected")
	}
}

func TestBuildReport(t *testing.T) {
	root := accountsFixture(t)
	if BuildReport(ReportInput{Root: root, AgentUser: "openlog-agent"}) != nil {
		t.Fatal("report without pools")
	}
	write(t, root, "/etc/php/8.2/fpm/pool.d/a.conf", "[a]\nuser = admin\n[b]\nuser = semihyurudu\n[c]\nuser = root\n")

	r := BuildReport(ReportInput{Root: root, AgentUser: "openlog-agent", SocketGroup: Group, SocketMode: 0o660})
	if !r.GroupExists || !r.AgentMember || r.Grants != GrantsAuto || r.SocketGroup != Group {
		t.Fatalf("report %+v", r)
	}
	access := func(r *Report) []string {
		var out []string
		for _, p := range r.Pools {
			out = append(out, p.Pool+"="+p.Access)
		}
		return out
	}
	if got := access(r); !reflect.DeepEqual(got, []string{"a=ok", "b=missing", "c=unsupported_user"}) {
		t.Errorf("access %v", got)
	}
	if m := r.Missing(); len(m) != 1 || m[0].User != "semihyurudu" || m[0].Unit != "php8.2-fpm.service" || m[0].PHPVersion != "8.2" {
		t.Errorf("missing %+v", m)
	}
	if got := access(BuildReport(ReportInput{Root: root, AgentUser: "openlog-agent", OptedOut: true})); !reflect.DeepEqual(got, []string{"a=ok", "b=opted_out", "c=unsupported_user"}) {
		t.Errorf("opted out %v", got)
	}
	if got := access(BuildReport(ReportInput{Root: root, AgentUser: "openlog-agent", SocketMode: 0o666})); !reflect.DeepEqual(got, []string{"a=ok", "b=ok", "c=ok"}) {
		t.Errorf("0666 %v", got)
	}
	// Fallback socket group www-data: admin is not in it.
	if got := access(BuildReport(ReportInput{Root: root, AgentUser: "openlog-agent", SocketGroup: "www-data"})); got[0] != "a=missing" {
		t.Errorf("fallback group %v", got)
	}
}
