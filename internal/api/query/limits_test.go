package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/onuragtas/openlog/internal/config"
)

func TestSettingsPerTenant(t *testing.T) {
	db := New(nil, "openlog", 20*time.Second)
	if s := db.Settings("t1"); s["max_execution_time"] != 20 || s["max_memory_usage"] != nil {
		t.Errorf("without limits: %v", s)
	}
	db.SetLimits("alert", config.Query{
		Defaults: config.QueryLimits{MaxMemoryUsage: 1 << 30, MaxRowsToRead: 1000},
		Tenants:  map[string]config.QueryLimits{"big": {MaxMemoryUsage: 8 << 30, MaxBytesToRead: 5}},
	})
	s := db.Settings("t1")
	if s["max_memory_usage"] != int64(1<<30) || s["max_rows_to_read"] != int64(1000) || s["max_bytes_to_read"] != nil {
		t.Errorf("defaults: %v", s)
	}
	var comment map[string]string
	if err := json.Unmarshal([]byte(s["log_comment"].(string)), &comment); err != nil || comment["component"] != "alert" || comment["tenant_id"] != "t1" {
		t.Errorf("log_comment %v: %v", s["log_comment"], err)
	}
	s = db.Settings("big")
	if s["max_memory_usage"] != int64(8<<30) || s["max_rows_to_read"] != nil || s["max_bytes_to_read"] != int64(5) {
		t.Errorf("override: %v", s)
	}
}

func TestAsLimitError(t *testing.T) {
	for _, tc := range []struct {
		err       error
		limit     string
		retryable bool
	}{
		{&ch.Exception{Code: 241, Message: "Memory limit (for query) exceeded"}, "max_memory_usage", false},
		{fmt.Errorf("scan: %w", &ch.Exception{Code: 241, Message: "Memory limit (total) exceeded"}), "server_memory", true},
		{&ch.Exception{Code: 158, Message: "Limit for rows"}, "max_rows_to_read", false},
		{&ch.Exception{Code: 307, Message: "Limit for bytes"}, "max_bytes_to_read", false},
		{&ch.Exception{Code: 201, Message: "Quota exceeded"}, "quota", true},
		{&ch.Exception{Code: 202, Message: "Too many simultaneous queries"}, "max_concurrent_queries", true},
		{&ch.Exception{Code: 60}, "", false},
		{context.DeadlineExceeded, "", false},
	} {
		le, ok := AsLimitError(tc.err)
		if tc.limit == "" {
			if ok {
				t.Errorf("%v classified as %+v", tc.err, le)
			}
			continue
		}
		if !ok || le.Limit != tc.limit || le.Retryable != tc.retryable || !errors.Is(le, tc.err) && !errors.Is(tc.err, le.Err) {
			t.Errorf("%v: %+v ok=%v", tc.err, le, ok)
		}
	}
}

type errConn struct {
	driver.Conn
	queryErr, rowsErr error
}

func (c errConn) Query(context.Context, string, ...any) (driver.Rows, error) {
	if c.queryErr != nil {
		return nil, c.queryErr
	}
	return errRows{err: c.rowsErr}, nil
}

type errRows struct {
	driver.Rows
	err error
}

func (errRows) Next() bool   { return false }
func (errRows) Close() error { return nil }
func (r errRows) Err() error { return r.err }

// Errors from the query call and from streaming the result both come back as *LimitError.
func TestQueryClassifiesLimitErrors(t *testing.T) {
	ex := &ch.Exception{Code: 241, Message: "Memory limit (for query) exceeded"}
	for name, conn := range map[string]errConn{"query": {queryErr: ex}, "rows": {rowsErr: ex}} {
		sc, _ := New(conn, "openlog", time.Second).Scope("t1")
		rows, err := sc.Query(context.Background(), sc.From(Hosts).Columns("host_id"))
		if err == nil {
			rows.Next()
			err = rows.Err()
		}
		var le *LimitError
		if !errors.As(err, &le) || le.Limit != "max_memory_usage" {
			t.Errorf("%s: %v", name, err)
		}
	}
}
