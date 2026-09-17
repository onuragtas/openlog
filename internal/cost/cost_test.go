package cost

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

const gib = 1 << 30

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func mustBuiltin(t *testing.T) *Table {
	t.Helper()
	tbl, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	return tbl
}

// The shipped table must be internally consistent: it is the default every installation prices with.
func TestBuiltinTable(t *testing.T) {
	tbl := mustBuiltin(t)
	if tbl.Currency != "USD" || tbl.Updated == "" || tbl.Note == "" {
		t.Errorf("currency=%q updated=%q note=%q", tbl.Currency, tbl.Updated, tbl.Note)
	}
	for _, provider := range []string{"aws", "gcp", "azure"} {
		types, ok := tbl.Instances[provider]
		if !ok || len(types) == 0 {
			t.Fatalf("no instances for %s", provider)
		}
		for name, p := range types {
			if p.OnDemand <= 0 {
				t.Errorf("%s/%s: on-demand price %v", provider, name, p.OnDemand)
			}
			if p.Spot > p.OnDemand {
				t.Errorf("%s/%s: spot %v above on-demand %v", provider, name, p.Spot, p.OnDemand)
			}
		}
		if r, ok := tbl.Fallback[provider]; !ok || r.VCPUHour <= 0 || r.GBHour <= 0 {
			t.Errorf("%s: fallback rate %+v", provider, r)
		}
	}
	if r, ok := tbl.Fallback["default"]; !ok || r.VCPUHour <= 0 {
		t.Errorf("no default fallback rate: %+v", r)
	}
}

func TestPriceInstanceTable(t *testing.T) {
	tbl := mustBuiltin(t)
	cases := []struct {
		name   string
		in     Instance
		want   float64
		source Source
		note   bool
	}{
		{"aws on-demand", Instance{Provider: "aws", Type: "m5.large"}, 0.096, SourceTable, false},
		{"aws spot", Instance{Provider: "aws", Type: "m5.large", Lifecycle: LifecycleSpot}, 0.0336, SourceTable, false},
		{"gcp preemptible uses the spot rate", Instance{Provider: "gcp", Type: "n2-standard-4", Lifecycle: LifecyclePreemptible}, 0.047, SourceTable, false},
		// Azure quotes mixed case; the lookup must not care.
		{"azure case-insensitive", Instance{Provider: "AZURE", Type: "standard_d4s_v5"}, 0.192, SourceTable, false},
		// A region away from the reference region is scaled by the documented multiplier.
		{"region multiplier", Instance{Provider: "aws", Type: "m5.large", Region: "eu-central-1"}, 0.096 * 1.14, SourceTable, false},
		{"unknown region is the reference price", Instance{Provider: "aws", Type: "m5.large", Region: "moon-1"}, 0.096, SourceTable, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := tbl.Price(c.in)
			if !near(got.USDPerHour, c.want) || got.Source != c.source {
				t.Errorf("price = %v (%s), want %v (%s)", got.USDPerHour, got.Source, c.want, c.source)
			}
			if (got.Note != "") != c.note {
				t.Errorf("note = %q", got.Note)
			}
		})
	}
}

// An instance type with no spot price must fall back to on-demand and say so, never report 0.
func TestPriceSpotWithoutSpotRate(t *testing.T) {
	tbl := mustBuiltin(t)
	tbl.Instances["aws"]["x1.custom"] = InstancePrice{OnDemand: 1.5}
	tbl.buildIndex()
	got := tbl.Price(Instance{Provider: "aws", Type: "x1.custom", Lifecycle: LifecycleSpot})
	if !near(got.USDPerHour, 1.5) || got.Source != SourceTable || got.Note == "" {
		t.Errorf("price = %+v", got)
	}
}

func TestPriceFallback(t *testing.T) {
	tbl := mustBuiltin(t)

	// A known provider, an instance type the table does not have: per-vCPU/per-GB.
	got := tbl.Price(Instance{Provider: "aws", Type: "m9.enormous", VCPUs: 4, MemoryBytes: 16 * gib})
	want := 0.0335*4 + 0.0045*16
	if !near(got.USDPerHour, want) || got.Source != SourceFallback || got.Note == "" {
		t.Errorf("unknown type: %+v, want %v", got, want)
	}

	// No provider at all (a machine that is not in a cloud): the generic rate.
	got = tbl.Price(Instance{VCPUs: 2, MemoryBytes: 8 * gib})
	want = 0.035*2 + 0.0045*8
	if !near(got.USDPerHour, want) || got.Source != SourceFallback {
		t.Errorf("no provider: %+v, want %v", got, want)
	}

	// The fallback is an on-demand rate; a spot machine must be flagged as over-estimated.
	got = tbl.Price(Instance{Provider: "aws", Type: "m9.enormous", Lifecycle: LifecycleSpot, VCPUs: 4, MemoryBytes: 16 * gib})
	if got.Source != SourceFallback || got.Note == "" {
		t.Errorf("spot fallback: %+v", got)
	}

	// Nothing known at all: no price, and explicitly not a price of zero.
	got = tbl.Price(Instance{})
	if got.Priced() || got.Source != SourceNone || got.Note == "" {
		t.Errorf("unknown host: %+v", got)
	}
}

func TestLoadOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prices.json")
	const override = `{
	  "updated": "2026-10-01",
	  "instances": {"aws": {"m5.large": {"on_demand": 0.05, "spot": 0.02}}},
	  "fallback": {"aws": {"vcpu_hour": 0.01, "gb_hour": 0.001}},
	  "region_multipliers": {"aws": {"eu-central-1": 2.0}}
	}`
	if err := os.WriteFile(path, []byte(override), 0o600); err != nil {
		t.Fatal(err)
	}
	tbl, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	got := tbl.Price(Instance{Provider: "aws", Type: "m5.large"})
	if !near(got.USDPerHour, 0.05) || got.Source != SourceOverride {
		t.Errorf("overridden price = %+v", got)
	}
	if got := tbl.Price(Instance{Provider: "aws", Type: "m5.large", Region: "eu-central-1"}); !near(got.USDPerHour, 0.10) {
		t.Errorf("overridden multiplier = %v, want 0.10", got.USDPerHour)
	}
	if got := tbl.Price(Instance{Provider: "aws", Type: "m9.enormous", VCPUs: 4, MemoryBytes: 16 * gib}); !near(got.USDPerHour, 0.01*4+0.001*16) {
		t.Errorf("overridden fallback = %v", got.USDPerHour)
	}
	// An override of one machine must not drop the rest of the table.
	if got := tbl.Price(Instance{Provider: "aws", Type: "c5.large"}); !near(got.USDPerHour, 0.085) || got.Source != SourceTable {
		t.Errorf("untouched entry = %+v", got)
	}
	if tbl.Updated != "2026-10-01" || tbl.OverrideFile != path {
		t.Errorf("updated=%q file=%q", tbl.Updated, tbl.OverrideFile)
	}
	want := []string{"aws/m5.large", "fallback/aws", "region_multipliers/aws"}
	if len(tbl.OverriddenKeys) != len(want) {
		t.Fatalf("overridden keys = %v, want %v", tbl.OverriddenKeys, want)
	}
	for i, k := range want {
		if tbl.OverriddenKeys[i] != k {
			t.Errorf("overridden keys = %v, want %v", tbl.OverriddenKeys, want)
			break
		}
	}
}

// A price correction that cannot be read must fail loudly: silently pricing with stale numbers
// is worse than not starting.
func TestLoadOverrideErrors(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("missing override file: want an error")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"instances": [1,2]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil {
		t.Error("invalid override file: want an error")
	}
	typo := filepath.Join(t.TempDir(), "typo.json")
	if err := os.WriteFile(typo, []byte(`{"instance": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(typo); err == nil {
		t.Error("misspelled key: want an error")
	}
	// No override configured is not an error.
	if tbl, err := Load(""); err != nil || tbl.OverrideFile != "" {
		t.Errorf("empty path: %v %q", err, tbl.OverrideFile)
	}
}

// The headline case: one host, two containers, real idle capacity left over.
func TestAttributeSplitsHostAcrossWorkloads(t *testing.T) {
	tbl := mustBuiltin(t)
	in := Input{
		Hosts: []Host{{
			HostID: "h1", HostName: "web-1", Provider: "aws", InstanceType: "m5.large",
			VCPUs: 4, MemoryBytes: 16 * gib, Hours: 1,
			UsedCores: 2, UsedMemoryByte: 8 * gib, UsageKnown: true,
		}},
		Workloads: []Workload{
			{HostID: "h1", ContainerID: "c1", ServiceName: "orders", Cores: 1, MemoryBytes: 4 * gib, Hours: 1},
			{HostID: "h1", ContainerID: "c2", Cores: 0.5, MemoryBytes: 2 * gib, Hours: 1},
		},
	}
	res := Attribute(tbl, in)
	h := res.Hosts[0]

	// 0.096 USD/h for one hour.
	if !near(h.Total, 0.096) || !h.Priced {
		t.Fatalf("host total = %v (priced %v)", h.Total, h.Priced)
	}
	// c1 holds 1/4 of the cores and 1/4 of the memory → a quarter of the machine.
	if !near(h.Services, 0.25*0.096) {
		t.Errorf("services = %v, want %v", h.Services, 0.25*0.096)
	}
	// c2 has no service: real cost, but openlog cannot name an owner.
	if !near(h.Unallocated, 0.125*0.096) {
		t.Errorf("unallocated = %v, want %v", h.Unallocated, 0.125*0.096)
	}
	// The host reports half of itself busy, the containers explain 0.375 of it.
	if !near(h.Unattributed, 0.125*0.096) {
		t.Errorf("unattributed = %v, want %v", h.Unattributed, 0.125*0.096)
	}
	// Half the machine is doing nothing, and that half stays visible as idle.
	if !near(h.Idle, 0.048) || !near(h.IdleShare, 0.5) {
		t.Errorf("idle = %v (share %v), want 0.048 (0.5)", h.Idle, h.IdleShare)
	}
	if sum := h.Services + h.Unallocated + h.Unattributed + h.Idle; !near(sum, h.Total) {
		t.Errorf("buckets sum to %v, want %v", sum, h.Total)
	}
	if h.Oversubscribed {
		t.Error("host must not be marked oversubscribed")
	}

	if len(res.Services) != 1 || res.Services[0].ServiceName != "orders" || !near(res.Services[0].Total, 0.024) {
		t.Errorf("services = %+v", res.Services)
	}
	s := res.Summary
	if !near(s.Total, 0.096) || !near(s.Idle, 0.048) || !near(s.IdleShare, 0.5) || !near(s.PerHour, 0.096) {
		t.Errorf("summary = %+v", s)
	}
	if s.Hosts != 1 || s.PricedHosts != 1 || s.UnpricedHosts != 0 {
		t.Errorf("summary host counts = %+v", s)
	}
}

// A host nobody runs anything on is the finding the whole feature exists for: its entire cost
// must show up as idle, not disappear.
func TestAttributeHostWithoutWorkloads(t *testing.T) {
	tbl := mustBuiltin(t)
	res := Attribute(tbl, Input{Hosts: []Host{{
		HostID: "idle-1", Provider: "aws", InstanceType: "m5.large", VCPUs: 4, MemoryBytes: 16 * gib,
		Hours: 10, UsageKnown: true,
	}}})
	h := res.Hosts[0]
	if !near(h.Total, 0.96) || !near(h.Idle, 0.96) || !near(h.IdleShare, 1) {
		t.Errorf("host = %+v", h)
	}
	if !near(h.Services, 0) || !near(h.Unallocated, 0) || !near(h.Unattributed, 0) {
		t.Errorf("non-idle buckets = %v %v %v", h.Services, h.Unallocated, h.Unattributed)
	}
	if !near(res.Summary.IdleShare, 1) {
		t.Errorf("summary idle share = %v", res.Summary.IdleShare)
	}
	if len(res.Services) != 0 || len(res.Containers) != 0 {
		t.Errorf("services=%d containers=%d", len(res.Services), len(res.Containers))
	}
}

// Usage that no container explains (a database straight on the machine) is its own bucket,
// never reported as idle.
func TestAttributeUncontainerizedUsage(t *testing.T) {
	tbl := mustBuiltin(t)
	res := Attribute(tbl, Input{Hosts: []Host{{
		HostID: "db-1", Provider: "aws", InstanceType: "m5.large", VCPUs: 4, MemoryBytes: 16 * gib,
		Hours: 1, UsedCores: 3, UsedMemoryByte: 12 * gib, UsageKnown: true,
	}}})
	h := res.Hosts[0]
	if !near(h.Unattributed, 0.75*0.096) || !near(h.Idle, 0.25*0.096) {
		t.Errorf("unattributed=%v idle=%v", h.Unattributed, h.Idle)
	}
}

// Without host system metrics the workloads are all openlog can see; the rest is idle and
// nothing is invented as "unattributed".
func TestAttributeWithoutHostUsageMetrics(t *testing.T) {
	tbl := mustBuiltin(t)
	res := Attribute(tbl, Input{
		Hosts:     []Host{{HostID: "h1", Provider: "aws", InstanceType: "m5.large", VCPUs: 4, MemoryBytes: 16 * gib, Hours: 1}},
		Workloads: []Workload{{HostID: "h1", ContainerID: "c1", ServiceName: "api", Cores: 1, MemoryBytes: 4 * gib, Hours: 1}},
	})
	h := res.Hosts[0]
	if !near(h.Unattributed, 0) || !near(h.Services, 0.024) || !near(h.Idle, 0.096-0.024) {
		t.Errorf("host = %+v", h)
	}
}

// Containers claiming more than the machine has must be scaled to fit, never produce negative idle.
func TestAttributeOversubscribed(t *testing.T) {
	tbl := mustBuiltin(t)
	res := Attribute(tbl, Input{
		Hosts: []Host{{
			HostID: "h1", Provider: "aws", InstanceType: "m5.large", VCPUs: 2, MemoryBytes: 8 * gib,
			Hours: 1, UsedCores: 2, UsedMemoryByte: 8 * gib, UsageKnown: true,
		}},
		Workloads: []Workload{
			{HostID: "h1", ContainerID: "c1", ServiceName: "a", Cores: 3, MemoryBytes: 8 * gib, Hours: 1},
			{HostID: "h1", ContainerID: "c2", ServiceName: "b", Cores: 3, MemoryBytes: 8 * gib, Hours: 1},
		},
	})
	h := res.Hosts[0]
	if !h.Oversubscribed {
		t.Error("want the host marked oversubscribed")
	}
	if h.Idle < 0 || h.Unattributed < 0 {
		t.Errorf("negative bucket: idle=%v unattributed=%v", h.Idle, h.Unattributed)
	}
	if sum := h.Services + h.Unallocated + h.Unattributed + h.Idle; !near(sum, h.Total) {
		t.Errorf("buckets sum to %v, want %v", sum, h.Total)
	}
	// The whole machine is claimed, so the two services split it evenly and nothing is idle.
	if !near(h.Services, h.Total) || !near(h.Idle, 0) {
		t.Errorf("services=%v idle=%v total=%v", h.Services, h.Idle, h.Total)
	}
}

// A host openlog cannot price is reported as unpriced, never as costing nothing.
func TestAttributeUnpricedHost(t *testing.T) {
	tbl := mustBuiltin(t)
	res := Attribute(tbl, Input{
		Hosts:     []Host{{HostID: "mystery", Hours: 1}},
		Workloads: []Workload{{HostID: "mystery", ContainerID: "c1", ServiceName: "a", Cores: 1, Hours: 1}},
	})
	if len(res.Hosts) != 1 || res.Hosts[0].Priced {
		t.Fatalf("hosts = %+v", res.Hosts)
	}
	s := res.Summary
	if s.Hosts != 1 || s.UnpricedHosts != 1 || s.PricedHosts != 0 || !near(s.Total, 0) {
		t.Errorf("summary = %+v", s)
	}
	if len(res.Containers) != 0 || len(res.Services) != 0 {
		t.Error("an unpriced host must contribute no service or container cost")
	}
}

// One service on two hosts is one row, and the per-host costs add up.
func TestAttributeServiceAcrossHosts(t *testing.T) {
	tbl := mustBuiltin(t)
	host := func(id string) Host {
		return Host{HostID: id, Provider: "aws", InstanceType: "m5.large", VCPUs: 4, MemoryBytes: 16 * gib,
			Hours: 1, UsedCores: 4, UsedMemoryByte: 16 * gib, UsageKnown: true}
	}
	res := Attribute(tbl, Input{
		Hosts: []Host{host("h2"), host("h1")},
		Workloads: []Workload{
			{HostID: "h1", ContainerID: "c1", ServiceName: "orders", Environment: "prod", Cores: 2, MemoryBytes: 8 * gib, Hours: 1},
			{HostID: "h2", ContainerID: "c2", ServiceName: "orders", Environment: "prod", Cores: 1, MemoryBytes: 4 * gib, Hours: 1},
			{HostID: "h2", ContainerID: "c3", ServiceName: "orders", Environment: "staging", Cores: 1, MemoryBytes: 4 * gib, Hours: 1},
		},
	})
	var prod *ServiceCost
	for i := range res.Services {
		if res.Services[i].Environment == "prod" {
			prod = &res.Services[i]
		}
	}
	if prod == nil {
		t.Fatalf("services = %+v", res.Services)
	}
	// Half of h1 plus a quarter of h2.
	if !near(prod.Total, 0.5*0.096+0.25*0.096) || prod.Containers != 2 {
		t.Errorf("prod = %+v", prod)
	}
	if len(prod.Hosts) != 2 || prod.Hosts[0] != "h1" || prod.Hosts[1] != "h2" {
		t.Errorf("prod hosts = %v", prod.Hosts)
	}
	// prod and staging are separate rows: the same service name in two environments is two costs.
	if len(res.Services) != 2 {
		t.Errorf("services = %+v", res.Services)
	}
	// Results are ordered by cost so the UI can show a top-N without sorting again.
	if res.Services[0].Total < res.Services[1].Total {
		t.Errorf("services not ordered by cost: %+v", res.Services)
	}
}

// A host that reported for part of the range is charged for that part only.
func TestAttributePartialReporting(t *testing.T) {
	tbl := mustBuiltin(t)
	res := Attribute(tbl, Input{Hosts: []Host{
		{HostID: "h1", Provider: "aws", InstanceType: "m5.large", VCPUs: 4, MemoryBytes: 16 * gib, Hours: 0.5, UsageKnown: true},
		{HostID: "h2", Provider: "aws", InstanceType: "m5.large", VCPUs: 4, MemoryBytes: 16 * gib, Hours: 0, UsageKnown: true},
	}})
	if !near(res.Summary.Total, 0.048) {
		t.Errorf("total = %v, want 0.048", res.Summary.Total)
	}
	for _, h := range res.Hosts {
		if h.HostID == "h2" && (!h.Priced || !near(h.Total, 0)) {
			t.Errorf("host with no observed time = %+v", h)
		}
	}
}
