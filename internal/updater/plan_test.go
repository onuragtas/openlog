package updater

import (
	"context"
	"strings"
	"testing"
)

func TestSelectTarget(t *testing.T) {
	src := &fakeSource{
		channels: map[string][]string{
			"stable": {"0.9.0", "0.9.1", "0.10.0", "0.11.0"},
			"beta":   {"0.12.0-beta.1"},
		},
		manifests: map[string]string{
			"0.9.1":         manifestJSON("0.9.1", "stable", "0.8.0", "ghcr.io/onuragtas/openlog@sha256:91"),
			"0.10.0":        manifestJSON("0.10.0", "stable", "0.9.0", "ghcr.io/onuragtas/openlog@sha256:100"),
			"0.11.0":        manifestJSON("0.11.0", "stable", "0.10.0", "ghcr.io/onuragtas/openlog@sha256:110"),
			"0.12.0-beta.1": manifestJSON("0.12.0-beta.1", "beta", "0.11.0", "ghcr.io/onuragtas/openlog@sha256:120b"),
		},
	}
	idx, _ := src.Index(context.Background(), "")
	pick := func(current, channel string, failed []string, repo string) (string, string) {
		t.Helper()
		tg, reason, err := SelectTarget(context.Background(), idx, channel, mustVersion(t, current), failed, repo, src.Manifest)
		if err != nil {
			t.Fatal(err)
		}
		if tg == nil {
			return "", reason
		}
		return tg.Version.String() + " " + tg.Image, reason
	}
	// 0.11.0 requires >= 0.10.0: from 0.9.0 the newest reachable release is 0.10.0.
	if got, _ := pick("0.9.0", "stable", nil, ""); got != "0.10.0 ghcr.io/onuragtas/openlog@sha256:100" {
		t.Errorf("intermediate hop: %q", got)
	}
	if got, _ := pick("0.10.0", "stable", nil, ""); !strings.HasPrefix(got, "0.11.0 ") {
		t.Errorf("from 0.10.0: %q", got)
	}
	if got, _ := pick("0.10.0", "beta", nil, ""); !strings.HasPrefix(got, "0.11.0 ") {
		t.Errorf("beta requires 0.11.0 first: %q", got)
	}
	if got, _ := pick("0.11.0", "beta", nil, ""); !strings.HasPrefix(got, "0.12.0-beta.1 ") {
		t.Errorf("beta: %q", got)
	}
	if got, reason := pick("0.11.0", "stable", nil, ""); got != "" || !strings.Contains(reason, "newest release") {
		t.Errorf("up to date: %q %q", got, reason)
	}
	if got, reason := pick("0.10.0", "stable", []string{"0.11.0"}, ""); got != "" || !strings.Contains(reason, "failed before") {
		t.Errorf("failed version must be skipped: %q %q", got, reason)
	}
	if got, _ := pick("0.10.0", "stable", nil, "registry.local/mirror/openlog"); got != "0.11.0 registry.local/mirror/openlog@sha256:110" {
		t.Errorf("repository rewrite: %q", got)
	}
	// A missing manifest or image makes the next older release the candidate.
	delete(src.manifests, "0.11.0")
	src.manifests["0.10.0"] = manifestJSON("0.10.0", "stable", "0.9.0", "")
	if got, reason := pick("0.9.0", "stable", nil, ""); !strings.HasPrefix(got, "0.9.1 ") {
		t.Errorf("fallback: %q (%s)", got, reason)
	}
}

func TestRewriteRepository(t *testing.T) {
	for in, want := range map[string]string{
		"ghcr.io/o/openlog@sha256:abc": "mirror:5000/openlog@sha256:abc",
		"ghcr.io/o/openlog:0.9.1":      "mirror:5000/openlog:0.9.1",
		"openlog":                      "mirror:5000/openlog",
	} {
		if got := rewriteRepository(in, "mirror:5000/openlog"); got != want {
			t.Errorf("rewriteRepository(%q) = %q, want %q", in, got, want)
		}
	}
	if repo, tag := splitRef("localhost:5000/openlog"); repo != "localhost:5000/openlog" || tag != "latest" {
		t.Errorf("splitRef: %s %s", repo, tag)
	}
}
