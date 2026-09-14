package operator

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/quota"
)

// OrgPlanFinder returns an organization's plan assignment (quota.PGStore).
type OrgPlanFinder interface {
	FindOrgPlan(ctx context.Context, ref string) (quota.OrgPlan, error)
}

// MemberLimiter enforces the plan's users limit (SaaS mode, D-105) for invitations, invitation acceptance, SSO
// just-in-time provisioning and SCIM. It is the auth.MemberLimitFunc.
type MemberLimiter struct {
	Catalog *quota.Catalog
	Plans   OrgPlanFinder
	Pool    *pgxpool.Pool
}

// ErrUserLimit is the auth error code of a rejected member (HTTP 403, code quota_exceeded).
func userLimitError(plan string, limit, used int64) error {
	return &auth.Error{Code: auth.CodeQuotaExceeded, Message: fmt.Sprintf(
		"the %s plan allows %d users and the organization already has %d (members and pending invitations); "+
			"an owner can upgrade the plan or contact openlog support", plan, limit, used)}
}

// Check returns a quota_exceeded auth error when adding one member to orgID would exceed the plan's users limit.
// includePending counts pending invitations too (creating an invitation reserves a seat; accepting uses it).
func (m MemberLimiter) Check(ctx context.Context, orgID string, includePending bool) error {
	op, err := m.Plans.FindOrgPlan(ctx, orgID)
	if errors.Is(err, quota.ErrOrgNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	plan := m.Catalog.EffectivePlan(op)
	if plan.Limits.Users <= 0 {
		return nil
	}
	var used int64
	err = m.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM memberships ms JOIN users u ON u.id = ms.user_id WHERE ms.org_id = $1::uuid AND u.disabled_at IS NULL)
		+ CASE WHEN $2 THEN (SELECT count(*) FROM invitations i WHERE i.org_id = $1::uuid AND i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > now()
			AND NOT EXISTS (SELECT 1 FROM memberships ms JOIN users u ON u.id = ms.user_id WHERE ms.org_id = i.org_id AND u.email = i.email)) ELSE 0 END`,
		orgID, includePending).Scan(&used)
	if err != nil {
		return err
	}
	if used >= plan.Limits.Users {
		return userLimitError(plan.Name, plan.Limits.Users, used)
	}
	return nil
}
