package fleet

import (
	"strings"
	"testing"
)

func TestParsePHPAccessReport(t *testing.T) {
	for _, raw := range []string{"", "null", "[1]", "{"} {
		if ParsePHPAccessReport([]byte(raw)) != nil {
			t.Errorf("%q: want nil", raw)
		}
	}
	r := ParsePHPAccessReport([]byte(`{"socket_group":"openlog-php","group":"openlog-php","group_exists":true,"agent_member":true,
		"grants":"auto","pools":[{"pool":"example.com","php_version":"7.2","user":"admin","unit":"php7.2-fpm.service","access":"missing"}]}`))
	if r == nil || r.SocketGroup != "openlog-php" || !r.GroupExists || !r.AgentMember || len(r.Pools) != 1 ||
		r.Pools[0] != (PHPPoolAccess{Pool: "example.com", PHPVersion: "7.2", User: "admin", Unit: "php7.2-fpm.service", Access: "missing"}) {
		t.Fatalf("%+v", r)
	}

	// Bounded: pool count and field sizes; pools never null.
	big := `{"grants":"` + strings.Repeat("x", 100) + `","pools":[` + strings.Repeat(`{"user":"`+strings.Repeat("u", 200)+`"},`, maxPHPPools+5)
	big = strings.TrimSuffix(big, ",") + "]}"
	r = ParsePHPAccessReport([]byte(big))
	if r == nil || len(r.Pools) != maxPHPPools || len(r.Pools[0].User) != 64 || len(r.Grants) != 16 {
		t.Fatalf("bounds: pools %d user %d grants %d", len(r.Pools), len(r.Pools[0].User), len(r.Grants))
	}
	if r := ParsePHPAccessReport([]byte(`{"group":"openlog-php"}`)); r == nil || r.Pools == nil {
		t.Error("pools must be [] when absent")
	}
}
