package dataexport

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/deletion"
	mailtemplates "github.com/onuragtas/openlog/internal/mail/templates"
	"github.com/onuragtas/openlog/internal/objstore"
)

// Manifest is manifest.json of an archive.
type Manifest struct {
	FormatVersion int                       `json:"format_version"`
	ExportID      string                    `json:"export_id"`
	Kind          string                    `json:"kind"`
	GeneratedAt   string                    `json:"generated_at"`
	Organization  map[string]string         `json:"organization,omitempty"`
	UserID        string                    `json:"user_id,omitempty"`
	From          string                    `json:"from,omitempty"`
	To            string                    `json:"to,omitempty"`
	Files         []string                  `json:"files"`
	Telemetry     map[string]SignalManifest `json:"telemetry,omitempty"`
	TelemetryRows int64                     `json:"telemetry_rows"`
	Truncated     bool                      `json:"truncated"`
	Limits        map[string]int64          `json:"limits"`
	Notes         []string                  `json:"notes"`
}

// Service is the export API and the storage side shared by Job and the deletion job.
type Service struct {
	Store   PGStore
	Objects objstore.Store
	Limits  Limits
	Now     func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// InvalidError is a request validation error (400).
type InvalidError struct{ Msg string }

func (e *InvalidError) Error() string { return e.Msg }

// RequestOrg queues an organization export of signals between from and to (owner check by the caller).
func (s *Service) RequestOrg(ctx context.Context, orgID, userID, locale string, from, to time.Time, signals []string) (Export, error) {
	seen := map[string]bool{}
	var sigs []string
	for _, sig := range signals {
		sig = strings.TrimSpace(sig)
		if _, ok := Signals[sig]; !ok {
			return Export{}, &InvalidError{fmt.Sprintf("unknown signal %q (logs, traces, metrics)", sig)}
		}
		if !seen[sig] {
			seen[sig] = true
			sigs = append(sigs, sig)
		}
	}
	sort.Strings(sigs)
	e := Export{Kind: KindOrganization, OrgID: orgID, UserID: userID, Locale: locale, Signals: sigs}
	if len(sigs) > 0 {
		if !from.Before(to) {
			return Export{}, &InvalidError{"from must be before to"}
		}
		if to.Sub(from) > s.Limits.MaxRange {
			return Export{}, &InvalidError{fmt.Sprintf("the time range must not exceed %s (OPENLOG_DATA_EXPORT_MAX_RANGE)", s.Limits.MaxRange)}
		}
		if to.After(s.now().Add(time.Hour)) {
			to = s.now()
		}
		f, t := from.UTC(), to.UTC()
		e.From, e.To = &f, &t
	}
	return e, s.Store.Create(ctx, &e, s.Limits.maxPerDay(), s.now())
}

// RequestUser queues a personal export of userID.
func (s *Service) RequestUser(ctx context.Context, userID, locale string) (Export, error) {
	e := Export{Kind: KindUser, UserID: userID, Locale: locale}
	return e, s.Store.Create(ctx, &e, s.Limits.maxPerDay(), s.now())
}

// OpenArchive opens the stored archive of e (completed and not expired, else ErrNotFound).
func (s *Service) OpenArchive(ctx context.Context, e Export) (io.ReadCloser, int64, error) {
	if e.Status != StatusCompleted || e.ObjectKey == "" || (e.ExpiresAt != nil && !e.ExpiresAt.After(s.now())) {
		return nil, 0, ErrNotFound
	}
	if e.Storage != s.Objects.Kind() {
		return nil, 0, fmt.Errorf("archive is in %s storage but %s is configured", e.Storage, s.Objects.Kind())
	}
	r, n, err := s.Objects.Open(ctx, e.ObjectKey)
	if errors.Is(err, objstore.ErrNotFound) {
		return nil, 0, ErrNotFound
	}
	return r, n, err
}

// DeleteOrgExports implements deletion.ExportCleaner.
func (s *Service) DeleteOrgExports(ctx context.Context, orgID string) error {
	es, err := s.Store.OrgObjects(ctx, orgID)
	if err != nil {
		return err
	}
	for _, e := range es {
		if err := s.Objects.Delete(ctx, e.ObjectKey); err != nil {
			return err
		}
		if err := s.Store.MarkExpired(ctx, e.ID); err != nil {
			return err
		}
	}
	return nil
}

// DeleteObjects implements deletion.ExportCleaner.
func (s *Service) DeleteObjects(ctx context.Context, objs []deletion.ExportObject) error {
	var errs []error
	for _, o := range objs {
		if o.Storage == s.Objects.Kind() && o.Key != "" {
			errs = append(errs, s.Objects.Delete(ctx, o.Key))
		}
	}
	return errors.Join(errs...)
}

// Job runs queued exports on the api leader.
type Job struct {
	Service   *Service
	Pool      *pgxpool.Pool
	Rows      RowSource // nil: organization exports contain no telemetry
	TempDir   string
	Mailer    auth.Mailer
	PublicURL string
	Interval  time.Duration // default 15s
	Log       *slog.Logger
	sleep     func(time.Duration)
}

func (j *Job) log() *slog.Logger {
	if j.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return j.Log
}

// Run processes exports until ctx is done.
func (j *Job) Run(ctx context.Context) {
	interval := j.Interval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	lastCleanup := time.Time{}
	for {
		if time.Since(lastCleanup) > 10*time.Minute {
			if err := j.Cleanup(ctx); err != nil && ctx.Err() == nil {
				j.log().Warn("data export cleanup failed", "err", err)
			}
			lastCleanup = time.Now()
		}
		for ctx.Err() == nil {
			ran, err := j.RunOnce(ctx)
			if err != nil && ctx.Err() == nil {
				j.log().Warn("data export run failed", "err", err)
			}
			if !ran || err != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// Cleanup deletes expired archives and old finished rows.
func (j *Job) Cleanup(ctx context.Context) error {
	s := j.Service
	es, err := s.Store.Expired(ctx, s.now(), 100)
	if err != nil {
		return err
	}
	for _, e := range es {
		if e.ObjectKey != "" && e.Storage == s.Objects.Kind() {
			if err := s.Objects.Delete(ctx, e.ObjectKey); err != nil {
				return err
			}
		}
		if err := s.Store.MarkExpired(ctx, e.ID); err != nil {
			return err
		}
	}
	return s.Store.PruneOld(ctx, s.now(), 90*24*time.Hour)
}

// RunOnce claims and builds one export; false when the queue was empty.
func (j *Job) RunOnce(ctx context.Context) (bool, error) {
	s := j.Service
	e, err := s.Store.Claim(ctx, s.now(), 5*time.Minute)
	if err != nil || e == nil {
		return false, err
	}
	hctx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-hctx.Done():
				return
			case <-t.C:
				_ = s.Store.Heartbeat(hctx, e.ID, s.now())
			}
		}
	}()
	c, err := j.build(ctx, *e)
	stop()
	if err != nil {
		j.log().Warn("data export failed", "export_id", e.ID, "kind", e.Kind, "err", err)
		msg := "the export failed; try again later"
		var inv *InvalidError
		if errors.As(err, &inv) {
			msg = inv.Msg
		}
		return true, s.Store.Fail(context.WithoutCancel(ctx), e.ID, msg, s.now())
	}
	if err := s.Store.Complete(ctx, e.ID, c.Completion, s.now()); err != nil {
		_ = s.Objects.Delete(context.WithoutCancel(ctx), c.ObjectKey)
		return true, err
	}
	j.log().Info("data export completed", "export_id", e.ID, "kind", e.Kind, "bytes", c.SizeBytes, "telemetry_rows", c.TelemetryRows, "truncated", c.Truncated)
	j.mail(ctx, *e, c)
	return true, nil
}

type built struct {
	Completion
	token string
}

func (j *Job) build(ctx context.Context, e Export) (built, error) {
	s := j.Service
	dir := j.TempDir
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return built{}, err
	}
	f, err := os.CreateTemp(dir, "export-*.zip.tmp")
	if err != nil {
		return built{}, err
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}()
	buf := bufio.NewWriterSize(f, 1<<20)
	cw := &countingWriter{w: buf}
	zw := zip.NewWriter(cw)
	now := s.now().UTC()
	man := Manifest{FormatVersion: 1, ExportID: e.ID, Kind: e.Kind, GeneratedAt: now.Format(time.RFC3339), Files: []string{},
		Limits: map[string]int64{"max_bytes": s.Limits.MaxBytes, "max_rows": s.Limits.MaxRows, "rows_per_second": s.Limits.RowsPerSecond}}
	switch e.Kind {
	case KindOrganization:
		man.Organization = map[string]string{"id": e.OrgID, "tenant_id": e.TenantID, "name": e.OrgName}
		names, err := writeDocuments(ctx, j.Pool, zw, "organization/", OrgDocuments, e.OrgID, now)
		if err != nil {
			return built{}, err
		}
		man.Files = append(man.Files, names...)
		man.Notes = append(man.Notes, "Secrets (channel credentials, SSO client secrets and keys), key hashes and tokens are not exported.")
		if len(e.Signals) > 0 && e.From != nil && e.To != nil {
			if j.Rows == nil {
				man.Notes = append(man.Notes, "Telemetry export is not available on this server.")
			} else {
				man.From, man.To = e.From.Format(time.RFC3339), e.To.Format(time.RFC3339)
				man.Telemetry = map[string]SignalManifest{}
				tw := &telemetryWriter{zw: zw, size: func() int64 { return cw.n }, limits: s.Limits, started: time.Now(), now: time.Now, sleep: j.sleepFn(),
					maxBytes: s.Limits.MaxBytes - 1<<20}
				for _, sig := range e.Signals {
					m, stopped, err := tw.signal(ctx, j.Rows, sig, Signals[sig], e.TenantID, *e.From, *e.To)
					if err != nil {
						return built{}, fmt.Errorf("telemetry %s: %w", sig, err)
					}
					man.Telemetry[sig] = m
					man.Files = append(man.Files, m.Files...)
					if stopped {
						man.Truncated = true
						man.Notes = append(man.Notes, fmt.Sprintf("Telemetry stopped at a size or row limit while exporting %s; complete_until shows the covered range.", sig))
						break
					}
				}
				man.TelemetryRows = tw.rows
				man.Notes = append(man.Notes, "Telemetry files are gzip-compressed NDJSON, one JSON object per stored row, in the table's column names.")
			}
		}
	case KindUser:
		man.UserID = e.UserID
		names, err := writeDocuments(ctx, j.Pool, zw, "user/", UserDocuments, e.UserID, now)
		if err != nil {
			return built{}, err
		}
		man.Files = append(man.Files, names...)
	default:
		return built{}, fmt.Errorf("unknown export kind %q", e.Kind)
	}
	mw, err := zw.CreateHeader(&zip.FileHeader{Name: "manifest.json", Method: zip.Deflate, Modified: now})
	if err != nil {
		return built{}, err
	}
	enc := json.NewEncoder(mw)
	enc.SetIndent("", "  ")
	if err := enc.Encode(man); err != nil {
		return built{}, err
	}
	if err := zw.Close(); err != nil {
		return built{}, err
	}
	if err := buf.Flush(); err != nil {
		return built{}, err
	}
	if cw.n > s.Limits.MaxBytes {
		return built{}, &InvalidError{fmt.Sprintf("the archive would be %d bytes, more than the limit of %d bytes (OPENLOG_DATA_EXPORT_MAX_BYTES)", cw.n, s.Limits.MaxBytes)}
	}
	if _, err := f.Seek(0, 0); err != nil {
		return built{}, err
	}
	owner := e.OrgID
	if e.Kind == KindUser {
		owner = e.UserID
	}
	key := fmt.Sprintf("exports/%s/%s/%s.zip", e.Kind, owner, e.ID)
	if err := s.Objects.Put(ctx, key, f, cw.n); err != nil {
		return built{}, fmt.Errorf("store archive: %w", err)
	}
	tok := make([]byte, 32)
	if _, err := rand.Read(tok); err != nil {
		return built{}, err
	}
	token := "olx_" + base64.RawURLEncoding.EncodeToString(tok)
	hash := sha256.Sum256([]byte(token))
	return built{Completion: Completion{Storage: s.Objects.Kind(), ObjectKey: key, SizeBytes: cw.n, TelemetryRows: man.TelemetryRows,
		Truncated: man.Truncated, Manifest: man, TokenHash: hash[:], ExpiresAt: now.Add(s.Limits.TTL)}, token: token}, nil
}

func (j *Job) sleepFn() func(time.Duration) {
	if j.sleep != nil {
		return j.sleep
	}
	return time.Sleep
}

// TokenHash hashes a download link token.
func TokenHash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// DownloadLink is the public download URL of token.
func DownloadLink(publicURL, token string) string {
	return strings.TrimRight(publicURL, "/") + "/api/v1/data-exports/download?token=" + token
}

func (j *Job) mail(ctx context.Context, e Export, b built) {
	if j.Mailer == nil || j.PublicURL == "" || e.UserEmail == "" {
		return
	}
	msg := mailtemplates.DataExport(e.Locale, mailtemplates.DataExportData{Kind: e.Kind, OrgName: e.OrgName, Size: FormatBytes(b.SizeBytes),
		Truncated: b.Truncated, ExpiresAt: b.ExpiresAt, Link: DownloadLink(j.PublicURL, b.token)})
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := j.Mailer.Send(mctx, auth.Mail{To: e.UserEmail, Subject: msg.Subject, Text: msg.Text, HTML: msg.HTML}); err != nil {
		j.log().Warn("cannot send data export e-mail", "export_id", e.ID, "err", err)
	}
}

// FormatBytes renders a size with binary units.
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// ArchivePath is where a local archive lives (diagnostics).
func ArchivePath(dir, key string) string { return filepath.Join(dir, filepath.FromSlash(key)) }
