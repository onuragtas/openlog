package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/tenant"
)

type failingConn struct {
	driver.Conn
	err error
}

func (c failingConn) Query(context.Context, string, ...any) (driver.Rows, error) { return nil, c.err }

// Per-tenant query limits (D-047): an expensive query is 422, load-dependent limits 429 with Retry-After.
func TestQueryLimitErrors(t *testing.T) {
	res, _ := tenant.ParseStatic("key-a=tenant-a")
	for _, tc := range []struct {
		code       int32
		msg        string
		status     int
		retryAfter bool
		errCode    string
	}{
		{241, "Memory limit (for query) exceeded", http.StatusUnprocessableEntity, false, "resource_exhausted"},
		{158, "Limit for rows (controlled by 'max_rows_to_read' setting) exceeded", http.StatusUnprocessableEntity, false, "resource_exhausted"},
		{307, "Limit for (uncompressed) bytes to read exceeded", http.StatusUnprocessableEntity, false, "resource_exhausted"},
		{201, "Quota for user exceeded", http.StatusTooManyRequests, true, "resource_exhausted"},
		{202, "Too many simultaneous queries", http.StatusTooManyRequests, true, "resource_exhausted"},
		{241, "Memory limit (total) exceeded", http.StatusTooManyRequests, true, "resource_exhausted"},
		{159, "Timeout exceeded", http.StatusGatewayTimeout, false, "timeout"},
		// Cold (S3) parts unreadable: 503 storage_unavailable, retryable (query/storage.go).
		{499, "S3 exception: Access Denied", http.StatusServiceUnavailable, true, "storage_unavailable"},
		{209, "Timeout exceeded while reading from ReadBufferFromS3", http.StatusServiceUnavailable, true, "storage_unavailable"},
	} {
		conn := failingConn{err: &ch.Exception{Code: tc.code, Message: tc.msg}}
		s := New(config.API{QueryTimeout: time.Second, MaxRows: 100}, query.New(conn, "openlog", time.Second),
			auth.StaticAuthenticator{Resolver: res}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/hosts", nil)
		req.Header.Set("Authorization", "Bearer key-a")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		var body struct {
			Error struct {
				Code, Message string
				Retryable     bool
			}
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != tc.status || body.Error.Code != tc.errCode || (rec.Header().Get("Retry-After") != "") != tc.retryAfter ||
			body.Error.Retryable != (tc.errCode == "storage_unavailable") {
			t.Errorf("code %d %q: HTTP %d %s Retry-After %q", tc.code, tc.msg, rec.Code, rec.Body, rec.Header().Get("Retry-After"))
		}
	}
}
