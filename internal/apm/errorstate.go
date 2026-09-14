package apm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Error inbox workflow (apm.md §3.4): status, assignee, regressions and comments of error groups, kept in PostgreSQL
// (migrations/postgres/0025_apm_error_workflow.sql).

// ErrorStatus is the workflow status of an error group.
type ErrorStatus string

const (
	StatusUnresolved ErrorStatus = "unresolved"
	StatusResolved   ErrorStatus = "resolved"
	StatusIgnored    ErrorStatus = "ignored"
)

// Valid reports a known status.
func (s ErrorStatus) Valid() bool {
	return s == StatusUnresolved || s == StatusResolved || s == StatusIgnored
}

// Limits of the workflow API.
const (
	MaxResolvedVersionBytes = 256
	MaxCommentBytes         = 4000
	MaxBulkGroups           = 500
)

// ErrorGroupRef identifies a group and its service.
type ErrorGroupRef struct {
	GroupID uint64
	Key     ServiceKey
}

// ErrorGroupState is the workflow state of one group. The zero value (Status "") means "no row": unresolved.
type ErrorGroupState struct {
	ErrorGroupRef
	Status            ErrorStatus
	AssigneeUserID    string
	AssigneeEmail     string
	AssigneeName      string
	ResolvedAt        *time.Time
	ResolvedInVersion string
	ResolvedByEmail   string
	RegressedAt       *time.Time
	RegressionCount   int
	CommentCount      int
	UpdatedAt         time.Time
	UpdatedByEmail    string
}

// EffectiveStatus is Status, or unresolved without a row.
func (s ErrorGroupState) EffectiveStatus() ErrorStatus {
	if s.Status == "" {
		return StatusUnresolved
	}
	return s.Status
}

// ErrorGroupPatch changes some fields of groups. nil fields are unchanged.
type ErrorGroupPatch struct {
	Status *ErrorStatus
	// AssigneeUserID "" unassigns.
	AssigneeUserID *string
	// ResolvedInVersion is only allowed when the resulting status is resolved.
	ResolvedInVersion *string
}

// ErrInvalidErrorPatch is returned for invalid patches and comments.
var ErrInvalidErrorPatch = errors.New("invalid error group change")

// ErrNotMember is returned when the assignee is not a member of the organization.
var ErrNotMember = errors.New("assignee is not a member of the organization")

// ErrCommentNotFound is returned for an unknown comment (or one the actor may not delete).
var ErrCommentNotFound = errors.New("comment not found")

// Validate checks a patch on its own.
func (p ErrorGroupPatch) Validate() error {
	if p.Status == nil && p.AssigneeUserID == nil && p.ResolvedInVersion == nil {
		return fmt.Errorf("%w: nothing to change (status, assignee_user_id or resolved_in_version)", ErrInvalidErrorPatch)
	}
	if p.Status != nil && !p.Status.Valid() {
		return fmt.Errorf("%w: status must be unresolved, resolved or ignored", ErrInvalidErrorPatch)
	}
	if p.ResolvedInVersion != nil {
		if len(*p.ResolvedInVersion) > MaxResolvedVersionBytes {
			return fmt.Errorf("%w: resolved_in_version is at most %d bytes", ErrInvalidErrorPatch, MaxResolvedVersionBytes)
		}
		if *p.ResolvedInVersion != "" && (p.Status == nil || *p.Status != StatusResolved) {
			return fmt.Errorf("%w: resolved_in_version needs status resolved", ErrInvalidErrorPatch)
		}
	}
	return nil
}

// ApplyPatch returns the state after p at now (p must be valid). Resolving sets resolved_at (and the version);
// unresolving or ignoring clears them. Re-resolving an already resolved group keeps resolved_at unless the
// version changes.
func ApplyPatch(cur ErrorGroupState, p ErrorGroupPatch, now time.Time) ErrorGroupState {
	next := cur
	if next.Status == "" {
		next.Status = StatusUnresolved
	}
	if p.Status != nil {
		switch *p.Status {
		case StatusResolved:
			version := ""
			if p.ResolvedInVersion != nil {
				version = strings.TrimSpace(*p.ResolvedInVersion)
			}
			if cur.EffectiveStatus() != StatusResolved || next.ResolvedAt == nil || version != cur.ResolvedInVersion {
				t := now
				next.ResolvedAt = &t
			}
			next.ResolvedInVersion = version
		case StatusUnresolved, StatusIgnored:
			next.ResolvedAt, next.ResolvedInVersion = nil, ""
		}
		next.Status = *p.Status
	}
	if p.AssigneeUserID != nil {
		next.AssigneeUserID = *p.AssigneeUserID
		if next.AssigneeUserID == "" {
			next.AssigneeEmail, next.AssigneeName = "", ""
		}
	}
	return next
}

// VersionOccurrence is the newest occurrence of a group in one service.version.
type VersionOccurrence struct {
	Version  string
	LastSeen time.Time
}

// Regression is a detected regression of a resolved group.
type Regression struct {
	// At is the newest qualifying occurrence.
	At      time.Time
	Version string
}

// DetectRegression reports whether a resolved group occurred again (apm.md §3.4):
//
//   - without resolved_in_version: any occurrence after resolved_at (lastSeen of the group, or of any version);
//   - with resolved_in_version V: an occurrence after resolved_at in V, or in a version first seen at or after
//     V's first appearance (V deployed: newer versions), or — while V has not reported yet — in a version first
//     seen after resolved_at. Occurrences of versions that were already running are the old code draining.
//
// firstSeen maps versions of the group's service to their first span within retention.
func DetectRegression(st ErrorGroupState, groupLastSeen time.Time, occ []VersionOccurrence, firstSeen map[string]time.Time) (Regression, bool) {
	if st.EffectiveStatus() != StatusResolved || st.ResolvedAt == nil {
		return Regression{}, false
	}
	resolved := *st.ResolvedAt
	if st.ResolvedInVersion == "" {
		var best Regression
		found := false
		for _, o := range occ {
			if o.LastSeen.After(resolved) && (!found || o.LastSeen.After(best.At)) {
				best, found = Regression{At: o.LastSeen, Version: o.Version}, true
			}
		}
		if !found && groupLastSeen.After(resolved) {
			best, found = Regression{At: groupLastSeen}, true
		}
		return best, found
	}
	fixed, fixedSeen := firstSeen[st.ResolvedInVersion]
	var best Regression
	found := false
	for _, o := range occ {
		if !o.LastSeen.After(resolved) {
			continue
		}
		vFirst, known := firstSeen[o.Version]
		qualifies := o.Version == st.ResolvedInVersion
		if !qualifies && known {
			if fixedSeen {
				qualifies = !vFirst.Before(fixed)
			} else {
				qualifies = vFirst.After(resolved)
			}
		}
		if qualifies && (!found || o.LastSeen.After(best.At)) {
			best, found = Regression{At: o.LastSeen, Version: o.Version}, true
		}
	}
	return best, found
}

// ErrorStateFilter selects states. Empty fields do not filter.
type ErrorStateFilter struct {
	GroupIDs []uint64
	Statuses []ErrorStatus
	// Assignee: nil any, "" unassigned, else a user id.
	Assignee *string
	// Service restricts to a service name; Namespace/Environment (nil = all) further.
	Service     string
	Namespace   *string
	Environment *string
	// RegressedSince selects groups with regressed_at >= RegressedSince.
	RegressedSince *time.Time
	Limit          int
}

// ErrorComment is a note on a group.
type ErrorComment struct {
	ID           string
	GroupID      uint64
	AuthorUserID string
	AuthorEmail  string
	AuthorName   string
	Body         string
	CreatedAt    time.Time
}

// ErrorActivity is an audit event of a group.
type ErrorActivity struct {
	Action     string
	ActorEmail string
	Details    map[string]any
	CreatedAt  time.Time
}

// ErrorStateStore persists the workflow per organization.
type ErrorStateStore interface {
	States(ctx context.Context, orgID string, f ErrorStateFilter) ([]ErrorGroupState, error)
	// Update applies p to every ref in one transaction (upsert + audit event apm.error_group.update per changed group).
	Update(ctx context.Context, orgID string, refs []ErrorGroupRef, p ErrorGroupPatch, actor Actor) ([]ErrorGroupState, error)
	// MarkRegressed reopens a group that is still resolved with resolved_at before r.At; false when it no longer is.
	MarkRegressed(ctx context.Context, orgID string, ref ErrorGroupRef, r Regression) (bool, error)
	Comments(ctx context.Context, orgID string, groupID uint64) ([]ErrorComment, error)
	AddComment(ctx context.Context, orgID string, ref ErrorGroupRef, body string, actor Actor) (ErrorComment, error)
	// DeleteComment deletes a comment of its author, or any comment when manageAny.
	DeleteComment(ctx context.Context, orgID string, groupID uint64, commentID string, actor Actor, manageAny bool) error
	Activity(ctx context.Context, orgID string, groupID uint64, limit int) ([]ErrorActivity, error)
}

// RegressionActor is the audit actor of automatic reopenings.
const RegressionActor = "openlog-apm"
