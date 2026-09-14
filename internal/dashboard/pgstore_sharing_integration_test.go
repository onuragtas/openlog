//go:build integration

package dashboard_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/dashboard"
)

// Version history, share links and scheduled reports on PostgreSQL (migrations 0053–0055).
func TestPGVersionsSharesReports(t *testing.T) {
	pool := pgPool(t)
	f := newPGFixture(t, pool)
	var aliceEmail, tenant string
	if err := pool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, f.alice).Scan(&aliceEmail); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT tenant_id FROM organizations WHERE id = $1`, f.orgID).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{f.alice, f.bob} {
		if _, err := pool.Exec(ctx, `INSERT INTO memberships (org_id, user_id, role) VALUES ($1, $2, 'member')`, f.orgID, u); err != nil {
			t.Fatal(err)
		}
	}
	store := dashboard.NewPGStore(pool)
	m := dashboard.NewManager(store)
	va := dashboard.Viewer{UserID: f.alice, CanWrite: true}
	admin := dashboard.Viewer{UserID: f.bob, CanWrite: true, Admin: true}

	// ---- versions ----
	d, err := m.Create(ctx, f.orgID, sample(), va)
	if err != nil {
		t.Fatal(err)
	}
	in := inputOf(d)
	in.Name = "Checkout v2"
	in.Pages[0].Widgets = in.Pages[0].Widgets[:1]
	d2, err := m.Update(ctx, f.orgID, d.ID, in, va)
	if err != nil {
		t.Fatal(err)
	}
	list, err := m.Versions(ctx, f.orgID, d.ID, va)
	if err != nil || len(list) != 2 || list[0].Version != 2 || list[0].AuthorEmail != aliceEmail || list[0].WidgetCount != 1 || list[1].WidgetCount != 2 {
		t.Fatalf("versions %+v %v", list, err)
	}
	det, err := m.VersionDetail(ctx, f.orgID, d.ID, 2, va)
	if err != nil || det.Changes == nil || !det.Changes.Name || len(det.Changes.WidgetsRemoved) != 1 || det.Document.Pages[0].Widgets[0].Thresholds == nil {
		t.Fatalf("detail %+v %v", det, err)
	}
	r, err := m.Restore(ctx, f.orgID, d.ID, 1, d2.Version, va)
	if err != nil || r.Version != 3 || r.Name != "Checkout" || !reflect.DeepEqual(widgetIDs(r), widgetIDs(d)) {
		t.Fatalf("restore %+v %v", r, err)
	}
	if list, _ = m.Versions(ctx, f.orgID, d.ID, va); len(list) != 3 || list[0].RestoredFrom != 1 {
		t.Fatalf("after restore %+v", list)
	}
	if _, err := m.Versions(ctx, f.otherOrg, d.ID, va); !errors.Is(err, dashboard.ErrNotFound) {
		t.Errorf("other org: %v", err)
	}
	cur := r
	for i := 0; i < dashboard.MaxVersions+2; i++ {
		in := inputOf(cur)
		in.Name = fmt.Sprintf("n%d", i)
		if cur, err = m.Update(ctx, f.orgID, d.ID, in, va); err != nil {
			t.Fatal(err)
		}
	}
	var stored int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dashboard_versions WHERE dashboard_id = $1`, d.ID).Scan(&stored); err != nil || stored != dashboard.MaxVersions {
		t.Errorf("retention: %d rows %v", stored, err)
	}

	// ---- share links ----
	valid := dashboard.ShareInput{Label: "TV", ExpiresAt: time.Now().Add(time.Hour), Range: "24h", Variables: map[string][]string{"env": {"prod"}}}
	if _, _, err := m.CreateShare(ctx, f.orgID, d.ID, valid, va); !errors.Is(err, dashboard.ErrSharingDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	st, err := m.UpdateSettings(ctx, f.orgID, dashboard.Settings{SharesEnabled: true, ReportDomains: []string{"partner.io"}}, admin)
	if err != nil || !st.SharesEnabled || !reflect.DeepEqual(st.ReportDomains, []string{"partner.io"}) || st.UpdatedBy != f.bob {
		t.Fatalf("settings %+v %v", st, err)
	}
	sh, token, err := m.CreateShare(ctx, f.orgID, d.ID, valid, va)
	if err != nil {
		t.Fatal(err)
	}
	from, to := time.Now().Add(-2*time.Hour).UTC().Truncate(time.Second), time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	fixed, fixedToken, err := m.CreateShare(ctx, f.orgID, d.ID, dashboard.ShareInput{ExpiresAt: time.Now().Add(time.Hour), From: &from, To: &to}, va)
	if err != nil {
		t.Fatal(err)
	}
	sd, err := m.OpenShare(ctx, token)
	if err != nil || sd.TenantID != tenant || !sd.Audit || sd.Dashboard.ID != d.ID || !reflect.DeepEqual(sd.Share.Variables, valid.Variables) || sd.Share.Range != "24h" {
		t.Fatalf("open %+v %v", sd, err)
	}
	if sd, err := m.OpenShare(ctx, token); err != nil || sd.Audit {
		t.Errorf("second use: %+v %v", sd, err)
	}
	if sd, err := m.OpenShare(ctx, fixedToken); err != nil || !sd.Share.From.Equal(from) || !sd.Share.To.Equal(to) || sd.Share.ID != fixed.ID {
		t.Errorf("fixed range share %+v %v", sd, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM memberships WHERE org_id = $1 AND user_id = $2`, f.orgID, f.alice); err != nil {
		t.Fatal(err)
	}
	if _, err := m.OpenShare(ctx, token); !errors.Is(err, dashboard.ErrNotFound) {
		t.Errorf("creator left: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO memberships (org_id, user_id, role) VALUES ($1, $2, 'member')`, f.orgID, f.alice); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE dashboard_shares SET expires_at = now() - interval '1 second' WHERE id = $1`, fixed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.OpenShare(ctx, fixedToken); !errors.Is(err, dashboard.ErrNotFound) {
		t.Errorf("expired: %v", err)
	}
	if rv, err := m.RevokeShare(ctx, f.orgID, d.ID, sh.ID, admin); err != nil || rv.RevokedAt == nil {
		t.Fatalf("revoke %+v %v", rv, err)
	}
	if _, err := m.OpenShare(ctx, token); !errors.Is(err, dashboard.ErrNotFound) {
		t.Errorf("revoked: %v", err)
	}
	shares, err := m.Shares(ctx, f.orgID, d.ID, admin)
	if err != nil || len(shares) != 2 || shares[0].CreatedByEmail != aliceEmail || shares[1].UseCount != 1 {
		t.Errorf("list shares %+v %v", shares, err)
	}
	var hashLen int
	if err := pool.QueryRow(ctx, `SELECT length(token_hash) FROM dashboard_shares WHERE id = $1`, sh.ID).Scan(&hashLen); err != nil || hashLen != 32 {
		t.Errorf("token hash length %d %v", hashLen, err)
	}

	// ---- reports ----
	rep, err := m.CreateReport(ctx, f.orgID, d.ID, dashboard.ReportInput{Frequency: "weekly", Weekday: 1, Hour: 9, Timezone: "Europe/Istanbul",
		Recipients: []string{aliceEmail, "ops@partner.io"}, Language: "tr", Variables: map[string][]string{"env": {"prod"}}}, va)
	if err != nil || rep.Range != "7d" || rep.CreatedByEmail != aliceEmail || rep.LastRun != nil || !reflect.DeepEqual(rep.Variables, map[string][]string{"env": {"prod"}}) {
		t.Fatalf("create report %+v %v", rep, err)
	}
	if _, err := m.CreateReport(ctx, f.orgID, d.ID, dashboard.ReportInput{Frequency: "daily", Hour: 9, Recipients: []string{"x@evil.com"}}, va); err == nil {
		t.Error("recipient outside the organization accepted")
	}
	enabled, err := store.EnabledReports(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range enabled {
		found = found || (e.ID == rep.ID && e.TenantID == tenant)
	}
	if !found {
		t.Errorf("enabled reports %+v", enabled)
	}
	now := time.Now()
	if ok, err := store.ClaimReportRun(ctx, rep.ID, "2026-09-14", now); !ok || err != nil {
		t.Fatalf("claim %v %v", ok, err)
	}
	if ok, err := store.ClaimReportRun(ctx, rep.ID, "2026-09-14", now); ok || err != nil {
		t.Errorf("claimed twice %v %v", ok, err)
	}
	finished := time.Now()
	if err := store.FinishReportRun(ctx, rep.ID, "2026-09-14", dashboard.ReportRun{Status: "sent", Recipients: 2, FinishedAt: &finished}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetReport(ctx, f.orgID, d.ID, rep.ID)
	if err != nil || got.LastRun == nil || got.LastRun.Status != "sent" || got.LastRun.Recipients != 2 || got.LastRun.FinishedAt == nil {
		t.Errorf("last run %+v %v", got, err)
	}
	disabled := false
	upd, err := m.UpdateReport(ctx, f.orgID, d.ID, rep.ID, dashboard.ReportInput{Frequency: "daily", Hour: 7, Minute: 30, Recipients: []string{aliceEmail}, Enabled: &disabled}, admin)
	if err != nil || upd.Enabled || upd.Frequency != "daily" || upd.Minute != 30 {
		t.Fatalf("update report %+v %v", upd, err)
	}
	if list, err := m.Reports(ctx, f.orgID, d.ID, va); err != nil || len(list) != 1 {
		t.Errorf("list reports %+v %v", list, err)
	}

	// Deleting the dashboard removes versions, share links and reports.
	if _, err := m.Delete(ctx, f.orgID, d.ID, va); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM dashboard_versions WHERE dashboard_id = $1) + (SELECT count(*) FROM dashboard_shares WHERE dashboard_id = $1)
		+ (SELECT count(*) FROM dashboard_reports WHERE dashboard_id = $1)`, d.ID).Scan(&left); err != nil || left != 0 {
		t.Errorf("rows left after delete: %d %v", left, err)
	}
}
