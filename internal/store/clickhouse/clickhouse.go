// Package clickhouse opens ClickHouse connections and performs deduplicated
// batch inserts.
package clickhouse

import (
	"context"
	"fmt"
	"strings"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/onuragtas/openlog/internal/config"
)

// Conn is the driver connection type.
type Conn = driver.Conn

// Options configure a connection.
type Options struct {
	Addr     []string
	Database string
	User     string
	Password string
	// Settings are applied to every query of the connection.
	Settings map[string]any
	MaxConns int
	// TLS enables the native TLS protocol (OPENLOG_CLICKHOUSE_TLS_*). Addresses must then
	// point at the secure native port (tcp_port_secure, usually 9440). Applies to every
	// connection pool opened with these options, including direct shard connections.
	TLS config.TLS
}

// OptionsFromConfig builds Options from the common configuration.
func OptionsFromConfig(c config.Common) Options {
	return Options{Addr: c.ClickHouseAddr, Database: c.ClickHouseDatabase, User: c.ClickHouseUser, Password: c.ClickHousePassword, TLS: c.ClickHouseTLS}
}

// Open connects (lazily) and pings once.
func Open(ctx context.Context, o Options) (Conn, error) {
	maxConns := o.MaxConns
	if maxConns == 0 {
		maxConns = 20
	}
	tc, err := o.TLS.Config()
	if err != nil {
		return nil, fmt.Errorf("clickhouse tls: %w", err)
	}
	conn, err := ch.Open(&ch.Options{
		Addr:             o.Addr,
		Auth:             ch.Auth{Database: o.Database, Username: o.User, Password: o.Password},
		Settings:         ch.Settings(o.Settings),
		DialTimeout:      5 * time.Second,
		MaxOpenConns:     maxConns,
		MaxIdleConns:     maxConns,
		ConnMaxLifetime:  time.Hour,
		Compression:      &ch.Compression{Method: ch.CompressionLZ4},
		ConnOpenStrategy: ch.ConnOpenRoundRobin,
		TLS:              tc,
	})
	if err != nil {
		return nil, err
	}
	if err := conn.Ping(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("clickhouse ping: %w", err)
	}
	return conn, nil
}

// OpenRetry calls Open until it succeeds or ctx is done.
func OpenRetry(ctx context.Context, o Options, onErr func(error)) (Conn, error) {
	backoff := 500 * time.Millisecond
	for {
		conn, err := Open(ctx, o)
		if err == nil {
			return conn, nil
		}
		if onErr != nil {
			onErr(err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 10*time.Second)
	}
}

// InsertSettings are the settings used for processor inserts into Distributed
// tables (OPENLOG_PROCESSOR_INSERT_MODE=distributed).
func InsertSettings(token string) ch.Settings {
	return ch.Settings{
		"distributed_foreground_insert": 1,
		"insert_deduplicate":            1,
		"insert_deduplication_token":    token,
		// Without this, a retried block that the source table drops as a duplicate is
		// still pushed into materialized views (metrics_1m, trace_index) and counted
		// twice; measured on ClickHouse 25.8 for Distributed and local inserts.
		"deduplicate_blocks_in_dependent_materialized_views": 1,
	}
}

// LocalInsertSettings are the settings for direct inserts into a shard's
// *_local ReplicatedMergeTree table. quorum > 0 sets insert_quorum.
func LocalInsertSettings(token string, quorum int) ch.Settings {
	s := ch.Settings{
		"insert_deduplicate":                                 1,
		"insert_deduplication_token":                         token,
		"deduplicate_blocks_in_dependent_materialized_views": 1,
	}
	if quorum > 0 {
		s["insert_quorum"] = quorum
	}
	return s
}

// Insert writes rows into table (a Distributed table) as one block with the
// given deduplication token. Each row must match columns in order.
func Insert(ctx context.Context, conn Conn, table string, columns []string, token string, rows [][]any) error {
	return InsertWithSettings(ctx, conn, table, columns, InsertSettings(token), rows)
}

// InsertWithSettings writes rows into table as one block using settings.
func InsertWithSettings(ctx context.Context, conn Conn, table string, columns []string, settings ch.Settings, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	ctx = ch.Context(ctx, ch.WithSettings(settings))
	b, err := conn.PrepareBatch(ctx, "INSERT INTO "+table+" ("+strings.Join(columns, ", ")+")")
	if err != nil {
		return fmt.Errorf("prepare insert %s: %w", table, err)
	}
	defer b.Abort()
	for _, r := range rows {
		if err := b.Append(r...); err != nil {
			return fmt.Errorf("append %s: %w", table, err)
		}
	}
	if err := b.Send(); err != nil {
		return fmt.Errorf("insert %s: %w", table, err)
	}
	return nil
}
