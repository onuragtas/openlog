package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Privacy configures data subject requests: exports (data portability) and account and organization deletion
// (docs/operations/saas.md "Data subject requests", D-107).
type Privacy struct {
	// OrgDeletionGrace is how long a scheduled organization deletion can be cancelled before the hard deletion job
	// removes its data (OPENLOG_ORG_DELETION_GRACE).
	OrgDeletionGrace time.Duration
	Export           DataExport
}

// DataExport configures organization and personal data exports.
type DataExport struct {
	// Enabled offers exports (OPENLOG_DATA_EXPORT_ENABLED; postgres auth mode only).
	Enabled bool
	// Storage is auto (s3 when an S3 URL is known, else local), local or s3 (OPENLOG_DATA_EXPORT_STORAGE).
	Storage string
	// LocalPath is the directory of local archives and of the temporary file of S3 uploads (OPENLOG_DATA_EXPORT_LOCAL_PATH).
	LocalPath string
	// S3URL is the object base URL, bucket and prefix included (OPENLOG_DATA_EXPORT_S3_URL). Empty with tiered storage
	// enabled: derived from OPENLOG_S3_ENDPOINT (same bucket, prefix openlog-exports/).
	S3URL             string
	S3Region          string // OPENLOG_DATA_EXPORT_S3_REGION, else OPENLOG_S3_REGION, else us-east-1
	S3AccessKeyID     string // OPENLOG_DATA_EXPORT_S3_ACCESS_KEY_ID, else OPENLOG_S3_ACCESS_KEY_ID
	S3SecretAccessKey string // OPENLOG_DATA_EXPORT_S3_SECRET_ACCESS_KEY, else OPENLOG_S3_SECRET_ACCESS_KEY
	// S3Credentials is static (the keys above) or auto (AWS credential chain: env, web identity, ECS, IMDSv2; D-116)
	// (OPENLOG_DATA_EXPORT_S3_CREDENTIALS; default static when keys are set, else auto).
	S3Credentials string
	// TTL is how long a finished archive and its download link stay available (OPENLOG_DATA_EXPORT_TTL).
	TTL time.Duration
	// MaxBytes bounds the archive size; the telemetry part stops (truncated) when reached (OPENLOG_DATA_EXPORT_MAX_BYTES).
	MaxBytes int64
	// MaxRows bounds the telemetry rows of one export (OPENLOG_DATA_EXPORT_MAX_ROWS).
	MaxRows int64
	// MaxRange bounds the requested telemetry time range (OPENLOG_DATA_EXPORT_MAX_RANGE).
	MaxRange time.Duration
	// RowsPerSecond throttles reading telemetry from ClickHouse; 0 = unthrottled (OPENLOG_DATA_EXPORT_ROWS_PER_SECOND).
	RowsPerSecond int64
}

// StatusPage configures the public status page (D-108).
type StatusPage struct {
	// Enabled serves GET /api/v1/status and /status and runs the self-checks (OPENLOG_STATUS_PAGE_ENABLED; default
	// OPENLOG_SAAS_MODE).
	Enabled bool
}

func loadPrivacy(p *parser) Privacy {
	e := DataExport{
		Enabled:           p.bool("OPENLOG_DATA_EXPORT_ENABLED", true),
		Storage:           p.str("OPENLOG_DATA_EXPORT_STORAGE", "auto"),
		LocalPath:         p.str("OPENLOG_DATA_EXPORT_LOCAL_PATH", "/tmp/openlog-exports"),
		S3URL:             p.str("OPENLOG_DATA_EXPORT_S3_URL", ""),
		S3Region:          p.str("OPENLOG_DATA_EXPORT_S3_REGION", ""),
		S3AccessKeyID:     p.str("OPENLOG_DATA_EXPORT_S3_ACCESS_KEY_ID", ""),
		S3SecretAccessKey: p.str("OPENLOG_DATA_EXPORT_S3_SECRET_ACCESS_KEY", ""),
		TTL:               p.duration("OPENLOG_DATA_EXPORT_TTL", 7*24*time.Hour),
		MaxBytes:          p.int64("OPENLOG_DATA_EXPORT_MAX_BYTES", 4<<30),
		MaxRows:           p.int64("OPENLOG_DATA_EXPORT_MAX_ROWS", 50_000_000),
		MaxRange:          p.duration("OPENLOG_DATA_EXPORT_MAX_RANGE", 31*24*time.Hour),
		RowsPerSecond:     p.int64("OPENLOG_DATA_EXPORT_ROWS_PER_SECOND", 200_000),
	}
	// Tiered storage settings (the ClickHouse S3 disk) are reused when the export has none of its own.
	tiering, _ := strconv.ParseBool(strings.TrimSpace(p.getenv("OPENLOG_STORAGE_TIERING_ENABLED")))
	if e.S3URL == "" && tiering {
		e.S3URL = ExportURLFromTieredEndpoint(p.str("OPENLOG_S3_ENDPOINT", ""))
	}
	if e.S3Region == "" {
		e.S3Region = p.str("OPENLOG_S3_REGION", "us-east-1")
	}
	e.S3Credentials = strings.ToLower(strings.TrimSpace(p.str("OPENLOG_DATA_EXPORT_S3_CREDENTIALS", "")))
	// With explicit auto the tiered storage keys (ClickHouse's) are not borrowed: the export uses its IAM role.
	if e.S3AccessKeyID == "" && e.S3SecretAccessKey == "" && e.S3Credentials != "auto" {
		e.S3AccessKeyID, e.S3SecretAccessKey = p.str("OPENLOG_S3_ACCESS_KEY_ID", ""), p.str("OPENLOG_S3_SECRET_ACCESS_KEY", "")
	}
	if e.S3Credentials == "" {
		e.S3Credentials = "auto"
		if e.S3AccessKeyID != "" || e.S3SecretAccessKey != "" {
			e.S3Credentials = "static"
		}
	}
	return Privacy{OrgDeletionGrace: p.duration("OPENLOG_ORG_DELETION_GRACE", 7*24*time.Hour), Export: e}
}

func loadStatusPage(p *parser) StatusPage {
	saas, _ := strconv.ParseBool(strings.TrimSpace(p.getenv("OPENLOG_SAAS_MODE")))
	return StatusPage{Enabled: p.bool("OPENLOG_STATUS_PAGE_ENABLED", saas)}
}

// virtualHostedS3 matches bucket.s3.<region>.amazonaws.com style hosts (the bucket is not in the path).
var virtualHostedS3 = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]*\.s3[.\-]`)

// ExportURLFromTieredEndpoint derives the export base URL from a ClickHouse S3 disk endpoint
// ("http://minio:9000/openlog-cold/ch-1/" → "http://minio:9000/openlog-cold/openlog-exports/"): the same bucket, the
// prefix openlog-exports/ beside the disk prefixes. "" when the endpoint is empty or unusable.
func ExportURLFromTieredEndpoint(endpoint string) string {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	base := u.Scheme + "://" + u.Host + "/"
	if !virtualHostedS3.MatchString(u.Hostname()) {
		bucket, _, _ := strings.Cut(strings.TrimPrefix(u.Path, "/"), "/")
		if bucket == "" || strings.Contains(bucket, "{") {
			return ""
		}
		base += bucket + "/"
	}
	return base + "openlog-exports/"
}

// ExportStorage returns the effective storage: local or s3.
func (e DataExport) ExportStorage() string {
	if e.Storage == "auto" {
		if e.S3URL != "" {
			return "s3"
		}
		return "local"
	}
	return e.Storage
}

func (c Config) validatePrivacy() []error {
	var errs []error
	p := c.Privacy
	if p.OrgDeletionGrace < 0 || p.OrgDeletionGrace > 90*24*time.Hour {
		errs = append(errs, fmt.Errorf("OPENLOG_ORG_DELETION_GRACE: must be between 0 and 2160h (90 days), got %s", p.OrgDeletionGrace))
	}
	e := p.Export
	switch e.Storage {
	case "auto", "local", "s3":
	default:
		errs = append(errs, fmt.Errorf("OPENLOG_DATA_EXPORT_STORAGE: must be auto, local or s3, got %q", e.Storage))
	}
	if e.Storage == "s3" && e.S3URL == "" {
		errs = append(errs, errors.New("OPENLOG_DATA_EXPORT_STORAGE=s3 requires OPENLOG_DATA_EXPORT_S3_URL (or tiered storage with OPENLOG_S3_ENDPOINT)"))
	}
	if e.S3URL != "" {
		if u, err := url.Parse(e.S3URL); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || !strings.HasSuffix(u.Path, "/") {
			errs = append(errs, fmt.Errorf("OPENLOG_DATA_EXPORT_S3_URL: must be an http(s) URL ending with / (bucket and prefix), got %q", e.S3URL))
		}
	}
	if (e.S3AccessKeyID == "") != (e.S3SecretAccessKey == "") {
		errs = append(errs, errors.New("OPENLOG_DATA_EXPORT_S3_ACCESS_KEY_ID and OPENLOG_DATA_EXPORT_S3_SECRET_ACCESS_KEY must be set together"))
	}
	switch e.S3Credentials {
	case "static":
		if e.S3AccessKeyID == "" && e.S3SecretAccessKey == "" {
			errs = append(errs, errors.New("OPENLOG_DATA_EXPORT_S3_CREDENTIALS=static requires OPENLOG_DATA_EXPORT_S3_ACCESS_KEY_ID and OPENLOG_DATA_EXPORT_S3_SECRET_ACCESS_KEY (or OPENLOG_S3_*)"))
		}
	case "auto":
		if e.S3AccessKeyID != "" || e.S3SecretAccessKey != "" {
			errs = append(errs, errors.New("OPENLOG_DATA_EXPORT_S3_CREDENTIALS=auto uses the AWS credential chain; unset OPENLOG_DATA_EXPORT_S3_ACCESS_KEY_ID/OPENLOG_DATA_EXPORT_S3_SECRET_ACCESS_KEY or use static"))
		}
	default:
		errs = append(errs, fmt.Errorf("OPENLOG_DATA_EXPORT_S3_CREDENTIALS: must be static or auto, got %q", e.S3Credentials))
	}
	if e.LocalPath == "" || !strings.HasPrefix(e.LocalPath, "/") {
		errs = append(errs, fmt.Errorf("OPENLOG_DATA_EXPORT_LOCAL_PATH: must be an absolute path, got %q", e.LocalPath))
	}
	if e.TTL < time.Hour || e.TTL > 30*24*time.Hour {
		errs = append(errs, fmt.Errorf("OPENLOG_DATA_EXPORT_TTL: must be between 1h and 720h, got %s", e.TTL))
	}
	if e.MaxBytes < 1<<20 || e.MaxBytes > 5<<30 {
		errs = append(errs, fmt.Errorf("OPENLOG_DATA_EXPORT_MAX_BYTES: must be between 1048576 and 5368709120 (a single S3 PUT), got %d", e.MaxBytes))
	}
	if e.MaxRows < 1000 {
		errs = append(errs, fmt.Errorf("OPENLOG_DATA_EXPORT_MAX_ROWS: must be at least 1000, got %d", e.MaxRows))
	}
	if e.MaxRange < time.Hour || e.MaxRange > 400*24*time.Hour {
		errs = append(errs, fmt.Errorf("OPENLOG_DATA_EXPORT_MAX_RANGE: must be between 1h and 9600h, got %s", e.MaxRange))
	}
	if e.RowsPerSecond < 0 {
		errs = append(errs, errors.New("OPENLOG_DATA_EXPORT_ROWS_PER_SECOND must be >= 0"))
	}
	return errs
}
