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
