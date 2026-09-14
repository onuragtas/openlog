package update

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/onuragtas/openlog/agents/infra/internal/phpaccess"
)

// phpFixture: a HestiaCP-like host with PHP 7.2 and 8.2 pools of site users, nginx + Apache installed.
func phpFixture(t *testing.T) *reconcileFixture {
	f := newReconcileFixture(t)
	s := f.sys
	s.write("/etc/passwd", s.read("/etc/passwd")+"www-data:x:33:33::/var/www:/usr/sbin/nologin\n"+
		"admin:x:1000:1000::/home/admin:/bin/bash\nsemihyurudu:x:1001:1001::/home/s:/bin/bash\n"+
		"Projects:x:1002:1002::/home/p:/bin/bash\n")
	s.write("/etc/group", s.read("/etc/group")+"www-data:x:33:\nadmin:x:1000:\n")
	s.write("/etc/php/7.2/fpm/pool.d/example.com.conf", "; site\n[example.com]\nuser = admin\ngroup = admin\n[shop.example.com]\nuser = semihyurudu\n")
	s.write("/etc/php/8.2/fpm/pool.d/projects.conf", "[projects]\nuser = Projects\n[root-pool]\nuser = root\n[ghost]\nuser = nobody-here\n")
	s.write("/lib/systemd/system/nginx.service", "[Unit]\n")
	os.RemoveAll(s.path("/etc/systemd")) // no unit handling in these tests
	return f
}

func members(s *fakeSys, group string) []string {
	m, _ := s.groupMembers(group)
	return m
}

func TestReconcilePHPAccessGrantsPoolUsers(t *testing.T) {
	f := phpFixture(t)
	f.ctx = ReconcilePackage
	st, err := f.run()
	if err != nil {
		t.Fatalf("%+v %v", st, err)
	}
	p := st.PHPAccess
	if p == nil || p.Group != PHPCreated || p.Grants != PHPGrantsOn {
		t.Fatalf("php access %+v", p)
	}
	if !f.sys.ran("groupadd --system " + phpaccess.Group) {
		t.Errorf("group not created: %v", f.sys.runs)
	}
	want := []string{"admin", "semihyurudu", "Projects", "www-data"}
	if !slices.Equal(p.Added, want) {
		t.Errorf("added %v, want %v", p.Added, want)
	}
	if got := members(f.sys, phpaccess.Group); !slices.Equal(got, append([]string{AgentUser}, want...)) {
		t.Errorf("group members %v", got)
	}
	for _, u := range []string{"root", "nobody-here"} {
		if f.sys.ran("usermod -aG " + phpaccess.Group + " " + u) {
			t.Errorf("%s must not be added", u)
		}
	}
	// The agent user joins the group once to set the socket group; it is never a pool grant.
	if p.Agent != PHPAdded || slices.Contains(p.Added, AgentUser) {
		t.Errorf("agent membership %+v", p)
	}
	agentRuns := 0
	for _, c := range f.sys.runs {
		if c == "usermod -aG "+phpaccess.Group+" "+AgentUser {
			agentRuns++
		}
	}
	if agentRuns != 1 {
		t.Errorf("agent added %d times: %v", agentRuns, f.sys.runs)
	}
	// No www-data reload: apache2 is not installed (nginx runs no PHP); FPM units derived from the pool directories.
	if !slices.Equal(p.Reloaded, []string{"php7.2-fpm.service", "php8.2-fpm.service"}) || !f.sys.ran("systemctl is-active --quiet php7.2-fpm.service") {
		t.Errorf("reloaded %v runs %v", p.Reloaded, f.sys.runs)
	}
	if slices.ContainsFunc(f.sys.runs, func(c string) bool { return strings.Contains(c, "restart") }) {
		t.Errorf("never restart PHP-FPM: %v", f.sys.runs)
	}
	if r := st.Report(); r.PHPAccess != PHPAdded {
		t.Errorf("report %+v", r)
	}

	// Idempotent: nothing added, nothing reloaded.
	f.sys.runs = nil
	st, err = f.run()
	if err != nil || len(st.PHPAccess.Added) != 0 || len(f.sys.runs) != 0 || st.Report().PHPAccess != PHPMember {
		t.Fatalf("second run %+v %v runs %v", st.PHPAccess, err, f.sys.runs)
	}

	// A new site: only its FPM service is reloaded; a manually added member stays.
	f.sys.write("/etc/passwd", f.sys.read("/etc/passwd")+"tunnel:x:1003:1003::/home/t:/bin/bash\nmanual:x:1004:1004::/x:/bin/sh\n")
	f.sys.addMember(phpaccess.Group, "manual")
	f.sys.write("/etc/php/8.2/fpm/pool.d/tunnel.conf", "[tunnel]\nuser = tunnel\n")
	f.sys.runs = nil
	st, _ = f.run()
	if !slices.Equal(st.PHPAccess.Added, []string{"tunnel"}) || !slices.Equal(st.PHPAccess.Reloaded, []string{"php8.2-fpm.service"}) {
		t.Errorf("new site %+v runs %v", st.PHPAccess, f.sys.runs)
	}
	if !slices.Contains(members(f.sys, phpaccess.Group), "manual") {
		t.Error("manual member removed")
	}
}

func TestReconcilePHPAccessReloadOnlyActiveUnits(t *testing.T) {
	f := phpFixture(t)
	f.sys.fail = "systemctl is-active --quiet php7.2-fpm.service"
	st, err := f.run()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(st.PHPAccess.NotActive, []string{"php7.2-fpm.service"}) || !slices.Equal(st.PHPAccess.Reloaded, []string{"php8.2-fpm.service"}) ||
		f.sys.ran("systemctl reload php7.2-fpm.service") {
		t.Errorf("%+v runs %v", st.PHPAccess, f.sys.runs)
	}

	// Without systemd nothing is reloaded, the users are still added.
	f = phpFixture(t)
	os.RemoveAll(f.sys.path("/run/systemd"))
	st, _ = f.run()
	if len(st.PHPAccess.Added) != 4 || f.sys.ran("systemctl") {
		t.Errorf("no systemd: %+v runs %v", st.PHPAccess, f.sys.runs)
	}
}

func TestReconcilePHPAccessApacheReload(t *testing.T) {
	f := phpFixture(t)
	f.sys.write("/lib/systemd/system/apache2.service", "[Unit]\n")
	st, _ := f.run()
	if !slices.Contains(st.PHPAccess.Reloaded, "apache2.service") {
		t.Errorf("mod_php user added without Apache reload: %+v", st.PHPAccess)
	}
}

func TestReconcilePHPAccessAgentMembership(t *testing.T) {
	f := phpFixture(t)
	// The group exists with another gid, so the agent is not a member through its primary group.
	f.sys.write("/etc/group", f.sys.read("/etc/group")+phpaccess.Group+":x:4242:\n")
	f.ctx = ReconcileApply
	st, _ := f.run()
	if st.PHPAccess.Group != PHPExists || st.PHPAccess.Agent != PHPAdded || !f.sys.ran("usermod -aG "+phpaccess.Group+" "+AgentUser) {
		t.Fatalf("%+v runs %v", st.PHPAccess, f.sys.runs)
	}
	if st.RestartRequired {
		t.Error("apply context: the agent about to start gets the group, no restart")
	}

	f = phpFixture(t)
	f.sys.write("/etc/group", f.sys.read("/etc/group")+phpaccess.Group+":x:4242:\n")
	f.ctx = ReconcileInstall
	if st, _ := f.run(); !st.RestartRequired {
		t.Error("install context: running agent must restart for the new group")
	}
}

func TestReconcilePHPAccessOptOut(t *testing.T) {
	marker := func(f *reconcileFixture) string { return filepath.Join(filepath.Dir(f.config), phpaccess.OptOutFile) }
	check := func(t *testing.T, f *reconcileFixture) {
		t.Helper()
		st, err := f.run()
		if err != nil {
			t.Fatal(err)
		}
		// The group and the agent membership are still set up (manual grants keep working).
		if st.PHPAccess.Grants != PHPOptOut || len(st.PHPAccess.Added) != 0 || st.PHPAccess.Group != PHPCreated ||
			f.sys.ran("usermod -aG "+phpaccess.Group+" admin") || f.sys.ran("systemctl reload") || st.Report().PHPAccess != PHPOptOut {
			t.Errorf("%+v runs %v", st.PHPAccess, f.sys.runs)
		}
	}
	t.Run("env records the marker", func(t *testing.T) {
		f := phpFixture(t)
		f.sys.env[phpaccess.AccessEnv] = "0"
		check(t, f)
		if _, err := os.Stat(marker(f)); err != nil {
			t.Fatal("opt-out not recorded")
		}
		delete(f.sys.env, phpaccess.AccessEnv)
		f.sys.runs = nil
		if st, _ := f.run(); st.PHPAccess.Grants != PHPOptOut || f.sys.ran("usermod") {
			t.Errorf("marker ignored: %+v", st.PHPAccess)
		}
	})
	t.Run("marker file", func(t *testing.T) {
		f := phpFixture(t)
		os.WriteFile(marker(f), nil, 0o644)
		check(t, f)
	})
	t.Run("config", func(t *testing.T) {
		f := phpFixture(t)
		f.phpGrantsDisabled = true
		check(t, f)
	})
}

func TestReconcilePHPAccessFailures(t *testing.T) {
	f := phpFixture(t)
	f.sys.fail = "usermod -aG " + phpaccess.Group + " semihyurudu"
	st, err := f.run()
	if err == nil || !slices.Equal(st.PHPAccess.Failed, []string{"semihyurudu"}) || st.Report().PHPAccess != PHPFailed ||
		!slices.Contains(st.PHPAccess.Added, "admin") {
		t.Fatalf("%+v %v", st.PHPAccess, err)
	}

	f = phpFixture(t)
	f.sys.fail = "groupadd --system " + phpaccess.Group
	st, err = f.run()
	if err == nil || st.PHPAccess.Group != PHPFailed || f.sys.ran("usermod -aG "+phpaccess.Group) {
		t.Fatalf("groupadd failure %+v %v", st.PHPAccess, err)
	}
}
