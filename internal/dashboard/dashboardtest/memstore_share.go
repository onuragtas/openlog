package dashboardtest

import (
	"bytes"
	"context"
	"slices"
	"sort"
	"time"

	"github.com/onuragtas/openlog/internal/dashboard"
)

// In-memory sharing settings, share links and report schedules with the semantics of the PostgreSQL store.

type shareRec struct {
	share dashboard.Share
	hash  []byte
}

type sharingState struct {
	settings map[string]dashboard.Settings
	shares   map[string]*shareRec
	reports  map[string]*dashboard.Report
	runs     map[string]dashboard.ReportRun // report id + "/" + period
}

func newSharingState() sharingState {
	return sharingState{settings: map[string]dashboard.Settings{}, shares: map[string]*shareRec{}, reports: map[string]*dashboard.Report{},
		runs: map[string]dashboard.ReportRun{}}
}

var (
	_ dashboard.ShareStore  = (*MemStore)(nil)
	_ dashboard.ReportStore = (*MemStore)(nil)
)

func (s *MemStore) GetSettings(_ context.Context, orgID string) (dashboard.Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sharing.settings[orgID]
	if !ok {
		return dashboard.Settings{ReportDomains: []string{}}, nil
	}
	st.ReportDomains = append([]string{}, st.ReportDomains...)
	return st, nil
}

func (s *MemStore) PutSettings(_ context.Context, orgID string, st dashboard.Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st.UpdatedAt = s.Now()
	s.sharing.settings[orgID] = st
	return nil
}

func (s *MemStore) ListShares(_ context.Context, orgID, dashboardID string) ([]dashboard.Share, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []dashboard.Share{}
	for _, r := range s.sharing.shares {
		if r.share.OrgID == orgID && r.share.DashboardID == dashboardID {
			sh := r.share
			sh.CreatedByEmail = s.Emails[sh.CreatedBy]
			out = append(out, sh)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *MemStore) CreateShare(_ context.Context, sh *dashboard.Share, tokenHash []byte, maxActive int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.items[sh.DashboardID]; !ok || d.OrgID != sh.OrgID {
		return dashboard.ErrNotFound
	}
	active := 0
	for _, r := range s.sharing.shares {
		if r.share.DashboardID == sh.DashboardID && r.share.Active(s.Now()) {
			active++
		}
	}
	if active >= maxActive {
		return &dashboard.ValidationError{Msg: "a dashboard has at most 20 active share links; revoke one first"}
	}
	sh.CreatedAt = s.Now()
	s.sharing.shares[sh.ID] = &shareRec{share: *sh, hash: append([]byte{}, tokenHash...)}
	return nil
}

func (s *MemStore) RevokeShare(_ context.Context, orgID, dashboardID, shareID, _ string) (*dashboard.Share, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.sharing.shares[shareID]
	if !ok || r.share.OrgID != orgID || r.share.DashboardID != dashboardID {
		return nil, dashboard.ErrNotFound
	}
	if r.share.RevokedAt == nil {
		now := s.Now()
		r.share.RevokedAt = &now
	}
	sh := r.share
	return &sh, nil
}

func (s *MemStore) ResolveShare(_ context.Context, tokenHash []byte, now time.Time) (*dashboard.Share, string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.sharing.shares {
		if !bytes.Equal(r.hash, tokenHash) {
			continue
		}
		sh := &r.share
		st := s.sharing.settings[sh.OrgID]
		if !st.SharesEnabled || !sh.Active(now) || sh.CreatedBy == "" || !slices.Contains(s.Members[sh.OrgID], s.Emails[sh.CreatedBy]) {
			return nil, "", false, dashboard.ErrNotFound
		}
		audit := sh.LastUsedAt == nil || now.Sub(*sh.LastUsedAt) >= time.Hour
		sh.LastUsedAt = &now
		sh.UseCount++
		c := *sh
		return &c, s.Tenants[sh.OrgID], audit, nil
	}
	return nil, "", false, dashboard.ErrNotFound
}

func cloneReport(r *dashboard.Report) *dashboard.Report {
	c := *r
	c.Recipients = append([]string{}, r.Recipients...)
	return &c
}

func (s *MemStore) ListReports(_ context.Context, orgID, dashboardID string) ([]dashboard.Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []dashboard.Report{}
	for _, r := range s.sharing.reports {
		if r.OrgID == orgID && r.DashboardID == dashboardID {
			out = append(out, *s.withRun(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *MemStore) withRun(r *dashboard.Report) *dashboard.Report {
	c := cloneReport(r)
	c.CreatedByEmail = s.Emails[c.CreatedBy]
	var last *dashboard.ReportRun
	for key, run := range s.sharing.runs {
		if len(key) > len(r.ID) && key[:len(r.ID)] == r.ID && (last == nil || run.StartedAt.After(last.StartedAt)) {
			rc := run
			last = &rc
		}
	}
	c.LastRun = last
	return c
}

func (s *MemStore) GetReport(_ context.Context, orgID, dashboardID, id string) (*dashboard.Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.sharing.reports[id]
	if !ok || r.OrgID != orgID || r.DashboardID != dashboardID {
		return nil, dashboard.ErrNotFound
	}
	return s.withRun(r), nil
}

func (s *MemStore) CreateReport(_ context.Context, r *dashboard.Report, maxPerDashboard int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, x := range s.sharing.reports {
		if x.DashboardID == r.DashboardID {
			n++
		}
	}
	if n >= maxPerDashboard {
		return &dashboard.ValidationError{Msg: "a dashboard has at most 10 scheduled reports"}
	}
	c := cloneReport(r)
	c.CreatedAt, c.UpdatedAt = s.Now(), s.Now()
	s.sharing.reports[r.ID] = c
	return nil
}

func (s *MemStore) UpdateReport(_ context.Context, r *dashboard.Report) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.sharing.reports[r.ID]
	if !ok || old.OrgID != r.OrgID || old.DashboardID != r.DashboardID {
		return dashboard.ErrNotFound
	}
	c := cloneReport(r)
	c.CreatedAt, c.UpdatedAt, c.LastRun = old.CreatedAt, s.Now(), nil
	s.sharing.reports[r.ID] = c
	return nil
}

func (s *MemStore) DeleteReport(_ context.Context, orgID, dashboardID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.sharing.reports[id]
	if !ok || r.OrgID != orgID || r.DashboardID != dashboardID {
		return dashboard.ErrNotFound
	}
	delete(s.sharing.reports, id)
	return nil
}

func (s *MemStore) MemberEmails(_ context.Context, orgID string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.Members[orgID]...), nil
}

func (s *MemStore) EnabledReports(_ context.Context, limit int) ([]dashboard.ScheduledReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []dashboard.ScheduledReport{}
	for _, r := range s.sharing.reports {
		if r.Enabled && len(out) < limit {
			out = append(out, dashboard.ScheduledReport{Report: *s.withRun(r), TenantID: s.Tenants[r.OrgID]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *MemStore) ClaimReportRun(_ context.Context, reportID, period string, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := reportID + "/" + period
	if _, ok := s.sharing.runs[key]; ok {
		return false, nil
	}
	s.sharing.runs[key] = dashboard.ReportRun{Period: period, Status: "running", StartedAt: now}
	return true, nil
}

func (s *MemStore) FinishReportRun(_ context.Context, reportID, period string, run dashboard.ReportRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := reportID + "/" + period
	old := s.sharing.runs[key]
	run.Period, run.StartedAt = period, old.StartedAt
	s.sharing.runs[key] = run
	return nil
}

// Runs returns the recorded runs (tests).
func (s *MemStore) Runs() map[string]dashboard.ReportRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]dashboard.ReportRun{}
	for k, v := range s.sharing.runs {
		out[k] = v
	}
	return out
}
