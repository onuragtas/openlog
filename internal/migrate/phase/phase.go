// Package phase implements the rolling-update rules for schema migrations
// (docs/contracts/releases-updates.md §6): every PostgreSQL and ClickHouse
// migration declares whether it is an `expand` or a `contract` change, and a
// contract change runs only once every live openlog instance is new enough.
//
// The package depends only on libs/release so both migrators and the store can use it.
package phase

import (
	"fmt"
	"strings"
	"time"

	lib "github.com/onuragtas/openlog/libs/release"
)

// Phase of a migration.
type Phase string

const (
	// Expand changes (new tables, nullable columns, indexes) are compatible with the previous
	// release and run before new code is deployed.
	Expand Phase = "expand"
	// Contract changes (drop/rename) break the previous release; they run only when every live
	// instance reports at least RequiresAllAtLeast.
	Contract Phase = "contract"
)

// LiveWindow is how recent a component heartbeat must be for the instance to count as running.
const LiveWindow = 5 * time.Minute

const (
	phaseDirective    = "openlog:phase"
	requiresDirective = "openlog:requires-all-at-least"
)

// Header is the parsed phase header of a migration file.
type Header struct {
	Phase Phase
	// RequiresAllAtLeast is the minimum product version of every live instance (contract only).
	RequiresAllAtLeast string
}

// Parse reads the header of a migration file. The first line must be
// "-- openlog:phase expand|contract"; the comment lines directly after it may contain
// "-- openlog:requires-all-at-least <version>", which contract migrations must declare.
func Parse(sql string) (Header, error) {
	sql = strings.TrimPrefix(sql, "\uFEFF")
	var h Header
	for i, raw := range strings.Split(sql, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "--") {
			break // end of the leading comment block
		}
		body := strings.TrimSpace(strings.TrimPrefix(line, "--"))
		if !strings.HasPrefix(body, "openlog:") {
			continue
		}
		fields := strings.Fields(body)
		switch fields[0] {
		case phaseDirective:
			if i != 0 {
				return Header{}, fmt.Errorf("line %d: %q must be the first line", i+1, phaseDirective)
			}
			if len(fields) != 2 {
				return Header{}, fmt.Errorf("line 1: want \"-- %s expand|contract\"", phaseDirective)
			}
			switch p := Phase(fields[1]); p {
			case Expand, Contract:
				h.Phase = p
			default:
				return Header{}, fmt.Errorf("line 1: unknown phase %q (expand or contract)", fields[1])
			}
		case requiresDirective:
			if len(fields) != 2 {
				return Header{}, fmt.Errorf("line %d: want \"-- %s <version>\"", i+1, requiresDirective)
			}
			if h.RequiresAllAtLeast != "" {
				return Header{}, fmt.Errorf("line %d: duplicate %s", i+1, requiresDirective)
			}
			if _, err := lib.ParseVersion(fields[1]); err != nil {
				return Header{}, fmt.Errorf("line %d: %w", i+1, err)
			}
			h.RequiresAllAtLeast = strings.TrimPrefix(fields[1], "v")
		default:
			return Header{}, fmt.Errorf("line %d: unknown directive %q", i+1, fields[0])
		}
	}
	switch {
	case h.Phase == "":
		return Header{}, fmt.Errorf("missing header: the first line must be \"-- %s expand\" or \"-- %s contract\"", phaseDirective, phaseDirective)
	case h.Phase == Contract && h.RequiresAllAtLeast == "":
		return Header{}, fmt.Errorf("contract migration must declare \"-- %s <version>\"", requiresDirective)
	case h.Phase == Expand && h.RequiresAllAtLeast != "":
		return Header{}, fmt.Errorf("%s is only allowed on contract migrations", requiresDirective)
	}
	return h, nil
}

// Instance is one running openlog process as recorded in component_heartbeats.
type Instance struct {
	Component  string    `json:"component"`
	InstanceID string    `json:"instance_id"`
	Version    string    `json:"version"`
	StartedAt  time.Time `json:"started_at"`
	LastSeen   time.Time `json:"last_seen"`
}

// Gate decides whether a migration may run now.
type Gate struct {
	// Self is the product version of the migrating binary.
	Self string
	// Instances are the instances seen within LiveWindow.
	Instances []Instance
	// Known is false when the heartbeats could not be read (no PostgreSQL, table missing, error).
	Known bool
	// Err explains why heartbeats are unknown.
	Err error
	// Force applies contract migrations regardless of running versions (operator override).
	Force bool
}

// Decision is the outcome for one migration.
type Decision struct {
	Apply  bool
	Reason string
}

// Decide applies the rules of contract §6. A nil gate skips every contract migration.
func (g *Gate) Decide(h Header) Decision {
	if h.Phase != Contract {
		return Decision{Apply: true, Reason: "expand"}
	}
	if g == nil {
		return Decision{Reason: "contract migrations are not enabled for this run"}
	}
	if g.Force {
		return Decision{Apply: true, Reason: "forced (-force-contract)"}
	}
	req, err := lib.ParseVersion(h.RequiresAllAtLeast)
	if err != nil {
		return Decision{Reason: "invalid requires-all-at-least: " + err.Error()}
	}
	if self, err := lib.ParseVersion(g.Self); err != nil || self.Less(req) {
		return Decision{Reason: fmt.Sprintf("this binary is %s, requires all at least %s", g.Self, h.RequiresAllAtLeast)}
	}
	if !g.Known {
		msg := "running component versions are unknown"
		if g.Err != nil {
			msg += ": " + g.Err.Error()
		}
		return Decision{Reason: msg}
	}
	for _, in := range g.Instances {
		v, err := lib.ParseVersion(in.Version)
		if err != nil {
			return Decision{Reason: fmt.Sprintf("%s %s reports an unparseable version %q", in.Component, in.InstanceID, in.Version)}
		}
		if v.Less(req) {
			return Decision{Reason: fmt.Sprintf("%s %s runs %s < %s", in.Component, in.InstanceID, in.Version, h.RequiresAllAtLeast)}
		}
	}
	return Decision{Apply: true, Reason: fmt.Sprintf("all %d live instances >= %s", len(g.Instances), h.RequiresAllAtLeast)}
}

// Step describes one migration in a plan or a run.
type Step struct {
	Database string
	Version  int
	Name     string
	Header   Header
	// Action is "applied" (already recorded), "apply" or "skip".
	Action string
	Reason string
}

// Step actions.
const (
	ActionApplied = "applied"
	ActionApply   = "apply"
	ActionSkip    = "skip"
)
