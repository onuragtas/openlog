package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Abuse flag kinds.
const (
	FlagIngestSpike     = "ingest_spike"
	FlagNewOrgHosts     = "new_org_hosts"
	FlagIngestSourceIPs = "ingest_source_ips"
)

// Flag is an abuse detector finding.
type Flag struct {
	ID              int64          `json:"id"`
	OrgID           string         `json:"org_id"`
	OrgName         string         `json:"org_name"`
	TenantID        string         `json:"tenant_id"`
	Kind            string         `json:"kind"`
	Status          string         `json:"status"`
	Details         map[string]any `json:"details"`
	Occurrences     int            `json:"occurrences"`
	FirstSeenAt     time.Time      `json:"first_seen_at"`
	LastSeenAt      time.Time      `json:"last_seen_at"`
	AutoSuspended   bool           `json:"auto_suspended"`
	ResolvedByEmail string         `json:"resolved_by_email"`
	ResolvedAt      *time.Time     `json:"resolved_at"`
	ResolutionNote  string         `json:"resolution_note"`
	OrgSuspended    bool           `json:"org_suspended"`
}

const flagSelect = `SELECT f.id, f.org_id::text, o.name, o.tenant_id, f.kind, f.status, f.details, f.occurrences, f.first_seen_at, f.last_seen_at,
	f.auto_suspended, f.resolved_by_email, f.resolved_at, f.resolution_note, coalesce(st.suspended_at IS NOT NULL, false)
FROM abuse_flags f JOIN organizations o ON o.id = f.org_id LEFT JOIN org_saas_state st ON st.org_id = f.org_id `

func (s Store) listFlags(ctx context.Context, where string, limit int, args ...any) ([]Flag, error) {
	args = append(args, limit)
	rows, err := s.Pool.Query(ctx, flagSelect+where+` ORDER BY f.last_seen_at DESC, f.id DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Flag{}
	for rows.Next() {
		var f Flag
		var details []byte
		if err := rows.Scan(&f.ID, &f.OrgID, &f.OrgName, &f.TenantID, &f.Kind, &f.Status, &details, &f.Occurrences, &f.FirstSeenAt, &f.LastSeenAt,
			&f.AutoSuspended, &f.ResolvedByEmail, &f.ResolvedAt, &f.ResolutionNote, &f.OrgSuspended); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(details, &f.Details)
		out = append(out, f)
	}
	return out, rows.Err()
}

// ListFlags returns flags with status ("" = open).
func (s Store) ListFlags(ctx context.Context, status string, limit int) ([]Flag, error) {
	if status == "" {
		status = "open"
	}
	if status != "open" && status != "dismissed" && status != "actioned" && status != "all" {
		return nil, fmt.Errorf("%w: status must be open, dismissed, actioned or all", ErrInvalidState)
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if status == "all" {
		return s.listFlags(ctx, "", limit)
	}
	return s.listFlags(ctx, `WHERE f.status = $1`, limit, status)
}

// RaiseFlag creates an open flag or refreshes the open flag of (org, kind). It returns the flag and whether it is new.
func (s Store) RaiseFlag(ctx context.Context, orgID, kind string, details map[string]any) (int64, bool, error) {
	b, err := json.Marshal(details)
	if err != nil {
		return 0, false, err
	}
	var id int64
	var inserted bool
	err = s.Pool.QueryRow(ctx, `INSERT INTO abuse_flags (org_id, kind, details) VALUES ($1::uuid, $2, $3::jsonb)
		ON CONFLICT (org_id, kind) WHERE status = 'open' DO UPDATE SET details = EXCLUDED.details, last_seen_at = now(),
			occurrences = abuse_flags.occurrences + 1
		RETURNING id, xmax = 0`, orgID, kind, string(b)).Scan(&id, &inserted)
	return id, inserted, err
}

// MarkAutoSuspended records that the flag suspended its organization.
func (s Store) MarkAutoSuspended(ctx context.Context, id int64) error {
	_, err := s.Pool.Exec(ctx, `UPDATE abuse_flags SET auto_suspended = true WHERE id = $1`, id)
	return err
}

// ResolveFlag closes an open flag as dismissed or actioned. Audit abuse_flag.resolve.
func (s Store) ResolveFlag(ctx context.Context, id int64, status, note string, a Actor) (Flag, error) {
	if status != "dismissed" && status != "actioned" {
		return Flag{}, fmt.Errorf("%w: status must be dismissed or actioned", ErrInvalidState)
	}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var orgID, kind string
		err := tx.QueryRow(ctx, `UPDATE abuse_flags SET status = $2, resolution_note = $3, resolved_by = $4::uuid, resolved_by_email = $5, resolved_at = now()
			WHERE id = $1 AND status = 'open' RETURNING org_id::text, kind`, id, status, note, nullUUID(a.UserID), a.Email).Scan(&orgID, &kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return audit(ctx, tx, orgID, a, "abuse_flag.resolve", "abuse_flag", strconv.FormatInt(id, 10),
			map[string]any{"kind": kind, "status": status, "reason": note})
	})
	if err != nil {
		return Flag{}, err
	}
	fs, err := s.listFlags(ctx, `WHERE f.id = $1`, 1, id)
	if err != nil || len(fs) == 0 {
		return Flag{}, errors.Join(err, ErrNotFound)
	}
	return fs[0], nil
}
