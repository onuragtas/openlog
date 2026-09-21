package memstore

import (
	"bytes"
	"context"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/rum"
	"github.com/onuragtas/openlog/internal/tenant"
)

// Browser keys in the in-memory store, mirroring internal/store/postgres/browserkeys.go (the PostgreSQL
// implementation is covered by the integration suite). Like the real store it serves both interfaces:
// auth.Store for management and rum.Store for the ingest lookup.
var _ rum.Store = (*Store)(nil)

func (s *Store) CreateBrowserKey(_ context.Context, k *auth.BrowserKey) error {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return err
	}
	for _, x := range s.browserKeys {
		if bytes.Equal(x.Hash, k.Hash) {
			return auth.ErrAlreadyExists
		}
	}
	k.ID = uuid.NewString()
	k.CreatedAt = now(k.CreatedAt)
	k.UpdatedAt = now(k.UpdatedAt)
	s.browserKeys[k.ID] = *k
	return nil
}

func (s *Store) ListBrowserKeys(_ context.Context, orgID string) ([]auth.BrowserKey, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return nil, err
	}
	out := []auth.BrowserKey{}
	for _, k := range s.browserKeys {
		if k.OrgID == orgID {
			k.CreatedByEmail = s.users[k.CreatedBy].Email
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) GetBrowserKey(_ context.Context, orgID, id string) (auth.BrowserKey, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return auth.BrowserKey{}, err
	}
	k, ok := s.browserKeys[id]
	if !ok || k.OrgID != orgID {
		return auth.BrowserKey{}, auth.ErrNotFound
	}
	k.CreatedByEmail = s.users[k.CreatedBy].Email
	return k, nil
}

func (s *Store) UpdateBrowserKey(_ context.Context, orgID, id string, in auth.BrowserKeyInput, by string, at time.Time) (auth.BrowserKey, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return auth.BrowserKey{}, err
	}
	k, ok := s.browserKeys[id]
	// A revoked key is never edited back into service (see the PostgreSQL store).
	if !ok || k.OrgID != orgID || k.RevokedAt != nil {
		return auth.BrowserKey{}, auth.ErrNotFound
	}
	k.Name, k.ServiceName, k.Environment = in.Name, in.ServiceName, in.Environment
	k.Kind, k.Origins, k.AppIDs = in.Kind, in.Origins, in.AppIDs
	k.RateLimitPerMinute, k.SampleRate = in.RateLimitPerMinute, in.SampleRate
	k.UpdatedAt = now(at)
	_ = by
	s.browserKeys[id] = k
	return k, nil
}

func (s *Store) RevokeBrowserKey(_ context.Context, orgID, id, _ string, at time.Time) (auth.BrowserKey, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return auth.BrowserKey{}, err
	}
	k, ok := s.browserKeys[id]
	if !ok || k.OrgID != orgID {
		return auth.BrowserKey{}, auth.ErrNotFound
	}
	if k.RevokedAt == nil {
		k.RevokedAt = tp(at)
		s.browserKeys[id] = k
	}
	return k, nil
}

// LookupBrowserKey implements rum.Store: the best (lowest index) candidate hash wins.
func (s *Store) LookupBrowserKey(_ context.Context, hashes [][]byte) (rum.Key, error) {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return rum.Key{}, err
	}
	best, bestIdx := "", -1
	for id, k := range s.browserKeys {
		if i := tenant.MatchIndex(hashes, k.Hash); i >= 0 && k.RevokedAt == nil && (bestIdx < 0 || i < bestIdx) {
			best, bestIdx = id, i
		}
	}
	if bestIdx < 0 {
		return rum.Key{}, rum.ErrUnknownKey
	}
	k := s.browserKeys[best]
	return rum.Key{
		KeyID: k.ID, TenantID: s.orgs[k.OrgID].TenantID, ServiceName: k.ServiceName,
		Environment: k.Environment, Kind: k.Kind, Origins: k.Origins, AppIDs: k.AppIDs,
		RateLimitPerMinute: k.RateLimitPerMinute, SampleRate: k.SampleRate,
	}, nil
}

// TouchBrowserKeys implements rum.Store.
func (s *Store) TouchBrowserKeys(_ context.Context, ids []string, at time.Time) error {
	defer s.mu.Unlock()
	if err := s.lock(); err != nil {
		return err
	}
	for _, id := range ids {
		if k, ok := s.browserKeys[id]; ok {
			k.LastUsedAt = tp(at)
			s.browserKeys[id] = k
		}
	}
	return nil
}

// BrowserKeyHash returns the stored hash of a browser key (tests).
func (s *Store) BrowserKeyHash(id string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.browserKeys[id].Hash
}
