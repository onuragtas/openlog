package agent

import (
	"hash/fnv"
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

// servicesFingerprint identifies a published set cheaply, so an unchanged set costs no file I/O at all.
//
// The zero value means "never published" rather than "empty": an empty set hashes to the FNV offset basis,
// which is not zero, so the first round always publishes even on a machine that discovered nothing.
//
// Order sensitive on purpose. Discovery may hand back the same services in a different order, and the only
// cost of that is one extra call — which PublishServices then turns into a no-op after comparing bytes.
// Sorting here to avoid it would spend work on every round to save work on almost none.
func servicesFingerprint(entries []resource.ServiceEntry) uint64 {
	h := fnv.New64a()
	for _, e := range entries {
		_, _ = h.Write([]byte(e.Kind))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(e.Key))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(e.ID))
		_, _ = h.Write([]byte{0x1e})
	}
	return h.Sum64()
}
