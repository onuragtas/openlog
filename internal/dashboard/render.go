package dashboard

import "context"

// OpenReportRender resolves the dashboard and the enabled report of a verified render token (D-097; the token itself
// is checked by internal/renderer.VerifyToken in the api). ErrNotFound when the dashboard or report no longer exists,
// the report belongs to another dashboard or organization, or it was disabled.
func (m *Manager) OpenReportRender(ctx context.Context, orgID, dashboardID, reportID string) (*Dashboard, *Report, error) {
	rs, err := m.reportStore()
	if err != nil {
		return nil, nil, err
	}
	if _, ok := canonicalID(dashboardID); !ok {
		return nil, nil, ErrNotFound
	}
	if _, ok := canonicalID(reportID); !ok {
		return nil, nil, ErrNotFound
	}
	r, err := rs.GetReport(ctx, orgID, dashboardID, reportID)
	if err != nil {
		return nil, nil, err
	}
	if !r.Enabled {
		return nil, nil, ErrNotFound
	}
	d, err := m.store.Get(ctx, orgID, dashboardID)
	if err != nil {
		return nil, nil, err
	}
	return d, r, nil
}
