package operator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/onuragtas/openlog/internal/auth"
	mailtemplates "github.com/onuragtas/openlog/internal/mail/templates"
	"github.com/onuragtas/openlog/internal/quota"
	"github.com/onuragtas/openlog/internal/store/postgres"
)

// Trials (SaaS mode, D-106). A trial assigns a plan with trial_days and records trial_ends_at; the api leader job
// e-mails owners OPENLOG_SAAS_TRIAL_NOTIFY_DAYS before the end and, once the end has passed, assigns the plan's
// trial fallback (unless an operator changed the plan meanwhile).

// SystemTrialActor is the audit actor of automatic trial changes.
var SystemTrialActor = Actor{Email: "system:trial"}

// assignPlanTx sets the organization's plan (keeping overrides and billing ids) and writes plan.update.
func assignPlanTx(ctx context.Context, tx pgx.Tx, orgID, planID, note string, a Actor) error {
	if _, err := tx.Exec(ctx, `INSERT INTO org_plans (org_id, plan_id, note, updated_by, updated_at) VALUES ($1::uuid, $2, $3, $4::uuid, now())
		ON CONFLICT (org_id) DO UPDATE SET plan_id = EXCLUDED.plan_id, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		orgID, planID, note, nullUUID(a.UserID)); err != nil {
		return err
	}
	return audit(ctx, tx, orgID, a, "plan.update", "organization", orgID, map[string]any{"plan_id": planID, "via": note})
}

// StartTrialWithPlan assigns planID and starts its trial ending at endsAt. Audit plan.update and trial.start.
func (s Store) StartTrialWithPlan(ctx context.Context, ref, planID string, endsAt time.Time, reason string, a Actor) (Lifecycle, error) {
	id, err := s.resolveOrg(ctx, ref)
	if err != nil {
		return Lifecycle{}, err
	}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		if err := assignPlanTx(ctx, tx, id, planID, "trial", a); err != nil {
			return err
		}
		return s.StartTrial(ctx, tx, id, planID, endsAt, reason, a)
	})
	if err != nil {
		return Lifecycle{}, err
	}
	return s.GetLifecycle(ctx, id)
}

// TrialJobStore is the persistence of TrialJob beyond Store (quota.PGStore owners).
type TrialOwners interface {
	Owners(ctx context.Context, orgID string) ([]quota.Owner, error)
}

// TrialJob is the api leader job of trials.
type TrialJob struct {
	Store   Store
	Owners  TrialOwners
	Catalog *quota.Catalog
	// Mailer is nil without SMTP (trials still end).
	Mailer          auth.Mailer
	PublicURL       string
	NotifyDays      []int // descending
	SignupTrialPlan string
	Interval        time.Duration
	Log             *slog.Logger
	Now             func() time.Time
}

const signupTrialSinceKey = "saas.signup_trial_since"

type trialRow struct {
	orgID, tenantID, name, planID string
	endsAt                        time.Time
}

func (j *TrialJob) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

func (j *TrialJob) log() *slog.Logger {
	if j.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return j.Log
}

// RunOnce starts sign-up trials, sends trial ending e-mails and ends expired trials.
func (j *TrialJob) RunOnce(ctx context.Context) error {
	var errs []error
	if err := j.startSignupTrials(ctx); err != nil {
		errs = append(errs, fmt.Errorf("sign-up trials: %w", err))
	}
	if err := j.notify(ctx); err != nil {
		errs = append(errs, fmt.Errorf("trial notifications: %w", err))
	}
	if err := j.endTrials(ctx); err != nil {
		errs = append(errs, fmt.Errorf("end trials: %w", err))
	}
	return errors.Join(errs...)
}

// Run runs every Interval until ctx is done (leader task).
func (j *TrialJob) Run(ctx context.Context) {
	runEvery(ctx, j.Interval, j.Log, "trial lifecycle", j.RunOnce)
}

// startSignupTrials gives organizations created after the feature was first enabled, without a plan assignment and
// without any trial, a trial of SignupTrialPlan.
func (j *TrialJob) startSignupTrials(ctx context.Context) error {
	if j.SignupTrialPlan == "" {
		return nil
	}
	plan, ok := j.Catalog.Plan(j.SignupTrialPlan)
	if !ok || plan.TrialDays <= 0 {
		return fmt.Errorf("OPENLOG_SAAS_SIGNUP_TRIAL_PLAN %q is not a plan with trial_days", j.SignupTrialPlan)
	}
	var since time.Time
	_, found, err := postgres.GetSystemState(ctx, j.Store.Pool, signupTrialSinceKey, &since)
	if err != nil {
		return err
	}
	if !found {
		since = j.now().UTC()
		return postgres.PutSystemState(ctx, j.Store.Pool, signupTrialSinceKey, since)
	}
	rows, err := j.Store.Pool.Query(ctx, `SELECT o.id::text FROM organizations o
		WHERE o.created_at >= $1 AND NOT EXISTS (SELECT 1 FROM org_plans p WHERE p.org_id = o.id)
		AND NOT EXISTS (SELECT 1 FROM org_saas_state st WHERE st.org_id = o.id AND st.trial_started_at IS NOT NULL) LIMIT 500`, since)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range ids {
		ends := j.now().Add(time.Duration(plan.TrialDays) * 24 * time.Hour)
		if _, err := j.Store.StartTrialWithPlan(ctx, id, plan.ID, ends, "sign-up trial", SystemTrialActor); err != nil {
			return err
		}
		j.log().Info("sign-up trial started", "org_id", id, "plan_id", plan.ID, "ends_at", ends)
	}
	return nil
}

func (j *TrialJob) activeTrials(ctx context.Context, cond string, args ...any) ([]trialRow, error) {
	rows, err := j.Store.Pool.Query(ctx, `SELECT o.id::text, o.tenant_id, o.name, st.trial_plan_id, st.trial_ends_at FROM org_saas_state st
		JOIN organizations o ON o.id = st.org_id WHERE st.trial_ends_at IS NOT NULL AND st.trial_ended_at IS NULL AND `+cond+` LIMIT 1000`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []trialRow
	for rows.Next() {
		var r trialRow
		if err := rows.Scan(&r.orgID, &r.tenantID, &r.name, &r.planID, &r.endsAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// daysLeft rounds the remaining time up to whole days.
func daysLeft(ends, now time.Time) int {
	return int(math.Ceil(ends.Sub(now).Hours() / 24))
}

func (j *TrialJob) notify(ctx context.Context) error {
	if len(j.NotifyDays) == 0 {
		return nil
	}
	now := j.now()
	trials, err := j.activeTrials(ctx, `st.trial_ends_at > $1 AND st.trial_ends_at <= $2`, now, now.Add(time.Duration(j.NotifyDays[0])*24*time.Hour))
	if err != nil {
		return err
	}
	for _, t := range trials {
		left := daysLeft(t.endsAt, now)
		// The most urgent step reached is mailed; earlier (larger) steps are claimed silently.
		step := 0
		for _, d := range j.NotifyDays {
			if left <= d {
				step = d
			}
		}
		if step == 0 {
			continue
		}
		for _, d := range j.NotifyDays {
			if d > step {
				if _, err := j.claim(ctx, t, d); err != nil {
					return err
				}
			}
		}
		if err := j.send(ctx, t, step, left); err != nil {
			return err
		}
	}
	return nil
}

func (j *TrialJob) claim(ctx context.Context, t trialRow, days int) (bool, error) {
	tag, err := j.Store.Pool.Exec(ctx, `INSERT INTO trial_notifications (org_id, trial_ends_at, days_left) VALUES ($1::uuid, $2, $3) ON CONFLICT DO NOTHING`,
		t.orgID, t.endsAt, days)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// send claims (t, step) and e-mails the owners; the claim is released when nothing could be sent.
func (j *TrialJob) send(ctx context.Context, t trialRow, step, left int) error {
	if j.Mailer == nil {
		return nil
	}
	claimed, err := j.claim(ctx, t, step)
	if err != nil || !claimed {
		return err
	}
	owners, err := j.Owners.Owners(ctx, t.orgID)
	if err != nil {
		_, _ = j.Store.Pool.Exec(ctx, `DELETE FROM trial_notifications WHERE org_id = $1::uuid AND trial_ends_at = $2 AND days_left = $3`, t.orgID, t.endsAt, step)
		return err
	}
	plan := j.Catalog.Resolve(t.planID)
	fallback := j.Catalog.Resolve(j.Catalog.TrialFallback(plan))
	sent := 0
	for _, o := range owners {
		d := mailtemplates.TrialData{OrgName: t.name, TenantID: t.tenantID, PlanName: plan.Name, FallbackPlanName: fallback.Name,
			DaysLeft: max(left, 0), EndsAt: t.endsAt, Ended: step == 0}
		if j.PublicURL != "" {
			d.Link = strings.TrimRight(j.PublicURL, "/") + "/settings/usage"
		}
		msg := mailtemplates.Trial(o.Locale, d)
		if err := j.Mailer.Send(ctx, auth.Mail{To: o.Email, Subject: msg.Subject, Text: msg.Text, HTML: msg.HTML}); err != nil {
			j.log().Warn("trial e-mail failed", "org_id", t.orgID, "err", err)
			continue
		}
		sent++
	}
	if sent == 0 && len(owners) > 0 {
		_, err := j.Store.Pool.Exec(ctx, `DELETE FROM trial_notifications WHERE org_id = $1::uuid AND trial_ends_at = $2 AND days_left = $3`, t.orgID, t.endsAt, step)
		return err
	}
	_, err = j.Store.Pool.Exec(ctx, `UPDATE trial_notifications SET recipients = $4, sent_at = now() WHERE org_id = $1::uuid AND trial_ends_at = $2 AND days_left = $3`,
		t.orgID, t.endsAt, step, sent)
	return err
}

func (j *TrialJob) endTrials(ctx context.Context) error {
	now := j.now()
	trials, err := j.activeTrials(ctx, `st.trial_ends_at <= $1`, now)
	if err != nil {
		return err
	}
	for _, t := range trials {
		plan := j.Catalog.Resolve(t.planID)
		fallback := j.Catalog.TrialFallback(plan)
		if fallback == t.planID {
			fallback = j.Catalog.Default
		}
		var moved bool
		err := j.Store.inTx(ctx, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE org_saas_state SET trial_ended_at = now(), updated_at = now()
				WHERE org_id = $1::uuid AND trial_ended_at IS NULL AND trial_ends_at <= $2`, t.orgID, now)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			var current string
			if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT plan_id FROM org_plans WHERE org_id = $1::uuid), '')`, t.orgID).Scan(&current); err != nil {
				return err
			}
			// An operator who assigned another plan during the trial wins.
			if current == t.planID || current == "" {
				if err := assignPlanTx(ctx, tx, t.orgID, fallback, "trial ended", SystemTrialActor); err != nil {
					return err
				}
				moved = true
			}
			return audit(ctx, tx, t.orgID, SystemTrialActor, "trial.end", "organization", t.orgID,
				map[string]any{"plan_id": t.planID, "fallback_plan_id": fallback, "plan_changed": moved})
		})
		if err != nil {
			return err
		}
		j.log().Info("trial ended", "org_id", t.orgID, "plan_id", t.planID, "fallback_plan_id", fallback, "plan_changed", moved)
		if err := j.send(ctx, t, 0, 0); err != nil {
			j.log().Warn("trial ended e-mail failed", "org_id", t.orgID, "err", err)
		}
	}
	return nil
}
