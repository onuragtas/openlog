package agent

import (
	"testing"

	"github.com/onuragtas/openlog/agents/infra/internal/discovery"
	"github.com/onuragtas/openlog/agents/infra/internal/resource"
)

// The published map has to agree with NewServiceIndex about what an executable key is: it publishes
// Instance only when it is absolute, and a reader that disagreed would name processes after a service that
// the agent's own lookup never matches.
func TestPublishedServicesMirrorsTheIndexRules(t *testing.T) {
	got := publishedServices([]discovery.Service{
		{RuleID: "redis", Instance: "/usr/bin/redis-server", ContainerIDs: []string{"c1", "c2"}},
		{RuleID: "jvm", Instance: "java"}, // not a path: discovery groups it, byExe does not index it
		{RuleID: "", Instance: "/x"},      // no rule id is nothing to name a sample after
		{RuleID: "nginx", Instance: "/usr/sbin/nginx"},
	})
	want := []resource.ServiceEntry{
		{Kind: "exe", Key: "/usr/bin/redis-server", ID: "redis"},
		{Kind: "container", Key: "c1", ID: "redis"},
		{Kind: "container", Key: "c2", ID: "redis"},
		{Kind: "exe", Key: "/usr/sbin/nginx", ID: "nginx"},
	}
	if len(got) != len(want) {
		t.Fatalf("published %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// The fingerprint is what keeps file I/O off the collection round when nothing changed, so it has to
// notice every field — a key that moved to another service is exactly the change worth republishing for.
func TestServicesFingerprintNoticesEveryField(t *testing.T) {
	base := []resource.ServiceEntry{
		{Kind: "exe", Key: "/usr/bin/redis-server", ID: "redis"},
		{Kind: "container", Key: "c1", ID: "redis"},
	}
	fp := servicesFingerprint(base)
	if fp != servicesFingerprint([]resource.ServiceEntry{
		{Kind: "exe", Key: "/usr/bin/redis-server", ID: "redis"},
		{Kind: "container", Key: "c1", ID: "redis"},
	}) {
		t.Error("an unchanged set changed its fingerprint")
	}
	for name, changed := range map[string][]resource.ServiceEntry{
		"kind":    {{Kind: "container", Key: "/usr/bin/redis-server", ID: "redis"}, base[1]},
		"key":     {{Kind: "exe", Key: "/usr/bin/redis-cli", ID: "redis"}, base[1]},
		"id":      {{Kind: "exe", Key: "/usr/bin/redis-server", ID: "valkey"}, base[1]},
		"removed": {base[0]},
		"added":   append(append([]resource.ServiceEntry{}, base...), resource.ServiceEntry{Kind: "exe", Key: "/x", ID: "y"}),
	} {
		if servicesFingerprint(changed) == fp {
			t.Errorf("changing the %s did not change the fingerprint", name)
		}
	}
}

// Zero means "never published", so an empty set must not hash to zero: a machine that discovered nothing
// would otherwise never publish, and never correct itself when something appeared.
func TestEmptySetIsNotTheZeroFingerprint(t *testing.T) {
	if servicesFingerprint(nil) == 0 {
		t.Error("an empty set hashes to zero, which is the never-published sentinel")
	}
}
