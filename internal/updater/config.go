// Package updater implements openlog-updater (docs/plan/09-releases-updates.md §5,
// docs/operations/upgrading.md): it reads the signed release index, picks a target release and,
// in `auto` mode, upgrades a Docker Compose installation (Docker Engine API) or the chart's
// Kubernetes Deployments (Kubernetes REST API) with backup, expand migrations, health checks and
// automatic rollback.
package updater

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/updatecheck"
)

// Modes (OPENLOG_UPDATER_MODE).
const (
	ModeOff    = "off"
	ModeNotify = "notify"
	ModeAuto   = "auto"
)

// Compose bundle sync (OPENLOG_UPDATER_COMPOSE_SYNC).
const (
	ComposeSyncAuto = "auto"
	ComposeSyncOff  = "off"
)

// Updater self-update (OPENLOG_UPDATER_SELF_UPDATE, Compose only).
const (
	SelfUpdateAuto = "auto"
	SelfUpdateOn   = "on"
	SelfUpdateOff  = "off"
)

// Config is parsed from OPENLOG_UPDATER_* (and the shared release variables).
type Config struct {
	Mode            string
	Channel         string
	IndexURL        string
	TrustedKeysFile string
	Interval        time.Duration
	// RequestPoll is how often update_requests ("Check now" / "Update now" in the UI) are polled.
	RequestPoll        time.Duration
	MaintenanceWindows []Window
	HealthTimeout      time.Duration
	// ImageRepository replaces the repository of the manifest image (mirrors); the digest is kept.
	ImageRepository string

	// Docker Compose engine.
	DockerHost      string
	Project         string
	Services        []string
	PostgresService string
	PGDumpUser      string
	PGDumpDatabase  string
	BackupDir       string
	BackupKeep      int
	HealthURLs      []string
	EnvFile         string
	MigrateCommand  []string
	// ComposeDir is the compose project directory as mounted in the updater (default: the directory of EnvFile).
	// Its docker-compose.yml and .env describe the environment of recreated containers; with a .bundle-version
	// file (install-server.sh) its compose files are kept at the running version.
	ComposeDir string
	// ComposeSync is auto (replace the compose bundle of install-server.sh installations on update) or off.
	ComposeSync string
	// SelfUpdate is auto (after a successful update, install-server.sh installations replace the updater's own
	// container with the installed image), on (every Compose installation) or off (notice only). D-120.
	SelfUpdate string

	// Kubernetes engine.
	Deployments     []string
	MigrateTemplate string
	VersionURL      string
	RolloutTimeout  time.Duration
	MigrateTimeout  time.Duration
}

// LoadConfig parses the updater configuration. Unset variables take their documented defaults.
func LoadConfig(getenv func(string) string) (Config, error) {
	var errs []error
	str := func(name, def string) string {
		if v := strings.TrimSpace(getenv(name)); v != "" {
			return v
		}
		return def
	}
	list := func(name, def string) []string {
		var out []string
		for _, s := range strings.Split(str(name, def), ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	dur := func(name string, def time.Duration) time.Duration {
		v := str(name, "")
		if v == "" {
			return def
		}
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("%s: invalid duration %q", name, v))
			return def
		}
		return d
	}
	c := Config{
		Mode:            str("OPENLOG_UPDATER_MODE", ModeNotify),
		Channel:         str("OPENLOG_UPDATE_CHANNEL", "stable"),
		IndexURL:        str("OPENLOG_RELEASE_INDEX_URL", updatecheck.DefaultIndexURL),
		TrustedKeysFile: str("OPENLOG_RELEASE_TRUSTED_KEYS_FILE", ""),
		Interval:        dur("OPENLOG_UPDATER_INTERVAL", time.Hour),
		RequestPoll:     dur("OPENLOG_UPDATER_REQUEST_POLL", 10*time.Second),
		HealthTimeout:   dur("OPENLOG_UPDATER_HEALTH_TIMEOUT", 5*time.Minute),
		ImageRepository: str("OPENLOG_UPDATER_IMAGE_REPOSITORY", ""),

		DockerHost:      str("DOCKER_HOST", "unix:///var/run/docker.sock"),
		Project:         str("OPENLOG_UPDATER_COMPOSE_PROJECT", ""),
		Services:        list("OPENLOG_UPDATER_SERVICES", "openlog"),
		PostgresService: str("OPENLOG_UPDATER_POSTGRES_SERVICE", "postgres"),
		PGDumpUser:      str("OPENLOG_UPDATER_PGDUMP_USER", "openlog"),
		PGDumpDatabase:  str("OPENLOG_UPDATER_PGDUMP_DATABASE", "openlog"),
		BackupDir:       str("OPENLOG_UPDATER_BACKUP_DIR", "/backups"),
		HealthURLs:      list("OPENLOG_UPDATER_HEALTH_URLS", "http://openlog:9464/readyz"),
		EnvFile:         str("OPENLOG_UPDATER_ENV_FILE", ""),
		MigrateCommand:  []string{"/usr/local/bin/openlog-migrate"},
		ComposeDir:      str("OPENLOG_UPDATER_COMPOSE_DIR", ""),
		ComposeSync:     str("OPENLOG_UPDATER_COMPOSE_SYNC", ComposeSyncAuto),
		SelfUpdate:      str("OPENLOG_UPDATER_SELF_UPDATE", SelfUpdateAuto),

		Deployments:     list("OPENLOG_UPDATER_K8S_DEPLOYMENTS", ""),
		MigrateTemplate: str("OPENLOG_UPDATER_K8S_MIGRATE_TEMPLATE", ""),
		VersionURL:      str("OPENLOG_UPDATER_VERSION_URL", ""),
		RolloutTimeout:  dur("OPENLOG_UPDATER_ROLLOUT_TIMEOUT", 15*time.Minute),
		MigrateTimeout:  dur("OPENLOG_UPDATER_MIGRATE_TIMEOUT", 30*time.Minute),
	}
	keep := str("OPENLOG_UPDATER_BACKUP_KEEP", "5")
	n, err := strconv.Atoi(keep)
	if err != nil || n < 1 {
		errs = append(errs, fmt.Errorf("OPENLOG_UPDATER_BACKUP_KEEP: must be a positive integer, got %q", keep))
		n = 5
	}
	c.BackupKeep = n
	switch c.Mode {
	case ModeOff, ModeNotify, ModeAuto:
	default:
		errs = append(errs, fmt.Errorf("OPENLOG_UPDATER_MODE: must be off, notify or auto, got %q", c.Mode))
	}
	switch c.Channel {
	case "stable", "beta":
	default:
		errs = append(errs, fmt.Errorf("OPENLOG_UPDATE_CHANNEL: must be stable or beta, got %q", c.Channel))
	}
	switch c.ComposeSync {
	case ComposeSyncAuto, ComposeSyncOff:
	default:
		errs = append(errs, fmt.Errorf("OPENLOG_UPDATER_COMPOSE_SYNC: must be auto or off, got %q", c.ComposeSync))
	}
	switch c.SelfUpdate {
	case SelfUpdateAuto, SelfUpdateOn, SelfUpdateOff:
	default:
		errs = append(errs, fmt.Errorf("OPENLOG_UPDATER_SELF_UPDATE: must be auto, on or off, got %q", c.SelfUpdate))
	}
	if c.ComposeDir == "" && c.EnvFile != "" {
		c.ComposeDir = filepath.Dir(c.EnvFile)
	}
	if c.MaintenanceWindows, err = ParseWindows(getenv("OPENLOG_UPDATER_MAINTENANCE_WINDOW")); err != nil {
		errs = append(errs, fmt.Errorf("OPENLOG_UPDATER_MAINTENANCE_WINDOW: %w", err))
	}
	if len(c.HealthURLs) == 0 {
		errs = append(errs, errors.New("OPENLOG_UPDATER_HEALTH_URLS must not be empty"))
	}
	if c.VersionURL == "" {
		c.VersionURL = c.HealthURLs[0]
	}
	if len(c.Services) == 0 {
		errs = append(errs, errors.New("OPENLOG_UPDATER_SERVICES must not be empty"))
	}
	return c, errors.Join(errs...)
}
