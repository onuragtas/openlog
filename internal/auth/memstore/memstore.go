// Package memstore is an in-memory auth.Store for tests. It mirrors the
// semantics of the PostgreSQL store (internal/store/postgres), which is
// covered by the integration test suite.
package memstore

import (
	"bytes"
	"context"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/tenant"
)

type membership struct {
	role    auth.Role
	created time.Time
}

// Store is a concurrency-safe in-memory auth.Store.
type Store struct {
	mu          sync.Mutex
	orgs        map[string]auth.Organization
	users       map[string]auth.User
	members     map[[2]string]membership // {org, user}
	sessions    map[string]auth.Session
	licenseKeys map[string]auth.LicenseKey
	apiKeys     map[string]auth.APIKey
	invitations map[string]auth.Invitation
	audit       []auth.AuditEvent
	failures    map[string][]time.Time

	// Err, when set, is returned by every method (simulates an outage).
	Err error
	// Lookups counts LookupLicenseKey calls.
	Lookups int
}

var _ auth.Store = (*Store)(nil)

// New returns an empty store.
func New() *Store {
	return &Store{
		orgs: map[string]auth.Organization{}, users: map[string]auth.User{}, members: map[[2]string]membership{},
		sessions: map[string]auth.Session{}, licenseKeys: map[string]auth.LicenseKey{}, apiKeys: map[string]auth.APIKey{},
		invitations: map[string]auth.Invitation{}, failures: map[string][]time.Time{},
	}
}

// SetErr sets or clears the simulated outage error.
func (s *Store) SetErr(err error) {
	s.mu.Lock()
	s.Err = err
	s.mu.Unlock()
}

// AuditEvents returns a copy of all audit events (oldest first).
func (s *Store) AuditEvents() []auth.AuditEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]auth.AuditEvent(nil), s.audit...)
}

func now(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}

func tp(t time.Time) *time.Time { return &t }

func (s *Store) lock() error {
	s.mu.Lock()
	return s.Err
}

func (s *Store) createUserLocked(u *auth.User) error {
	for _, x := range s.users {
		if x.Email == u.Email {
			return auth.ErrAlreadyExists
		}
	}
	u.ID = uuid.NewString()
	u.CreatedAt = now(u.CreatedAt)
	s.users[u.ID] = *u
	return nil
}

func (s *Store) CreateOrganization(_ context.Context, org *auth.Organization, owner *auth.User) error {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return err
	}
	for _, o := range s.orgs {
		if o.TenantID == org.TenantID {
			return auth.ErrAlreadyExists
		}
	}
	if owner.ID == "" {
		if err := s.createUserLocked(owner); err != nil {
			return err
		}
	} else if _, ok := s.users[owner.ID]; !ok {
		return auth.ErrNotFound
	}
	org.ID = uuid.NewString()
	org.CreatedAt = now(org.CreatedAt)
	s.orgs[org.ID] = *org
	s.members[[2]string{org.ID, owner.ID}] = membership{auth.RoleOwner, org.CreatedAt}
	return nil
}

func (s *Store) GetOrganization(_ context.Context, id string) (auth.Organization, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return auth.Organization{}, err
	}
	o, ok := s.orgs[id]
	if !ok {
		return o, auth.ErrNotFound
	}
	return o, nil
}

func (s *Store) GetOrganizationByTenant(_ context.Context, tenantID string) (auth.Organization, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return auth.Organization{}, err
	}
	for _, o := range s.orgs {
		if o.TenantID == tenantID {
			return o, nil
		}
	}
	return auth.Organization{}, auth.ErrNotFound
}

func (s *Store) UpdateOrganizationName(_ context.Context, id, name string) error {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return err
	}
	o, ok := s.orgs[id]
	if !ok {
		return auth.ErrNotFound
	}
	o.Name = name
	s.orgs[id] = o
	return nil
}

func (s *Store) CreateUser(_ context.Context, u *auth.User) error {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return err
	}
	return s.createUserLocked(u)
}

func (s *Store) GetUser(_ context.Context, id string) (auth.User, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return auth.User{}, err
	}
	u, ok := s.users[id]
	if !ok {
		return u, auth.ErrNotFound
	}
	return u, nil
}

func (s *Store) GetUserByEmail(_ context.Context, email string) (auth.User, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return auth.User{}, err
	}
	for _, u := range s.users {
		if u.Email == email {
			return u, nil
		}
	}
	return auth.User{}, auth.ErrNotFound
}

func (s *Store) SetUserPassword(_ context.Context, userID, hash string) error {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return err
	}
	u, ok := s.users[userID]
	if !ok {
		return auth.ErrNotFound
	}
	u.PasswordHash = hash
	s.users[userID] = u
	return nil
}

// DisableUser marks a user disabled (tests).
func (s *Store) DisableUser(userID string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.users[userID]
	u.DisabledAt = tp(at)
	s.users[userID] = u
}

func (s *Store) SetUserLastLogin(_ context.Context, userID string, at time.Time) error {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return err
	}
	u, ok := s.users[userID]
	if !ok {
		return auth.ErrNotFound
	}
	u.LastLoginAt = tp(at)
	s.users[userID] = u
	return nil
}

func (s *Store) AddMember(_ context.Context, orgID, userID string, role auth.Role) error {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return err
	}
	if _, ok := s.orgs[orgID]; !ok {
		return auth.ErrNotFound
	}
	if _, ok := s.users[userID]; !ok {
		return auth.ErrNotFound
	}
	k := [2]string{orgID, userID}
	if _, ok := s.members[k]; ok {
		return auth.ErrAlreadyExists
	}
	s.members[k] = membership{role, time.Now()}
	return nil
}

func (s *Store) GetMembership(_ context.Context, orgID, userID string) (auth.Membership, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return auth.Membership{}, err
	}
	m, ok := s.members[[2]string{orgID, userID}]
	if !ok {
		return auth.Membership{}, auth.ErrNotFound
	}
	return auth.Membership{Org: s.orgs[orgID], Role: m.role, CreatedAt: m.created}, nil
}

func (s *Store) ListMemberships(_ context.Context, userID string) ([]auth.Membership, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return nil, err
	}
	out := []auth.Membership{}
	for k, m := range s.members {
		if k[1] == userID {
			out = append(out, auth.Membership{Org: s.orgs[k[0]], Role: m.role, CreatedAt: m.created})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].Org.Name < out[j].Org.Name
	})
	return out, nil
}

func (s *Store) ListMembers(_ context.Context, orgID string) ([]auth.Member, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return nil, err
	}
	out := []auth.Member{}
	for k, m := range s.members {
		if k[0] == orgID {
			u := s.users[k[1]]
			out = append(out, auth.Member{UserID: u.ID, Email: u.Email, Name: u.Name, Role: m.role, JoinedAt: m.created})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Email < out[j].Email })
	return out, nil
}

func (s *Store) ownersLocked(orgID string) int {
	n := 0
	for k, m := range s.members {
		if k[0] == orgID && m.role == auth.RoleOwner {
			n++
		}
	}
	return n
}

func (s *Store) UpdateMemberRole(_ context.Context, orgID, userID string, role auth.Role) error {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return err
	}
	k := [2]string{orgID, userID}
	m, ok := s.members[k]
	if !ok {
		return auth.ErrNotFound
	}
	if m.role == auth.RoleOwner && role != auth.RoleOwner && s.ownersLocked(orgID) <= 1 {
		return auth.ErrLastOwner
	}
	m.role = role
	s.members[k] = m
	return nil
}

func (s *Store) RemoveMember(_ context.Context, orgID, userID string) error {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return err
	}
	k := [2]string{orgID, userID}
	m, ok := s.members[k]
	if !ok {
		return auth.ErrNotFound
	}
	if m.role == auth.RoleOwner && s.ownersLocked(orgID) <= 1 {
		return auth.ErrLastOwner
	}
	delete(s.members, k)
	return nil
}

func (s *Store) CreateSession(_ context.Context, sess *auth.Session) error {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return err
	}
	sess.ID = uuid.NewString()
	sess.CreatedAt = now(sess.CreatedAt)
	s.sessions[sess.ID] = *sess
	return nil
}

func (s *Store) GetSessionByTokenHash(_ context.Context, hash []byte) (auth.Session, auth.User, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return auth.Session{}, auth.User{}, err
	}
	for _, sess := range s.sessions {
		if bytes.Equal(sess.TokenHash, hash) {
			return sess, s.users[sess.UserID], nil
		}
	}
	return auth.Session{}, auth.User{}, auth.ErrNotFound
}

func (s *Store) TouchSession(_ context.Context, id string, at time.Time) error {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return err
	}
	sess, ok := s.sessions[id]
	if ok && sess.LastSeenAt.Before(at) {
		sess.LastSeenAt = at
		s.sessions[id] = sess
	}
	return nil
}

func (s *Store) ListSessions(_ context.Context, userID string, at time.Time) ([]auth.Session, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return nil, err
	}
	out := []auth.Session{}
	for _, sess := range s.sessions {
		if sess.UserID == userID && sess.RevokedAt == nil && at.Before(sess.ExpiresAt) {
			out = append(out, sess)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) RevokeSession(_ context.Context, userID, id string, at time.Time) error {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return err
	}
	sess, ok := s.sessions[id]
	if !ok || sess.UserID != userID || sess.RevokedAt != nil {
		return auth.ErrNotFound
	}
	sess.RevokedAt = tp(at)
	s.sessions[id] = sess
	return nil
}

func (s *Store) RevokeUserSessions(_ context.Context, userID, exceptID string, at time.Time) error {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return err
	}
	for id, sess := range s.sessions {
		if sess.UserID == userID && id != exceptID && sess.RevokedAt == nil {
			sess.RevokedAt = tp(at)
			s.sessions[id] = sess
		}
	}
	return nil
}

func (s *Store) CreateLicenseKey(_ context.Context, k *auth.LicenseKey) error {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return err
	}
	for _, x := range s.licenseKeys {
		if bytes.Equal(x.Hash, k.Hash) {
			return auth.ErrAlreadyExists
		}
	}
	k.ID = uuid.NewString()
	k.CreatedAt = now(k.CreatedAt)
	s.licenseKeys[k.ID] = *k
	return nil
}

func (s *Store) ListLicenseKeys(_ context.Context, orgID string) ([]auth.LicenseKey, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return nil, err
	}
	out := []auth.LicenseKey{}
	for _, k := range s.licenseKeys {
		if k.OrgID == orgID {
			k.CreatedByEmail = s.users[k.CreatedBy].Email
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) RevokeLicenseKey(_ context.Context, orgID, id, _ string, at time.Time) (auth.LicenseKey, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return auth.LicenseKey{}, err
	}
	k, ok := s.licenseKeys[id]
	if !ok || k.OrgID != orgID {
		return auth.LicenseKey{}, auth.ErrNotFound
	}
	if k.RevokedAt == nil {
		k.RevokedAt = tp(at)
		s.licenseKeys[id] = k
	}
	return k, nil
}

func (s *Store) LookupLicenseKey(_ context.Context, hash []byte) (tenant.KeyInfo, error) {
	defer s.mu.Unlock()
	s.Lookups++
	if err := s.lock(); err != nil {
		return tenant.KeyInfo{}, err
	}
	for _, k := range s.licenseKeys {
		if bytes.Equal(k.Hash, hash) && k.RevokedAt == nil {
			return tenant.KeyInfo{KeyID: k.ID, TenantID: s.orgs[k.OrgID].TenantID}, nil
		}
	}
	return tenant.KeyInfo{}, tenant.ErrUnknownKey
}

func (s *Store) TouchLicenseKeys(_ context.Context, ids []string, at time.Time) error {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return err
	}
	for _, id := range ids {
		if k, ok := s.licenseKeys[id]; ok {
			k.LastUsedAt = tp(at)
			s.licenseKeys[id] = k
		}
	}
	return nil
}

func (s *Store) CreateAPIKey(_ context.Context, k *auth.APIKey) error {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return err
	}
	for _, x := range s.apiKeys {
		if bytes.Equal(x.Hash, k.Hash) {
			return auth.ErrAlreadyExists
		}
	}
	k.ID = uuid.NewString()
	k.CreatedAt = now(k.CreatedAt)
	s.apiKeys[k.ID] = *k
	return nil
}

func (s *Store) ListAPIKeys(_ context.Context, orgID string) ([]auth.APIKey, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return nil, err
	}
	out := []auth.APIKey{}
	for _, k := range s.apiKeys {
		if k.OrgID == orgID {
			k.CreatedByEmail = s.users[k.CreatedBy].Email
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) GetAPIKey(_ context.Context, orgID, id string) (auth.APIKey, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return auth.APIKey{}, err
	}
	k, ok := s.apiKeys[id]
	if !ok || k.OrgID != orgID {
		return auth.APIKey{}, auth.ErrNotFound
	}
	return k, nil
}

func (s *Store) RevokeAPIKey(_ context.Context, orgID, id, _ string, at time.Time) (auth.APIKey, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return auth.APIKey{}, err
	}
	k, ok := s.apiKeys[id]
	if !ok || k.OrgID != orgID {
		return auth.APIKey{}, auth.ErrNotFound
	}
	if k.RevokedAt == nil {
		k.RevokedAt = tp(at)
		s.apiKeys[id] = k
	}
	return k, nil
}

func (s *Store) LookupAPIKey(_ context.Context, hash []byte) (auth.APIKey, auth.Organization, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return auth.APIKey{}, auth.Organization{}, err
	}
	for _, k := range s.apiKeys {
		if bytes.Equal(k.Hash, hash) {
			return k, s.orgs[k.OrgID], nil
		}
	}
	return auth.APIKey{}, auth.Organization{}, auth.ErrNotFound
}

func (s *Store) TouchAPIKey(_ context.Context, id string, at time.Time) error {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return err
	}
	if k, ok := s.apiKeys[id]; ok {
		k.LastUsedAt = tp(at)
		s.apiKeys[id] = k
	}
	return nil
}

func (s *Store) CreateInvitation(_ context.Context, inv *auth.Invitation) error {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return err
	}
	at := now(inv.CreatedAt)
	for id, x := range s.invitations {
		if x.OrgID == inv.OrgID && x.Email == inv.Email && x.AcceptedAt == nil && x.RevokedAt == nil {
			if !at.Before(x.ExpiresAt) {
				x.RevokedAt = tp(at)
				s.invitations[id] = x
				continue
			}
			return auth.ErrAlreadyExists
		}
	}
	inv.ID = uuid.NewString()
	inv.CreatedAt = at
	s.invitations[inv.ID] = *inv
	return nil
}

func (s *Store) ListInvitations(_ context.Context, orgID string, at time.Time) ([]auth.Invitation, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return nil, err
	}
	out := []auth.Invitation{}
	for _, inv := range s.invitations {
		if inv.OrgID == orgID && inv.Pending(at) {
			inv.InvitedByEmail = s.users[inv.InvitedBy].Email
			out = append(out, inv)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) RevokeInvitation(_ context.Context, orgID, id string, at time.Time) (auth.Invitation, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return auth.Invitation{}, err
	}
	inv, ok := s.invitations[id]
	if !ok || inv.OrgID != orgID || inv.AcceptedAt != nil {
		return auth.Invitation{}, auth.ErrNotFound
	}
	if inv.RevokedAt == nil {
		inv.RevokedAt = tp(at)
		s.invitations[id] = inv
	}
	return inv, nil
}

func (s *Store) GetInvitationByTokenHash(_ context.Context, hash []byte) (auth.Invitation, auth.Organization, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return auth.Invitation{}, auth.Organization{}, err
	}
	for _, inv := range s.invitations {
		if bytes.Equal(inv.TokenHash, hash) {
			return inv, s.orgs[inv.OrgID], nil
		}
	}
	return auth.Invitation{}, auth.Organization{}, auth.ErrNotFound
}

func (s *Store) AcceptInvitation(_ context.Context, invID string, user *auth.User, at time.Time) error {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return err
	}
	inv, ok := s.invitations[invID]
	if !ok || !inv.Pending(at) {
		return auth.ErrNotFound
	}
	if user.ID != "" {
		if _, member := s.members[[2]string{inv.OrgID, user.ID}]; member {
			return auth.ErrAlreadyExists
		}
	} else if err := s.createUserLocked(user); err != nil {
		return err
	}
	s.members[[2]string{inv.OrgID, user.ID}] = membership{inv.Role, at}
	inv.AcceptedAt = tp(at)
	s.invitations[invID] = inv
	return nil
}

func (s *Store) AddAuditEvent(_ context.Context, e *auth.AuditEvent) error {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return err
	}
	e.ID = int64(len(s.audit) + 1)
	e.CreatedAt = now(e.CreatedAt)
	s.audit = append(s.audit, *e)
	return nil
}

func (s *Store) ListAuditEvents(_ context.Context, orgID string, limit int) ([]auth.AuditEvent, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return nil, err
	}
	out := []auth.AuditEvent{}
	for i := len(s.audit) - 1; i >= 0 && len(out) < limit; i-- {
		if s.audit[i].OrgID == orgID {
			out = append(out, s.audit[i])
		}
	}
	return out, nil
}

func (s *Store) CountLoginFailures(_ context.Context, key []byte, since time.Time) (int, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return 0, err
	}
	n := 0
	for _, t := range s.failures[string(key)] {
		if t.After(since) {
			n++
		}
	}
	return n, nil
}

func (s *Store) AddLoginFailure(_ context.Context, key []byte, at time.Time) error {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return err
	}
	s.failures[string(key)] = append(s.failures[string(key)], at)
	return nil
}

func (s *Store) ClearLoginFailures(_ context.Context, key []byte) error {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return err
	}
	delete(s.failures, string(key))
	return nil
}
