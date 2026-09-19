// Package profiles converts OTLP profiles into the rows openlog stores, and aggregates those rows into the
// tree a flame graph draws.
//
// **This package is the only place that knows the OTLP profiles wire types.** The signal is still
// `v1development` upstream — the proto has been reshaped more than once and will be again — so everything
// past this boundary speaks Row and Node, which are openlog's own. When the proto moves, this file moves
// and nothing else does.
//
// The wire model is a dictionary plus indices: a Sample points at a Stack, a Stack at Locations, a Location
// at Lines, a Line at a Function, and a Function at strings. Storing that faithfully would mean five joins
// to answer "which function burned the CPU", so a row carries the resolved frame names instead: the
// dictionary is small, the expansion happens once here, and the query side stays a GROUP BY.
package profiles

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pprofile"
)

// Limits bound what one request may produce. A profile is the widest signal openlog accepts — a minute of
// CPU samples from one process is thousands of stacks — so the caps are on the number of rows and the depth
// of a stack rather than on bytes alone.
const (
	// MaxFrames is how deep a stored stack goes. Deeper stacks keep their innermost frames, because that is
	// where the time is spent; the outer ones are what a flame graph collapses anyway.
	MaxFrames = 128
	// MaxFrameBytes bounds one frame name. Generics and lambdas produce long symbols; past this the name is
	// truncated rather than dropped, so the frame still groups.
	MaxFrameBytes = 512
	// MaxRowsPerRequest bounds one payload's expansion.
	MaxRowsPerRequest = 200_000
	// MaxAttrBytes bounds one attribute key or value. Sample attributes carry thread names and frame kinds,
	// not payloads, so anything past this is a mistake on the producer's side rather than information.
	MaxAttrBytes = 1024
)

// ErrTooManyRows reports a payload whose samples exceed MaxRowsPerRequest.
var ErrTooManyRows = errors.New("profiles: too many samples in one request")

// Row is one stored sample: a resolved stack and the value measured on it.
type Row struct {
	TenantID         string
	Timestamp        time.Time
	ServiceName      string
	ServiceNamespace string
	Environment      string
	HostID           string
	// ProfileType is the sample type of the profile ("cpu", "alloc_space", "goroutine"), and Unit its unit
	// ("nanoseconds", "bytes", "count") — both from the profile's own ValueType, not invented here.
	ProfileType string
	Unit        string
	// Stack is root first, the way a flame graph reads: main, then its callee, down to the leaf.
	Stack []string
	// Value is the sample's measurement in Unit. A sample carries one value per sample type; openlog stores
	// the profile's own type, so this is that one value.
	Value int64
	// DurationNs is the wall time the profile covers, kept so a rate can be computed without a second query.
	DurationNs uint64
	// ResourceAttributes are the producing resource's attributes, the same ones a span of this service carries.
	ResourceAttributes map[string]string
	// Attributes are the sample's own attributes, resolved from the payload's attribute table: thread name,
	// frame kind, and whatever else the profiler labelled the sample with. These are what a flame graph is
	// filtered by, so they are resolved here rather than left as dictionary indices no query can read.
	Attributes map[string]string
}

// Leaf is the innermost frame, which is what "self time" is attributed to. Empty for an empty stack.
func (r Row) Leaf() string {
	if len(r.Stack) == 0 {
		return ""
	}
	return r.Stack[len(r.Stack)-1]
}

// Payload is an OTLP profiles message. The wire has two forms of it — pprofile.Profiles, which the
// ProfilesService carries, and pprofile.ProfilesData, which persists — and they share these accessors without
// sharing a type, so expansion is written once against what they have in common.
type Payload interface {
	Dictionary() pprofile.ProfilesDictionary
	ResourceProfiles() pprofile.ResourceProfilesSlice
}

// resourceInfo is the identity every row of one resource shares.
type resourceInfo struct {
	service   string
	namespace string
	env       string
	host      string
	attrs     map[string]string
}

// FromOTLP expands an OTLP profiles payload into rows.
//
// Resource attributes decide the identity of every row (service, environment, host) exactly as they do for
// spans, so a profile lands beside the APM service it belongs to rather than in a namespace of its own.
func FromOTLP(pd Payload, tenantID string, received time.Time) ([]Row, error) {
	dict := pd.Dictionary()
	strs := dict.StringTable()
	var out []Row
	for i := 0; i < pd.ResourceProfiles().Len(); i++ {
		rp := pd.ResourceProfiles().At(i)
		res := rp.Resource().Attributes()
		ri := resourceInfo{
			service:   stringAttr(res, "service.name"),
			namespace: stringAttr(res, "service.namespace"),
			env:       stringAttr(res, "deployment.environment.name"),
			host:      stringAttr(res, "host.id"),
			attrs:     attrMap(res),
		}
		for j := 0; j < rp.ScopeProfiles().Len(); j++ {
			sp := rp.ScopeProfiles().At(j)
			for k := 0; k < sp.Profiles().Len(); k++ {
				p := sp.Profiles().At(k)
				rows, err := profileRows(p, dict, strs, tenantID, ri, received)
				if err != nil {
					return nil, err
				}
				if len(out)+len(rows) > MaxRowsPerRequest {
					return nil, fmt.Errorf("%w: more than %d", ErrTooManyRows, MaxRowsPerRequest)
				}
				out = append(out, rows...)
			}
		}
	}
	return out, nil
}

func profileRows(p pprofile.Profile, dict pprofile.ProfilesDictionary, strs pcommon.StringSlice,
	tenantID string, ri resourceInfo, received time.Time,
) ([]Row, error) {
	typ := lookupString(strs, p.SampleType().TypeStrindex())
	unit := lookupString(strs, p.SampleType().UnitStrindex())
	// A profile without a sample type says nothing about what its numbers mean, and a chart of unnamed
	// numbers is worse than no chart: the whole profile is refused rather than stored as "".
	if typ == "" {
		return nil, errors.New("profiles: a profile carries no sample type")
	}
	base := Row{TenantID: tenantID, ServiceName: ri.service, ServiceNamespace: ri.namespace,
		Environment: ri.env, HostID: ri.host, ProfileType: typ, Unit: unit,
		DurationNs: p.DurationNano(), ResourceAttributes: ri.attrs}
	when := received
	if t := p.Time().AsTime(); !t.IsZero() {
		when = t
	}
	rows := make([]Row, 0, p.Samples().Len())
	for i := 0; i < p.Samples().Len(); i++ {
		s := p.Samples().At(i)
		stack := resolveStack(dict, strs, s.StackIndex())
		if len(stack) == 0 {
			// A sample with no resolvable frame cannot be attributed to anything; storing it would add a
			// nameless block to every flame graph.
			continue
		}
		attrs := sampleAttrs(dict, strs, s.AttributeIndices())
		for v := 0; v < s.Values().Len(); v++ {
			r := base
			r.Timestamp = when
			r.Stack = stack
			r.Value = s.Values().At(v)
			r.Attributes = attrs
			rows = append(rows, r)
		}
	}
	return rows, nil
}

// sampleAttrs resolves a sample's attribute indices against the payload's attribute table. An index that
// points outside the table is skipped rather than failing the profile: one unreadable label is not a reason
// to lose a minute of samples.
func sampleAttrs(dict pprofile.ProfilesDictionary, strs pcommon.StringSlice, idx pcommon.Int32Slice) map[string]string {
	if idx.Len() == 0 {
		return nil
	}
	table := dict.AttributeTable()
	var out map[string]string
	for i := 0; i < idx.Len(); i++ {
		j := idx.At(i)
		if j < 0 || int(j) >= table.Len() {
			continue
		}
		kv := table.At(int(j))
		key := lookupString(strs, kv.KeyStrindex())
		if key == "" {
			continue
		}
		if out == nil {
			out = make(map[string]string, idx.Len())
		}
		out[truncate(key, MaxAttrBytes)] = truncate(kv.Value().AsString(), MaxAttrBytes)
	}
	return out
}

// attrMap copies a resource's attributes, which are stored beside every row of that resource.
func attrMap(m pcommon.Map) map[string]string {
	if m.Len() == 0 {
		return nil
	}
	out := make(map[string]string, m.Len())
	for k, v := range m.All() {
		out[truncate(k, MaxAttrBytes)] = truncate(v.AsString(), MaxAttrBytes)
	}
	return out
}

// resolveStack walks stack → locations → lines → function → string and returns the frame names root first.
func resolveStack(dict pprofile.ProfilesDictionary, strs pcommon.StringSlice, idx int32) []string {
	stacks := dict.StackTable()
	if idx < 0 || int(idx) >= stacks.Len() {
		return nil
	}
	locs := dict.LocationTable()
	funcs := dict.FunctionTable()
	indices := stacks.At(int(idx)).LocationIndices()
	frames := make([]string, 0, indices.Len())
	for i := 0; i < indices.Len(); i++ {
		li := indices.At(i)
		if li < 0 || int(li) >= locs.Len() {
			continue
		}
		loc := locs.At(int(li))
		// A location can carry several lines when the compiler inlined callees into it. Each is a frame:
		// dropping them would hide exactly the functions an optimiser made invisible.
		for l := 0; l < loc.Lines().Len(); l++ {
			fi := loc.Lines().At(l).FunctionIndex()
			if fi < 0 || int(fi) >= funcs.Len() {
				continue
			}
			if name := lookupString(strs, funcs.At(int(fi)).NameStrindex()); name != "" {
				frames = append(frames, truncate(name, MaxFrameBytes))
			}
		}
	}
	// OTLP orders a stack leaf first (like pprof); a flame graph reads root first.
	reverse(frames)
	if len(frames) > MaxFrames {
		// Keep the innermost frames: that is where the time is, and the outermost are the ones a flame
		// graph merges into one wide block anyway.
		frames = frames[len(frames)-MaxFrames:]
	}
	return frames
}

func reverse(s []string) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func lookupString(strs pcommon.StringSlice, idx int32) string {
	if idx < 0 || int(idx) >= strs.Len() {
		return ""
	}
	return strings.TrimSpace(strs.At(int(idx)))
}

func stringAttr(m pcommon.Map, key string) string {
	if v, ok := m.Get(key); ok {
		return strings.TrimSpace(v.AsString())
	}
	return ""
}

// Node is one block of a flame graph: a frame, the total value below it, and the frames it called.
type Node struct {
	Name     string  `json:"name"`
	Value    int64   `json:"value"`
	Children []*Node `json:"children,omitempty"`
}

// Flame folds rows into the tree a flame graph draws. Rows of different profile types must not be mixed by
// the caller — nanoseconds and bytes do not add up — so this sums whatever it is given.
func Flame(rows []Row) *Node {
	root := &Node{Name: "all"}
	for _, r := range rows {
		root.Value += r.Value
		node := root
		for _, frame := range r.Stack {
			node = child(node, frame)
			node.Value += r.Value
		}
	}
	return root
}

func child(parent *Node, name string) *Node {
	for _, c := range parent.Children {
		if c.Name == name {
			return c
		}
	}
	c := &Node{Name: name}
	parent.Children = append(parent.Children, c)
	return c
}
