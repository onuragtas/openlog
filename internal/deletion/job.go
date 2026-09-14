package deletion

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
	mailtemplates "github.com/onuragtas/openlog/internal/mail/templates"
)

// ExportCleaner deletes export archives from storage (internal/dataexport).
type ExportCleaner interface {
	// DeleteOrgExports deletes the stored archives of the organization's exports.
	DeleteOrgExports(ctx context.Context, orgID string) error
	// DeleteObjects deletes archives by key.
	DeleteObjects(ctx context.Context, objs []ExportObject) error
}

// Step advances ClickHouse deletion of a tenant (Purger; a fake in tests).
type Stepper interface {
	Step(ctx context.Context, tenant string, prog *Progress) (bool, error)
}

// Job hard-deletes organizations whose grace period ended (api leader task).
type Job struct {
	Store     PGStore
	Purger    Stepper
	Exports   ExportCleaner // nil: exports disabled
	Mailer    auth.Mailer   // nil: no e-mails
	PublicURL string
	Interval  time.Duration // default 1m
	Log       *slog.Logger
	Now       func() time.Time
}

func (j *Job) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

func (j *Job) log() *slog.Logger {
	if j.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return j.Log
}

// RunOnce advances every due deletion by one step and returns how many completed.
func (j *Job) RunOnce(ctx context.Context) (int, error) {
	due, err := j.Store.Due(ctx, j.now(), 20)
	if err != nil {
		return 0, err
	}
	completed := 0
	for _, d := range due {
		ok, err := j.Store.Start(ctx, d.ID, j.now())
		if err != nil {
			return completed, err
		}
		if !ok {
			continue
		}
		prog := d.Progress
		done, err := j.Purger.Step(ctx, d.TenantID, &prog)
		msg := ""
		if err != nil {
			msg = err.Error()
			j.log().Warn("organization deletion step failed; retrying", "deletion_id", d.ID, "attempts", d.Attempts+1, "err", err)
		}
		if serr := j.Store.SaveProgress(ctx, d.ID, prog, msg); serr != nil {
			return completed, serr
		}
		if err != nil || !done {
			continue
		}
		if j.Exports != nil && d.OrgID != "" {
			if err := j.Exports.DeleteOrgExports(ctx, d.OrgID); err != nil {
				_ = j.Store.SaveProgress(ctx, d.ID, prog, "delete export archives: "+err.Error())
				continue
			}
		}
		res, err := j.Store.PurgeOrg(ctx, d, prog.CountsBefore, prog.Verified, j.now())
		if err != nil {
			_ = j.Store.SaveProgress(ctx, d.ID, prog, "postgres: "+err.Error())
			continue
		}
		if j.Exports != nil && len(res.ExportKeys) > 0 {
			if err := j.Exports.DeleteObjects(ctx, res.ExportKeys); err != nil {
				j.log().Warn("cannot delete personal export archives of deleted accounts", "err", err)
			}
		}
		completed++
		j.log().Info("organization deleted", "deletion_id", d.ID, "certificate_id", res.Certificate.ID, "clickhouse_tables", len(prog.CountsBefore))
		j.mail(ctx, res.Notify, mailtemplates.OrgDeletionData{Stage: mailtemplates.StageCompleted, OrgName: res.Notify.OrgName})
	}
	return completed, nil
}

func (j *Job) mail(ctx context.Context, n Notify, d mailtemplates.OrgDeletionData) {
	SendOrgDeletionMail(ctx, j.Mailer, j.log(), n, d)
}

// SendOrgDeletionMail e-mails every owner in n (best effort).
func SendOrgDeletionMail(ctx context.Context, m auth.Mailer, log *slog.Logger, n Notify, d mailtemplates.OrgDeletionData) {
	if m == nil {
		return
	}
	for _, o := range n.Owners {
		msg := mailtemplates.OrgDeletion(o.Locale, d)
		mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		if err := m.Send(mctx, auth.Mail{To: o.Email, Subject: msg.Subject, Text: msg.Text, HTML: msg.HTML}); err != nil && log != nil {
			log.Warn("cannot send organization deletion e-mail", "stage", d.Stage, "err", err)
		}
		cancel()
	}
}

// SettingsLink returns the web UI link of path, "" without OPENLOG_PUBLIC_URL.
func SettingsLink(publicURL, path string) string {
	if publicURL == "" {
		return ""
	}
	return strings.TrimRight(publicURL, "/") + path
}

// Run runs RunOnce every Interval until ctx is done.
func (j *Job) Run(ctx context.Context) {
	interval := j.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	for {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		if _, err := j.RunOnce(rctx); err != nil && ctx.Err() == nil {
			j.log().Warn("organization deletion run failed", "err", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
