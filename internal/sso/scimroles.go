package sso

import (
	"context"
	"errors"

	"github.com/onuragtas/openlog/internal/auth"
)

// SCIMDefaultRole is the role of provisioned members without a matching group: the connection's default role,
// or viewer without a connection.
func (s *Service) SCIMDefaultRole(ctx context.Context, orgID string) (auth.Role, error) {
	c, err := s.store.GetConnection(ctx, orgID)
	if errors.Is(err, auth.ErrNotFound) {
		return auth.RoleViewer, nil
	}
	if err != nil {
		return "", s.fail(err)
	}
	return c.DefaultRole, nil
}

// SyncSCIMRoles recomputes the roles of active SCIM-provisioned members (userID "" = all of them) from their SCIM
// groups and the organization's role mappings. Nothing changes when the organization has no mappings; owners are
// never changed. It returns the number of changed memberships.
func (s *Service) SyncSCIMRoles(ctx context.Context, orgID, userID, ip string) (int, error) {
	mappings, err := s.store.ListRoleMappings(ctx, orgID)
	if err != nil {
		return 0, s.fail(err)
	}
	if len(mappings) == 0 {
		return 0, nil
	}
	def, err := s.SCIMDefaultRole(ctx, orgID)
	if err != nil {
		return 0, err
	}
	var users []SCIMUser
	if userID != "" {
		u, err := s.store.GetSCIMUser(ctx, orgID, userID)
		if errors.Is(err, auth.ErrNotFound) {
			return 0, nil
		}
		if err != nil {
			return 0, s.fail(err)
		}
		users = []SCIMUser{u}
	} else {
		for offset := 0; ; offset += 500 {
			page, total, err := s.store.ListSCIMUsers(ctx, orgID, SCIMFilter{}, offset, 500)
			if err != nil {
				return 0, s.fail(err)
			}
			users = append(users, page...)
			if len(page) == 0 || offset+len(page) >= total {
				break
			}
		}
	}
	changed := 0
	for _, u := range users {
		if !u.Active {
			continue
		}
		m, err := s.users.GetMembership(ctx, orgID, u.UserID)
		if errors.Is(err, auth.ErrNotFound) {
			continue
		}
		if err != nil {
			return changed, s.fail(err)
		}
		if m.Role == auth.RoleOwner {
			continue
		}
		groups, err := s.store.SCIMGroupNames(ctx, orgID, u.UserID)
		if err != nil {
			return changed, s.fail(err)
		}
		role := def
		if r, ok := roleFor(mappings, groups); ok {
			role = r
		}
		if role == m.Role {
			continue
		}
		if err := s.users.UpdateMemberRole(ctx, orgID, u.UserID, role); err != nil {
			return changed, s.fail(err)
		}
		changed++
		s.audit(ctx, orgID, "", "scim", ip, "member.role_change", "user", u.UserID, map[string]any{"from": m.Role, "to": role, "via": "scim_groups"})
	}
	return changed, nil
}

// RevokeOrgSessions revokes the user's single sign-on sessions bound to the organization and returns how many.
// Other sessions of the user lose the organization with the membership (it is read on every request, D-046).
func (s *Service) RevokeOrgSessions(ctx context.Context, orgID, userID string) (int, error) {
	now := s.now()
	ss, err := s.users.ListSessions(ctx, userID, now)
	if err != nil {
		return 0, s.fail(err)
	}
	n := 0
	for _, x := range ss {
		if x.OrgID != orgID {
			continue
		}
		if err := s.users.RevokeSession(ctx, userID, x.ID, now); err != nil && !errors.Is(err, auth.ErrNotFound) {
			return n, s.fail(err)
		}
		n++
	}
	return n, nil
}
