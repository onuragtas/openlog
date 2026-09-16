// Package slo implements service level objectives: their definition (PostgreSQL `slos`,
// migrations/postgres/0087_slo.sql), the error budget math on the APM rollup (docs/contracts/slo.md,
// apm.md §4) and the data the API and the slo_burn alert rule read (alerting.md §2.11).
//
// Layout: slo.go (model + validation), budget.go (SLI, error budget, burn rate, series),
// query.go (tenant-scoped reads of apm_transactions_1m), pgstore.go (PostgreSQL store).
package slo

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

// SLI types.
const (
	// SLIAvailability counts non-error entry spans as good (apm.md §3).
	SLIAvailability = "availability"
	// SLILatency counts entry spans faster than LatencyThresholdMs as good (duration histogram, apm.md §4.1).
	SLILatency = "latency"
)

// Limits of a definition.
const (
	MaxNameRunes        = 200
	MaxDescriptionRunes = 2000
	MaxServiceBytes     = 512
	MinLatencyMs        = 1
	MaxLatencyMs        = 600000
	MinObjective        = 50
	MaxPerOrg           = 200
)

// WindowDays are the rolling windows an SLO may use.
var WindowDays = []int{7, 28, 30}

var (
	// ErrNotFound is returned for an unknown SLO of the organization.
	ErrNotFound = errors.New("slo not found")
	// ErrLimit reports that the organization already has MaxPerOrg SLOs.
	ErrLimit = errors.New("slo limit reached")
)

// ValidationError is an invalid definition (400 invalid_argument with the field path).
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return e.Msg
	}
	return e.Field + ": " + e.Msg
}

func invalid(field, msg string) error { return &ValidationError{Field: field, Msg: msg} }

// Actor is the user changing a definition (audit log).
type Actor struct {
	UserID string
	Email  string
	IP     string
}

// Input is the writable part of an SLO. Namespace and environment follow apm.md §1: nil = every
// namespace/environment (aggregated), a string (also "") = exact match.
type Input struct {
	Name               string  `json:"name"`
	Description        string  `json:"description"`
	ServiceName        string  `json:"service_name"`
	ServiceNamespace   *string `json:"service_namespace"`
	Environment        *string `json:"environment"`
	SLIType            string  `json:"sli_type"`
	LatencyThresholdMs float64 `json:"latency_threshold_ms"`
	Objective          float64 `json:"objective"`
	WindowDays         int     `json:"window_days"`
}

// SLO is a stored definition.
type SLO struct {
	ID    string
	OrgID string
	Input
	CreatedBy      string // "" when the creator was deleted
	CreatedByEmail string
	UpdatedByEmail string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Objective returns the target as a fraction (99.9 → 0.999).
func (in Input) ObjectiveFraction() float64 { return in.Objective / 100 }

// Window is the rolling window of the SLO.
func (in Input) Window() time.Duration { return time.Duration(in.WindowDays) * 24 * time.Hour }

// Validate checks and normalizes an input.
func (in *Input) Validate() error {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	in.ServiceName = strings.TrimSpace(in.ServiceName)
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > MaxNameRunes || !utf8.ValidString(in.Name) {
		return invalid("name", "must be 1-200 characters")
	}
	if utf8.RuneCountInString(in.Description) > MaxDescriptionRunes {
		return invalid("description", "must be at most 2000 characters")
	}
	if in.ServiceName == "" || len(in.ServiceName) > MaxServiceBytes {
		return invalid("service_name", "required, at most 512 bytes")
	}
	for field, v := range map[string]*string{"service_namespace": in.ServiceNamespace, "environment": in.Environment} {
		if v != nil && len(*v) > MaxServiceBytes {
			return invalid(field, "at most 512 bytes")
		}
	}
	switch in.SLIType {
	case SLIAvailability:
		if in.LatencyThresholdMs != 0 {
			return invalid("latency_threshold_ms", "only applies to a latency SLI")
		}
	case SLILatency:
		t := in.LatencyThresholdMs
		if t != math.Trunc(t) || t < MinLatencyMs || t > MaxLatencyMs {
			return invalid("latency_threshold_ms", "must be a whole number of milliseconds between 1 and 600000")
		}
	default:
		return invalid("sli_type", "must be availability or latency")
	}
	if !finite(in.Objective) || in.Objective < MinObjective || in.Objective >= 100 {
		return invalid("objective", "must be at least 50 and below 100 (percent)")
	}
	if !contains(WindowDays, in.WindowDays) {
		return invalid("window_days", "must be 7, 28 or 30")
	}
	return nil
}

func contains(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// Store persists SLO definitions per organization (PostgreSQL: PGStore).
type Store interface {
	// List returns the SLOs of the organization ordered by name.
	List(ctx context.Context, orgID string) ([]SLO, error)
	// Get returns one SLO (ErrNotFound when it belongs to another organization).
	Get(ctx context.Context, orgID, id string) (*SLO, error)
	// Create stores a new SLO (ErrLimit at MaxPerOrg) and writes the audit event slo.create.
	Create(ctx context.Context, orgID string, in Input, actor Actor) (*SLO, error)
	// Update replaces the writable fields and writes the audit event slo.update.
	Update(ctx context.Context, orgID, id string, in Input, actor Actor) (*SLO, error)
	// Delete removes an SLO and writes the audit event slo.delete.
	Delete(ctx context.Context, orgID, id string, actor Actor) error
}
