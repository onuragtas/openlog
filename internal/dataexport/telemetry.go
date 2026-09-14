package dataexport

import (
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/onuragtas/openlog/internal/store/clickhouse"
)

// RowSource streams the tenant's rows of one table between from and to as JSON objects (one per call of fn).
type RowSource interface {
	Stream(ctx context.Context, table, tenant string, from, to time.Time, fn func(row []byte) error) error
}

// ClickHouseSource reads rows with formatRowNoNewline('JSONEachRow', <columns>) over the Distributed table: every
// stored (non-alias, non-materialized) column, filtered by tenant_id and the table's timestamp column, with a
// bounded number of threads per query.
type ClickHouseSource struct {
	Conn     clickhouse.Conn
	Database string
	// MaxThreads per export query (default 2); MaxExecutionTime per day window (default 1h).
	MaxThreads       int
	MaxExecutionTime time.Duration
}

var columnRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)

func (s ClickHouseSource) db() string {
	if s.Database == "" {
		return "openlog"
	}
	return s.Database
}

func (s ClickHouseSource) columns(ctx context.Context, table string) ([]string, error) {
	qctx := ch.Context(ctx, ch.WithParameters(ch.Parameters{"db": s.db(), "t": table}))
	rows, err := s.Conn.Query(qctx, `SELECT name FROM system.columns WHERE database = {db:String} AND table = {t:String}
		AND default_kind NOT IN ('ALIAS', 'MATERIALIZED', 'EPHEMERAL') ORDER BY position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		if columnRe.MatchString(c) {
			cols = append(cols, "`"+c+"`")
		}
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("table %s has no columns", table)
	}
	return cols, rows.Err()
}

// Stream implements RowSource.
func (s ClickHouseSource) Stream(ctx context.Context, table, tenant string, from, to time.Time, fn func(row []byte) error) error {
	if !columnRe.MatchString(table) {
		return fmt.Errorf("invalid table %q", table)
	}
	cols, err := s.columns(ctx, table)
	if err != nil {
		return err
	}
	threads := s.MaxThreads
	if threads <= 0 {
		threads = 2
	}
	maxExec := s.MaxExecutionTime
	if maxExec <= 0 {
		maxExec = time.Hour
	}
	qctx := ch.Context(ctx,
		ch.WithParameters(ch.Parameters{"tenant": tenant, "from": fmt.Sprint(from.UTC().UnixNano()), "to": fmt.Sprint(to.UTC().UnixNano())}),
		ch.WithSettings(ch.Settings{"max_threads": threads, "max_execution_time": int(maxExec.Seconds()), "skip_unavailable_shards": 0}))
	q := fmt.Sprintf("SELECT formatRowNoNewline('JSONEachRow', %s) FROM `%s`.`%s` WHERE tenant_id = {tenant:String} "+
		"AND timestamp >= fromUnixTimestamp64Nano({from:Int64}) AND timestamp < fromUnixTimestamp64Nano({to:Int64})",
		strings.Join(cols, ", "), s.db(), table)
	rows, err := s.Conn.Query(qctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return err
		}
		if err := fn([]byte(line)); err != nil {
			return err
		}
	}
	return rows.Err()
}

// errStop ends a stream early because a limit was reached.
var errStop = errors.New("export limit reached")

// SignalManifest describes the exported telemetry of one signal.
type SignalManifest struct {
	Table        string   `json:"table"`
	Rows         int64    `json:"rows"`
	Files        []string `json:"files"`
	CompleteTill string   `json:"complete_until,omitempty"`
	Truncated    bool     `json:"truncated"`
}

// telemetryWriter writes chunked NDJSON.gz entries into a ZIP archive, enforcing row, byte and rate limits.
type telemetryWriter struct {
	zw      *zip.Writer
	size    func() int64 // current archive size
	limits  Limits
	rows    int64
	started time.Time
	now     func() time.Time
	sleep   func(time.Duration)
	// maxBytes leaves room for the manifest and the ZIP directory.
	maxBytes int64
}

func (t *telemetryWriter) throttle() {
	if t.limits.RowsPerSecond <= 0 || t.rows%1000 != 0 {
		return
	}
	expected := time.Duration(float64(t.rows) / float64(t.limits.RowsPerSecond) * float64(time.Second))
	if d := expected - t.now().Sub(t.started); d > 0 {
		t.sleep(d)
	}
}

// signal exports one signal day by day (partition-aligned queries). truncated reports that a limit stopped it.
func (t *telemetryWriter) signal(ctx context.Context, src RowSource, signal, table, tenant string, from, to time.Time) (SignalManifest, bool, error) {
	m := SignalManifest{Table: table, Files: []string{}}
	for day := from; day.Before(to); {
		next := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
		if next.After(to) {
			next = to
		}
		stopped, err := t.window(ctx, src, &m, signal, table, tenant, day, next)
		if err != nil {
			return m, false, err
		}
		if stopped {
			m.Truncated = true
			return m, true, nil
		}
		m.CompleteTill = next.UTC().Format(time.RFC3339)
		day = next
	}
	return m, false, nil
}

func (t *telemetryWriter) window(ctx context.Context, src RowSource, m *SignalManifest, signal, table, tenant string, from, to time.Time) (bool, error) {
	var (
		gz      *gzip.Writer
		inChunk int64
		part    int
	)
	closeChunk := func() error {
		if gz == nil {
			return nil
		}
		err := gz.Close()
		gz = nil
		return err
	}
	stopped := false
	err := src.Stream(ctx, table, tenant, from, to, func(row []byte) error {
		if t.rows >= t.limits.MaxRows || t.size() >= t.maxBytes {
			stopped = true
			return errStop
		}
		if gz == nil || inChunk >= t.limits.chunkRows() {
			if err := closeChunk(); err != nil {
				return err
			}
			part++
			name := fmt.Sprintf("telemetry/%s/%s-%04d.ndjson.gz", signal, from.UTC().Format("2006-01-02T15"), part)
			w, err := t.zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store, Modified: t.now()})
			if err != nil {
				return err
			}
			gz = gzip.NewWriter(w)
			inChunk = 0
			m.Files = append(m.Files, name)
		}
		if _, err := gz.Write(row); err != nil {
			return err
		}
		if _, err := gz.Write([]byte{'\n'}); err != nil {
			return err
		}
		inChunk++
		m.Rows++
		t.rows++
		t.throttle()
		return nil
	})
	if cerr := closeChunk(); cerr != nil && err == nil {
		err = cerr
	}
	if errors.Is(err, errStop) {
		return true, nil
	}
	return stopped, err
}

// countingWriter counts bytes written through it.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
