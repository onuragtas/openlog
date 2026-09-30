package diskspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Built-in levels, used for every field the operator has not set.
const (
	DefaultWarnPercent = 80
	DefaultHighPercent = 90
)

// Settings is what an operator may change. A nil field means "use the built-in default": the same convention as
// org_query_limits, and it keeps "left alone" distinguishable from "deliberately set to the default".
type Settings struct {
	WarnPercent *int `json:"warn_percent"`
	HighPercent *int `json:"high_percent"`
	Hysteresis  *int `json:"hysteresis"`
}

// Empty reports whether nothing is set, which is how a stored row is told from no row at all.
func (s Settings) Empty() bool {
	return s.WarnPercent == nil && s.HighPercent == nil && s.Hysteresis == nil
}

// Validate checks a settings change before it is stored. The database has the same constraints; this is what
// turns a violation into a message instead of a 500.
func (s Settings) Validate() error {
	pct := func(name string, v *int) error {
		if v != nil && (*v < 1 || *v > 99) {
			return fmt.Errorf("%s = %d: must be between 1 and 99", name, *v)
		}
		return nil
	}
	if err := pct("warn_percent", s.WarnPercent); err != nil {
		return err
	}
	if err := pct("high_percent", s.HighPercent); err != nil {
		return err
	}
	if s.Hysteresis != nil && (*s.Hysteresis < 0 || *s.Hysteresis > 50) {
		return fmt.Errorf("hysteresis = %d: must be between 0 and 50", *s.Hysteresis)
	}
	e := s.Resolve()
	if e.Warn >= e.High {
		return fmt.Errorf("warn_percent = %d must be below high_percent = %d", e.Warn, e.High)
	}
	return nil
}

// Effective is what a round of checks runs with, after the defaults have filled in whatever is not set.
type Effective struct {
	Warn       int `json:"warn_percent"`
	High       int `json:"high_percent"`
	Hysteresis int `json:"hysteresis"`
}

// Thresholds are the levels of Effective, lowest first.
func (e Effective) Thresholds() []int { return []int{e.Warn, e.High} }

// Resolve fills the unset fields with the built-in defaults.
func (s Settings) Resolve() Effective {
	e := Effective{Warn: DefaultWarnPercent, High: DefaultHighPercent, Hysteresis: DefaultHysteresis}
	if s.WarnPercent != nil {
		e.Warn = *s.WarnPercent
	}
	if s.HighPercent != nil {
		e.High = *s.HighPercent
	}
	if s.Hysteresis != nil {
		e.Hysteresis = *s.Hysteresis
	}
	return e
}

// Defaults is the built-in layer, for a UI that shows what a cleared field falls back to.
func Defaults() Effective { return Settings{}.Resolve() }

// Stored is a settings row with its provenance.
type Stored struct {
	Settings
	UpdatedAt      time.Time
	UpdatedByEmail string
}

// Actor is who changed the settings, for the audit log.
type Actor struct {
	UserID string
	Email  string
	IP     string
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)

// PGStore reads and writes the settings row.
type PGStore struct{ Pool *pgxpool.Pool }

// Get returns the stored settings, if an operator has ever set any.
func (st PGStore) Get(ctx context.Context) (Stored, bool, error) {
	var s Stored
	err := st.Pool.QueryRow(ctx, `SELECT d.warn_percent, d.high_percent, d.hysteresis, d.updated_at,
	        coalesce(u.email, '')
	    FROM disk_space_settings d LEFT JOIN users u ON u.id = d.updated_by`).
		Scan(&s.WarnPercent, &s.HighPercent, &s.Hysteresis, &s.UpdatedAt, &s.UpdatedByEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		return Stored{}, false, nil
	}
	if err != nil {
		return Stored{}, false, err
	}
	return s, true, nil
}

// Put stores the settings, or deletes the row when nothing is set, and records the change in the audit log in
// the same transaction: a change that is applied but not recorded is worse than one that fails outright.
func (st PGStore) Put(ctx context.Context, s Settings, actor Actor) error {
	if err := s.Validate(); err != nil {
		return err
	}
	tx, err := st.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a commit makes this a no-op
	action := "disk_space_settings.update"
	if s.Empty() {
		action = "disk_space_settings.delete"
		if _, err := tx.Exec(ctx, "DELETE FROM disk_space_settings"); err != nil {
			return err
		}
	} else if _, err := tx.Exec(ctx, `INSERT INTO disk_space_settings (id, warn_percent, high_percent, hysteresis, updated_by, updated_at)
	        VALUES (true, $1, $2, $3, $4::uuid, now())
	        ON CONFLICT (id) DO UPDATE SET warn_percent = excluded.warn_percent, high_percent = excluded.high_percent,
	            hysteresis = excluded.hysteresis, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		s.WarnPercent, s.HighPercent, s.Hysteresis, actorID(actor.UserID)); err != nil {
		return err
	}
	details, _ := json.Marshal(s)
	// org_id stays NULL: the disks belong to the installation, not to an organization.
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log (org_id, actor_user_id, actor_email, action, target_type, target_id, details, ip)
	        VALUES (NULL, $1::uuid, $2, $3, 'installation', 'disk_space', $4::jsonb, $5)`,
		actorID(actor.UserID), actor.Email, action, string(details), actor.IP); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// actorID turns a non-UUID principal (an API key, the system) into SQL NULL rather than a failed insert.
func actorID(id string) any {
	if uuidRe.MatchString(id) {
		return id
	}
	return nil
}
