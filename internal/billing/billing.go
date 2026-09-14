// Package billing is the provider-agnostic billing integration layer (docs/contracts/usage.md "Billing",
// docs/operations/saas.md "Adding a billing provider"). openlog meters usage and enforces plans itself; a provider
// only owns customers, subscriptions, invoices and payment. The provider is not chosen yet: this package defines the
// interface, a noop implementation, the daily usage push job (idempotency keys) and webhook event handling.
package billing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ErrNotSupported is returned by providers for operations they do not implement.
var ErrNotSupported = errors.New("not supported by the billing provider")

// ErrInvalidSignature is returned by ParseWebhook for payloads that fail verification.
var ErrInvalidSignature = errors.New("invalid webhook signature")

// Customer is an organization as a billing customer.
type Customer struct {
	OrgID    string
	TenantID string
	Name     string
	Email    string // billing contact (an owner)
}

// Subscription is a provider subscription.
type Subscription struct {
	ID         string
	CustomerID string
	Status     string // active, trialing, past_due, canceled
	// PlanRef is the provider's plan/price reference; mapped to an openlog plan by Plan.Billing["plan_ref"].
	PlanRef            string
	CurrentPeriodStart time.Time
	CurrentPeriodEnd   time.Time
}

// UsageRecord is one metered quantity pushed for one day.
type UsageRecord struct {
	CustomerID     string
	SubscriptionID string
	// Metric: ingest_gb, hosts, users (docs/contracts/usage.md "Billing metrics").
	Metric   string
	Quantity float64
	Day      time.Time
	// IdempotencyKey is stable for (org, day, metric); providers must pass it on so retries are not billed twice.
	IdempotencyKey string
}

// EventType is a normalized webhook event.
type EventType string

// Normalized events.
const (
	EventSubscriptionUpdated  EventType = "subscription.updated" // created or changed: plan follows PlanRef
	EventSubscriptionCanceled EventType = "subscription.canceled"
	EventPaymentFailed        EventType = "payment.failed"
	EventInvoicePaid          EventType = "invoice.paid"
	EventIgnored              EventType = "ignored"
)

// Event is a verified webhook event.
type Event struct {
	ID             string
	Type           EventType
	CustomerID     string
	SubscriptionID string
	PlanRef        string
}

// Provider is a billing provider (Stripe, iyzico, Paddle, ...). Implementations must be safe for concurrent use.
type Provider interface {
	// Name is the provider id stored in org_plans.billing_provider.
	Name() string
	// EnsureCustomer creates the customer when needed and returns its id.
	EnsureCustomer(ctx context.Context, c Customer) (string, error)
	// GetSubscription returns the customer's current subscription.
	GetSubscription(ctx context.Context, customerID string) (Subscription, error)
	// PushUsage reports a usage record; it must be idempotent on UsageRecord.IdempotencyKey.
	PushUsage(ctx context.Context, r UsageRecord) error
	// ParseWebhook verifies the signature of a webhook request and normalizes the event.
	ParseWebhook(payload []byte, header http.Header) (Event, error)
}

// New returns the provider named by OPENLOG_BILLING_PROVIDER: nil for "none", Noop for "noop".
func New(name string) (Provider, error) {
	switch name {
	case "", "none":
		return nil, nil
	case "noop":
		return Noop{}, nil
	}
	return nil, fmt.Errorf("unknown billing provider %q", name)
}

// Noop accepts everything and bills nothing: customer ids are derived from the organization, usage pushes succeed
// without leaving the process. Used to exercise the push job and bookkeeping before a provider is chosen.
type Noop struct{}

// Name implements Provider.
func (Noop) Name() string { return "noop" }

// EnsureCustomer implements Provider.
func (Noop) EnsureCustomer(_ context.Context, c Customer) (string, error) {
	return "noop_" + c.TenantID, nil
}

// GetSubscription implements Provider.
func (Noop) GetSubscription(_ context.Context, customerID string) (Subscription, error) {
	return Subscription{CustomerID: customerID, Status: "active"}, nil
}

// PushUsage implements Provider.
func (Noop) PushUsage(context.Context, UsageRecord) error { return nil }

// ParseWebhook implements Provider: noop has no webhooks.
func (Noop) ParseWebhook([]byte, http.Header) (Event, error) { return Event{}, ErrNotSupported }
