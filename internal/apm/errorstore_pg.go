package apm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGErrorStates is the PostgreSQL ErrorStateStore (migrations/postgres/0025_apm_error_workflow.sql).
type PGErrorStates struct {
	Pool *pgxpool.Pool
}

var _ ErrorStateStore = PGErrorStates{}

const stateColumns = `st.group_id, st.service_name, st.service_namespace, st.deployment_environment, st.status,
	COALESCE(st.assignee_user_id::text, ''), COALESCE(au.email, ''), COALESCE(au.name, ''),
	st.resolved_at, st.resolved_in_version, COALESCE(ru.email, ''), st.regressed_at, st.regression_count,
	(SELECT count(*) FROM apm_error_group_comments c WHERE c.org_id = st.org_id AND c.group_id = st.group_id),
	st.updated_at, COALESCE(uu.email, '')`

const stateFrom = `FROM apm_error_group_states st
	LEFT JOIN users au ON au.id = st.assignee_user_id
	LEFT JOIN users ru ON ru.id = st.resolved_by
	LEFT JOIN users uu ON uu.id = st.updated_by`

func scanState(row pgx.Row) (ErrorGroupState, error) {
	var st ErrorGroupState
	var gid, status string
	if err := row.Scan(&gid, &st.Key.Name, &st.Key.Namespace, &st.Key.Environment, &status, &st.AssigneeUserID, &st.AssigneeEmail,
		&st.AssigneeName, &st.ResolvedAt, &st.ResolvedInVersion, &st.ResolvedByEmail, &st.RegressedAt, &st.RegressionCount,
		&st.CommentCount, &st.UpdatedAt, &st.UpdatedByEmail); err != nil {
		return st, err
	}
	id, ok := ParseGroupID(gid)
	if !ok {
		return st, fmt.Errorf("invalid stored group id %q", gid)
	}
	st.GroupID, st.Status = id, ErrorStatus(status)
	return st, nil
}

func groupIDStrings(ids []uint64) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = GroupIDString(id)
	}
	return out
}

// States implements ErrorStateStore.
func (s PGErrorStates) States(ctx context.Context, orgID string, f ErrorStateFilter) ([]ErrorGroupState, error) {
	where := []string{"st.org_id = $1"}
	args := []any{orgID}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if f.GroupIDs != nil {
		where = append(where, "st.group_id = ANY("+arg(groupIDStrings(f.GroupIDs))+"::text[])")
	}
	if len(f.Statuses) > 0 {
		ss := make([]string, len(f.Statuses))
		for i, v := range f.Statuses {
			ss[i] = string(v)
		}
		where = append(where, "st.status = ANY("+arg(ss)+"::text[])")
	}
	if f.Assignee != nil {
		if *f.Assignee == "" {
			where = append(where, "st.assignee_user_id IS NULL")
		} else {
			where = append(where, "st.assignee_user_id = "+arg(*f.Assignee)+"::uuid")
		}
	}
	if f.Service != "" {
		where = append(where, "st.service_name = "+arg(f.Service))
	}
	if f.Namespace != nil {
		where = append(where, "st.service_namespace = "+arg(*f.Namespace))
	}
	if f.Environment != nil {
		where = append(where, "st.deployment_environment = "+arg(*f.Environment))
	}
	if f.RegressedSince != nil {
		where = append(where, "st.regressed_at >= "+arg(*f.RegressedSince))
	}
	limit := f.Limit
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	rows, err := s.Pool.Query(ctx, "SELECT "+stateColumns+" "+stateFrom+" WHERE "+strings.Join(where, " AND ")+
		" ORDER BY st.updated_at DESC, st.group_id LIMIT "+arg(limit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ErrorGroupState{}
	for rows.Next() {
		st, err := scanState(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func uuidOrNil(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// Update implements ErrorStateStore.
func (s PGErrorStates) Update(ctx context.Context, orgID string, refs []ErrorGroupRef, p ErrorGroupPatch, actor Actor) ([]ErrorGroupState, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if len(refs) == 0 || len(refs) > MaxBulkGroups {
		return nil, fmt.Errorf("%w: 1-%d groups", ErrInvalidErrorPatch, MaxBulkGroups)
	}
	var out []ErrorGroupState
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if p.AssigneeUserID != nil && *p.AssigneeUserID != "" {
			var one int
			err := tx.QueryRow(ctx, `SELECT 1 FROM memberships WHERE org_id = $1 AND user_id::text = $2`, orgID, *p.AssigneeUserID).Scan(&one)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotMember
			}
			if err != nil {
				return err
			}
		}
		now := time.Now().UTC()
		for _, ref := range refs {
			gid := GroupIDString(ref.GroupID)
			cur := ErrorGroupState{ErrorGroupRef: ref}
			err := tx.QueryRow(ctx, `SELECT status, COALESCE(assignee_user_id::text, ''), resolved_at, resolved_in_version, regressed_at, regression_count
				FROM apm_error_group_states WHERE org_id = $1 AND group_id = $2 FOR UPDATE`, orgID, gid).
				Scan((*string)(&cur.Status), &cur.AssigneeUserID, &cur.ResolvedAt, &cur.ResolvedInVersion, &cur.RegressedAt, &cur.RegressionCount)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			next := ApplyPatch(cur, p, now)
			if next.EffectiveStatus() == cur.EffectiveStatus() && next.AssigneeUserID == cur.AssigneeUserID &&
				next.ResolvedInVersion == cur.ResolvedInVersion && sameTime(next.ResolvedAt, cur.ResolvedAt) {
				continue // no change, no audit event
			}
			resolvedBy := any(nil)
			if next.EffectiveStatus() == StatusResolved {
				resolvedBy = uuidOrNil(actor.UserID)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO apm_error_group_states (org_id, group_id, service_name, service_namespace,
				deployment_environment, status, assignee_user_id, resolved_at, resolved_in_version, resolved_by, updated_at, updated_by)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now(), $11)
				ON CONFLICT (org_id, group_id) DO UPDATE SET status = EXCLUDED.status, assignee_user_id = EXCLUDED.assignee_user_id,
					resolved_at = EXCLUDED.resolved_at, resolved_in_version = EXCLUDED.resolved_in_version,
					resolved_by = CASE WHEN EXCLUDED.status = 'resolved' AND apm_error_group_states.resolved_at IS NOT DISTINCT FROM EXCLUDED.resolved_at
						THEN apm_error_group_states.resolved_by ELSE EXCLUDED.resolved_by END,
					updated_at = now(), updated_by = EXCLUDED.updated_by`,
				orgID, gid, ref.Key.Name, ref.Key.Namespace, ref.Key.Environment, string(next.Status), uuidOrNil(next.AssigneeUserID),
				next.ResolvedAt, next.ResolvedInVersion, resolvedBy, uuidOrNil(actor.UserID)); err != nil {
				return err
			}
			details := map[string]any{"service_name": ref.Key.Name, "service_namespace": ref.Key.Namespace, "environment": ref.Key.Environment}
			if next.EffectiveStatus() != cur.EffectiveStatus() {
				details["status"] = map[string]any{"from": cur.EffectiveStatus(), "to": next.EffectiveStatus()}
			}
			if next.AssigneeUserID != cur.AssigneeUserID {
				details["assignee_user_id"] = map[string]any{"from": cur.AssigneeUserID, "to": next.AssigneeUserID}
			}
			if next.ResolvedInVersion != "" {
				details["resolved_in_version"] = next.ResolvedInVersion
			}
			if err := audit(ctx, tx, orgID, actor, "apm.error_group.update", gid, details); err != nil {
				return err
			}
		}
		ids := make([]uint64, len(refs))
		for i, r := range refs {
			ids[i] = r.GroupID
		}
		rows, err := tx.Query(ctx, "SELECT "+stateColumns+" "+stateFrom+" WHERE st.org_id = $1 AND st.group_id = ANY($2::text[]) ORDER BY st.group_id",
			orgID, groupIDStrings(ids))
		if err != nil {
			return err
		}
		defer rows.Close()
		stored := map[uint64]ErrorGroupState{}
		for rows.Next() {
			st, err := scanState(rows)
			if err != nil {
				return err
			}
			stored[st.GroupID] = st
		}
		if err := rows.Err(); err != nil {
			return err
		}
		// Groups that still have no row (a no-op change of an unresolved, unassigned group) keep the default state.
		for _, ref := range refs {
			st, ok := stored[ref.GroupID]
			if !ok {
				st = ErrorGroupState{ErrorGroupRef: ref, Status: StatusUnresolved}
			}
			out = append(out, st)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func audit(ctx context.Context, tx pgx.Tx, orgID string, actor Actor, action, groupID string, details map[string]any) error {
	b, _ := json.Marshal(details)
	_, err := tx.Exec(ctx, `INSERT INTO audit_log (org_id, actor_user_id, actor_email, action, target_type, target_id, details, ip)
		VALUES ($1, $2, $3, $4, 'apm_error_group', $5, $6, $7)`, orgID, uuidOrNil(actor.UserID), actor.Email, action, groupID, b, actor.IP)
	return err
}

// MarkRegressed implements ErrorStateStore.
func (s PGErrorStates) MarkRegressed(ctx context.Context, orgID string, ref ErrorGroupRef, r Regression) (bool, error) {
	gid := GroupIDString(ref.GroupID)
	changed := false
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var resolvedAt time.Time
		var version string
		err := tx.QueryRow(ctx, `UPDATE apm_error_group_states AS st SET status = 'unresolved', regressed_at = $3,
				regression_count = st.regression_count + 1, resolved_at = NULL, resolved_in_version = '', resolved_by = NULL,
				updated_at = now(), updated_by = NULL
			FROM apm_error_group_states old
			WHERE st.org_id = $1 AND st.group_id = $2 AND old.org_id = st.org_id AND old.group_id = st.group_id
				AND st.status = 'resolved' AND st.resolved_at < $3
			RETURNING old.resolved_at, old.resolved_in_version`, orgID, gid, r.At).Scan(&resolvedAt, &version)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		changed = true
		return audit(ctx, tx, orgID, Actor{Email: RegressionActor}, "apm.error_group.regressed", gid, map[string]any{
			"service_name": ref.Key.Name, "service_namespace": ref.Key.Namespace, "environment": ref.Key.Environment,
			"resolved_at": resolvedAt.UTC().Format(time.RFC3339Nano), "resolved_in_version": version,
			"occurrence_at": r.At.UTC().Format(time.RFC3339Nano), "version": r.Version,
		})
	})
	return changed, err
}

// Comments implements ErrorStateStore (oldest first, at most 500).
func (s PGErrorStates) Comments(ctx context.Context, orgID string, groupID uint64) ([]ErrorComment, error) {
	rows, err := s.Pool.Query(ctx, `SELECT c.id::text, COALESCE(c.author_user_id::text, ''), c.author_email, COALESCE(u.name, ''), c.body, c.created_at
		FROM apm_error_group_comments c LEFT JOIN users u ON u.id = c.author_user_id
		WHERE c.org_id = $1 AND c.group_id = $2 ORDER BY c.created_at, c.id LIMIT 500`, orgID, GroupIDString(groupID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ErrorComment{}
	for rows.Next() {
		c := ErrorComment{GroupID: groupID}
		if err := rows.Scan(&c.ID, &c.AuthorUserID, &c.AuthorEmail, &c.AuthorName, &c.Body, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ValidateComment trims and checks a comment body.
func ValidateComment(body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" || len(body) > MaxCommentBytes {
		return "", fmt.Errorf("%w: body must be 1-%d bytes", ErrInvalidErrorPatch, MaxCommentBytes)
	}
	return body, nil
}

// AddComment implements ErrorStateStore.
func (s PGErrorStates) AddComment(ctx context.Context, orgID string, ref ErrorGroupRef, body string, actor Actor) (ErrorComment, error) {
	body, err := ValidateComment(body)
	if err != nil {
		return ErrorComment{}, err
	}
	c := ErrorComment{GroupID: ref.GroupID, AuthorUserID: actor.UserID, AuthorEmail: actor.Email, Body: body}
	gid := GroupIDString(ref.GroupID)
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO apm_error_group_comments (org_id, group_id, author_user_id, author_email, body)
			VALUES ($1, $2, $3, $4, $5) RETURNING id::text, created_at`, orgID, gid, uuidOrNil(actor.UserID), actor.Email, body).
			Scan(&c.ID, &c.CreatedAt); err != nil {
			return err
		}
		return audit(ctx, tx, orgID, actor, "apm.error_group.comment", gid, map[string]any{"comment_id": c.ID,
			"service_name": ref.Key.Name, "service_namespace": ref.Key.Namespace, "environment": ref.Key.Environment})
	})
	return c, err
}

// DeleteComment implements ErrorStateStore.
func (s PGErrorStates) DeleteComment(ctx context.Context, orgID string, groupID uint64, commentID string, actor Actor, manageAny bool) error {
	gid := GroupIDString(groupID)
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM apm_error_group_comments WHERE org_id = $1 AND group_id = $2 AND id::text = $3
			AND ($4 OR author_user_id::text = $5)`, orgID, gid, commentID, manageAny, actor.UserID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrCommentNotFound
		}
		return audit(ctx, tx, orgID, actor, "apm.error_group.comment_delete", gid, map[string]any{"comment_id": commentID})
	})
}

// Activity implements ErrorStateStore (newest first).
func (s PGErrorStates) Activity(ctx context.Context, orgID string, groupID uint64, limit int) ([]ErrorActivity, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.Pool.Query(ctx, `SELECT action, actor_email, details, created_at FROM audit_log
		WHERE org_id = $1 AND target_type = 'apm_error_group' AND target_id = $2 ORDER BY created_at DESC, id DESC LIMIT $3`,
		orgID, GroupIDString(groupID), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ErrorActivity{}
	for rows.Next() {
		var a ErrorActivity
		var raw []byte
		if err := rows.Scan(&a.Action, &a.ActorEmail, &raw, &a.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &a.Details)
		if a.Details == nil {
			a.Details = map[string]any{}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
