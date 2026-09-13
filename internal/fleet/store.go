package fleet

import (
	"context"
	"errors"
	"time"

	"github.com/onuragtas/openlog/internal/intsettings"
)

// Store errors.
var (
	ErrNotFound = errors.New("fleet: not found")
	// ErrConflict means a conditional write lost against a concurrent change (rollout state
	// changed, another open rollout was created).
	ErrConflict = errors.New("fleet: conflicting change")
)

// OrgState is what the sync handler needs per organization, cached per ingest pod.
type OrgState struct {
	OrgID     string
	Policy    Policy
	Overrides map[string]Override // by host id
	Rollout   *Rollout            // current rollout: newest not superseded, or nil
	// Integration settings of the organization (all hosts); sync sends each host its effective part.
	Integrations []intsettings.Setting
	// IntegrationsLoaded is false when the settings are unknown (table not migrated yet): sync then omits
	// integrations_config so agents keep their last applied config.
	IntegrationsLoaded bool
}

// StoredPolicy is a policy with its metadata.
type StoredPolicy struct {
	Policy
	IsDefault      bool
	UpdatedAt      *time.Time
	UpdatedByEmail string
}

// HostRecord is one sync report queued for PostgreSQL.
type HostRecord struct {
	TenantID  string
	Report    HostReport
	SyncAt    time.Time
	RolloutID string // rollout that offered an update in this sync ("" = keep the stored one)
}

// Host is a stored agent host.
type Host struct {
	HostReport
	OrgID       string
	FirstSeenAt time.Time
	LastSyncAt  time.Time
	RolloutID   string
}

// HostFilter selects hosts for the management API.
type HostFilter struct {
	Version string
	State   string
	Query   string
	Limit   int
	Cursor  string
}

// HostGroup is an aggregate row for the fleet summary.
type HostGroup struct {
	Version       string
	UpdateCapable bool
	InstallMethod string
	UpdateState   string
	Hosts         int
	RecentlySeen  int // synced within the stale window
}

// AuditEntry is written to audit_log.
type AuditEntry struct {
	OrgID       string
	ActorUserID string
	ActorEmail  string
	Action      string
	TargetType  string
	TargetID    string
	Details     map[string]any
	IP          string
	At          time.Time
}

// Store persists fleet state (PostgreSQL: PGStore).
type Store interface {
	// LoadOrgState loads the policy, overrides and current rollout of the organization of a
	// tenant. ErrNotFound when the tenant has no organization.
	LoadOrgState(ctx context.Context, tenantID string) (OrgState, error)
	// UpsertHosts writes sync reports. Records must be unique per (tenant, host).
	UpsertHosts(ctx context.Context, recs []HostRecord) error

	GetPolicy(ctx context.Context, orgID string) (StoredPolicy, error)
	PutPolicy(ctx context.Context, orgID string, p Policy, userID string, at time.Time) error
	ListOverrides(ctx context.Context, orgID string) (map[string]Override, error)
	PutOverride(ctx context.Context, orgID string, o Override, userID string) error
	DeleteOverride(ctx context.Context, orgID, hostID string) (bool, error)

	GetHost(ctx context.Context, orgID, hostID string) (Host, error)
	ListHosts(ctx context.Context, orgID string, f HostFilter) (hosts []Host, nextCursor string, err error)
	// AllHosts returns the hosts that synced at or after since.
	AllHosts(ctx context.Context, orgID string, since time.Time) ([]Host, error)
	HostGroups(ctx context.Context, orgID string, since time.Time) ([]HostGroup, error)
	// FleetOrgs lists organizations that have agent hosts or open rollouts.
	FleetOrgs(ctx context.Context) ([]string, error)

	ListRollouts(ctx context.Context, orgID string, limit int) ([]Rollout, error)
	GetRollout(ctx context.Context, orgID, id string) (Rollout, error)
	CurrentRollout(ctx context.Context, orgID string) (*Rollout, error)
	// CreateRollout supersedes the organization's open rollouts and inserts r (ID and CreatedAt
	// are filled in). ErrConflict if a concurrent open rollout was created.
	CreateRollout(ctx context.Context, r *Rollout, supersedeReason string) error
	// UpdateRollout writes r's mutable fields if the stored state is still expectState.
	UpdateRollout(ctx context.Context, r *Rollout, expectState string) error

	AddAudit(ctx context.Context, e AuditEntry) error
}
