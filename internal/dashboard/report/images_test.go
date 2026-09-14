package report_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/dashboard"
	"github.com/onuragtas/openlog/internal/dashboard/dashboardtest"
	"github.com/onuragtas/openlog/internal/dashboard/report"
)

type fakeImager struct {
	calls  int
	images map[string]report.Image
	err    error
	sr     *dashboard.ScheduledReport
	from   time.Time
	to     time.Time
}

func (f *fakeImager) Images(_ context.Context, sr *dashboard.ScheduledReport, from, to time.Time) (map[string]report.Image, error) {
	f.calls++
	f.sr, f.from, f.to = sr, from, to
	return f.images, f.err
}

var fakePNG = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)

func TestJobImages(t *testing.T) {
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
		{Title: "Total", Visualization: "billboard", Layout: dashboard.Layout{W: 4, H: 2}, Query: "SELECT count(*) FROM Log"},
		{Title: "By host", Visualization: "table", Layout: dashboard.Layout{X: 4, W: 4, H: 2}, Query: "SELECT count(*) FROM Log FACET host.name"},
		{Title: "Trend", Visualization: "line", Layout: dashboard.Layout{X: 8, W: 4, H: 2}, Query: "SELECT count(*) FROM Log TIMESERIES"},
	}}}}, member)
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.CreateReport(ctx, org, d.ID, dashboard.ReportInput{Frequency: "daily", Hour: 9, Timezone: "UTC", Recipients: []string{"alice@example.com"}}, member)
	if err != nil {
		t.Fatal(err)
	}
	w := d.Pages[0].Widgets
	imager := &fakeImager{images: map[string]report.Image{
		w[0].ID: {PNG: fakePNG, Width: 720, Height: 140},
		w[1].ID: {PNG: []byte("GIF89a not a png"), Width: 720, Height: 300},                              // not a PNG: table
		w[2].ID: {PNG: append(fakePNG, make([]byte, report.MaxImageBytes)...), Width: 1440, Height: 560}, // too large: table
	}}
	var tenants []string
	mailer := &fakeMailer{}
	job := report.NewJob(report.Options{Store: store, Run: fakeRun(&tenants), Mailer: mailer, Images: imager, Now: now})
	clock = time.Date(2026, 9, 14, 9, 1, 0, 0, time.UTC)
	if err := job.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if mailer.count() != 1 || imager.calls != 1 {
		t.Fatalf("mails %d, imager calls %d", mailer.count(), imager.calls)
	}
	if imager.sr.ID != r.ID || imager.sr.TenantID != "tenant-1" || !imager.to.Equal(time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)) ||
		imager.to.Sub(imager.from) != 24*time.Hour {
		t.Errorf("imager got report %+v, %s – %s", imager.sr, imager.from, imager.to)
	}
	mail := mailer.sent[0]
	if len(mail.Inline) != 1 || mail.Inline[0].ContentID != "widget-1@openlog" || mail.Inline[0].ContentType != "image/png" || string(mail.Inline[0].Data) != string(fakePNG) {
		t.Fatalf("inline %+v", mail.Inline)
	}
	if !strings.Contains(mail.HTML, `<img src="cid:widget-1@openlog" width="720" height="140" alt="Total"`) {
		t.Errorf("html has no inline image:\n%s", mail.HTML)
	}
	// The other widgets fall back to tables in HTML; the text part keeps all tables.
	if !strings.Contains(mail.HTML, "host-a") || strings.Count(mail.HTML, "<img") != 1 || !strings.Contains(mail.Text, "42") || !strings.Contains(mail.Text, "host-a | 12") {
		t.Errorf("fallback tables missing:\nHTML %s\nText %s", mail.HTML, mail.Text)
	}

	// Renderer failure: tables only, still sent.
	imager.err = errors.New("renderer down")
	clock = time.Date(2026, 9, 15, 9, 1, 0, 0, time.UTC)
	if err := job.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if mailer.count() != 2 || len(mailer.sent[1].Inline) != 0 || strings.Contains(mailer.sent[1].HTML, "<img") || !strings.Contains(mailer.sent[1].HTML, "host-a") {
		t.Errorf("fallback mail %+v", mailer.sent[1])
	}
	if run := store.Runs()[r.ID+"/2026-09-15"]; run.Status != "sent" {
		t.Errorf("run with renderer failure %+v", run)
	}
}
