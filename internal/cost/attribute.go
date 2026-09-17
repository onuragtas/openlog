package cost

import (
	"math"
	"sort"
)

// Cost attribution (docs/contracts/cost.md §3). The maths here is pure: the API layer reads the
// rollups, fills in Input and renders Result. Every number below is derived from data openlog
// already stores; nothing is inferred from a cloud bill.
//
// The split of one host's price:
//
//	host cost      = price per hour × hours the host reported
//	workload share = CPUWeight × (workload core-hours / host core-hours)
//	               + MemoryWeight × (workload byte-hours / host byte-hours)
//	host usage     = CPUWeight × (used cores / vCPUs) + MemoryWeight × (used bytes / total bytes)
//
// and the host cost decomposes into four buckets that always add up to it:
//
//	services      Σ shares of workloads linked to an APM service
//	unallocated   Σ shares of workloads with no service (containers openlog sees but cannot name)
//	unattributed  host usage that no workload accounts for (processes outside containers)
//	idle          capacity nobody used at all
//
// Idle is never spread over the services: an idle machine is the finding, not an accounting error.

// Weights of the two resources in a workload's share. They are equal on purpose: a single
// "cost driver" (CPU only) makes memory-heavy services look free, and vice versa. Documented
// in cost.md so a reader can reproduce the arithmetic by hand.
const (
	CPUWeight    = 0.5
	MemoryWeight = 0.5
)

// Host is one host over the queried range.
type Host struct {
	HostID   string
	HostName string
	// Instance facts from the host's resource attributes (semantic-conventions §1).
	Provider, InstanceType, Region, Zone, Lifecycle string
	// Capacity: system.cpu.logical.count and system.memory.limit.
	VCPUs       float64
	MemoryBytes float64
	// Mean usage over the range: cores actually busy and bytes actually used. Both 0 when the
	// host reports no system metrics, in which case usage is taken to be the workloads' usage
	// and UsageKnown is false.
	UsedCores      float64
	UsedMemoryByte float64
	UsageKnown     bool
	// Hours the host reported within the range. A host that reported for half the range costs
	// half as much: openlog prices what it observed, never what it did not see.
	Hours float64
}

// Workload is one container over the queried range, already linked to a service where possible.
type Workload struct {
	HostID        string
	ContainerID   string
	ContainerName string
	// ServiceName is "" when no APM service was linked to this container.
	ServiceName      string
	ServiceNamespace string
	Environment      string
	// Mean cores and bytes used while the workload ran.
	Cores       float64
	MemoryBytes float64
	Hours       float64
}

// Input is everything the attribution needs.
type Input struct {
	Hosts     []Host
	Workloads []Workload
}

// HostCost is one host's price and its split.
type HostCost struct {
	HostID       string  `json:"host_id"`
	HostName     string  `json:"host_name"`
	Provider     string  `json:"provider"`
	InstanceType string  `json:"instance_type"`
	Region       string  `json:"region"`
	Zone         string  `json:"zone"`
	Lifecycle    string  `json:"lifecycle"`
	VCPUs        float64 `json:"vcpus"`
	MemoryBytes  float64 `json:"memory_bytes"`
	Hours        float64 `json:"hours"`
	Price        Price   `json:"price"`
	// Total is the whole host over the range; the four buckets below add up to it.
	Total        float64 `json:"total"`
	Services     float64 `json:"services"`
	Unallocated  float64 `json:"unallocated"`
	Unattributed float64 `json:"unattributed"`
	Idle         float64 `json:"idle"`
	// UsedShare is the fraction of the machine that was in use (0..1).
	UsedShare float64 `json:"used_share"`
	// IdleShare is 1 − UsedShare, the headline "how much of this machine is doing nothing".
	IdleShare float64 `json:"idle_share"`
	// Oversubscribed: the workloads' shares summed above the host's usage or above the whole
	// machine and were scaled down to fit. The host's total is still exact.
	Oversubscribed bool `json:"oversubscribed"`
	// Priced is false when no price could be determined; the host then contributes nothing to
	// the totals and is counted in Summary.UnpricedHosts instead.
	Priced bool `json:"priced"`
}

// ServiceCost is one APM service's share across all hosts.
type ServiceCost struct {
	ServiceName      string   `json:"service_name"`
	ServiceNamespace string   `json:"service_namespace"`
	Environment      string   `json:"environment"`
	Total            float64  `json:"total"`
	Hosts            []string `json:"hosts"`
	Containers       int      `json:"containers"`
}

// ContainerCost is one container's share of its host.
type ContainerCost struct {
	ContainerID   string  `json:"container_id"`
	ContainerName string  `json:"container_name"`
	HostID        string  `json:"host_id"`
	HostName      string  `json:"host_name"`
	ServiceName   string  `json:"service_name"`
	Total         float64 `json:"total"`
	CPUShare      float64 `json:"cpu_share"`
	MemoryShare   float64 `json:"memory_share"`
	Share         float64 `json:"share"`

	// Service coordinates needed to aggregate by service; not part of the container response,
	// which names the service by ServiceName only.
	serviceNamespace string
	environment      string
}

// Summary totals the range.
type Summary struct {
	Currency string `json:"currency"`
	// Total is the cost of every priced host over the range; the four buckets add up to it.
	Total        float64 `json:"total"`
	Services     float64 `json:"services"`
	Unallocated  float64 `json:"unallocated"`
	Unattributed float64 `json:"unattributed"`
	Idle         float64 `json:"idle"`
	// IdleShare is Idle / Total (0 when Total is 0).
	IdleShare float64 `json:"idle_share"`
	// PerHour is the current run rate: Total / host-hours, i.e. what the fleet costs per hour.
	PerHour       float64 `json:"per_hour"`
	Hosts         int     `json:"hosts"`
	PricedHosts   int     `json:"priced_hosts"`
	UnpricedHosts int     `json:"unpriced_hosts"`
	HostHours     float64 `json:"host_hours"`
}

// Result is the whole attribution.
type Result struct {
	Summary    Summary         `json:"summary"`
	Hosts      []HostCost      `json:"hosts"`
	Services   []ServiceCost   `json:"services"`
	Containers []ContainerCost `json:"containers"`
}

// Attribute prices every host with t and splits each host's cost over its workloads.
// Hosts without a price are reported (Priced=false) but contribute to no total.
func Attribute(t *Table, in Input) Result {
	byHost := map[string][]Workload{}
	for _, w := range in.Workloads {
		byHost[w.HostID] = append(byHost[w.HostID], w)
	}

	res := Result{Hosts: []HostCost{}, Services: []ServiceCost{}, Containers: []ContainerCost{}}
	res.Summary.Currency = t.Currency
	services := map[serviceKey]*ServiceCost{}
	serviceHosts := map[serviceKey]map[string]bool{}

	for _, h := range in.Hosts {
		hc, containers := attributeHost(t, h, byHost[h.HostID])
		res.Hosts = append(res.Hosts, hc)
		res.Summary.Hosts++
		if !hc.Priced {
			res.Summary.UnpricedHosts++
			continue
		}
		res.Summary.PricedHosts++
		res.Summary.HostHours += hc.Hours
		res.Summary.Total += hc.Total
		res.Summary.Services += hc.Services
		res.Summary.Unallocated += hc.Unallocated
		res.Summary.Unattributed += hc.Unattributed
		res.Summary.Idle += hc.Idle
		res.Containers = append(res.Containers, containers...)
		for _, c := range containers {
			if c.ServiceName == "" {
				continue
			}
			k := serviceKey{c.ServiceName, c.serviceNamespace, c.environment}
			s, ok := services[k]
			if !ok {
				s = &ServiceCost{ServiceName: k.name, ServiceNamespace: k.namespace, Environment: k.environment, Hosts: []string{}}
				services[k] = s
				serviceHosts[k] = map[string]bool{}
			}
			s.Total += c.Total
			s.Containers++
			if !serviceHosts[k][c.HostID] {
				serviceHosts[k][c.HostID] = true
				s.Hosts = append(s.Hosts, c.HostID)
			}
		}
	}

	for _, s := range services {
		sort.Strings(s.Hosts)
		res.Services = append(res.Services, *s)
	}
	if res.Summary.Total > 0 {
		res.Summary.IdleShare = res.Summary.Idle / res.Summary.Total
	}
	if res.Summary.HostHours > 0 {
		res.Summary.PerHour = res.Summary.Total / res.Summary.HostHours
	}

	sort.Slice(res.Hosts, func(i, j int) bool {
		if res.Hosts[i].Total != res.Hosts[j].Total {
			return res.Hosts[i].Total > res.Hosts[j].Total
		}
		return res.Hosts[i].HostID < res.Hosts[j].HostID
	})
	sort.Slice(res.Services, func(i, j int) bool {
		a, b := res.Services[i], res.Services[j]
		if a.Total != b.Total {
			return a.Total > b.Total
		}
		if a.ServiceName != b.ServiceName {
			return a.ServiceName < b.ServiceName
		}
		return a.Environment < b.Environment
	})
	sort.Slice(res.Containers, func(i, j int) bool {
		if res.Containers[i].Total != res.Containers[j].Total {
			return res.Containers[i].Total > res.Containers[j].Total
		}
		return res.Containers[i].ContainerID < res.Containers[j].ContainerID
	})
	return res
}

type serviceKey struct{ name, namespace, environment string }

// attributeHost prices one host and splits it. The returned containers carry the service
// coordinates needed for the service aggregation.
func attributeHost(t *Table, h Host, workloads []Workload) (HostCost, []ContainerCost) {
	price := t.Price(Instance{
		Provider: h.Provider, Type: h.InstanceType, Region: h.Region,
		Lifecycle: h.Lifecycle, VCPUs: h.VCPUs, MemoryBytes: h.MemoryBytes,
	})
	hc := HostCost{
		HostID: h.HostID, HostName: h.HostName, Provider: h.Provider, InstanceType: h.InstanceType,
		Region: h.Region, Zone: h.Zone, Lifecycle: h.Lifecycle, VCPUs: h.VCPUs,
		MemoryBytes: h.MemoryBytes, Hours: h.Hours, Price: price,
	}
	if !price.Priced() || h.Hours <= 0 {
		if !price.Priced() {
			return hc, nil
		}
		// A host with a price but no observed time costs nothing over this range.
		hc.Priced = true
		return hc, nil
	}
	hc.Priced = true
	hc.Total = price.USDPerHour * h.Hours

	// Capacity over the range, in core-hours and byte-hours.
	coreHours := h.VCPUs * h.Hours
	byteHours := h.MemoryBytes * h.Hours

	// Each workload's share of the machine.
	containers := make([]ContainerCost, 0, len(workloads))
	var sumShare float64
	for _, w := range workloads {
		cpu := ratio(w.Cores*w.Hours, coreHours)
		mem := ratio(w.MemoryBytes*w.Hours, byteHours)
		share := CPUWeight*cpu + MemoryWeight*mem
		if share <= 0 {
			continue
		}
		containers = append(containers, ContainerCost{
			ContainerID: w.ContainerID, ContainerName: w.ContainerName, HostID: h.HostID, HostName: h.HostName,
			ServiceName: w.ServiceName, serviceNamespace: w.ServiceNamespace, environment: w.Environment,
			CPUShare: cpu, MemoryShare: mem, Share: share,
		})
		sumShare += share
	}

	// How much of the machine was in use at all. Without host metrics the workloads are all
	// openlog can see, so usage is taken to equal their sum (Unattributed is then 0).
	usedShare := sumShare
	if h.UsageKnown {
		usedShare = CPUWeight*ratio(h.UsedCores, h.VCPUs) + MemoryWeight*ratio(h.UsedMemoryByte, h.MemoryBytes)
	}
	usedShare = clamp01(usedShare)

	// Containers cannot own more of the machine than exists, nor more than the host reports as
	// used. Scaling down keeps the host total exact instead of inventing negative idle time.
	if sumShare > usedShare && sumShare > 0 {
		scale := usedShare / sumShare
		for i := range containers {
			containers[i].CPUShare *= scale
			containers[i].MemoryShare *= scale
			containers[i].Share *= scale
		}
		hc.Oversubscribed = true
		sumShare = usedShare
	}

	for i := range containers {
		containers[i].Total = containers[i].Share * hc.Total
		if containers[i].ServiceName == "" {
			hc.Unallocated += containers[i].Total
		} else {
			hc.Services += containers[i].Total
		}
	}
	hc.UsedShare = usedShare
	hc.IdleShare = 1 - usedShare
	hc.Unattributed = math.Max(0, usedShare-sumShare) * hc.Total
	// Idle takes the remainder rather than IdleShare × Total, so floating point drift lands in
	// one bucket and the four always add up to the host total exactly.
	hc.Idle = hc.Total - hc.Services - hc.Unallocated - hc.Unattributed
	if hc.Idle < 0 {
		hc.Idle = 0
	}
	return hc, containers
}

// ratio is a / b, 0 when b is not positive or the result is not finite.
func ratio(a, b float64) float64 {
	if b <= 0 {
		return 0
	}
	v := a / b
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
