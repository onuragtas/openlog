// Package cost prices hosts from their cloud instance facts and attributes that price to the
// services and containers running on them (docs/contracts/cost.md).
//
// The prices are a static table in the repository, not billing data. openlog never talks to a
// cloud billing API: it knows what machine a host is (semantic-conventions §1 "Cloud instance
// facts") and multiplies by a list price. Everything the estimate ignores is named in the table's
// "note" and in the contract; the API repeats it in every response so a number is never shown
// without its caveat.
package cost

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

//go:embed prices.json
var builtinPricesJSON []byte

// Source says where a host's price came from. It travels with the number so the UI can label an
// estimate as an estimate.
type Source string

const (
	// SourceTable: an exact instance type match in the built-in table.
	SourceTable Source = "table"
	// SourceOverride: an exact match in the operator's override file.
	SourceOverride Source = "override"
	// SourceFallback: no instance type match; priced per vCPU and per GB of memory.
	SourceFallback Source = "fallback"
	// SourceNone: not priceable (no instance facts and no capacity).
	SourceNone Source = "none"
)

// Lifecycle values (openlog.host.lifecycle).
const (
	LifecycleOnDemand    = "on-demand"
	LifecycleSpot        = "spot"
	LifecyclePreemptible = "preemptible"
)

// InstancePrice is the hourly price of one instance type in the table's reference region.
// Spot is 0 when unknown, in which case the on-demand rate is used and the result says so.
type InstancePrice struct {
	OnDemand float64 `json:"on_demand"`
	Spot     float64 `json:"spot,omitempty"`
}

// FallbackRate prices a machine openlog has no entry for, from its capacity alone.
type FallbackRate struct {
	VCPUHour float64 `json:"vcpu_hour"`
	GBHour   float64 `json:"gb_hour"`
}

// Table is the price table: the built-in one, with the operator's override merged over it.
type Table struct {
	Version  int    `json:"version"`
	Updated  string `json:"updated"`
	Currency string `json:"currency"`
	// Note is the honesty caveat; the API returns it with every cost response.
	Note             string            `json:"note,omitempty"`
	ReferenceRegions map[string]string `json:"reference_regions,omitempty"`
	Sources          []string          `json:"sources,omitempty"`
	// Instances is provider → instance type → price.
	Instances map[string]map[string]InstancePrice `json:"instances"`
	// RegionMultipliers is provider → region → factor applied to every price of that provider.
	// A region that is not listed uses 1.0 (the reference region's price).
	RegionMultipliers map[string]map[string]float64 `json:"region_multipliers,omitempty"`
	// Fallback is provider → per-vCPU/per-GB rate; the key "default" is used for unknown providers.
	Fallback map[string]FallbackRate `json:"fallback"`

	// OverrideFile is the path the override was read from ("" when none was configured).
	OverrideFile string `json:"override_file,omitempty"`
	// OverriddenKeys lists what the override changed ("aws/m5.large", "fallback/aws",
	// "region_multipliers/aws"), so an operator can see their corrections took effect.
	OverriddenKeys []string `json:"overridden_keys,omitempty"`

	// index is a case-insensitive lookup of Instances: provider → lower(type) → price.
	index map[string]map[string]InstancePrice
}

// Instance is what is known about a host's machine; every field may be empty.
type Instance struct {
	Provider    string
	Type        string
	Region      string
	Lifecycle   string
	VCPUs       float64
	MemoryBytes float64
}

// MemoryGB is the instance memory in GiB (the unit cloud providers quote).
func (i Instance) MemoryGB() float64 { return i.MemoryBytes / (1 << 30) }

// Price is a resolved hourly price with its provenance.
type Price struct {
	// USDPerHour is in the table's currency (USD in the built-in table).
	USDPerHour float64 `json:"usd_per_hour"`
	Source     Source  `json:"source"`
	// Note is a caveat specific to this lookup, e.g. an unknown spot price. May be empty.
	Note string `json:"note,omitempty"`
	// RegionMultiplier is the factor applied for the host's region (1.0 when unknown).
	RegionMultiplier float64 `json:"region_multiplier"`
}

// Priced reports whether a usable price was found.
func (p Price) Priced() bool { return p.Source != SourceNone && p.USDPerHour > 0 }

// Builtin returns the price table compiled into the binary.
func Builtin() (*Table, error) {
	var t Table
	if err := json.Unmarshal(builtinPricesJSON, &t); err != nil {
		return nil, fmt.Errorf("cost: built-in price table: %w", err)
	}
	t.buildIndex()
	return &t, nil
}

// Load returns the built-in table with the override file at path merged over it. An empty path
// returns the built-in table unchanged. A missing, unreadable or invalid file is an error: a
// silently ignored price correction would be worse than a failed start-up.
func Load(path string) (*Table, error) {
	t, err := Builtin()
	if err != nil {
		return nil, err
	}
	if path == "" {
		return t, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cost: price override %s: %w", path, err)
	}
	var o Table
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields() // a typo in a key must fail loudly, not price nothing
	if err := dec.Decode(&o); err != nil {
		return nil, fmt.Errorf("cost: price override %s: %w", path, err)
	}
	t.merge(&o, path)
	t.buildIndex()
	return t, nil
}

// merge applies an override document. Only what the override names is replaced: one instance
// type, one provider's fallback or one provider's region multipliers, so a correction for a
// single machine does not drop the rest of the table.
func (t *Table) merge(o *Table, path string) {
	t.OverrideFile = path
	if o.Updated != "" {
		t.Updated = o.Updated
	}
	if o.Currency != "" {
		t.Currency = o.Currency
	}
	if o.Note != "" {
		t.Note = o.Note
	}
	var keys []string
	for provider, types := range o.Instances {
		provider = strings.ToLower(provider)
		if t.Instances == nil {
			t.Instances = map[string]map[string]InstancePrice{}
		}
		if t.Instances[provider] == nil {
			t.Instances[provider] = map[string]InstancePrice{}
		}
		for typ, price := range types {
			t.Instances[provider][typ] = price
			keys = append(keys, provider+"/"+typ)
		}
	}
	for provider, rate := range o.Fallback {
		if t.Fallback == nil {
			t.Fallback = map[string]FallbackRate{}
		}
		t.Fallback[strings.ToLower(provider)] = rate
		keys = append(keys, "fallback/"+strings.ToLower(provider))
	}
	for provider, regions := range o.RegionMultipliers {
		provider = strings.ToLower(provider)
		if t.RegionMultipliers == nil {
			t.RegionMultipliers = map[string]map[string]float64{}
		}
		if t.RegionMultipliers[provider] == nil {
			t.RegionMultipliers[provider] = map[string]float64{}
		}
		for region, m := range regions {
			t.RegionMultipliers[provider][strings.ToLower(region)] = m
		}
		keys = append(keys, "region_multipliers/"+provider)
	}
	sort.Strings(keys)
	t.OverriddenKeys = keys
}

// buildIndex makes instance type lookup case-insensitive (Azure quotes "Standard_D4s_v5",
// agents and operators write it in every casing).
func (t *Table) buildIndex() {
	t.index = make(map[string]map[string]InstancePrice, len(t.Instances))
	for provider, types := range t.Instances {
		p := strings.ToLower(provider)
		if t.index[p] == nil {
			t.index[p] = make(map[string]InstancePrice, len(types))
		}
		for typ, price := range types {
			t.index[p][strings.ToLower(typ)] = price
		}
	}
}

// overridden reports whether the override file replaced this instance type.
func (t *Table) overridden(provider, typ string) bool {
	key := strings.ToLower(provider) + "/" + typ
	for _, k := range t.OverriddenKeys {
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}

// regionMultiplier returns the factor for a provider's region, 1.0 when it is not listed.
func (t *Table) regionMultiplier(provider, region string) float64 {
	regions, ok := t.RegionMultipliers[strings.ToLower(provider)]
	if !ok {
		return 1
	}
	if m, ok := regions[strings.ToLower(region)]; ok && m > 0 {
		return m
	}
	return 1
}

// Price resolves the hourly price of one machine:
//
//  1. exact instance type in the table (the override wins, it is merged in);
//  2. otherwise the per-vCPU/per-GB fallback of the provider, or of "default";
//  3. otherwise no price at all — the host is reported as unpriced, never as free.
//
// The region multiplier is applied in cases 1 and 2.
func (t *Table) Price(in Instance) Price {
	mult := t.regionMultiplier(in.Provider, in.Region)
	p := Price{Source: SourceNone, RegionMultiplier: mult}

	if types, ok := t.index[strings.ToLower(in.Provider)]; ok && in.Type != "" {
		if entry, ok := types[strings.ToLower(in.Type)]; ok {
			rate := entry.OnDemand
			p.Source = SourceTable
			if t.overridden(in.Provider, in.Type) {
				p.Source = SourceOverride
			}
			switch in.Lifecycle {
			case LifecycleSpot, LifecyclePreemptible:
				if entry.Spot > 0 {
					rate = entry.Spot
				} else {
					p.Note = "no spot price for this instance type; the on-demand rate is used, so the estimate is too high"
				}
			}
			p.USDPerHour = rate * mult
			return p
		}
	}

	rate, ok := t.Fallback[strings.ToLower(in.Provider)]
	if !ok {
		rate, ok = t.Fallback["default"]
	}
	if !ok || (in.VCPUs <= 0 && in.MemoryBytes <= 0) {
		p.Note = "no instance type and no capacity known for this host; it has no price"
		return p
	}
	p.Source = SourceFallback
	p.USDPerHour = (rate.VCPUHour*in.VCPUs + rate.GBHour*in.MemoryGB()) * mult
	switch {
	case in.Type == "" && in.Provider == "":
		p.Note = "host is not in a known cloud; priced per vCPU and per GB at a generic rate"
	case in.Type == "":
		p.Note = "instance type unknown; priced per vCPU and per GB"
	default:
		p.Note = fmt.Sprintf("no price for instance type %q; priced per vCPU and per GB", in.Type)
	}
	if in.Lifecycle == LifecycleSpot || in.Lifecycle == LifecyclePreemptible {
		p.Note += "; the per-vCPU rate is an on-demand rate, so a spot machine is estimated too high"
	}
	return p
}
