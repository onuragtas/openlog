package config

import (
	"strings"
	"testing"
)

func TestOnboardingPublicURLs(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }

	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Onboarding.IngestPublicURL != "" || c.Onboarding.IngestPublicGRPCURL != "" {
		t.Fatalf("defaults = %+v, want empty", c.Onboarding)
	}

	c, err = Load(env(map[string]string{
		"OPENLOG_INGEST_PUBLIC_URL":      " https://ingest.example.com:4318/ ",
		"OPENLOG_INGEST_PUBLIC_GRPC_URL": "https://ingest.example.com:4317",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Onboarding.IngestPublicURL != "https://ingest.example.com:4318" || c.Onboarding.IngestPublicGRPCURL != "https://ingest.example.com:4317" {
		t.Fatalf("parsed = %+v", c.Onboarding)
	}

	// A path prefix behind a reverse proxy is allowed.
	if _, err := Load(env(map[string]string{"OPENLOG_INGEST_PUBLIC_URL": "https://example.com/otlp"})); err != nil {
		t.Fatalf("path prefix: %v", err)
	}

	for _, bad := range []string{
		"ingest.example.com:4318",
		"ftp://ingest.example.com",
		"https://",
		"https://user:pw@ingest.example.com",
		"https://ingest.example.com/?x=1",
		"https://ingest.example.com/#frag",
		"https://ingest.example.com/$(id)",
		"https://ingest.example.com/'x'",
	} {
		_, err := Load(env(map[string]string{"OPENLOG_INGEST_PUBLIC_GRPC_URL": bad}))
		if err == nil || !strings.Contains(err.Error(), "OPENLOG_INGEST_PUBLIC_GRPC_URL") {
			t.Errorf("%q: err = %v, want OPENLOG_INGEST_PUBLIC_GRPC_URL error", bad, err)
		}
	}
}
