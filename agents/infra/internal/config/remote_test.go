package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestLiteralSecretIsNeverAReference(t *testing.T) {
	t.Setenv("OPENLOG_TEST_REMOTE_PW", "from-env")
	for _, v := range []string{"env:OPENLOG_TEST_REMOTE_PW", "file:/etc/shadow", "plain"} {
		s := LiteralSecret(v)
		got, err := s.Resolve()
		if err != nil || got != v {
			t.Errorf("LiteralSecret(%q).Resolve() = %q, %v", v, got, err)
		}
		if s.String() != "***" || fmt.Sprintf("%v %#v", s, s) != "*** ***" || s.Source() != "literal" {
			t.Errorf("literal secret revealed: %s %s", s.String(), s.Source())
		}
	}
	if LiteralSecret("") != "" {
		t.Error("empty literal must stay empty")
	}
}

func TestApplyRemote(t *testing.T) {
	off := false
	base := defaultIntegrations()
	base.Redis.Username = "local-user"
	base.Redis.Password = "env:OPENLOG_REDIS_PASSWORD"
	base.Redis.Instances = []InstanceConfig{{Match: InstanceMatch{Port: 6380}, InstanceSettings: InstanceSettings{Endpoint: "127.0.0.1:6380"}}}
	base.MySQL.Instances = []InstanceConfig{{Match: InstanceMatch{Port: 3307}, Enabled: &off}}

	items := []RemoteItem{
		{Integration: "redis", Enabled: true, Password: "s3cret"},                                  // all hosts
		{Integration: "redis", Enabled: true, Match: &RemoteMatch{Port: 6380}, Username: "remote"}, // updates the config.yaml instance
		{Integration: "nginx", Enabled: true, Match: &RemoteMatch{Instance: "/usr/sbin/nginx"}, Endpoint: "http://127.0.0.1:8080/status"},
		{Integration: "docker", Enabled: false},                                       // disable on this host
		{Integration: "nginx", Enabled: true, Endpoint: "127.0.0.1:80"},               // invalid: not a URL
		{Integration: "redis", Enabled: true, Database: "x", Password: "leak-me-not"}, // invalid: database unsupported
		{Integration: "cassandra", Enabled: true},                                     // unknown
		{Integration: "postgresql", Enabled: true, Match: &RemoteMatch{Container: "db"}, Username: "a"},
		{Integration: "postgresql", Enabled: true, Match: &RemoteMatch{Container: "db"}, Databases: []string{"app"}}, // merges into the previous
	}
	got, errs := base.ApplyRemote(items)
	if len(errs) != 3 {
		t.Fatalf("errors = %v, want 3", errs)
	}
	for _, err := range errs {
		if strings.Contains(err.Error(), "leak-me-not") {
			t.Errorf("error reveals a password: %v", err)
		}
	}
	if pw, _ := got.Redis.Password.Resolve(); pw != "s3cret" || got.Redis.Username != "local-user" {
		t.Errorf("redis defaults = %q/%q", got.Redis.Username, pw)
	}
	if len(got.Redis.Instances) != 1 || got.Redis.Instances[0].Username != "remote" || got.Redis.Instances[0].Endpoint != "127.0.0.1:6380" ||
		got.Redis.Instances[0].Enabled == nil || !*got.Redis.Instances[0].Enabled {
		t.Errorf("redis instances = %+v", got.Redis.Instances)
	}
	if len(got.Nginx.Instances) != 1 || got.Nginx.Instances[0].Match.Instance != "/usr/sbin/nginx" || got.Nginx.Endpoint != "" {
		t.Errorf("nginx = %+v", got.Nginx)
	}
	if got.Docker.Enabled {
		t.Error("docker must be disabled")
	}
	pg := got.PostgreSQL.Instances
	if len(pg) != 1 || pg[0].Username != "a" || strings.Join(pg[0].Databases, ",") != "app" {
		t.Errorf("postgresql instances = %+v", pg)
	}
	// The base configuration is not modified.
	if base.Redis.Password != "env:OPENLOG_REDIS_PASSWORD" || base.Redis.Instances[0].Username != "" || !base.Docker.Enabled || len(base.Nginx.Instances) != 0 {
		t.Errorf("base modified: %+v", base.Redis)
	}
	if !base.RemoteConfig {
		t.Error("remote_config defaults to true")
	}
}

func TestRemoteConfigYAMLOptOut(t *testing.T) {
	cfg := Default()
	if err := Parse([]byte("integrations:\n  remote_config: false\n"), cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Integrations.RemoteConfig {
		t.Error("integrations.remote_config: false not applied")
	}
}
