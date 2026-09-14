//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/store/postgres"
)

// TestRemoveMemberCleansReportRecipients: removing a member removes their address from the organization's report
// recipient lists in the same transaction; reports left without recipients are disabled; other organizations are not
// touched; a refused removal (last owner) changes nothing (0062, D-096).
func TestRemoveMemberCleansReportRecipients(t *testing.T) {
	migrate(t)
	ctx := context.Background()
	st := postgres.NewStore(pool)
	sfx := uuid.NewString()[:8]
	owner := &auth.User{Email: "owner-" + sfx + "@example.com", Name: "Owner"}
	org := &auth.Organization{TenantID: "rc-" + sfx, Name: "Reports"}
	if err := st.CreateOrganization(ctx, org, owner); err != nil {
		t.Fatal(err)
	}
	other := &auth.Organization{TenantID: "rc2-" + sfx, Name: "Other"}
	if err := st.CreateOrganization(ctx, other, owner); err != nil {
		t.Fatal(err)
	}
	member := &auth.User{Email: "member-" + sfx + "@example.com", Name: "Member"}
	if err := st.CreateUser(ctx, member); err != nil {
		t.Fatal(err)
	}
	for _, o := range []string{org.ID, other.ID} {
		if err := st.AddMember(ctx, o, member.ID, auth.RoleMember); err != nil {
			t.Fatal(err)
		}
	}
	report := func(orgID string, recipients ...string) string {
		var dash string
		if err := pool.QueryRow(ctx, `INSERT INTO dashboards (org_id, name) VALUES ($1, 'd') RETURNING id::text`, orgID).Scan(&dash); err != nil {
			t.Fatal(err)
		}
		id := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO dashboard_reports (id, org_id, dashboard_id, frequency, hour, recipients, time_range)
			VALUES ($1, $2, $3, 'daily', 8, $4, '24h')`, id, orgID, dash, recipients); err != nil {
			t.Fatal(err)
		}
		return id
	}
	shared := report(org.ID, member.Email, "ops@example.com")
	only := report(org.ID, member.Email)
	untouched := report(org.ID, "ops@example.com")
	otherOrg := report(other.ID, member.Email)

	cleanup, err := st.RemoveMemberWithCleanup(ctx, org.ID, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(cleanup.ReportsUpdated)
	want := []string{shared, only}
	slices.Sort(want)
	if !slices.Equal(cleanup.ReportsUpdated, want) || !slices.Equal(cleanup.ReportsDisabled, []string{only}) {
		t.Fatalf("cleanup = %+v, want updated %v disabled [%s]", cleanup, want, only)
	}
	state := func(id string) ([]string, bool) {
		var rs []string
		var enabled bool
		if err := pool.QueryRow(ctx, `SELECT recipients, enabled FROM dashboard_reports WHERE id = $1`, id).Scan(&rs, &enabled); err != nil {
			t.Fatal(err)
		}
		return rs, enabled
	}
	if rs, en := state(shared); !slices.Equal(rs, []string{"ops@example.com"}) || !en {
		t.Errorf("shared report: %v enabled=%v", rs, en)
	}
	if rs, en := state(only); len(rs) != 0 || en {
		t.Errorf("single-recipient report: %v enabled=%v", rs, en)
	}
	if rs, en := state(untouched); !slices.Equal(rs, []string{"ops@example.com"}) || !en {
		t.Errorf("unrelated report changed: %v enabled=%v", rs, en)
	}
	if rs, _ := state(otherOrg); !slices.Equal(rs, []string{member.Email}) {
		t.Errorf("report of another organization changed: %v", rs)
	}
	if _, err := st.GetMembership(ctx, org.ID, member.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("membership kept: %v", err)
	}

	// The last owner cannot be removed: the transaction rolls back and recipient lists stay.
	ownerReport := report(org.ID, owner.Email)
	if _, err := st.RemoveMemberWithCleanup(ctx, org.ID, owner.ID); !errors.Is(err, auth.ErrLastOwner) {
		t.Fatalf("last owner removal: %v", err)
	}
	if rs, en := state(ownerReport); !slices.Equal(rs, []string{owner.Email}) || !en {
		t.Errorf("refused removal changed recipients: %v enabled=%v", rs, en)
	}

	// Plain RemoveMember (Store interface) cleans up too.
	if err := st.RemoveMember(ctx, other.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	if rs, _ := state(otherOrg); len(rs) != 0 {
		t.Errorf("RemoveMember kept recipients: %v", rs)
	}
}

// TestLanguageColumns round-trips the user preference and organization default (0060, D-095).
func TestLanguageColumns(t *testing.T) {
	migrate(t)
	ctx := context.Background()
	st := postgres.NewStore(pool)
	sfx := uuid.NewString()[:8]
	u := &auth.User{Email: "lang-" + sfx + "@example.com", Name: "L", Locale: "tr"}
	org := &auth.Organization{TenantID: "lang-" + sfx, Name: "Lang"}
	if err := st.CreateOrganization(ctx, org, u); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetUser(ctx, u.ID)
	if err != nil || got.Locale != "tr" || got.LocaleExplicit || got.Preference() != "" {
		t.Fatalf("new user: %+v %v", got, err)
	}
	if err := st.SetUserLocale(ctx, u.ID, "en", true); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.GetUserByEmail(ctx, u.Email); got.Preference() != "en" {
		t.Fatalf("preference: %+v", got)
	}
	if err := st.SetOrganizationLocale(ctx, org.ID, "tr"); err != nil {
		t.Fatal(err)
	}
	if o, err := st.GetOrganization(ctx, org.ID); err != nil || o.Locale != "tr" {
		t.Fatalf("org locale: %+v %v", o, err)
	}
	if err := st.SetOrganizationLocale(ctx, org.ID, "de"); err == nil {
		t.Fatal("unsupported organization language stored")
	}
}
