package deletion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	mailtemplates "github.com/onuragtas/openlog/internal/mail/templates"
)

// PGStore is the PostgreSQL side of deletions (migrations/postgres/0070_data_subject_requests.sql).
type PGStore struct {
	Pool *pgxpool.Pool
}

func inTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

const deletionCols = `d.id::text, coalesce(d.org_id::text, ''), d.tenant_id, coalesce(o.name, ''), d.status, d.initiator, d.reason,
	coalesce(d.requested_by::text, ''), d.requested_by_email, d.requested_at, d.purge_after, d.cancelled_at, d.started_at, d.completed_at,
	d.progress, d.attempts, d.last_error, coalesce(d.certificate_id::text, '')`

const deletionFrom = ` FROM org_deletions d LEFT JOIN organizations o ON o.id = d.org_id`

func scanDeletion(row pgx.Row) (OrgDeletion, error) {
	var d OrgDeletion
	var progress []byte
	err := row.Scan(&d.ID, &d.OrgID, &d.TenantID, &d.OrgName, &d.Status, &d.Initiator, &d.Reason, &d.RequestedBy, &d.RequestedByEmail,
		&d.RequestedAt, &d.PurgeAfter, &d.CancelledAt, &d.StartedAt, &d.CompletedAt, &progress, &d.Attempts, &d.LastError, &d.CertificateID)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, err
	}
	_ = json.Unmarshal(progress, &d.Progress)
	return d, nil
}

func collectDeletions(rows pgx.Rows, err error) ([]OrgDeletion, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (OrgDeletion, error) { return scanDeletion(r) })
}

// owners returns the organization's owners with their e-mail language (user choice, organization default, request
// language; D-095).
func owners(ctx context.Context, q pgx.Tx, orgID string) ([]Recipient, error) {
	rows, err := q.Query(ctx, `SELECT u.email, CASE WHEN u.locale_explicit THEN u.locale ELSE '' END, o.locale, u.locale
		FROM memberships m JOIN users u ON u.id = m.user_id JOIN organizations o ON o.id = m.org_id
		WHERE m.org_id = $1 AND m.role = 'owner' ORDER BY u.email`, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Recipient, error) {
		var email, pref, orgLocale, reqLocale string
		err := r.Scan(&email, &pref, &orgLocale, &reqLocale)
		return Recipient{Email: email, Locale: mailtemplates.Resolve(pref, orgLocale, reqLocale)}, err
	})
}

// ScheduleInput schedules an organization deletion.
type ScheduleInput struct {
	OrgID       string
	Initiator   string // owner | operator
	Reason      string
	ActorUserID string
	ActorEmail  string
	Grace       time.Duration
	Now         time.Time
}

// ScheduleOrg soft-deletes the organization: deleted_at is set (members lose access, API keys stop authenticating),
// active license keys and SCIM tokens are revoked and remembered for a cancellation. Returns the deletion and the owners
// to notify. ErrNotFound, ErrAlreadyScheduled.
func (s PGStore) ScheduleOrg(ctx context.Context, in ScheduleInput) (OrgDeletion, Notify, error) {
	var (
		d      OrgDeletion
		notify Notify
	)
	err := inTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var tenantID, name string
		var deletedAt *time.Time
		err := tx.QueryRow(ctx, `SELECT tenant_id, name, deleted_at FROM organizations WHERE id::text = $1 FOR UPDATE`, in.OrgID).Scan(&tenantID, &name, &deletedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if deletedAt != nil {
			return ErrAlreadyScheduled
		}
		if notify.Owners, err = owners(ctx, tx, in.OrgID); err != nil {
			return err
		}
		notify.OrgName = name
		if _, err := tx.Exec(ctx, `UPDATE organizations SET deleted_at = $2, updated_at = now() WHERE id = $1`, in.OrgID, in.Now); err != nil {
			return err
		}
		var actor *string
		if in.ActorUserID != "" {
			actor = &in.ActorUserID
		}
		rows, err := tx.Query(ctx, `UPDATE license_keys SET revoked_at = $2, revoked_by = $3::uuid WHERE org_id = $1 AND revoked_at IS NULL RETURNING id::text`,
			in.OrgID, in.Now, actor)
		if err != nil {
			return err
		}
		keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `UPDATE scim_tokens SET revoked_at = $2 WHERE org_id = $1 AND revoked_at IS NULL RETURNING id::text`, in.OrgID, in.Now)
		if err != nil {
			return err
		}
		tokens, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		nb, _ := json.Marshal(notify)
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO org_deletions (org_id, tenant_id, status, initiator, reason, requested_by, requested_by_email, notify,
				revoked_license_key_ids, revoked_scim_token_ids, requested_at, purge_after)
			VALUES ($1, $2, 'scheduled', $3, $4, $5::uuid, $6, $7::jsonb, $8::uuid[], $9::uuid[], $10, $11) RETURNING id::text`,
			in.OrgID, tenantID, in.Initiator, in.Reason, actor, in.ActorEmail, string(nb), keys, tokens, in.Now, in.Now.Add(in.Grace)).Scan(&id); err != nil {
			return err
		}
		d, err = scanDeletion(tx.QueryRow(ctx, `SELECT `+deletionCols+deletionFrom+` WHERE d.id = $1`, id))
		return err
	})
	return d, notify, err
}

// CancelOrg cancels a scheduled deletion: the organization becomes accessible again and the keys revoked by the
// scheduling work again. ErrNotFound, ErrNotCancellable (already running or finished).
func (s PGStore) CancelOrg(ctx context.Context, id string, now time.Time) (OrgDeletion, Notify, error) {
	var (
		d      OrgDeletion
		notify Notify
	)
	err := inTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var status, orgID string
		var keys, tokens []string
		err := tx.QueryRow(ctx, `SELECT status, coalesce(org_id::text, ''), revoked_license_key_ids::text[], revoked_scim_token_ids::text[]
			FROM org_deletions WHERE id::text = $1 FOR UPDATE`, id).Scan(&status, &orgID, &keys, &tokens)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status != StatusScheduled || orgID == "" {
			return ErrNotCancellable
		}
		if _, err := tx.Exec(ctx, `UPDATE organizations SET deleted_at = NULL, updated_at = now() WHERE id = $1`, orgID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE license_keys SET revoked_at = NULL, revoked_by = NULL WHERE org_id = $1 AND id = ANY($2::uuid[])`, orgID, keys); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE scim_tokens SET revoked_at = NULL WHERE org_id = $1 AND id = ANY($2::uuid[])`, orgID, tokens); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE org_deletions SET status = 'cancelled', cancelled_at = $2, notify = '{}'::jsonb WHERE id = $1`, id, now); err != nil {
			return err
		}
		if notify.Owners, err = owners(ctx, tx, orgID); err != nil {
			return err
		}
		d, err = scanDeletion(tx.QueryRow(ctx, `SELECT `+deletionCols+deletionFrom+` WHERE d.id = $1`, id))
		notify.OrgName = d.OrgName
		return err
	})
	return d, notify, err
}

// Get returns one deletion.
func (s PGStore) Get(ctx context.Context, id string) (OrgDeletion, error) {
	return scanDeletion(s.Pool.QueryRow(ctx, `SELECT `+deletionCols+deletionFrom+` WHERE d.id::text = $1`, id))
}

// PendingForOwner lists the scheduled deletions of organizations where userID is an owner (Settings → Profile).
func (s PGStore) PendingForOwner(ctx context.Context, userID string) ([]OrgDeletion, error) {
	return collectDeletions(s.Pool.Query(ctx, `SELECT `+deletionCols+deletionFrom+`
		WHERE d.status IN ('scheduled', 'deleting') AND EXISTS (
			SELECT 1 FROM memberships m WHERE m.org_id = d.org_id AND m.user_id::text = $1 AND m.role = 'owner')
		ORDER BY d.purge_after`, userID))
}

// List lists deletions, newest first (operators).
func (s PGStore) List(ctx context.Context, limit int) ([]OrgDeletion, error) {
	return collectDeletions(s.Pool.Query(ctx, `SELECT `+deletionCols+deletionFrom+` ORDER BY d.requested_at DESC LIMIT $1`, limit))
}

// FindOrg resolves an organization id or tenant id: id, tenant id, name and whether it is scheduled for deletion.
func (s PGStore) FindOrg(ctx context.Context, ref string) (id, tenantID, name string, deleted bool, err error) {
	var deletedAt *time.Time
	err = s.Pool.QueryRow(ctx, `SELECT id::text, tenant_id, name, deleted_at FROM organizations WHERE id::text = $1 OR tenant_id = $1 LIMIT 1`, ref).
		Scan(&id, &tenantID, &name, &deletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", false, ErrNotFound
	}
	return id, tenantID, name, deletedAt != nil, err
}

// Due returns deletions whose grace period ended (scheduled, or deleting and not finished).
func (s PGStore) Due(ctx context.Context, now time.Time, limit int) ([]OrgDeletion, error) {
	return collectDeletions(s.Pool.Query(ctx, `SELECT `+deletionCols+deletionFrom+`
		WHERE d.status IN ('scheduled', 'deleting') AND d.purge_after <= $1 ORDER BY d.purge_after LIMIT $2`, now, limit))
}

// Start moves a due deletion to deleting (no cancellation from now on); false when it was cancelled meanwhile.
func (s PGStore) Start(ctx context.Context, id string, now time.Time) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE org_deletions SET status = 'deleting', started_at = coalesce(started_at, $2)
		WHERE id = $1 AND status IN ('scheduled', 'deleting') AND purge_after <= $2`, id, now)
	return tag.RowsAffected() == 1, err
}

// SaveProgress stores the ClickHouse progress and the last error ("" = none; a non-empty one counts an attempt).
func (s PGStore) SaveProgress(ctx context.Context, id string, p Progress, lastErr string) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if len(lastErr) > 1000 {
		lastErr = lastErr[:1000]
	}
	_, err = s.Pool.Exec(ctx, `UPDATE org_deletions SET progress = $2::jsonb, last_error = $3,
		attempts = attempts + CASE WHEN $3 = '' THEN 0 ELSE 1 END WHERE id = $1`, id, string(b), lastErr)
	return err
}

// PurgeResult is the outcome of the PostgreSQL part of an organization deletion.
type PurgeResult struct {
	Certificate Certificate
	Notify      Notify
	// ExportKeys are object keys of personal exports of deleted orphan accounts (the organization's own exports are
	// deleted by the job before).
	ExportKeys []ExportObject
}

// ExportObject is a stored export archive.
type ExportObject struct {
	Storage string
	Key     string
}

// PurgeOrg deletes the organization's PostgreSQL data in one transaction: every row with its org_id (most through
// ON DELETE CASCADE), accounts that belong to no other organization afterwards (DeleteUser semantics), then records
// the certificate with the ClickHouse counts and completes the deletion, clearing its personal fields.
func (s PGStore) PurgeOrg(ctx context.Context, d OrgDeletion, chRows map[string]uint64, verified bool, now time.Time) (PurgeResult, error) {
	var res PurgeResult
	err := inTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var notifyRaw []byte
		var startedAt *time.Time
		err := tx.QueryRow(ctx, `SELECT notify, started_at FROM org_deletions WHERE id = $1 AND status = 'deleting' FOR UPDATE`, d.ID).Scan(&notifyRaw, &startedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		_ = json.Unmarshal(notifyRaw, &res.Notify)
		counts := map[string]int64{}
		if d.OrgID != "" {
			if counts, res.ExportKeys, err = purgeOrgRows(ctx, tx, d.OrgID, d.TenantID, now); err != nil {
				return err
			}
		}
		started := now
		if startedAt != nil {
			started = *startedAt
		}
		if chRows == nil {
			chRows = map[string]uint64{}
		}
		grace := d.PurgeAfter
		res.Certificate = Certificate{SubjectType: "organization", SubjectHash: SubjectHash(d.TenantID), Initiator: d.Initiator,
			RequestedAt: d.RequestedAt, GraceEndedAt: &grace, StartedAt: started, CompletedAt: now, PostgresRows: counts, ClickHouseRows: chRows, Verified: verified}
		if res.Certificate.ID, err = insertCertificate(ctx, tx, res.Certificate); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE org_deletions SET status = 'completed', completed_at = $2, certificate_id = $3::uuid,
			requested_by = NULL, requested_by_email = '', reason = '', notify = '{}'::jsonb, last_error = '' WHERE id = $1`, d.ID, now, res.Certificate.ID)
		return err
	})
	return res, err
}

func purgeOrgRows(ctx context.Context, tx pgx.Tx, orgID, tenantID string, now time.Time) (map[string]int64, []ExportObject, error) {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM organizations WHERE id = $1 FOR UPDATE`, orgID); err != nil {
		return nil, nil, err
	}
	rows, err := tx.Query(ctx, `SELECT c.table_name FROM information_schema.columns c
		JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name AND t.table_type = 'BASE TABLE'
		WHERE c.table_schema = current_schema() AND c.column_name = 'org_id' AND c.table_name <> 'org_deletions' ORDER BY 1`)
	if err != nil {
		return nil, nil, err
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, nil, err
	}
	counts := map[string]int64{}
	for _, t := range tables {
		var n int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{t}.Sanitize()+` WHERE org_id::text = $1`, orgID).Scan(&n); err != nil {
			return nil, nil, fmt.Errorf("count %s: %w", t, err)
		}
		if n > 0 {
			counts[t] = n
		}
	}
	rows, err = tx.Query(ctx, `SELECT m.user_id::text FROM memberships m WHERE m.org_id = $1
		AND NOT EXISTS (SELECT 1 FROM memberships x WHERE x.user_id = m.user_id AND x.org_id <> $1)`, orgID)
	if err != nil {
		return nil, nil, err
	}
	orphans, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, nil, err
	}
	// update_requests keeps its rows on organization deletion (ON DELETE SET NULL); this organization's go with it.
	if _, err := tx.Exec(ctx, `DELETE FROM update_requests WHERE org_id = $1`, orgID); err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE usage_retention_mutations SET tenants = array_remove(tenants, $1) WHERE $1 = ANY (tenants)`, tenantID); err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, orgID); err != nil {
		return nil, nil, err
	}
	var exports []ExportObject
	for _, uid := range orphans {
		r, err := deleteUserTx(ctx, tx, uid, now)
		if err != nil {
			return nil, nil, fmt.Errorf("delete account without organization: %w", err)
		}
		exports = append(exports, r.Exports...)
	}
	if len(orphans) > 0 {
		counts["users_deleted"] = int64(len(orphans))
	}
	return counts, exports, nil
}

func insertCertificate(ctx context.Context, tx pgx.Tx, c Certificate) (string, error) {
	pg, _ := json.Marshal(c.PostgresRows)
	chr, _ := json.Marshal(c.ClickHouseRows)
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO deletion_certificates (subject_type, subject_hash, initiator, requested_at, grace_ended_at, started_at,
			completed_at, postgres_rows, clickhouse_rows, verified)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9::jsonb, $10) RETURNING id::text`,
		c.SubjectType, c.SubjectHash, c.Initiator, c.RequestedAt, c.GraceEndedAt, c.StartedAt, c.CompletedAt, string(pg), string(chr), c.Verified).Scan(&id)
	return id, err
}

// ListCertificates lists deletion certificates, newest first (operators).
func (s PGStore) ListCertificates(ctx context.Context, subjectHash string, limit int) ([]Certificate, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id::text, subject_type, subject_hash, initiator, requested_at, grace_ended_at, started_at, completed_at,
			postgres_rows, clickhouse_rows, verified
		FROM deletion_certificates WHERE ($1 = '' OR subject_hash = $1) ORDER BY completed_at DESC LIMIT $2`, subjectHash, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Certificate, error) {
		var c Certificate
		var pg, chr []byte
		err := r.Scan(&c.ID, &c.SubjectType, &c.SubjectHash, &c.Initiator, &c.RequestedAt, &c.GraceEndedAt, &c.StartedAt, &c.CompletedAt, &pg, &chr, &c.Verified)
		_ = json.Unmarshal(pg, &c.PostgresRows)
		_ = json.Unmarshal(chr, &c.ClickHouseRows)
		return c, err
	})
}

// UserDeletion is the outcome of DeleteUser.
type UserDeletion struct {
	Email       string
	Locale      string
	Pseudonym   string
	Rows        map[string]int64
	Exports     []ExportObject // personal export archives to delete from storage
	Certificate Certificate
}

// DeleteUser deletes an account (self-service or operator). *SoleOwnerError when the user is the only owner of an
// organization that is not scheduled for deletion; ErrNotFound.
func (s PGStore) DeleteUser(ctx context.Context, userID, initiator string, now time.Time) (UserDeletion, error) {
	var res UserDeletion
	err := inTx(ctx, s.Pool, func(tx pgx.Tx) error {
		// Lock the owner memberships of every organization the user owns, so no concurrent demotion leaves one ownerless.
		rows, err := tx.Query(ctx, `SELECT m.org_id::text, m.user_id::text, o.name, o.deleted_at IS NOT NULL
			FROM memberships m JOIN organizations o ON o.id = m.org_id
			WHERE m.role = 'owner' AND m.org_id IN (SELECT org_id FROM memberships WHERE user_id::text = $1 AND role = 'owner')
			ORDER BY o.name FOR UPDATE OF m`, userID)
		if err != nil {
			return err
		}
		type ownerRow struct {
			org, user, name string
			deleted         bool
		}
		ors, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ownerRow, error) {
			var o ownerRow
			return o, r.Scan(&o.org, &o.user, &o.name, &o.deleted)
		})
		if err != nil {
			return err
		}
		others := map[string]int{}
		names := map[string]OrgRef{}
		for _, o := range ors {
			names[o.org] = OrgRef{ID: o.org, Name: o.name}
			if o.user != userID {
				others[o.org]++
			} else if o.deleted {
				others[o.org] += 1 << 20 // scheduled for deletion: no owner needed
			}
		}
		var sole []OrgRef
		for id, ref := range names {
			if others[id] == 0 {
				sole = append(sole, ref)
			}
		}
		if len(sole) > 0 {
			return &SoleOwnerError{Orgs: sole}
		}
		res, err = deleteUserTx(ctx, tx, userID, now)
		if err != nil {
			return err
		}
		res.Certificate = Certificate{SubjectType: "user", SubjectHash: SubjectHash(userID), Initiator: initiator, RequestedAt: now, StartedAt: now,
			CompletedAt: now, PostgresRows: res.Rows, ClickHouseRows: map[string]uint64{}, Verified: true}
		res.Certificate.ID, err = insertCertificate(ctx, tx, res.Certificate)
		return err
	})
	return res, err
}

// deleteUserTx removes the account and pseudonymizes what other records keep about it.
func deleteUserTx(ctx context.Context, tx pgx.Tx, userID string, now time.Time) (UserDeletion, error) {
	res := UserDeletion{Pseudonym: NewPseudonym(), Rows: map[string]int64{}}
	var pref, reqLocale string
	var explicit bool
	err := tx.QueryRow(ctx, `SELECT email, locale, locale_explicit FROM users WHERE id::text = $1 FOR UPDATE`, userID).Scan(&res.Email, &reqLocale, &explicit)
	if errors.Is(err, pgx.ErrNoRows) {
		return res, ErrNotFound
	}
	if err != nil {
		return res, err
	}
	if explicit {
		pref = reqLocale
	}
	res.Locale = mailtemplates.Resolve(pref, reqLocale)
	email, p := res.Email, res.Pseudonym
	exec := func(key, sql string, args ...any) error {
		tag, err := tx.Exec(ctx, sql, args...)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		if n := tag.RowsAffected(); n > 0 {
			res.Rows[key] += n
		}
		return nil
	}
	steps := []struct {
		key, sql string
		args     []any
	}{
		{"audit_log_actor", `UPDATE audit_log SET actor_email = $1, actor_user_id = NULL, ip = ''
			WHERE actor_user_id::text = $2 OR (actor_email <> '' AND lower(actor_email) = $3)`, []any{p, userID, email}},
		{"audit_log_target", `UPDATE audit_log SET target_id = $1 WHERE target_type = 'user' AND target_id = $2`, []any{p, userID}},
		{"alert_incident_events", `UPDATE alert_incident_events SET actor_email = $1, actor_user_id = NULL
			WHERE actor_user_id::text = $2 OR (actor_email <> '' AND lower(actor_email) = $3)`, []any{p, userID, email}},
		{"apm_error_group_comments", `UPDATE apm_error_group_comments SET author_email = $1, author_user_id = NULL
			WHERE author_user_id::text = $2 OR (author_email <> '' AND lower(author_email) = $3)`, []any{p, userID, email}},
		{"update_requests", `UPDATE update_requests SET requested_by_email = $1, requested_by = NULL
			WHERE requested_by::text = $2 OR (requested_by_email <> '' AND lower(requested_by_email) = $3)`, []any{p, userID, email}},
		{"org_deletions", `UPDATE org_deletions SET requested_by_email = $1, requested_by = NULL WHERE requested_by::text = $2`, []any{p, userID}},
		{"invitations", `DELETE FROM invitations WHERE email = $1`, []any{email}},
		{"sso_domains", `UPDATE sso_domains SET email_address = '', email_token_hash = NULL, email_expires_at = NULL WHERE lower(email_address) = $1`, []any{email}},
		{"dashboard_report_recipients", `UPDATE dashboard_reports SET recipients = array_remove(recipients, $1),
			enabled = enabled AND cardinality(array_remove(recipients, $1)) > 0, updated_at = now() WHERE $1 = ANY (recipients)`, []any{email}},
		{"api_keys", `DELETE FROM api_keys WHERE created_by::text = $1`, []any{userID}},
	}
	for _, st := range steps {
		if err := exec(st.key, st.sql, st.args...); err != nil {
			return res, err
		}
	}
	// Details are JSON documents written by many features; replace the address and id wherever they appear as text.
	if !strings.ContainsAny(email, `"\`) {
		if err := exec("audit_log_details", `UPDATE audit_log SET details = replace(replace(details::text, $1, $3), $2, $3)::jsonb
			WHERE strpos(details::text, $1) > 0 OR strpos(details::text, $2) > 0`, email, userID, p); err != nil {
			return res, err
		}
	}
	rows, err := tx.Query(ctx, `DELETE FROM data_exports WHERE kind = 'user' AND user_id::text = $1 AND object_key <> '' RETURNING storage, object_key`, userID)
	if err != nil {
		return res, err
	}
	res.Exports, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (ExportObject, error) {
		var o ExportObject
		return o, r.Scan(&o.Storage, &o.Key)
	})
	if err != nil {
		return res, err
	}
	if err := exec("data_exports", `DELETE FROM data_exports WHERE kind = 'user' AND user_id::text = $1`, userID); err != nil {
		return res, err
	}
	if len(res.Exports) > 0 {
		res.Rows["data_exports"] += int64(len(res.Exports))
	}
	for key, sql := range map[string]string{
		"memberships": `SELECT count(*) FROM memberships WHERE user_id::text = $1`,
		"sessions":    `SELECT count(*) FROM sessions WHERE user_id::text = $1`,
		"scim_users":  `SELECT count(*) FROM scim_users WHERE user_id::text = $1`,
	} {
		var n int64
		if err := tx.QueryRow(ctx, sql, userID).Scan(&n); err != nil {
			return res, err
		}
		if n > 0 {
			res.Rows[key] = n
		}
	}
	// ON DELETE CASCADE: memberships, sessions, sso_sessions, sso_login_states, email_verifications, scim_users, scim_group_members.
	if err := exec("users", `DELETE FROM users WHERE id::text = $1`, userID); err != nil {
		return res, err
	}
	_ = now
	return res, nil
}
