package config

import (
	"fmt"
	"regexp"
	"strings"
)

// Storage configures tiered storage of the ClickHouse telemetry tables (D-066, docs/operations/tiered-storage.md).
// openlog-migrate (and openlog-allinone with migrations) switches the tables of every class with a move age to
// the storage policy Policy and adds `TTL … TO VOLUME 'warm'|'cold'` clauses next to the delete TTL. The policy,
// its S3 disk and cache are ClickHouse server configuration (deploy/compose/clickhouse/storage-tiered.xml).
type Storage struct {
	// TieringEnabled turns the moves on (OPENLOG_STORAGE_TIERING_ENABLED). false removes the move clauses again;
	// tables keep the policy and parts already on S3 stay there (readable).
	TieringEnabled bool
	// Policy is the ClickHouse storage policy with the volumes `hot` (the default disk), optional `warm` and `cold`
	// (OPENLOG_STORAGE_POLICY).
	Policy string
	// Tiers holds the move ages per table class, indexed like StorageClasses.
	Tiers map[string]StorageTier
}

// StorageTier holds the move ages of one table class in days; 0 = no move to that volume.
type StorageTier struct {
	WarmAfterDays int // OPENLOG_STORAGE_WARM_AFTER_DAYS_<CLASS>
	ColdAfterDays int // OPENLOG_STORAGE_COLD_AFTER_DAYS_<CLASS>
}

// StorageClass is a group of tables that share move ages.
type StorageClass struct {
	Name        string // metrics, metrics_1m, logs, traces, apm, alerts
	DefaultCold int    // OPENLOG_STORAGE_COLD_AFTER_DAYS_<CLASS> default
}

// StorageClasses lists the table classes (internal/migrate maps tables to them).
var StorageClasses = []StorageClass{
	{Name: "metrics", DefaultCold: 7},     // metrics_local (30-day retention)
	{Name: "metrics_1m", DefaultCold: 30}, // metrics_1m_local (395 days)
	{Name: "logs", DefaultCold: 3},        // logs_local (14 days)
	{Name: "traces", DefaultCold: 3},      // spans_local, trace_index_local (7 days)
	{Name: "apm", DefaultCold: 7},         // apm_* rollups (OPENLOG_APM_RETENTION_DAYS)
	{Name: "alerts", DefaultCold: 7},      // alert_evaluations_local (30 days)
	{Name: "rum", DefaultCold: 7},         // rum_* rollups (30 days, D-136)
}

// DefaultStoragePolicy is the policy defined by deploy/compose/clickhouse/storage-tiered.xml and the Helm chart.
const DefaultStoragePolicy = "openlog_tiered"

var storagePolicyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// Tier returns the move ages of class (zero when tiering is disabled or the class is unknown).
func (s Storage) Tier(class string) StorageTier {
	if !s.TieringEnabled {
		return StorageTier{}
	}
	return s.Tiers[class]
}

func loadStorage(p *parser) Storage {
	s := Storage{
		TieringEnabled: p.bool("OPENLOG_STORAGE_TIERING_ENABLED", false),
		Policy:         p.str("OPENLOG_STORAGE_POLICY", DefaultStoragePolicy),
		Tiers:          make(map[string]StorageTier, len(StorageClasses)),
	}
	for _, c := range StorageClasses {
		suffix := strings.ToUpper(c.Name)
		s.Tiers[c.Name] = StorageTier{
			WarmAfterDays: int(p.int64("OPENLOG_STORAGE_WARM_AFTER_DAYS_"+suffix, 0)),
			ColdAfterDays: int(p.int64("OPENLOG_STORAGE_COLD_AFTER_DAYS_"+suffix, int64(c.DefaultCold))),
		}
	}
	return s
}

func (c Config) validateStorage() []error {
	var errs []error
	s := c.Storage
	if !storagePolicyRe.MatchString(s.Policy) {
		errs = append(errs, fmt.Errorf("OPENLOG_STORAGE_POLICY: invalid policy name %q (letters, digits, underscore)", s.Policy))
	}
	for _, cl := range StorageClasses {
		t, suffix := s.Tiers[cl.Name], strings.ToUpper(cl.Name)
		if t.WarmAfterDays < 0 || t.WarmAfterDays > 3650 {
			errs = append(errs, fmt.Errorf("OPENLOG_STORAGE_WARM_AFTER_DAYS_%s: must be between 0 and 3650, got %d", suffix, t.WarmAfterDays))
		}
		if t.ColdAfterDays < 0 || t.ColdAfterDays > 3650 {
			errs = append(errs, fmt.Errorf("OPENLOG_STORAGE_COLD_AFTER_DAYS_%s: must be between 0 and 3650, got %d", suffix, t.ColdAfterDays))
		}
		if t.WarmAfterDays > 0 && t.ColdAfterDays > 0 && t.WarmAfterDays >= t.ColdAfterDays {
			errs = append(errs, fmt.Errorf("OPENLOG_STORAGE_WARM_AFTER_DAYS_%s (%d) must be less than OPENLOG_STORAGE_COLD_AFTER_DAYS_%s (%d)",
				suffix, t.WarmAfterDays, suffix, t.ColdAfterDays))
		}
	}
	return errs
}
