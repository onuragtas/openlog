package dataexport

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type fakeRows struct {
	perHour int
	queries []string
}

func (f *fakeRows) Stream(_ context.Context, table, tenant string, from, to time.Time, fn func([]byte) error) error {
	f.queries = append(f.queries, table+"@"+from.Format("01-02T15")+".."+to.Format("01-02T15"))
	for h := from; h.Before(to); h = h.Add(time.Hour) {
		for i := 0; i < f.perHour; i++ {
			if err := fn([]byte(`{"tenant_id":"` + tenant + `","body":"x"}`)); err != nil {
				return err
			}
		}
	}
	return nil
}

func readZip(t *testing.T, b []byte) map[string][]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		gz, err := gzip.NewReader(rc)
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		sc := bufio.NewScanner(gz)
		for sc.Scan() {
			out[f.Name] = append(out[f.Name], sc.Text())
		}
		rc.Close()
	}
	return out
}

func TestTelemetryChunksAndDays(t *testing.T) {
	var buf bytes.Buffer
	cw := &countingWriter{w: &buf}
	zw := zip.NewWriter(cw)
	src := &fakeRows{perHour: 3}
	tw := &telemetryWriter{zw: zw, size: func() int64 { return cw.n }, limits: Limits{MaxRows: 1000, MaxBytes: 1 << 30, ChunkRows: 40},
		started: time.Now(), now: time.Now, sleep: func(time.Duration) {}, maxBytes: 1 << 30}
	from := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	to := from.Add(36 * time.Hour) // 12h on the 1st, 24h on the 2nd
	m, stopped, err := tw.signal(context.Background(), src, "logs", "logs", "acme", from, to)
	if err != nil || stopped {
		t.Fatal(err, stopped)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(src.queries, " ") != "logs@09-01T12..09-02T00 logs@09-02T00..09-03T00" {
		t.Fatalf("day windows %v", src.queries)
	}
	if m.Rows != 108 || tw.rows != 108 || len(m.Files) != 3 || m.CompleteTill != "2026-09-03T00:00:00Z" {
		t.Fatalf("manifest %+v", m)
	}
	files := readZip(t, buf.Bytes())
	if len(files["telemetry/logs/2026-09-01T12-0001.ndjson.gz"]) != 36 || len(files["telemetry/logs/2026-09-02T00-0001.ndjson.gz"]) != 40 ||
		len(files["telemetry/logs/2026-09-02T00-0002.ndjson.gz"]) != 32 {
		t.Fatalf("chunks %v", func() map[string]int {
			o := map[string]int{}
			for k, v := range files {
				o[k] = len(v)
			}
			return o
		}())
	}
}

func TestTelemetryRowLimitAndThrottle(t *testing.T) {
	var buf bytes.Buffer
	cw := &countingWriter{w: &buf}
	zw := zip.NewWriter(cw)
	var slept time.Duration
	start := time.Unix(0, 0)
	tw := &telemetryWriter{zw: zw, size: func() int64 { return cw.n }, limits: Limits{MaxRows: 2500, MaxBytes: 1 << 30, RowsPerSecond: 1000},
		started: start, now: func() time.Time { return start }, sleep: func(d time.Duration) { slept += d }, maxBytes: 1 << 30}
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	m, stopped, err := tw.signal(context.Background(), &fakeRows{perHour: 1000}, "traces", "spans", "acme", from, from.Add(5*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !stopped || !m.Truncated || m.Rows != 2500 || m.CompleteTill != "" {
		t.Fatalf("limit: stopped=%v %+v", stopped, m)
	}
	if slept != 3*time.Second { // after 1000 and 2000 rows: 1s + 2s behind a frozen clock
		t.Fatalf("throttle slept %s", slept)
	}
}

func TestRequestValidationAndFormat(t *testing.T) {
	s := &Service{Limits: Limits{MaxRange: 24 * time.Hour}, Now: func() time.Time { return time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC) }}
	now := s.now()
	var inv *InvalidError
	if _, err := s.RequestOrg(context.Background(), "o", "u", "en", now.Add(-time.Hour), now, []string{"logs", "profiles"}); !errors.As(err, &inv) {
		t.Fatalf("unknown signal: %v", err)
	}
	if _, err := s.RequestOrg(context.Background(), "o", "u", "en", now.Add(-48*time.Hour), now, []string{"logs"}); !errors.As(err, &inv) || !strings.Contains(inv.Msg, "OPENLOG_DATA_EXPORT_MAX_RANGE") {
		t.Fatalf("range: %v", err)
	}
	if _, err := s.RequestOrg(context.Background(), "o", "u", "en", now, now, []string{"logs"}); !errors.As(err, &inv) {
		t.Fatalf("empty range: %v", err)
	}
	for n, want := range map[int64]string{512: "512 B", 1536: "1.5 KiB", 5 << 30: "5.0 GiB"} {
		if got := FormatBytes(n); got != want {
			t.Errorf("FormatBytes(%d) = %q", n, got)
		}
	}
	if DownloadLink("https://o.test/", "olx_a") != "https://o.test/api/v1/data-exports/download?token=olx_a" {
		t.Fatal(DownloadLink("https://o.test/", "olx_a"))
	}
	_ = io.EOF
}
