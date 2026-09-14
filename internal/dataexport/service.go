package dataexport

import "context"

// Get returns an export.
func (s *Service) Get(ctx context.Context, id string) (Export, error) { return s.Store.Get(ctx, id) }

// GetByToken returns the completed export of a download link token.
func (s *Service) GetByToken(ctx context.Context, token string) (Export, error) {
	if len(token) < 20 || len(token) > 200 {
		return Export{}, ErrNotFound
	}
	return s.Store.GetByTokenHash(ctx, TokenHash(token))
}

// ListOrg lists an organization's exports.
func (s *Service) ListOrg(ctx context.Context, orgID string, limit int) ([]Export, error) {
	return s.Store.ListOrg(ctx, orgID, limit)
}

// ListUser lists a user's personal exports.
func (s *Service) ListUser(ctx context.Context, userID string, limit int) ([]Export, error) {
	return s.Store.ListUser(ctx, userID, limit)
}
