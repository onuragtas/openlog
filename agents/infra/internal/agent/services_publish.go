package agent

import (
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/discovery"
	"github.com/onuragtas/openlog/agents/infra/internal/resource"
)

// publishedServices flattens discovered services into the published map (resource.PublishServices).
//
// Only the keys that outlive a round are published. Instance is the executable path discovery groups by,
// and is published only when it is absolute — the same rule NewServiceIndex applies when it builds byExe,
// so a reader and the agent's own lookup agree on what counts as an executable.
func publishedServices(services []discovery.Service) []resource.ServiceEntry {
	out := make([]resource.ServiceEntry, 0, len(services))
	for _, s := range services {
		if s.RuleID == "" {
			continue
		}
		if strings.HasPrefix(s.Instance, "/") {
			out = append(out, resource.ServiceEntry{Kind: "exe", Key: s.Instance, ID: s.RuleID})
		}
		for _, c := range s.ContainerIDs {
			out = append(out, resource.ServiceEntry{Kind: "container", Key: c, ID: s.RuleID})
		}
	}
	return out
}
