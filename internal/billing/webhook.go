package billing

import (
	"context"
	"errors"
	"fmt"

	"github.com/onuragtas/openlog/internal/quota"
)

// PlanStore changes plan assignments (quota.PGStore).
type PlanStore interface {
	FindOrgByBillingCustomer(ctx context.Context, provider, customerID string) (quota.OrgPlan, error)
	PutOrgPlan(ctx context.Context, op quota.OrgPlan, actor quota.Actor) error
}

// PlanForRef returns the catalog plan whose Billing["plan_ref"] is ref.
func PlanForRef(c *quota.Catalog, ref string) (quota.Plan, bool) {
	if ref == "" {
		return quota.Plan{}, false
	}
	for _, p := range c.Plans {
		if p.Billing["plan_ref"] == ref {
			return p, true
		}
	}
	return quota.Plan{}, false
}

// ApplyEvent applies a verified event: subscription.updated assigns the plan mapped from the event's PlanRef,
// subscription.canceled returns the organization to the default plan. Overrides are kept. Other events are
// accepted and ignored (payment dunning is the provider's job). It reports whether the assignment changed.
func ApplyEvent(ctx context.Context, store PlanStore, c *quota.Catalog, provider string, ev Event) (bool, error) {
	if ev.Type != EventSubscriptionUpdated && ev.Type != EventSubscriptionCanceled {
		return false, nil
	}
	op, err := store.FindOrgByBillingCustomer(ctx, provider, ev.CustomerID)
	if errors.Is(err, quota.ErrOrgNotFound) {
		return false, nil // not (yet) connected to an organization
	}
	if err != nil {
		return false, err
	}
	planID := c.Default
	if ev.Type == EventSubscriptionUpdated {
		p, ok := PlanForRef(c, ev.PlanRef)
		if !ok {
			return false, fmt.Errorf("billing event %s: no plan with billing.plan_ref %q", ev.ID, ev.PlanRef)
		}
		planID = p.ID
	}
	subID := ev.SubscriptionID
	if ev.Type == EventSubscriptionCanceled {
		subID = ""
	}
	if op.Assigned && op.PlanID == planID && op.BillingSubscriptionID == subID {
		return false, nil
	}
	op.PlanID, op.BillingSubscriptionID = planID, subID
	return true, store.PutOrgPlan(ctx, op, quota.Actor{Email: "billing:" + provider})
}
