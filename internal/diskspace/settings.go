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
	// Shedding starts at the critical level and stops below it, so a disk that has been dropped from does not
	// immediately qualify again.
	DefaultShedStartPercent = 90
	DefaultShedStopPercent  = 85
	// A table always keeps this many day-partitions, however full the disk is. Expressed in partitions rather
	// than days because a quiet day has no partition at all, and counting days would then delete more than asked.
	DefaultShedMinPartitions = 3
	// A bound on how much one round can delete, so a wrong setting or a mismeasured disk cannot empty the
	// installation in a single pass.
	DefaultShedMaxDropsPerRun = 20
)

// Settings is what an operator may change. A nil field means "use the built-in default": the same convention as
// org_query_limits, and it keeps "left alone" distinguishable from "deliberately set to the default".
type Settings struct {
	// The levels a disk is *reported* at. These delete nothing.
	WarnPercent *int `json:"warn_percent"`
	HighPercent *int `json:"high_percent"`
	Hysteresis  *int `json:"hysteresis"`

	// The levels data is *deleted* at, kept separate from the ones above so that setting a reporting level can
	// never delete anything. nil ShedEnabled means off.
	ShedEnabled        *bool `json:"shed_enabled"`
	ShedStartPercent   *int  `json:"shed_start_percent"`
	ShedStopPercent    *int  `json:"shed_stop_percent"`
	ShedMinPartitions  *int  `json:"shed_min_partitions"`
	ShedMaxDropsPerRun *int  `json:"shed_max_drops_per_run"`
}

// Empty reports whether nothing is set, which is how a stored row is told from no row at all.
func (s Settings) Empty() bool {
	return s.WarnPercent == nil && s.HighPercent == nil && s.Hysteresis == nil &&
		s.ShedEnabled == nil && s.ShedStartPercent == nil && s.ShedStopPercent == nil &&
		s.ShedMinPartitions == nil && s.ShedMaxDropsPerRun == nil
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
	for name, v := range map[string]*int{
		"warn_percent": s.WarnPercent, "high_percent": s.HighPercent,
		"shed_start_percent": s.ShedStartPercent, "shed_stop_percent": s.ShedStopPercent,
	} {
		if err := pct(name, v); err != nil {
			return err
		}
	}
	if s.Hysteresis != nil && (*s.Hysteresis < 0 || *s.Hysteresis > 50) {
		return fmt.Errorf("hysteresis = %d: must be between 0 and 50", *s.Hysteresis)
	}
	if s.ShedMinPartitions != nil && *s.ShedMinPartitions < 1 {
		return fmt.Errorf("shed_min_partitions = %d: must be at least 1", *s.ShedMinPartitions)
	}
	if s.ShedMaxDropsPerRun != nil && (*s.ShedMaxDropsPerRun < 1 || *s.ShedMaxDropsPerRun > 1000) {
		return fmt.Errorf("shed_max_drops_per_run = %d: must be between 1 and 1000", *s.ShedMaxDropsPerRun)
	}
	e := s.Resolve()
	if e.Warn >= e.High {
		return fmt.Errorf("warn_percent = %d must be below high_percent = %d", e.Warn, e.High)
	}
	if e.ShedStop >= e.ShedStart {
		return fmt.Errorf("shed_stop_percent = %d must be below shed_start_percent = %d", e.ShedStop, e.ShedStart)
	}
	// Deletion must never begin before the operator has been told the disk is critical. Otherwise data can go
	// away at a level that the same page calls healthy.
	if e.ShedStart < e.High {
		return fmt.Errorf("shed_start_percent = %d must not be below high_percent = %d: data must not be deleted at a level that is still reported as healthy",
			e.ShedStart, e.High)
	}
	return nil
}

// Effective is what a round of checks runs with, after the defaults have filled in whatever is not set.
type Effective struct {
	Warn       int `json:"warn_percent"`
	High       int `json:"high_percent"`
	Hysteresis int `json:"hysteresis"`

	ShedEnabled        bool `json:"shed_enabled"`
	ShedStart          int  `json:"shed_start_percent"`
	ShedStop           int  `json:"shed_stop_percent"`
	ShedMinPartitions  int  `json:"shed_min_partitions"`
	ShedMaxDropsPerRun int  `json:"shed_max_drops_per_run"`
}

// Thresholds are the reporting levels of Effective, lowest first.
func (e Effective) Thresholds() []int { return []int{e.Warn, e.High} }

// Resolve fills the unset fields with the built-in defaults.
func (s Settings) Resolve() Effective {
	e := Effective{
		Warn: DefaultWarnPercent, High: DefaultHighPercent, Hysteresis: DefaultHysteresis,
		ShedStart: DefaultShedStartPercent, ShedStop: DefaultShedStopPercent,
		ShedMinPartitions: DefaultShedMinPartitions, ShedMaxDropsPerRun: DefaultShedMaxDropsPerRun,
	}
	if s.WarnPercent != nil {
		e.Warn = *s.WarnPercent
	}
	if s.HighPercent != nil {
		e.High = *s.HighPercent
	}
	if s.Hysteresis != nil {
		e.Hysteresis = *s.Hysteresis
	}
	if s.ShedEnabled != nil {
		e.ShedEnabled = *s.ShedEnabled
	}
	if s.ShedStartPercent != nil {
		e.ShedStart = *s.ShedStartPercent
	}
	if s.ShedStopPercent != nil {
		e.ShedStop = *s.ShedStopPercent
	}
	if s.ShedMinPartitions != nil {
		e.ShedMinPartitions = *s.ShedMinPartitions
	}
	if s.ShedMaxDropsPerRun != nil {
		e.ShedMaxDropsPerRun = *s.ShedMaxDropsPerRun
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
	err := st.Pool.QueryRow(ctx, `SELECT d.warn_percent, d.high_percent, d.hysteresis,
	        d.shed_enabled, d.shed_start_percent, d.shed_stop_percent, d.shed_min_partitions,
	        d.shed_max_drops_per_run, d.updated_at, coalesce(u.email, '')
	    FROM disk_space_settings d LEFT JOIN users u ON u.id = d.updated_by`).
		Scan(&s.WarnPercent, &s.HighPercent, &s.Hysteresis,
			&s.ShedEnabled, &s.ShedStartPercent, &s.ShedStopPercent, &s.ShedMinPartitions,
			&s.ShedMaxDropsPerRun, &s.UpdatedAt, &s.UpdatedByEmail)
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
	} else if _, err := tx.Exec(ctx, `INSERT INTO disk_space_settings (id, warn_percent, high_percent, hysteresis,
	            shed_enabled, shed_start_percent, shed_stop_percent, shed_min_partitions, shed_max_drops_per_run,
	            updated_by, updated_at)
	        VALUES (true, $1, $2, $3, $4, $5, $6, $7, $8, $9::uuid, now())
	        ON CONFLICT (id) DO UPDATE SET warn_percent = excluded.warn_percent, high_percent = excluded.high_percent,
	            hysteresis = excluded.hysteresis, shed_enabled = excluded.shed_enabled,
	            shed_start_percent = excluded.shed_start_percent, shed_stop_percent = excluded.shed_stop_percent,
	            shed_min_partitions = excluded.shed_min_partitions,
	            shed_max_drops_per_run = excluded.shed_max_drops_per_run,
	            updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		s.WarnPercent, s.HighPercent, s.Hysteresis, s.ShedEnabled, s.ShedStartPercent, s.ShedStopPercent,
		s.ShedMinPartitions, s.ShedMaxDropsPerRun, actorID(actor.UserID)); err != nil {
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
