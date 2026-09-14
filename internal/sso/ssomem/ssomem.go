// Package ssomem is an in-memory sso.Store for tests. It mirrors the PostgreSQL implementation
// (internal/store/postgres/sso.go), which is covered by the integration tests.
package ssomem

import (
	"bytes"
	"context"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/sso"
)

// Store is a concurrency-safe in-memory sso.Store. Organization data comes from users.
type Store struct {
	mu         sync.Mutex
	users      auth.Store
	conns      map[string]sso.Connection // by id
	domains    map[string]sso.Domain
	mappings   map[string][]sso.RoleMapping
	states     map[string]sso.LoginState
	assertions map[string]time.Time
	tokens     map[string]sso.SCIMToken
	scimUsers  map[[2]string]sso.SCIMUser
	groups     map[string]sso.SCIMGroup
}

var _ sso.Store = (*Store)(nil)

// New returns an empty store.
func New(users auth.Store) *Store {
	return &Store{users: users, conns: map[string]sso.Connection{}, domains: map[string]sso.Domain{}, mappings: map[string][]sso.RoleMapping{},
		states: map[string]sso.LoginState{}, assertions: map[string]time.Time{}, tokens: map[string]sso.SCIMToken{},
		scimUsers: map[[2]string]sso.SCIMUser{}, groups: map[string]sso.SCIMGroup{}}
}

func tp(t time.Time) *time.Time { return &t }

func (s *Store) CreateConnection(_ context.Context, c *sso.Connection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.conns {
		if x.OrgID == c.OrgID {
			return auth.ErrAlreadyExists
		}
	}
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
	}
	s.conns[c.ID] = *c
	return nil
}

func (s *Store) GetConnection(_ context.Context, orgID string) (sso.Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.conns {
		if x.OrgID == orgID {
			return x, nil
		}
	}
	return sso.Connection{}, auth.ErrNotFound
}

func (s *Store) GetConnectionByID(_ context.Context, id string) (sso.Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.conns[id]
	if !ok {
		return sso.Connection{}, auth.ErrNotFound
	}
	return c, nil
}

func (s *Store) UpdateConnection(_ context.Context, c *sso.Connection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.conns[c.ID]
	if !ok {
		return auth.ErrNotFound
	}
	next := *c
	next.TestedVersion, next.LastTestAt, next.LastTestOK, next.LastTestError, next.LastTestDetails =
		old.TestedVersion, old.LastTestAt, old.LastTestOK, old.LastTestError, old.LastTestDetails
	s.conns[c.ID] = next
	return nil
}

func (s *Store) DeleteConnection(_ context.Context, orgID string) (sso.Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, x := range s.conns {
		if x.OrgID == orgID {
			delete(s.conns, id)
			for k, st := range s.states {
				if st.ConnectionID == id {
					delete(s.states, k)
				}
			}
			return x, nil
		}
	}
	return sso.Connection{}, auth.ErrNotFound
}

func (s *Store) RecordTest(_ context.Context, id string, version int, ok bool, errMsg string, details map[string]any, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, found := s.conns[id]
	if !found {
		return auth.ErrNotFound
	}
	c.LastTestAt, c.LastTestOK, c.LastTestError, c.LastTestDetails = tp(at), ok, errMsg, details
	if ok && version == c.ConfigVersion {
		c.TestedVersion = version
	}
	if !ok {
		c.LastTestOK = false
	}
	s.conns[id] = c
	return nil
}

func (s *Store) GetOrgPolicy(_ context.Context, orgID, emailDomain string) (sso.OrgPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var p sso.OrgPolicy
	for _, c := range s.conns {
		if c.OrgID == orgID {
			p = sso.OrgPolicy{ConnectionID: c.ID, Enabled: c.Enabled, Enforce: c.Enforce, BreakGlassUserIDs: append([]string(nil), c.BreakGlassUserIDs...),
				SessionMaxAge: c.SessionMaxAge}
		}
	}
	for _, d := range s.domains {
		if d.OrgID == orgID && d.VerifiedAt != nil && d.Domain == emailDomain {
			p.DomainVerified = true
		}
	}
	return p, nil
}

func (s *Store) CreateDomain(_ context.Context, d *sso.Domain) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.domains {
		if x.OrgID == d.OrgID && x.Domain == d.Domain {
			return auth.ErrAlreadyExists
		}
	}
	d.ID = uuid.NewString()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now()
	}
	s.domains[d.ID] = *d
	return nil
}

func (s *Store) ListDomains(_ context.Context, orgID string) ([]sso.Domain, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []sso.Domain{}
	for _, d := range s.domains {
		if d.OrgID == orgID {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out, nil
}

func (s *Store) GetDomain(_ context.Context, orgID, id string) (sso.Domain, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.domains[id]
	if !ok || d.OrgID != orgID {
		return sso.Domain{}, auth.ErrNotFound
	}
	return d, nil
}

func (s *Store) DeleteDomain(_ context.Context, orgID, id string) (sso.Domain, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.domains[id]
	if !ok || d.OrgID != orgID {
		return sso.Domain{}, auth.ErrNotFound
	}
	delete(s.domains, id)
	return d, nil
}

func (s *Store) verifyLocked(d sso.Domain, method string, at time.Time) (sso.Domain, error) {
	for _, x := range s.domains {
		if x.ID != d.ID && x.Domain == d.Domain && x.VerifiedAt != nil {
			return sso.Domain{}, auth.ErrAlreadyExists
		}
	}
	if d.VerifiedAt == nil {
		d.VerifiedAt, d.VerificationMethod = tp(at), method
	}
	d.EmailTokenHash, d.EmailExpiresAt = nil, nil
	s.domains[d.ID] = d
	return d, nil
}

func (s *Store) MarkDomainVerified(_ context.Context, orgID, id, method string, at time.Time) (sso.Domain, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.domains[id]
	if !ok || d.OrgID != orgID {
		return sso.Domain{}, auth.ErrNotFound
	}
	return s.verifyLocked(d, method, at)
}

func (s *Store) SetDomainChecked(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.domains[id]
	if !ok {
		return auth.ErrNotFound
	}
	d.LastCheckedAt = tp(at)
	s.domains[id] = d
	return nil
}

func (s *Store) SetDomainEmailToken(_ context.Context, orgID, id string, hash []byte, address string, expires time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.domains[id]
	if !ok || d.OrgID != orgID {
		return auth.ErrNotFound
	}
	d.EmailTokenHash, d.EmailAddress, d.EmailExpiresAt = hash, address, tp(expires)
	s.domains[id] = d
	return nil
}

func (s *Store) ConsumeDomainEmailToken(_ context.Context, hash []byte, at time.Time) (sso.Domain, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.domains {
		if d.EmailTokenHash != nil && bytes.Equal(d.EmailTokenHash, hash) && d.EmailExpiresAt != nil && at.Before(*d.EmailExpiresAt) {
			return s.verifyLocked(d, "email", at)
		}
	}
	return sso.Domain{}, auth.ErrNotFound
}

func (s *Store) FindVerifiedDomain(_ context.Context, domain string) (sso.Domain, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.domains {
		if d.Domain == domain && d.VerifiedAt != nil {
			return d, nil
		}
	}
	return sso.Domain{}, auth.ErrNotFound
}

func (s *Store) ListRoleMappings(_ context.Context, orgID string) ([]sso.RoleMapping, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sso.RoleMapping{}, s.mappings[orgID]...), nil
}

func (s *Store) ReplaceRoleMappings(_ context.Context, orgID string, ms []sso.RoleMapping) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]sso.RoleMapping{}, ms...)
	sort.Slice(out, func(i, j int) bool { return out[i].Group < out[j].Group })
	s.mappings[orgID] = out
	return nil
}

func (s *Store) CreateLoginState(_ context.Context, st *sso.LoginState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st.ID = uuid.NewString()
	if st.Result != nil {
		r := *st.Result
		st.Result = &r
	}
	s.states[st.ID] = *st
	return nil
}

func (s *Store) findState(hash []byte, now time.Time) (sso.LoginState, bool) {
	for _, st := range s.states {
		if bytes.Equal(st.StateHash, hash) && st.ConsumedAt == nil && now.Before(st.ExpiresAt) {
			return st, true
		}
	}
	return sso.LoginState{}, false
}

func (s *Store) GetLoginState(_ context.Context, hash []byte, now time.Time) (sso.LoginState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.findState(hash, now)
	if !ok {
		return sso.LoginState{}, auth.ErrNotFound
	}
	return st, nil
}

func (s *Store) SetLoginResult(_ context.Context, id string, result sso.Identity, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.states[id]
	if !ok || st.Result != nil || st.ConsumedAt != nil || !now.Before(st.ExpiresAt) {
		return auth.ErrNotFound
	}
	st.Result = &result
	s.states[id] = st
	return nil
}

func (s *Store) ConsumeLoginState(_ context.Context, hash []byte, now time.Time) (sso.LoginState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.findState(hash, now)
	if !ok {
		return sso.LoginState{}, auth.ErrNotFound
	}
	st.ConsumedAt = tp(now)
	s.states[st.ID] = st
	return st, nil
}

func (s *Store) RecordAssertion(_ context.Context, connectionID, assertionID string, expires time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := connectionID + "\x00" + assertionID
	if _, ok := s.assertions[k]; ok {
		return false, nil
	}
	s.assertions[k] = expires
	return true, nil
}

func (s *Store) Cleanup(_ context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, st := range s.states {
		if st.ExpiresAt.Before(now) {
			delete(s.states, k)
		}
	}
	for k, exp := range s.assertions {
		if exp.Before(now) {
			delete(s.assertions, k)
		}
	}
	return nil
}

// ---- SCIM tokens ----

func (s *Store) CreateSCIMToken(_ context.Context, t *sso.SCIMToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.tokens {
		if bytes.Equal(x.Hash, t.Hash) {
			return auth.ErrAlreadyExists
		}
	}
	t.ID = uuid.NewString()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}
	s.tokens[t.ID] = *t
	return nil
}

func (s *Store) ListSCIMTokens(ctx context.Context, orgID string) ([]sso.SCIMToken, error) {
	s.mu.Lock()
	out := []sso.SCIMToken{}
	for _, t := range s.tokens {
		if t.OrgID == orgID {
			out = append(out, t)
		}
	}
	s.mu.Unlock()
	for i := range out {
		if u, err := s.users.GetUser(ctx, out[i].CreatedBy); err == nil {
			out[i].CreatedByEmail = u.Email
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) RevokeSCIMToken(_ context.Context, orgID, id string, at time.Time) (sso.SCIMToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[id]
	if !ok || t.OrgID != orgID {
		return sso.SCIMToken{}, auth.ErrNotFound
	}
	if t.RevokedAt == nil {
		t.RevokedAt = tp(at)
	}
	s.tokens[id] = t
	return t, nil
}

func (s *Store) LookupSCIMToken(ctx context.Context, hashes [][]byte) (sso.SCIMToken, auth.Organization, error) {
	s.mu.Lock()
	var found *sso.SCIMToken
	for _, h := range hashes {
		for _, t := range s.tokens {
			if bytes.Equal(t.Hash, h) {
				found = &t
				break
			}
		}
		if found != nil {
			break
		}
	}
	s.mu.Unlock()
	if found == nil {
		return sso.SCIMToken{}, auth.Organization{}, auth.ErrNotFound
	}
	org, err := s.users.GetOrganization(ctx, found.OrgID)
	return *found, org, err
}

func (s *Store) TouchSCIMToken(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[id]
	if !ok {
		return auth.ErrNotFound
	}
	t.LastUsedAt = tp(at)
	s.tokens[id] = t
	return nil
}

// ---- SCIM users ----

func (s *Store) CreateSCIMUser(_ context.Context, u *sso.SCIMUser) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, x := range s.scimUsers {
		if k[0] != u.OrgID {
			continue
		}
		if x.UserID == u.UserID || strings.EqualFold(x.UserName, u.UserName) || (u.ExternalID != "" && x.ExternalID == u.ExternalID) {
			return auth.ErrAlreadyExists
		}
	}
	now := time.Now()
	if u.CreatedAt.IsZero() {
		u.CreatedAt = now
	}
	u.UpdatedAt = u.CreatedAt
	s.scimUsers[[2]string{u.OrgID, u.UserID}] = *u
	return nil
}

func (s *Store) GetSCIMUser(_ context.Context, orgID, userID string) (sso.SCIMUser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.scimUsers[[2]string{orgID, userID}]
	if !ok {
		return sso.SCIMUser{}, auth.ErrNotFound
	}
	return u, nil
}

func (s *Store) ListSCIMUsers(_ context.Context, orgID string, f sso.SCIMFilter, offset, limit int) ([]sso.SCIMUser, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []sso.SCIMUser
	for k, u := range s.scimUsers {
		if k[0] != orgID || (f.UserName != "" && !strings.EqualFold(u.UserName, f.UserName)) || (f.ExternalID != "" && u.ExternalID != f.ExternalID) {
			continue
		}
		all = append(all, u)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.Before(all[j].CreatedAt) || (all[i].CreatedAt.Equal(all[j].CreatedAt) && all[i].UserID < all[j].UserID)
	})
	return page(all, offset, limit), len(all), nil
}

func page[T any](all []T, offset, limit int) []T {
	if offset >= len(all) {
		return []T{}
	}
	end := min(len(all), offset+limit)
	return append([]T{}, all[offset:end]...)
}

func (s *Store) UpdateSCIMUser(_ context.Context, u *sso.SCIMUser) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := [2]string{u.OrgID, u.UserID}
	old, ok := s.scimUsers[k]
	if !ok {
		return auth.ErrNotFound
	}
	for kk, x := range s.scimUsers {
		if kk[0] == u.OrgID && kk != k && (strings.EqualFold(x.UserName, u.UserName) || (u.ExternalID != "" && x.ExternalID == u.ExternalID)) {
			return auth.ErrAlreadyExists
		}
	}
	u.CreatedAt = old.CreatedAt
	u.UpdatedAt = time.Now()
	s.scimUsers[k] = *u
	return nil
}

func (s *Store) DeleteSCIMUser(_ context.Context, orgID, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := [2]string{orgID, userID}
	if _, ok := s.scimUsers[k]; !ok {
		return auth.ErrNotFound
	}
	delete(s.scimUsers, k)
	for id, g := range s.groups {
		if g.OrgID == orgID {
			g.Members = slices.DeleteFunc(g.Members, func(m string) bool { return m == userID })
			s.groups[id] = g
		}
	}
	return nil
}

// ---- SCIM groups ----

func (s *Store) CreateSCIMGroup(_ context.Context, g *sso.SCIMGroup) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.groups {
		if x.OrgID == g.OrgID && strings.EqualFold(x.DisplayName, g.DisplayName) {
			return auth.ErrAlreadyExists
		}
	}
	g.ID = uuid.NewString()
	now := time.Now()
	g.CreatedAt, g.UpdatedAt = now, now
	g.Members = uniq(g.Members)
	s.groups[g.ID] = *g
	return nil
}

func uniq(v []string) []string {
	out := []string{}
	for _, x := range v {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

func (s *Store) GetSCIMGroup(_ context.Context, orgID, id string) (sso.SCIMGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[id]
	if !ok || g.OrgID != orgID {
		return sso.SCIMGroup{}, auth.ErrNotFound
	}
	g.Members = append([]string{}, g.Members...)
	return g, nil
}

func (s *Store) ListSCIMGroups(_ context.Context, orgID string, f sso.SCIMFilter, offset, limit int) ([]sso.SCIMGroup, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []sso.SCIMGroup
	for _, g := range s.groups {
		if g.OrgID != orgID || (f.DisplayName != "" && !strings.EqualFold(g.DisplayName, f.DisplayName)) || (f.ExternalID != "" && g.ExternalID != f.ExternalID) {
			continue
		}
		g.Members = append([]string{}, g.Members...)
		all = append(all, g)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.Before(all[j].CreatedAt) || (all[i].CreatedAt.Equal(all[j].CreatedAt) && all[i].ID < all[j].ID)
	})
	return page(all, offset, limit), len(all), nil
}

func (s *Store) UpdateSCIMGroup(_ context.Context, g *sso.SCIMGroup, members []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.groups[g.ID]
	if !ok || old.OrgID != g.OrgID {
		return auth.ErrNotFound
	}
	for _, x := range s.groups {
		if x.ID != g.ID && x.OrgID == g.OrgID && strings.EqualFold(x.DisplayName, g.DisplayName) {
			return auth.ErrAlreadyExists
		}
	}
	old.DisplayName, old.ExternalID, old.UpdatedAt = g.DisplayName, g.ExternalID, time.Now()
	if members != nil {
		old.Members = uniq(members)
	}
	s.groups[g.ID] = old
	*g = old
	return nil
}

func (s *Store) AddSCIMGroupMembers(_ context.Context, orgID, groupID string, userIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[groupID]
	if !ok || g.OrgID != orgID {
		return auth.ErrNotFound
	}
	g.Members = uniq(append(g.Members, userIDs...))
	g.UpdatedAt = time.Now()
	s.groups[groupID] = g
	return nil
}

func (s *Store) RemoveSCIMGroupMembers(_ context.Context, orgID, groupID string, userIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[groupID]
	if !ok || g.OrgID != orgID {
		return auth.ErrNotFound
	}
	g.Members = slices.DeleteFunc(g.Members, func(m string) bool { return slices.Contains(userIDs, m) })
	g.UpdatedAt = time.Now()
	s.groups[groupID] = g
	return nil
}

func (s *Store) DeleteSCIMGroup(_ context.Context, orgID, id string) (sso.SCIMGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[id]
	if !ok || g.OrgID != orgID {
		return sso.SCIMGroup{}, auth.ErrNotFound
	}
	delete(s.groups, id)
	return g, nil
}

func (s *Store) SCIMGroupNames(_ context.Context, orgID, userID string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for _, g := range s.groups {
		if g.OrgID == orgID && slices.Contains(g.Members, userID) {
			out = append(out, g.DisplayName)
		}
	}
	sort.Strings(out)
	return out, nil
}
