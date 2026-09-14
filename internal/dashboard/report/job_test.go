package report_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/dashboard"
	"github.com/onuragtas/openlog/internal/dashboard/dashboardtest"
	"github.com/onuragtas/openlog/internal/dashboard/report"
	"github.com/onuragtas/openlog/internal/oql"
)

const (
	org   = "11111111-1111-1111-1111-111111111111"
	alice = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
)

type fakeMailer struct {
	mu   sync.Mutex
	sent []auth.Mail
	fail bool
}

func (f *fakeMailer) Send(_ context.Context, m auth.Mail) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("smtp down")
	}
	f.sent = append(f.sent, m)
	return nil
}

func (f *fakeMailer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func fakeRun(tenants *[]string) report.Runner {
	return func(_ context.Context, tenantID string, p *oql.Plan) (*oql.Result, error) {
		*tenants = append(*tenants, tenantID)
		cols := []oql.ResultColumn{{Name: "count(*)", Function: "count", Type: "number"}}
		switch p.Kind {
		case oql.KindFacets:
			return &oql.Result{Kind: p.Kind, Columns: cols, Facets: []string{"host.name"}, Rows: []oql.Row{{Facets: []string{"host-a"}, Values: []any{float64(12)}}}}, nil
		case oql.KindTimeseries:
			return &oql.Result{Kind: p.Kind, Columns: cols, Facets: []string{}, Series: []oql.Series{{Facets: []string{}, Column: 0,
				Points: []oql.Point{{int64(1), float64(1)}, {int64(2), nil}, {int64(3), float64(5)}}}}}, nil
		}
		return &oql.Result{Kind: p.Kind, Columns: cols, Facets: []string{}, Rows: []oql.Row{{Facets: []string{}, Values: []any{float64(42)}}}}, nil
	}
}

func TestJob(t *testing.T) {
	ctx := context.Background()
	store := dashboardtest.New()
	store.Emails[alice] = "alice@example.com"
	store.Members[org] = []string{"alice@example.com"}
	store.Tenants[org] = "tenant-1"
	clock := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	store.Now = now
	m := dashboard.NewManager(store)
	m.SetClock(now)
	member := dashboard.Viewer{UserID: alice, CanWrite: true}
	d, err := m.Create(ctx, org, dashboard.Input{Name: "Checkout", Pages: []dashboard.Page{{Name: "Overview", Widgets: []dashboard.Widget{
		{Title: "<b>Total</b>", Visualization: "billboard", Layout: dashboard.Layout{W: 4, H: 2}, Query: "SELECT count(*) FROM Log"},
		{Title: "By host", Visualization: "table", Layout: dashboard.Layout{X: 4, W: 4, H: 2}, Query: "SELECT count(*) FROM Log FACET host.name"},
		{Title: "Trend", Visualization: "line", Layout: dashboard.Layout{X: 8, W: 4, H: 2}, Query: "SELECT count(*) FROM Log TIMESERIES"},
		{Title: "Notes", Visualization: "markdown", Layout: dashboard.Layout{Y: 2, W: 12, H: 2}, Markdown: "hi"},
	}}}}, member)
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.CreateReport(ctx, org, d.ID, dashboard.ReportInput{Frequency: "daily", Hour: 9, Timezone: "UTC", Language: "tr",
		Recipients: []string{"alice@example.com"}}, member)
	if err != nil {
		t.Fatal(err)
	}
	var tenants []string
	mailer := &fakeMailer{}
	job := report.NewJob(report.Options{Store: store, Run: fakeRun(&tenants), Mailer: mailer, PublicURL: "https://openlog.example.com/", Now: now})
	tick := func(at time.Time) {
		t.Helper()
		clock = at
		if err := job.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}

	tick(time.Date(2026, 9, 14, 8, 30, 0, 0, time.UTC)) // yesterday's send is older than the report
	if mailer.count() != 0 {
		t.Fatalf("sent before the first scheduled time: %d", mailer.count())
	}
	tick(time.Date(2026, 9, 14, 9, 1, 0, 0, time.UTC))
	if mailer.count() != 1 {
		t.Fatalf("due report: %d mails", mailer.count())
	}
	mail := mailer.sent[0]
	if mail.To != "alice@example.com" || !strings.Contains(mail.Subject, "Günlük") || !strings.Contains(mail.HTML, "&lt;b&gt;Total&lt;/b&gt;") ||
		strings.Contains(mail.HTML, "<b>Total</b>") || !strings.Contains(mail.HTML, "https://openlog.example.com/dashboards/"+d.ID+"?from=") ||
		!strings.Contains(mail.Text, "host-a | 12") || !strings.Contains(mail.Text, "42") || !strings.Contains(mail.Text, "3 | 1 | 5 | 5") ||
		strings.Contains(mail.Text, "Notes") {
		t.Errorf("mail %+v", mail)
	}
	for _, tn := range tenants {
		if tn != "tenant-1" {
			t.Errorf("query ran with tenant %q", tn)
		}
	}
	if run := store.Runs()[r.ID+"/2026-09-14"]; run.Status != "sent" || run.Recipients != 1 || run.FinishedAt == nil {
		t.Errorf("run %+v", run)
	}

	tick(time.Date(2026, 9, 14, 9, 2, 0, 0, time.UTC))
	job2 := report.NewJob(report.Options{Store: store, Run: fakeRun(&tenants), Mailer: mailer, Now: now}) // another leader
	if err := job2.Tick(ctx); err != nil || mailer.count() != 1 {
		t.Fatalf("period sent twice: %d %v", mailer.count(), err)
	}

	tick(time.Date(2026, 9, 15, 16, 0, 0, 0, time.UTC)) // 7 hours late: beyond the catch-up window
	if mailer.count() != 1 {
		t.Errorf("late send: %d", mailer.count())
	}
	tick(time.Date(2026, 9, 16, 9, 5, 0, 0, time.UTC))
	if mailer.count() != 2 {
		t.Errorf("next day: %d", mailer.count())
	}

	store.Members[org] = nil // the recipient left the organization
	tick(time.Date(2026, 9, 17, 9, 5, 0, 0, time.UTC))
	if run := store.Runs()[r.ID+"/2026-09-17"]; mailer.count() != 2 || run.Status != "skipped" {
		t.Errorf("recipient left: %d mails, run %+v", mailer.count(), run)
	}
	store.Members[org] = []string{"alice@example.com"}
	mailer.fail = true
	tick(time.Date(2026, 9, 18, 9, 5, 0, 0, time.UTC))
	if run := store.Runs()[r.ID+"/2026-09-18"]; run.Status != "failed" || !strings.Contains(run.Error, "smtp down") {
		t.Errorf("smtp failure run %+v", run)
	}
	noMail := report.NewJob(report.Options{Store: store, Run: fakeRun(&tenants), Now: now})
	clock = time.Date(2026, 9, 19, 9, 5, 0, 0, time.UTC)
	if err := noMail.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if run := store.Runs()[r.ID+"/2026-09-19"]; run.Status != "failed" || !strings.Contains(run.Error, "SMTP") {
		t.Errorf("no mailer run %+v", run)
	}
}
