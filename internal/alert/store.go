package alert

import (
	"context"
	"time"
)

// Lease is an owned rule lease.
type Lease struct {
	RuleID      string
	OrgID       string
	NextEvalAt  time.Time
	LastEvalEnd time.Time
	LeaseUntil  time.Time
}

// LeaseStore persists rule ownership (alert_rule_leases, alert_evaluators). Implementations use the database
// clock and FOR UPDATE SKIP LOCKED (docs/contracts/alerting.md §4).
type LeaseStore interface {
	// Heartbeat upserts the evaluator and returns the number of live evaluators (seen within ttl).
	Heartbeat(ctx context.Context, instance, version string, ttl time.Duration) (live int, err error)
	// Leave releases every lease of instance and removes its heartbeat.
	Leave(ctx context.Context, instance string) error
	// Renew extends the leases of instance on enabled rules, releases its leases on disabled rules and returns
	// the leases it still holds.
	Renew(ctx context.Context, instance string, ttl time.Duration) ([]Lease, error)
	// CountEnabled returns the number of enabled rules (creating missing lease rows first).
	CountEnabled(ctx context.Context) (int, error)
	// Claim takes up to n expired leases of enabled rules, oldest next_eval_at first.
	Claim(ctx context.Context, instance string, n int, ttl time.Duration) ([]Lease, error)
	// Release gives up n leases of instance (latest next_eval_at first) and returns their rule ids.
	Release(ctx context.Context, instance string, n int) ([]string, error)
}

// EvalStore loads rules and commits evaluations.
type EvalStore interface {
	// LoadRule returns the rule (with the organization's tenant id and name) and its channels.
	LoadRule(ctx context.Context, ruleID string) (*Rule, []ChannelRef, error)
	// LoadSeries returns the stored series states of a rule with their incidents.
	LoadSeries(ctx context.Context, ruleID string) (map[string]SeriesState, error)
	// Commit applies a plan if instance still holds the lease, the plan's end is newer than the stored
	// last_eval_end and the rule still has the evaluated version. Errors: ErrLeaseLost, ErrStale, ErrRuleChanged.
	Commit(ctx context.Context, instance string, p *Plan) error
}

// Attempt is one delivery attempt (alert_delivery_attempts).
type Attempt struct {
	Attempt    int
	Instance   string
	StartedAt  time.Time
	Duration   time.Duration
	Success    bool
	StatusCode int
	Error      string
}

// Delivery is a claimed outbox row with what the dispatcher needs to deliver it.
type Delivery struct {
	Notification
	Channel      *Channel  // nil when the channel was deleted
	Incident     *Incident // nil for test notifications
	OpenedStatus string    // status of the opened row of the same incident and channel (resolved/renotify rows)
	MutedLogged  bool
}

// DeliveryOutcome finishes a claimed row.
type DeliveryOutcome struct {
	Status        string // delivered, pending (retry or postponed), failed, suppressed
	NextAttemptAt time.Time
	Error         string
	Attempt       *Attempt       // nil when no delivery was attempted
	Event         *IncidentEvent // optional timeline event
	MutedLogged   bool
}

// OutboxStore is the notification outbox used by dispatchers.
type OutboxStore interface {
	// RequeueExpired returns sending rows whose claim expired to pending (a dispatcher died mid-delivery).
	RequeueExpired(ctx context.Context) (int, error)
	// ClaimDeliveries claims up to n due rows (FOR UPDATE SKIP LOCKED), skipping rows that have an older
	// pending/sending row of the same incident and channel.
	ClaimDeliveries(ctx context.Context, instance string, n int, claimTTL time.Duration) ([]*Delivery, error)
	// FinishDelivery records the outcome if instance still holds the claim.
	FinishDelivery(ctx context.Context, instance string, d *Delivery, out DeliveryOutcome) error
	// ActiveMutes returns the mutes of an organization active at at.
	ActiveMutes(ctx context.Context, orgID string, at time.Time) ([]Mute, error)
	// PendingCount returns the number of pending and sending rows.
	PendingCount(ctx context.Context) (int, error)
	// Prune deletes finished notifications (and attempts) older than before.
	Prune(ctx context.Context, before time.Time) (int, error)
}
