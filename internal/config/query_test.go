package config

import (
	"strings"
	"testing"
)

func TestQueryLimits(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if d := c.Query.Defaults; d.MaxMemoryUsage != 2<<30 || d.MaxRowsToRead != 2_000_000_000 || d.MaxBytesToRead != 0 {
		t.Errorf("defaults = %+v", d)
	}
	if c.ClickHouseRead != (ClickHouseRead{}) {
		t.Errorf("read user default = %+v", c.ClickHouseRead)
	}

	c, err = Load(env(map[string]string{
		"OPENLOG_CLICKHOUSE_READ_USER":     "openlog_reader",
		"OPENLOG_CLICKHOUSE_READ_PASSWORD": "pw",
		"OPENLOG_QUERY_MAX_MEMORY_USAGE":   "1000",
		"OPENLOG_QUERY_MAX_BYTES_TO_READ":  "5000",
		"OPENLOG_QUERY_TENANT_LIMITS":      " big:max_memory_usage=8000;max_rows_to_read=0 , small:max_bytes_to_read=10",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.ClickHouseRead.User != "openlog_reader" || c.ClickHouseRead.Password != "pw" {
		t.Errorf("read user = %+v", c.ClickHouseRead)
	}
	q := c.Query
	if got := q.Limits("big"); got != (QueryLimits{MaxMemoryUsage: 8000, MaxRowsToRead: 0, MaxBytesToRead: 5000}) {
		t.Errorf("big = %+v", got)
	}
	if got := q.Limits("small"); got != (QueryLimits{MaxMemoryUsage: 1000, MaxRowsToRead: 2_000_000_000, MaxBytesToRead: 10}) {
		t.Errorf("small = %+v", got)
	}
	if got := q.Limits("other"); got != q.Defaults {
		t.Errorf("other = %+v", got)
	}
	if ids := q.TenantIDs(); strings.Join(ids, ",") != "big,small" {
		t.Errorf("tenant ids = %v", ids)
	}

	for v, want := range map[string]string{
		"OPENLOG_QUERY_TENANT_LIMITS=big":                                       "want tenant:key=value",
		"OPENLOG_QUERY_TENANT_LIMITS=Big:max_memory_usage=1":                    "want tenant:key=value",
		"OPENLOG_QUERY_TENANT_LIMITS=big:max_memory=1":                          "unknown key",
		"OPENLOG_QUERY_TENANT_LIMITS=big:max_memory_usage=-1":                   "integer >= 0",
		"OPENLOG_QUERY_TENANT_LIMITS=a:max_rows_to_read=1,a:max_rows_to_read=2": "listed twice",
		"OPENLOG_QUERY_MAX_ROWS_TO_READ=-5":                                     "must be >= 0",
		"OPENLOG_CLICKHOUSE_READ_PASSWORD=pw":                                   "OPENLOG_CLICKHOUSE_READ_USER is empty",
	} {
		k, val, _ := strings.Cut(v, "=")
		if _, err := Load(env(map[string]string{k: val})); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err %v, want %q", v, err, want)
		}
	}
}
