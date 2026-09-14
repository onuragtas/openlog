// Package deletion implements account and organization deletion for data subject requests (D-107,
// docs/operations/saas.md "Data subject requests"):
//
//   - DeleteUser removes an account at once: sessions, API keys it created, SSO/SCIM identities, invitations and
//     report recipient entries go away, audit and incident entries keep a random stable pseudonym instead of the
//     name, e-mail address and IP. Refused while the user is the only owner of an organization.
//   - ScheduleOrg soft-deletes an organization (members lose access, license and SCIM keys are revoked) for a grace
//     period in which an owner can CancelOrg. The api leader's Job then deletes the tenant's ClickHouse rows with
//     partition-scoped mutations on every table that has a tenant_id column, verifies that none are left, deletes
//     the PostgreSQL rows (and accounts left without any organization) and records a deletion certificate without
//     personal data.
package deletion

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"
)

// Errors.
var (
	ErrNotFound         = errors.New("not found")
	ErrAlreadyScheduled = errors.New("the organization is already scheduled for deletion")
	ErrNotCancellable   = errors.New("the deletion can no longer be cancelled")
)

// OrgRef names an organization.
type OrgRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SoleOwnerError: the user is the only owner of these organizations and cannot be deleted.
type SoleOwnerError struct {
	Orgs []OrgRef
}

func (e *SoleOwnerError) Error() string {
	names := make([]string, 0, len(e.Orgs))
	for _, o := range e.Orgs {
		names = append(names, o.Name)
	}
	return "you are the only owner of " + strings.Join(names, ", ") + "; add another owner or delete the organization first"
}

// Deletion statuses and initiators.
const (
	StatusScheduled = "scheduled"
	StatusCancelled = "cancelled"
	StatusDeleting  = "deleting"
	StatusCompleted = "completed"

	InitiatorOwner    = "owner"
	InitiatorOperator = "operator"
	InitiatorSelf     = "self"
)

// OrgDeletion is a row of org_deletions.
type OrgDeletion struct {
	ID               string
	OrgID            string // "" once the organization row is gone
	TenantID         string
	OrgName          string // "" once the organization row is gone
	Status           string
	Initiator        string
	Reason           string
	RequestedBy      string
	RequestedByEmail string
	RequestedAt      time.Time
	PurgeAfter       time.Time
	CancelledAt      *time.Time
	StartedAt        *time.Time
	CompletedAt      *time.Time
	Progress         Progress
	Attempts         int
	LastError        string
	CertificateID    string
}

// Recipient is an owner to notify.
type Recipient struct {
	Email  string `json:"email"`
	Locale string `json:"locale"`
}

// Notify is who gets the deletion e-mails.
type Notify struct {
	OrgName string      `json:"org_name,omitempty"`
	Owners  []Recipient `json:"owners,omitempty"`
}

// Progress is the ClickHouse state of a running deletion (org_deletions.progress).
type Progress struct {
	// CountsBefore are the tenant's rows per ClickHouse table when the deletion started.
	CountsBefore map[string]uint64 `json:"counts_before,omitempty"`
	// Submitted is when a mutation was submitted per table and partition.
	Submitted map[string]map[string]time.Time `json:"submitted,omitempty"`
	// Remaining are the rows left at the last step.
	Remaining map[string]uint64 `json:"remaining,omitempty"`
	Verified  bool              `json:"verified,omitempty"`
}

// Certificate is a deletion_certificates row.
type Certificate struct {
	ID             string            `json:"id"`
	SubjectType    string            `json:"subject_type"`
	SubjectHash    string            `json:"subject_hash"`
	Initiator      string            `json:"initiator"`
	RequestedAt    time.Time         `json:"requested_at"`
	GraceEndedAt   *time.Time        `json:"grace_ended_at"`
	StartedAt      time.Time         `json:"started_at"`
	CompletedAt    time.Time         `json:"completed_at"`
	PostgresRows   map[string]int64  `json:"postgres_rows"`
	ClickHouseRows map[string]uint64 `json:"clickhouse_rows"`
	Verified       bool              `json:"verified"`
}

// SubjectHash is the certificate subject: hex sha256 of a tenant id or user id.
func SubjectHash(id string) string {
	h := sha256.Sum256([]byte(id))
	return hex.EncodeToString(h[:])
}

// NewPseudonym returns the random replacement of a deleted user's name, e-mail address and id in retained records.
// It is the same for all of that user's records and cannot be linked back to the account.
func NewPseudonym() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return "deleted-user-" + hex.EncodeToString(b)
}

var tenantRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// ValidTenant reports whether id is a tenant id (organizations.tenant_id CHECK), safe inside SQL string literals.
func ValidTenant(id string) bool { return tenantRe.MatchString(id) }
