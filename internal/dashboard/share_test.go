package dashboard_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/dashboard"
	"github.com/onuragtas/openlog/internal/dashboard/dashboardtest"
)

func sharingFixture(t *testing.T) (*dashboard.Manager, *dashboardtest.MemStore, *time.Time, *dashboard.Dashboard) {
	t.Helper()
	store := dashboardtest.New()
	store.Emails[alice] = "alice@example.com"
	store.Emails[bob] = "bob@example.com"
	store.Members[org] = []string{"alice@example.com", "bob@example.com"}
	store.Tenants[org] = "t1"
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store.Now = clock
	m := dashboard.NewManager(store)
	m.SetClock(clock)
	d, err := m.Create(ctx, org, sample(), memberA)
	if err != nil {
		t.Fatal(err)
	}
	return m, store, &now, d
}

func TestShareLinks(t *testing.T) {
	m, store, now, d := sharingFixture(t)
	valid := dashboard.ShareInput{Label: "Wall screen", ExpiresAt: now.Add(24 * time.Hour), Range: "24h", Variables: map[string][]string{"env": {"prod"}, "host": {"*"}}}
	if _, _, err := m.CreateShare(ctx, org, d.ID, valid, memberA); !errors.Is(err, dashboard.ErrSharingDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	if _, err := m.UpdateSettings(ctx, org, dashboard.Settings{SharesEnabled: true}, memberA); !errors.Is(err, dashboard.ErrForbidden) {
		t.Errorf("member changes settings: %v", err)
	}
	if _, err := m.UpdateSettings(ctx, org, dashboard.Settings{SharesEnabled: true}, apiKey); !errors.Is(err, dashboard.ErrForbidden) {
		t.Errorf("API key changes settings: %v", err)
	}
	if _, err := m.UpdateSettings(ctx, org, dashboard.Settings{ReportDomains: []string{"not a domain"}}, adminC); err == nil {
		t.Error("invalid domain accepted")
	}
	st, err := m.UpdateSettings(ctx, org, dashboard.Settings{SharesEnabled: true, ReportDomains: []string{"@Example.COM", "example.com", " partner.io "}}, adminC)
	if err != nil || !st.SharesEnabled || !reflect.DeepEqual(st.ReportDomains, []string{"example.com", "partner.io"}) {
		t.Fatalf("settings %+v %v", st, err)
	}

	later := now.Add(time.Hour)
	for want, in := range map[string]dashboard.ShareInput{
		"expires_at is required":           {Range: "24h"},
		"between 5 minutes and 90 days":    {ExpiresAt: now.Add(91 * 24 * time.Hour), Range: "24h"},
		"range must be a relative range":   {ExpiresAt: later, Range: "40d"},
		"either range or from":             {ExpiresAt: later, Range: "1h", From: now, To: &later},
		"from must be before to":           {ExpiresAt: later, From: &later, To: now},
		"has no variable":                  {ExpiresAt: later, Range: "1h", Variables: map[string][]string{"nope": {"x"}}},
		"takes a single value":             {ExpiresAt: later, Range: "1h", Variables: map[string][]string{"env": {"a", "b"}}},
		"range or from and to is required": {ExpiresAt: later},
		"label must be a single line":      {ExpiresAt: later, Range: "1h", Label: "a\nb"},
	} {
		_, _, err := m.CreateShare(ctx, org, d.ID, in, memberA)
		var ve *dashboard.ValidationError
		if !errors.As(err, &ve) || !strings.Contains(ve.Msg, want) {
			t.Errorf("%s: %v", want, err)
		}
	}
	for name, v := range map[string]dashboard.Viewer{"member who is not the creator": memberB, "viewer": viewer, "API key": apiKey} {
		if _, _, err := m.CreateShare(ctx, org, d.ID, valid, v); !errors.Is(err, dashboard.ErrForbidden) {
			t.Errorf("%s creates a share: %v", name, err)
		}
	}

	sh, token, err := m.CreateShare(ctx, org, d.ID, valid, memberA)
	if err != nil || !strings.HasPrefix(token, "olds_") || len(token) != 48 || !reflect.DeepEqual(sh.Variables, map[string][]string{"env": {"prod"}}) {
		t.Fatalf("create %+v %q %v", sh, token, err)
	}
	sd, err := m.OpenShare(ctx, token)
	if err != nil || sd.TenantID != "t1" || sd.Dashboard.ID != d.ID || !sd.Audit || sd.Share.Label != "Wall screen" {
		t.Fatalf("open %+v %v", sd, err)
	}
	if from, to := sd.Share.TimeRange(*now); !to.Equal(*now) || !from.Equal(now.Add(-24*time.Hour)) {
		t.Errorf("time range %v %v", from, to)
	}
	if sd, err := m.OpenShare(ctx, token); err != nil || sd.Audit {
		t.Errorf("second use within the hour must not be audited: %+v %v", sd, err)
	}
	tampered := token[:len(token)-1] + map[bool]string{true: "B", false: "A"}[strings.HasSuffix(token, "A")]
	for _, bad := range []string{tampered, "olds_short", "", strings.Repeat("x", 48), "olds_" + strings.Repeat("!", 43)} {
		if _, err := m.OpenShare(ctx, bad); !errors.Is(err, dashboard.ErrNotFound) {
			t.Errorf("token %q: %v", bad, err)
		}
	}

	if list, err := m.Shares(ctx, org, d.ID, adminC); err != nil || len(list) != 1 || list[0].CreatedByEmail != "alice@example.com" {
		t.Errorf("admin lists shares: %+v %v", list, err)
	}
	if _, err := m.Shares(ctx, org, d.ID, memberB); !errors.Is(err, dashboard.ErrForbidden) {
		t.Errorf("member lists shares of another user's dashboard: %v", err)
	}

	// Disabled sharing, expiry and a creator who left the organization turn the link off.
	if _, err := m.UpdateSettings(ctx, org, dashboard.Settings{SharesEnabled: false}, adminC); err != nil {
		t.Fatal(err)
	}
	if _, err := m.OpenShare(ctx, token); !errors.Is(err, dashboard.ErrNotFound) {
		t.Errorf("sharing disabled: %v", err)
	}
	if _, err := m.UpdateSettings(ctx, org, dashboard.Settings{SharesEnabled: true}, adminC); err != nil {
		t.Fatal(err)
	}
	store.Members[org] = []string{"bob@example.com"}
	if _, err := m.OpenShare(ctx, token); !errors.Is(err, dashboard.ErrNotFound) {
		t.Errorf("creator left: %v", err)
	}
	store.Members[org] = []string{"alice@example.com", "bob@example.com"}
	saved := *now
	*now = now.Add(25 * time.Hour)
	if _, err := m.OpenShare(ctx, token); !errors.Is(err, dashboard.ErrNotFound) {
		t.Errorf("expired: %v", err)
	}
	*now = saved

	if _, err := m.RevokeShare(ctx, org, d.ID, sh.ID, memberB); !errors.Is(err, dashboard.ErrForbidden) {
		t.Errorf("member revokes: %v", err)
	}
	if r, err := m.RevokeShare(ctx, org, d.ID, sh.ID, adminC); err != nil || r.RevokedAt == nil {
		t.Fatalf("revoke %+v %v", r, err)
	}
	if _, err := m.OpenShare(ctx, token); !errors.Is(err, dashboard.ErrNotFound) {
		t.Errorf("revoked: %v", err)
	}
	if _, err := m.RevokeShare(ctx, org, d.ID, "not-a-uuid", adminC); !errors.Is(err, dashboard.ErrNotFound) {
		t.Errorf("bad share id: %v", err)
	}
}

func TestParseRelativeRange(t *testing.T) {
	for in, want := range map[string]time.Duration{"15m": 15 * time.Minute, "24h": 24 * time.Hour, "31d": 31 * 24 * time.Hour} {
		if d, ok := dashboard.ParseRelativeRange(in); !ok || d != want {
			t.Errorf("%s: %v %v", in, d, ok)
		}
	}
	for _, in := range []string{"", "0m", "32d", "1w", "-1h", "01h", "1.5h", "99999d"} {
		if _, ok := dashboard.ParseRelativeRange(in); ok {
			t.Errorf("%q accepted", in)
		}
	}
}
