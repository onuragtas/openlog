// Package dataexport builds data portability exports (KVKK/GDPR; D-107, docs/operations/saas.md "Data subject
// requests"):
//
//   - organization exports (owners): the organization's PostgreSQL data as JSON documents (members, invitations, key
//     metadata, dashboards, reports, alert rules, channels and mutes, single sign-on settings, plan, audit log; never
//     secrets or key hashes) plus the telemetry of a time range and chosen signals as gzip-compressed NDJSON chunks
//     read from ClickHouse with bounded, throttled streaming queries;
//   - personal exports (any user): profile, memberships, session metadata, API key metadata, invitations, SCIM
//     identities, comments and the audit entries by or about the user.
//
// The api leader runs one export at a time (Job), writes a single ZIP archive with a manifest.json to object storage
// (internal/objstore: S3 or a local directory), e-mails an expiring download link and deletes archives when they
// expire.
package dataexport

import (
	"encoding/json"
	"errors"
	"time"
)

// Kinds, statuses and signals.
const (
	KindOrganization = "organization"
	KindUser         = "user"

	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusExpired   = "expired"
)

// Signals are the exportable telemetry signals and their ClickHouse tables.
var Signals = map[string]string{"logs": "logs", "traces": "spans", "metrics": "metrics", "profiles": "profiles"}

// Errors.
var (
	ErrNotFound    = errors.New("export not found")
	ErrActive      = errors.New("an export is already pending or running; wait until it finishes")
	ErrRateLimited = errors.New("too many exports in the last 24 hours; try again later")
)

// Export is a data_exports row.
type Export struct {
	ID            string
	Kind          string
	OrgID         string
	OrgName       string
	TenantID      string
	UserID        string
	UserEmail     string
	Status        string
	Signals       []string
	From, To      *time.Time
	Locale        string
	Storage       string
	ObjectKey     string
	SizeBytes     int64
	TelemetryRows int64
	Truncated     bool
	Manifest      json.RawMessage
	Attempts      int
	Error         string
	CreatedAt     time.Time
	StartedAt     *time.Time
	CompletedAt   *time.Time
	ExpiresAt     *time.Time
}

// Limits bound one export.
type Limits struct {
	MaxBytes      int64
	MaxRows       int64
	MaxRange      time.Duration
	RowsPerSecond int64
	TTL           time.Duration
	// ChunkRows is the number of rows per NDJSON chunk (default 500000).
	ChunkRows int64
	// MaxPerDay bounds exports per organization or user within 24 hours (default 5).
	MaxPerDay int
}

func (l Limits) chunkRows() int64 {
	if l.ChunkRows <= 0 {
		return 500_000
	}
	return l.ChunkRows
}

func (l Limits) maxPerDay() int {
	if l.MaxPerDay <= 0 {
		return 5
	}
	return l.MaxPerDay
}
