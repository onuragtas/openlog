// Package fleettest provides an in-memory fleet.Store for tests.
package fleettest

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/onuragtas/openlog/internal/fleet"
	"github.com/onuragtas/openlog/internal/intsettings"
)

// MemStore is an in-memory fleet.Store. Tenants map to organizations through TenantOrgs, or to
// "org-" + tenant id when not listed; tenant "unknown" has no organization.
type MemStore struct {
	TenantOrgs map[string]string
	// Integrations, when set, supplies the integration settings of LoadOrgState (nil = none).
	Integrations intsettings.Store

	mu        sync.Mutex
	policies  map[string]fleet.Policy
	overrides map[string]map[string]fleet.Override
	phpOvs    map[string]map[string]fleet.PHPOverride
	javaOvs   map[string]map[string]fleet.JavaOverride
	hosts     map[string]map[string]fleet.Host
	rollouts  []*fleet.Rollout
	audit     []fleet.AuditEntry
	seq       int
	err       error
	upserts   int
}

var _ fleet.Store = (*MemStore)(nil)

// NewMemStore creates an empty store.
func NewMemStore() *MemStore {
	return &MemStore{TenantOrgs: map[string]string{}, policies: map[string]fleet.Policy{},
		overrides: map[string]map[string]fleet.Override{}, hosts: map[string]map[string]fleet.Host{},
		phpOvs: map[string]map[string]fleet.PHPOverride{}, javaOvs: map[string]map[string]fleet.JavaOverride{}}
}

func (s *MemStore) ListJavaOverrides(_ context.Context, orgID string) (map[string]fleet.JavaOverride, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]fleet.JavaOverride{}
	for k, v := range s.javaOvs[orgID] {
		out[k] = v
	}
	return out, nil
}

func (s *MemStore) PutJavaOverride(_ context.Context, orgID string, o fleet.JavaOverride, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.javaOvs[orgID] == nil {
		s.javaOvs[orgID] = map[string]fleet.JavaOverride{}
	}
	s.javaOvs[orgID][o.HostID] = o
	return nil
}

func (s *MemStore) DeleteJavaOverride(_ context.Context, orgID, hostID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.javaOvs[orgID][hostID]
	delete(s.javaOvs[orgID], hostID)
	return ok, nil
}

func (s *MemStore) ListPHPOverrides(_ context.Context, orgID string) (map[string]fleet.PHPOverride, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]fleet.PHPOverride{}
	for k, v := range s.phpOvs[orgID] {
		out[k] = v
	}
	return out, nil
}

func (s *MemStore) PutPHPOverride(_ context.Context, orgID string, o fleet.PHPOverride, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phpOvs[orgID] == nil {
		s.phpOvs[orgID] = map[string]fleet.PHPOverride{}
	}
	s.phpOvs[orgID][o.HostID] = o
	return nil
}

func (s *MemStore) DeletePHPOverride(_ context.Context, orgID, hostID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.phpOvs[orgID][hostID]
	delete(s.phpOvs[orgID], hostID)
	return ok, nil
}

// SetErr makes LoadOrgState and UpsertHosts fail with err (nil clears it).
func (s *MemStore) SetErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// Upserts counts successful UpsertHosts calls.
func (s *MemStore) Upserts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.upserts
}

// AuditActions returns the audit actions in order.
func (s *MemStore) AuditActions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for _, e := range s.audit {
		out = append(out, e.Action)
	}
	return out
}

// OrgOf maps a tenant to its organization id.
func (s *MemStore) OrgOf(tenant string) string {
	if org, ok := s.TenantOrgs[tenant]; ok {
		return org
	}
	return "org-" + tenant
}

func (s *MemStore) LoadOrgState(ctx context.Context, tenantID string) (fleet.OrgState, error) {
	s.mu.Lock()
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return fleet.OrgState{}, err
	}
	if tenantID == "unknown" {
		return fleet.OrgState{}, fleet.ErrNotFound
	}
	org := s.OrgOf(tenantID)
	sp, _ := s.GetPolicy(ctx, org)
	ovs, _ := s.ListOverrides(ctx, org)
	cur, _ := s.CurrentRollout(ctx, org)
	phpOvs, _ := s.ListPHPOverrides(ctx, org)
	javaOvs, _ := s.ListJavaOverrides(ctx, org)
	st := fleet.OrgState{OrgID: org, Policy: sp.Policy, Overrides: ovs, Rollout: cur, IntegrationsLoaded: true, PHPOverrides: phpOvs,
		JavaOverrides: javaOvs}
	if s.Integrations != nil {
		ints, err := s.Integrations.ListSettings(ctx, org)
		if err != nil {
			return fleet.OrgState{}, err
		}
		st.Integrations = ints
	}
	return st, nil
}

func (s *MemStore) UpsertHosts(_ context.Context, recs []fleet.HostRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.upserts++
	for _, r := range recs {
		org := s.OrgOf(r.TenantID)
		if s.hosts[org] == nil {
			s.hosts[org] = map[string]fleet.Host{}
		}
		h := fleet.Host{HostReport: r.Report, OrgID: org, FirstSeenAt: r.SyncAt, LastSyncAt: r.SyncAt, RolloutID: r.RolloutID}
		if old, ok := s.hosts[org][r.Report.HostID]; ok {
			h.FirstSeenAt = old.FirstSeenAt
			if h.RolloutID == "" {
				h.RolloutID = old.RolloutID
			}
		}
		s.hosts[org][r.Report.HostID] = h
	}
	return nil
}

func (s *MemStore) GetPolicy(_ context.Context, orgID string) (fleet.StoredPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.policies[orgID]; ok {
		return fleet.StoredPolicy{Policy: p}, nil
	}
	return fleet.StoredPolicy{Policy: fleet.DefaultPolicy(), IsDefault: true}, nil
}

func (s *MemStore) PutPolicy(_ context.Context, orgID string, p fleet.Policy, _ string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policies[orgID] = p
	return nil
}

func (s *MemStore) ListOverrides(_ context.Context, orgID string) (map[string]fleet.Override, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]fleet.Override{}
	for k, v := range s.overrides[orgID] {
		out[k] = v
	}
	return out, nil
}

func (s *MemStore) PutOverride(_ context.Context, orgID string, o fleet.Override, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.overrides[orgID] == nil {
		s.overrides[orgID] = map[string]fleet.Override{}
	}
	s.overrides[orgID][o.HostID] = o
	return nil
}

func (s *MemStore) DeleteOverride(_ context.Context, orgID, hostID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.overrides[orgID][hostID]
	delete(s.overrides[orgID], hostID)
	return ok, nil
}

func (s *MemStore) GetHost(_ context.Context, orgID, hostID string) (fleet.Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.hosts[orgID][hostID]
	if !ok {
		return fleet.Host{}, fleet.ErrNotFound
	}
	return h, nil
}

func (s *MemStore) sortedHostsLocked(orgID string) []fleet.Host {
	out := []fleet.Host{}
	for _, h := range s.hosts[orgID] {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].HostName != out[j].HostName {
			return out[i].HostName < out[j].HostName
		}
		return out[i].HostID < out[j].HostID
	})
	return out
}

// ListHosts uses the last host id of a page as cursor.
func (s *MemStore) ListHosts(_ context.Context, orgID string, f fleet.HostFilter) ([]fleet.Host, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []fleet.Host{}
	started := f.Cursor == ""
	for _, h := range s.sortedHostsLocked(orgID) {
		if !started {
			started = h.HostID == f.Cursor
			continue
		}
		if (f.Version != "" && h.Version != f.Version) || (f.State != "" && h.UpdateState != f.State) ||
			(f.Query != "" && !strings.Contains(h.HostName, f.Query) && !strings.Contains(h.HostID, f.Query)) {
			continue
		}
		out = append(out, h)
	}
	next := ""
	if len(out) > f.Limit {
		out = out[:f.Limit]
		next = out[len(out)-1].HostID
	}
	return out, next, nil
}

func (s *MemStore) AllHosts(_ context.Context, orgID string, since time.Time) ([]fleet.Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []fleet.Host{}
	for _, h := range s.sortedHostsLocked(orgID) {
		if !h.LastSyncAt.Before(since) {
			out = append(out, h)
		}
	}
	return out, nil
}

func (s *MemStore) HostGroups(_ context.Context, orgID string, since time.Time) ([]fleet.HostGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type key struct {
		version, method, state string
		capable                bool
	}
	groups := map[key]*fleet.HostGroup{}
	for _, h := range s.hosts[orgID] {
		k := key{h.Version, h.InstallMethod, h.UpdateState, h.UpdateCapable}
		g := groups[k]
		if g == nil {
			g = &fleet.HostGroup{Version: h.Version, UpdateCapable: h.UpdateCapable, InstallMethod: h.InstallMethod, UpdateState: h.UpdateState}
			groups[k] = g
		}
		g.Hosts++
		if !h.LastSyncAt.Before(since) {
			g.RecentlySeen++
		}
	}
	out := []fleet.HostGroup{}
	for _, g := range groups {
		out = append(out, *g)
	}
	return out, nil
}

func (s *MemStore) FleetOrgs(context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	for org := range s.hosts {
		seen[org] = true
	}
	for _, r := range s.rollouts {
		if r.Open() {
			seen[r.OrgID] = true
		}
	}
	out := []string{}
	for org := range seen {
		out = append(out, org)
	}
	sort.Strings(out)
	return out, nil
}

func (s *MemStore) ListRollouts(_ context.Context, orgID string, limit int) ([]fleet.Rollout, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []fleet.Rollout{}
	for i := len(s.rollouts) - 1; i >= 0 && len(out) < limit; i-- {
		if s.rollouts[i].OrgID == orgID {
			out = append(out, *s.rollouts[i])
		}
	}
	return out, nil
}

func (s *MemStore) GetRollout(_ context.Context, orgID, id string) (fleet.Rollout, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rollouts {
		if r.OrgID == orgID && r.ID == id {
			return *r, nil
		}
	}
	return fleet.Rollout{}, fleet.ErrNotFound
}

func (s *MemStore) CurrentRollout(_ context.Context, orgID string) (*fleet.Rollout, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.rollouts) - 1; i >= 0; i-- {
		if r := s.rollouts[i]; r.OrgID == orgID && r.State != fleet.RolloutSuperseded {
			c := *r
			return &c, nil
		}
	}
	return nil, nil
}

func (s *MemStore) CreateRollout(_ context.Context, r *fleet.Rollout, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, old := range s.rollouts {
		if old.OrgID == r.OrgID && old.Open() {
			old.State, old.StateReason = fleet.RolloutSuperseded, reason
			t := r.CreatedAt
			old.EndedAt = &t
		}
	}
	s.seq++
	r.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", s.seq)
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = r.CreatedAt
	}
	c := *r
	s.rollouts = append(s.rollouts, &c)
	return nil
}

func (s *MemStore) UpdateRollout(_ context.Context, r *fleet.Rollout, expect string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, old := range s.rollouts {
		if old.OrgID == r.OrgID && old.ID == r.ID {
			if old.State != expect {
				return fleet.ErrConflict
			}
			*old = *r
			return nil
		}
	}
	return fleet.ErrNotFound
}

func (s *MemStore) AddAudit(_ context.Context, e fleet.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = append(s.audit, e)
	return nil
}
