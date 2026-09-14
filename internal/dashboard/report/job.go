package report

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/dashboard"
	"github.com/onuragtas/openlog/internal/oql"
)

// Store is what the job reads and writes (dashboard.PGStore).
type Store interface {
	Get(ctx context.Context, orgID, id string) (*dashboard.Dashboard, error)
	GetSettings(ctx context.Context, orgID string) (dashboard.Settings, error)
	dashboard.ReportStore
}

// Runner executes a compiled plan with the tenant scope of an organization (and its query limits).
type Runner func(ctx context.Context, tenantID string, p *oql.Plan) (*oql.Result, error)

// Options configure a Job.
type Options struct {
	Store  Store
	Run    Runner
	Mailer auth.Mailer // nil: runs are recorded as failed ("SMTP is not configured")
	// PublicURL is the web UI base URL for the dashboard link (OPENLOG_PUBLIC_URL); "" = no link.
	PublicURL string
	// Interval between schedule checks (default 1 minute); CatchUp is how late a missed send is still delivered
	// (default 6 hours, e.g. after a leader change or an outage).
	Interval, CatchUp time.Duration
	// MaxWidgets bounds the widgets queried per report (default 50); QueryTimeout bounds one widget query (default 30 s).
	MaxWidgets   int
	QueryTimeout time.Duration
	Log          *slog.Logger
	Now          func() time.Time
}

// Job sends due reports; run it on the api leader only.
type Job struct{ o Options }

// NewJob creates a job.
func NewJob(o Options) *Job {
	if o.Interval <= 0 {
		o.Interval = time.Minute
	}
	if o.CatchUp <= 0 {
		o.CatchUp = 6 * time.Hour
	}
	if o.MaxWidgets <= 0 {
		o.MaxWidgets = 50
	}
	if o.QueryTimeout <= 0 {
		o.QueryTimeout = 30 * time.Second
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Job{o: o}
}

// Run checks the schedules every interval until ctx is done.
func (j *Job) Run(ctx context.Context) {
	t := time.NewTicker(j.o.Interval)
	defer t.Stop()
	for {
		if err := j.Tick(ctx); err != nil && ctx.Err() == nil {
			j.o.Log.Warn("dashboard report round failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick sends every report whose latest scheduled time has passed (within the catch-up window) and whose period was
// not claimed yet. A period is claimed before sending, so each period is sent at most once across leaders.
func (j *Job) Tick(ctx context.Context) error {
	now := j.o.Now().UTC()
	reports, err := j.o.Store.EnabledReports(ctx, 10000)
	if err != nil {
		return err
	}
	for i := range reports {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		sr := &reports[i]
		at, period, ok := sr.Occurrence(now)
		// Not due, too old, already sent, or scheduled before the report was created or last changed.
		if !ok || now.Sub(at) > j.o.CatchUp || (sr.LastRun != nil && sr.LastRun.Period == period) || at.Before(sr.UpdatedAt) {
			continue
		}
		claimed, err := j.o.Store.ClaimReportRun(ctx, sr.ID, period, now)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		run := j.send(ctx, sr, at)
		run.Period = period
		finished := j.o.Now().UTC()
		run.FinishedAt = &finished
		if err := j.o.Store.FinishReportRun(context.WithoutCancel(ctx), sr.ID, period, run); err != nil {
			j.o.Log.Warn("cannot record dashboard report run", "report_id", sr.ID, "err", err)
		}
		lvl := slog.LevelInfo
		if run.Status != "sent" {
			lvl = slog.LevelWarn
		}
		j.o.Log.Log(ctx, lvl, "dashboard report", "report_id", sr.ID, "org_id", sr.OrgID, "dashboard_id", sr.DashboardID,
			"period", period, "status", run.Status, "recipients", run.Recipients, "error", run.Error, "variables", sortedVariables(sr.Variables))
	}
	return nil
}

func (j *Job) send(ctx context.Context, sr *dashboard.ScheduledReport, at time.Time) dashboard.ReportRun {
	d, err := j.o.Store.Get(ctx, sr.OrgID, sr.DashboardID)
	if err != nil {
		if errors.Is(err, dashboard.ErrNotFound) {
			return dashboard.ReportRun{Status: "skipped", Error: "the dashboard no longer exists"}
		}
		return dashboard.ReportRun{Status: "failed", Error: "cannot load the dashboard"}
	}
	if j.o.Mailer == nil {
		return dashboard.ReportRun{Status: "failed", Error: "SMTP is not configured (OPENLOG_SMTP_HOST)"}
	}
	allowed, err := dashboard.RecipientChecker(ctx, j.o.Store, func() (dashboard.Settings, error) { return j.o.Store.GetSettings(ctx, sr.OrgID) }, d)
	if err != nil {
		return dashboard.ReportRun{Status: "failed", Error: "cannot check the recipients"}
	}
	var recipients []string
	for _, r := range sr.Recipients {
		if allowed(strings.ToLower(r)) {
			recipients = append(recipients, r)
		}
	}
	if len(recipients) == 0 {
		return dashboard.ReportRun{Status: "skipped", Error: "no recipient is allowed any more (members or report domains)"}
	}

	content := j.Content(ctx, sr, d, at)
	subject, text, htmlBody := Render(content)
	sent := 0
	var lastErr error
	for _, to := range recipients {
		if err := j.o.Mailer.Send(ctx, auth.Mail{To: to, Subject: subject, Text: text, HTML: htmlBody}); err != nil {
			lastErr = err
			continue
		}
		sent++
	}
	run := dashboard.ReportRun{Status: "sent", Recipients: sent}
	switch {
	case sent == 0:
		run.Status, run.Error = "failed", "sending failed: "+lastErr.Error()
	case sent < len(recipients):
		run.Status, run.Error = "partial", "sending failed for some recipients: "+lastErr.Error()
	}
	return run
}

// Content runs the widget queries of a report for the period ending at at.
func (j *Job) Content(ctx context.Context, sr *dashboard.ScheduledReport, d *dashboard.Dashboard, at time.Time) Content {
	from := at.Add(-sr.RangeDuration())
	c := Content{Dashboard: d, Report: &sr.Report, From: from, To: at}
	if base := strings.TrimRight(j.o.PublicURL, "/"); base != "" {
		q := url.Values{"from": {strconv.FormatInt(from.UnixMilli(), 10)}, "to": {strconv.FormatInt(at.UnixMilli(), 10)}}
		c.Link = base + "/dashboards/" + url.PathEscape(d.ID) + "?" + q.Encode()
	}
	for _, p := range d.Pages {
		for _, w := range p.Widgets {
			if w.Visualization == "markdown" || strings.TrimSpace(w.Query) == "" {
				continue
			}
			if len(c.Widgets) == j.o.MaxWidgets {
				c.Skipped++
				continue
			}
			wr := WidgetResult{Widget: w, Page: p.Name}
			plan, err := oql.Compile(w.Query, oql.Options{Now: at, From: from, To: at, Variables: sr.Variables, MaxLimit: MaxRows * 5})
			if err != nil {
				wr.Err = oql.Describe(w.Query, err)
				c.Widgets = append(c.Widgets, wr)
				continue
			}
			qctx, cancel := context.WithTimeout(ctx, j.o.QueryTimeout)
			res, err := j.o.Run(qctx, sr.TenantID, plan)
			cancel()
			if err != nil {
				var oe *oql.Error
				if errors.As(err, &oe) {
					wr.Err = oe.Msg
				} else {
					wr.Err = "the query could not be run"
					j.o.Log.Warn("dashboard report query failed", "report_id", sr.ID, "widget_id", w.ID, "err", err)
				}
			}
			wr.Result = res
			c.Widgets = append(c.Widgets, wr)
		}
	}
	return c
}
