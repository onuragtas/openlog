package intsettings_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/alert/secrets"
	"github.com/onuragtas/openlog/internal/intsettings"
	"github.com/onuragtas/openlog/internal/intsettings/intsettingstest"
)

func ptr[T any](v T) *T { return &v }

func testKeyring(t *testing.T) *secrets.Keyring {
	t.Helper()
	kr, err := secrets.NewKeyring(base64.StdEncoding.EncodeToString(make([]byte, 32)), "")
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

func TestValidate(t *testing.T) {
	long := strings.Repeat("x", intsettings.MaxFieldLen+1)
	cases := []struct {
		name string
		in   intsettings.Input
		want string // "" = valid
	}{
		{"nginx url", intsettings.Input{Integration: "nginx", Endpoint: "http://127.0.0.1:8080/nginx_status"}, ""},
		{"docker match only", intsettings.Input{Integration: "docker", Match: &intsettings.Match{Container: "web"}, Enabled: ptr(false)}, ""},
		{"redis full", intsettings.Input{Integration: "redis", Endpoint: "127.0.0.1:6379", Username: "u", Password: ptr("p"), Match: &intsettings.Match{Port: ptr(6379)}}, ""},
		{"mysql unix", intsettings.Input{Integration: "mysql", Endpoint: "unix:/var/run/mysqld/mysqld.sock"}, ""},
		{"postgresql dbs", intsettings.Input{Integration: "postgresql", Endpoint: "[::1]:5432", Database: "app", Databases: []string{"a", "b"}}, ""},
		{"instance 512", intsettings.Input{Integration: "docker", Match: &intsettings.Match{Instance: strings.Repeat("a", 512)}}, ""},
		{"unknown integration", intsettings.Input{Integration: "cassandra"}, "integration must be one of"},
		{"nginx not url", intsettings.Input{Integration: "nginx", Endpoint: "127.0.0.1:80"}, "http(s) URL"},
		{"nginx ftp", intsettings.Input{Integration: "nginx", Endpoint: "ftp://h/x"}, "http(s) URL"},
		{"nginx password", intsettings.Input{Integration: "nginx", Password: ptr("p")}, "password is not used by the nginx"},
		{"docker endpoint", intsettings.Input{Integration: "docker", Endpoint: "unix:/var/run/docker.sock"}, "endpoint is not used"},
		{"redis database", intsettings.Input{Integration: "redis", Database: "0"}, "database is not used"},
		{"mysql databases", intsettings.Input{Integration: "mysql", Databases: []string{"x"}}, "databases is not used"},
		{"bad host port", intsettings.Input{Integration: "redis", Endpoint: "localhost"}, "host:port"},
		{"port range", intsettings.Input{Integration: "redis", Endpoint: "h:70000"}, "host:port"},
		{"relative unix", intsettings.Input{Integration: "redis", Endpoint: "unix:redis.sock"}, "host:port"},
		{"match port", intsettings.Input{Integration: "redis", Match: &intsettings.Match{Port: ptr(0)}}, "match.port"},
		{"long username", intsettings.Input{Integration: "redis", Username: long}, "username must be at most"},
		{"long instance", intsettings.Input{Integration: "docker", Match: &intsettings.Match{Instance: long}}, "match.instance"},
		{"long password", intsettings.Input{Integration: "redis", Password: ptr(long)}, "password must be at most"},
		{"control chars", intsettings.Input{Integration: "redis", Username: "a\nb"}, "control characters"},
		{"long host", intsettings.Input{Integration: "docker", HostID: ptr(strings.Repeat("h", 257))}, "host_id"},
		{"too many dbs", intsettings.Input{Integration: "postgresql", Databases: make([]string, 65)}, "at most 64"},
		{"empty db", intsettings.Input{Integration: "postgresql", Databases: []string{""}}, "databases[0]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := intsettings.Validate(tc.in)
			var ve *intsettings.ValidationError
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.want != "" && (!errors.As(err, &ve) || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestEffectiveOrderAndRevision(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	mk := func(id, host string, match intsettings.Match, at time.Duration) intsettings.Setting {
		return intsettings.Setting{ID: id, OrgID: "o", HostID: host, Integration: "redis", Match: match, Enabled: true, CreatedAt: t0.Add(at)}
	}
	port := intsettings.Match{Port: ptr(6380)}
	ss := []intsettings.Setting{
		mk("h1-match", "h1", port, 0),
		mk("all-match", "", port, 0),
		mk("h1-plain", "h1", intsettings.Match{}, time.Hour),
		mk("h2-plain", "h2", intsettings.Match{}, 0),
		mk("all-plain-b", "", intsettings.Match{}, time.Minute),
		mk("all-plain-a", "", intsettings.Match{}, time.Minute),
		mk("all-plain-old", "", intsettings.Match{}, 0),
	}
	var ids []string
	for _, s := range intsettings.Effective(ss, "h1") {
		ids = append(ids, s.ID)
	}
	if got := strings.Join(ids, ","); got != "all-plain-old,all-plain-a,all-plain-b,all-match,h1-plain,h1-match" {
		t.Fatalf("effective order = %s", got)
	}

	empty := intsettings.Revision(nil)
	if !strings.HasPrefix(empty, "sha256:") || len(empty) != 71 || empty != intsettings.Revision([]intsettings.Setting{}) {
		t.Fatalf("empty revision = %s", empty)
	}
	eff := intsettings.Effective(ss, "h1")
	rev := intsettings.Revision(eff)
	if rev == empty || rev != intsettings.Revision(intsettings.Effective(ss, "h1")) {
		t.Fatal("revision not stable")
	}
	if rev == intsettings.Revision(intsettings.Effective(ss, "h2")) {
		t.Fatal("different hosts share a revision")
	}
	changed := append([]intsettings.Setting(nil), eff...)
	changed[0].PasswordEnc = "ol1:x:y"
	if intsettings.Revision(changed) == rev {
		t.Fatal("password change does not change the revision")
	}
}

func TestAgentItems(t *testing.T) {
	kr := testKeyring(t)
	enc, _, _ := kr.Encrypt([]byte("s3cret"), intsettings.PasswordAAD("o", "id1"))
	ss := []intsettings.Setting{
		{ID: "id1", OrgID: "o", Integration: "redis", Enabled: true, Endpoint: "h:1", PasswordEnc: enc},
		{ID: "id2", OrgID: "o", Integration: "nginx", Match: intsettings.Match{Port: ptr(8080)}},
	}
	items, err := intsettings.AgentItems(kr, ss)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(items)
	want := `[{"integration":"redis","enabled":true,"endpoint":"h:1","username":"","password":"s3cret","database":"","databases":[]},` +
		`{"integration":"nginx","match":{"port":8080,"container":"","endpoint":"","instance":""},"enabled":false,"endpoint":"","username":"","password":"","database":"","databases":[]}]`
	if string(b) != want {
		t.Fatalf("items = %s", b)
	}
	// A ciphertext copied to another row does not decrypt.
	ss[1].PasswordEnc = enc
	var de *intsettings.DecryptError
	if _, err := intsettings.AgentItems(kr, ss); !errors.As(err, &de) || de.SettingID != "id2" || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("err = %v", err)
	}
	if _, err := intsettings.AgentItems(nil, ss[:1]); !errors.Is(err, secrets.ErrNoKey) {
		t.Fatalf("no keyring: %v", err)
	}
}

func TestManagerPasswordAndAudit(t *testing.T) {
	ctx := context.Background()
	st := intsettingstest.NewMemStore()
	m := intsettings.NewManager(st, intsettings.ManagerOptions{Keys: testKeyring(t)})
	a := intsettings.Actor{UserID: "u1", Email: "admin@example.com"}
	s, err := m.Create(ctx, "o", intsettings.Input{Integration: "redis", Endpoint: "127.0.0.1:6379", Password: ptr("pw1")}, a)
	if err != nil || s.PasswordEnc == "" || strings.Contains(s.PasswordEnc, "pw1") {
		t.Fatalf("create: %+v %v", s, err)
	}
	if _, err := m.Create(ctx, "o", intsettings.Input{Integration: "redis"}, a); !errors.Is(err, intsettings.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	// Omitted password keeps the stored ciphertext.
	kept, err := m.Update(ctx, "o", s.ID, intsettings.Input{Integration: "redis", Username: "u"}, a)
	if err != nil || kept.PasswordEnc != s.PasswordEnc || !kept.CreatedAt.Equal(s.CreatedAt) {
		t.Fatalf("keep: %+v %v", kept, err)
	}
	// Changing to an integration without passwords drops it.
	ng, err := m.Update(ctx, "o", s.ID, intsettings.Input{Integration: "nginx"}, a)
	if err != nil || ng.PasswordEnc != "" {
		t.Fatalf("nginx: %+v %v", ng, err)
	}
	set, _ := m.Update(ctx, "o", s.ID, intsettings.Input{Integration: "redis", Password: ptr("pw2")}, a)
	cleared, err := m.Update(ctx, "o", s.ID, intsettings.Input{Integration: "redis", Password: ptr("")}, a)
	if set.PasswordEnc == "" || err != nil || cleared.PasswordEnc != "" {
		t.Fatalf("set/clear: %+v %+v %v", set, cleared, err)
	}
	if err := m.Delete(ctx, "o", s.ID, a); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(ctx, "o", s.ID, a); !errors.Is(err, intsettings.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	audit := st.Audit()
	var actions []string
	for _, e := range audit {
		actions = append(actions, e.Action)
		b, _ := json.Marshal(e.Details)
		if strings.Contains(string(b), "pw1") || strings.Contains(string(b), "pw2") || strings.Contains(string(b), "ol1:") {
			t.Fatalf("audit leaks a password: %s", b)
		}
	}
	if strings.Join(actions, ",") != "integration_setting.create,integration_setting.update,integration_setting.update,integration_setting.update,integration_setting.update,integration_setting.delete" {
		t.Fatalf("audit = %v", actions)
	}
	// PUT replaces every non-secret field: the omitted endpoint is cleared.
	if d := audit[1].Details; d["password_changed"] != false || strings.Join(d["changed"].([]string), ",") != "endpoint,username" {
		t.Errorf("update details = %v", d)
	}
	if d := audit[4].Details; d["password_changed"] != true {
		t.Errorf("clear details = %v", d)
	}

	// Without OPENLOG_SECRETS_KEY a password cannot be stored, other settings can.
	nokey := intsettings.NewManager(intsettingstest.NewMemStore(), intsettings.ManagerOptions{})
	if _, err := nokey.Create(ctx, "o", intsettings.Input{Integration: "mysql", Password: ptr("x")}, a); !errors.Is(err, intsettings.ErrNoSecretsKey) {
		t.Fatalf("no key: %v", err)
	}
	if _, err := nokey.Create(ctx, "o", intsettings.Input{Integration: "mysql", Endpoint: "db:3306"}, a); err != nil {
		t.Fatalf("no key without password: %v", err)
	}
}
