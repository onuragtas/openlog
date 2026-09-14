// Package quota implements plans, quota evaluation and enforcement (docs/contracts/usage.md, D-080, D-081): the
// plan catalog (configuration, not code), per-organization overrides, evaluation of usage against limits, the ingest
// limiter (monthly hard limit + per-tenant token bucket) and per-tenant retention deletions.
package quota

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/onuragtas/openlog/internal/config"
)

// Signals with a per-plan retention (queue.Signal names).
const (
	SignalLogs    = "logs"
	SignalTraces  = "traces"
	SignalMetrics = "metrics"
)

// RetentionSignals lists the signals of Limits.RetentionDays.
var RetentionSignals = []string{SignalLogs, SignalTraces, SignalMetrics}

// GiB is the unit of ingest limits (ingest_gb_month is binary gigabytes).
const GiB = 1 << 30

// QueryLimits are ClickHouse limits applied to a tenant's api/alert queries (0 = not set by the plan).
type QueryLimits struct {
	MaxMemoryUsage int64 `json:"max_memory_usage,omitempty"`
	MaxRowsToRead  int64 `json:"max_rows_to_read,omitempty"`
	MaxBytesToRead int64 `json:"max_bytes_to_read,omitempty"`
}

// Limits of a plan. 0 means unlimited (retention: the table default).
type Limits struct {
	IngestGBMonth float64 `json:"ingest_gb_month,omitempty"`
	Hosts         int64   `json:"hosts,omitempty"`
	Users         int64   `json:"users,omitempty"`
	// RetentionDays per signal (logs, traces, metrics).
	RetentionDays map[string]int `json:"retention_days,omitempty"`
	Query         QueryLimits    `json:"query,omitempty"`
	// IngestBytesPerSecond and IngestBurstBytes are the tenant-wide ingest rate limit (SaaS mode).
	IngestBytesPerSecond int64 `json:"ingest_bytes_per_second,omitempty"`
	IngestBurstBytes     int64 `json:"ingest_burst_bytes,omitempty"`
}

// Enforcement controls what happens over a limit.
type Enforcement struct {
	// HardIngestLimit rejects ingest (429) once IngestGBMonth × (1 + GracePercent/100) is used (SaaS mode only).
	HardIngestLimit bool    `json:"hard_ingest_limit,omitempty"`
	GracePercent    float64 `json:"grace_percent,omitempty"`
}

// Plan is one catalog entry. Prices are not part of openlog: Billing maps the plan to provider price/product ids.
type Plan struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Limits      Limits      `json:"limits"`
	Enforcement Enforcement `json:"enforcement"`
	// Billing holds provider-specific identifiers, e.g. {"price_id": "..."}; opaque to openlog.
	Billing map[string]string `json:"billing,omitempty"`
	// TrialDays > 0 allows trials of this plan (SaaS mode, D-106); TrialFallbackPlan is the plan an organization
	// moves to when the trial ends (empty = the catalog default).
	TrialDays         int    `json:"trial_days,omitempty"`
	TrialFallbackPlan string `json:"trial_fallback_plan,omitempty"`
}

// TrialFallback returns the plan an organization on a trial of p moves to afterwards.
func (c *Catalog) TrialFallback(p Plan) string {
	if p.TrialFallbackPlan != "" {
		return p.TrialFallbackPlan
	}
	return c.Default
}

// Catalog is the parsed plan configuration.
type Catalog struct {
	Plans   []Plan
	Default string
	byID    map[string]int
}

// UnlimitedPlanID is the only plan when no catalog is configured.
const UnlimitedPlanID = "unlimited"

var planIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

type catalogJSON struct {
	Plans   []Plan `json:"plans"`
	Default string `json:"default,omitempty"`
}

// ParseCatalog parses the plan catalog JSON ({"plans": [...], "default": "free"}). An empty document yields the single
// unlimited plan. defaultPlan (OPENLOG_DEFAULT_PLAN) overrides the document's default; without either, the first
// plan is the default.
func ParseCatalog(doc, defaultPlan string) (*Catalog, error) {
	var cj catalogJSON
	if strings.TrimSpace(doc) == "" {
		cj.Plans = []Plan{{ID: UnlimitedPlanID, Name: "Unlimited"}}
	} else {
		dec := json.NewDecoder(strings.NewReader(doc))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cj); err != nil {
			return nil, fmt.Errorf("plan catalog: %w", err)
		}
	}
	if len(cj.Plans) == 0 {
		return nil, errors.New("plan catalog: no plans")
	}
	c := &Catalog{Plans: cj.Plans, byID: map[string]int{}}
	for i, p := range c.Plans {
		if !planIDRe.MatchString(p.ID) {
			return nil, fmt.Errorf("plan catalog: invalid plan id %q", p.ID)
		}
		if _, dup := c.byID[p.ID]; dup {
			return nil, fmt.Errorf("plan catalog: duplicate plan id %q", p.ID)
		}
		if p.Name == "" {
			c.Plans[i].Name = p.ID
		}
		if err := p.Limits.validate(); err != nil {
			return nil, fmt.Errorf("plan catalog: plan %s: %w", p.ID, err)
		}
		if p.Enforcement.GracePercent < 0 || p.Enforcement.GracePercent > 1000 {
			return nil, fmt.Errorf("plan catalog: plan %s: grace_percent must be between 0 and 1000", p.ID)
		}
		c.byID[p.ID] = i
	}
	c.Default = cj.Default
	if defaultPlan != "" {
		c.Default = defaultPlan
	}
	if c.Default == "" {
		c.Default = c.Plans[0].ID
	}
	if _, ok := c.byID[c.Default]; !ok {
		return nil, fmt.Errorf("plan catalog: default plan %q is not defined", c.Default)
	}
	for _, p := range c.Plans {
		if p.TrialDays < 0 || p.TrialDays > 365 {
			return nil, fmt.Errorf("plan catalog: plan %s: trial_days must be between 0 and 365", p.ID)
		}
		if fb := c.TrialFallback(p); p.TrialDays > 0 || p.TrialFallbackPlan != "" {
			if _, ok := c.byID[fb]; !ok {
				return nil, fmt.Errorf("plan catalog: plan %s: trial_fallback_plan %q is not defined", p.ID, fb)
			}
			if fb == p.ID {
				return nil, fmt.Errorf("plan catalog: plan %s: the trial fallback must be another plan (set trial_fallback_plan)", p.ID)
			}
		}
	}
	return c, nil
}

// LoadCatalog reads the catalog of cfg (OPENLOG_PLANS or OPENLOG_PLANS_FILE, OPENLOG_DEFAULT_PLAN).
func LoadCatalog(u config.Usage) (*Catalog, error) {
	doc := u.PlansJSON
	if u.PlansFile != "" {
		b, err := os.ReadFile(u.PlansFile)
		if err != nil {
			return nil, fmt.Errorf("OPENLOG_PLANS_FILE: %w", err)
		}
		doc = string(b)
	}
	return ParseCatalog(doc, u.DefaultPlan)
}

func (l Limits) validate() error {
	if l.IngestGBMonth < 0 || math.IsNaN(l.IngestGBMonth) || math.IsInf(l.IngestGBMonth, 0) {
		return errors.New("ingest_gb_month must be >= 0")
	}
	if l.Hosts < 0 || l.Users < 0 || l.IngestBytesPerSecond < 0 || l.IngestBurstBytes < 0 {
		return errors.New("hosts, users, ingest_bytes_per_second and ingest_burst_bytes must be >= 0")
	}
	if l.Query.MaxMemoryUsage < 0 || l.Query.MaxRowsToRead < 0 || l.Query.MaxBytesToRead < 0 {
		return errors.New("query limits must be >= 0")
	}
	for s, d := range l.RetentionDays {
		if !validSignal(s) {
			return fmt.Errorf("retention_days: unknown signal %q (logs, traces, metrics)", s)
		}
		if d < 0 || d > 3650 {
			return fmt.Errorf("retention_days.%s must be between 0 and 3650", s)
		}
	}
	return nil
}

func validSignal(s string) bool {
	for _, v := range RetentionSignals {
		if v == s {
			return true
		}
	}
	return false
}

// Plan returns the plan with id.
func (c *Catalog) Plan(id string) (Plan, bool) {
	i, ok := c.byID[id]
	if !ok {
		return Plan{}, false
	}
	return c.Plans[i], true
}

// Resolve returns the plan of an assignment: planID when defined, else the default plan.
func (c *Catalog) Resolve(planID string) Plan {
	if p, ok := c.Plan(planID); ok {
		return p
	}
	p, _ := c.Plan(c.Default)
	return p
}

// MaxRetentionDays returns, per signal, the longest retention of any plan (0 when no plan sets one). Table TTLs must
// be at least this long when per-tenant retention is enabled (D-081); overrides can only shorten below it.
func (c *Catalog) MaxRetentionDays() map[string]int {
	out := map[string]int{}
	for _, p := range c.Plans {
		for s, d := range p.Limits.RetentionDays {
			if d > out[s] {
				out[s] = d
			}
		}
	}
	return out
}

// Overrides replace individual limits of a plan for one organization. nil fields keep the plan's value.
type Overrides struct {
	IngestGBMonth        *float64       `json:"ingest_gb_month,omitempty"`
	Hosts                *int64         `json:"hosts,omitempty"`
	Users                *int64         `json:"users,omitempty"`
	RetentionDays        map[string]int `json:"retention_days,omitempty"`
	Query                *QueryLimits   `json:"query,omitempty"`
	IngestBytesPerSecond *int64         `json:"ingest_bytes_per_second,omitempty"`
	IngestBurstBytes     *int64         `json:"ingest_burst_bytes,omitempty"`
	HardIngestLimit      *bool          `json:"hard_ingest_limit,omitempty"`
	GracePercent         *float64       `json:"grace_percent,omitempty"`
}

// Validate checks the overrides like plan limits.
func (o Overrides) Validate() error {
	p := o.Apply(Plan{})
	if err := p.Limits.validate(); err != nil {
		return err
	}
	if p.Enforcement.GracePercent < 0 || p.Enforcement.GracePercent > 1000 {
		return errors.New("grace_percent must be between 0 and 1000")
	}
	return nil
}

// Empty reports whether no field is set.
func (o Overrides) Empty() bool {
	return o.IngestGBMonth == nil && o.Hosts == nil && o.Users == nil && len(o.RetentionDays) == 0 && o.Query == nil &&
		o.IngestBytesPerSecond == nil && o.IngestBurstBytes == nil && o.HardIngestLimit == nil && o.GracePercent == nil
}

// Apply returns p with the overrides applied (p is not modified).
func (o Overrides) Apply(p Plan) Plan {
	out := p
	out.Limits.RetentionDays = map[string]int{}
	for s, d := range p.Limits.RetentionDays {
		out.Limits.RetentionDays[s] = d
	}
	if o.IngestGBMonth != nil {
		out.Limits.IngestGBMonth = *o.IngestGBMonth
	}
	if o.Hosts != nil {
		out.Limits.Hosts = *o.Hosts
	}
	if o.Users != nil {
		out.Limits.Users = *o.Users
	}
	for s, d := range o.RetentionDays {
		out.Limits.RetentionDays[s] = d
	}
	if o.Query != nil {
		out.Limits.Query = *o.Query
	}
	if o.IngestBytesPerSecond != nil {
		out.Limits.IngestBytesPerSecond = *o.IngestBytesPerSecond
	}
	if o.IngestBurstBytes != nil {
		out.Limits.IngestBurstBytes = *o.IngestBurstBytes
	}
	if o.HardIngestLimit != nil {
		out.Enforcement.HardIngestLimit = *o.HardIngestLimit
	}
	if o.GracePercent != nil {
		out.Enforcement.GracePercent = *o.GracePercent
	}
	return out
}

// PlanIDs returns the catalog's plan ids, sorted.
func (c *Catalog) PlanIDs() []string {
	ids := make([]string, 0, len(c.Plans))
	for _, p := range c.Plans {
		ids = append(ids, p.ID)
	}
	sort.Strings(ids)
	return ids
}
