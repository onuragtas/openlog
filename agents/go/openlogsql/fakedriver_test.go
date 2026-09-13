package openlogsql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync/atomic"
)

// fakeDriver is a minimal database/sql driver. With legacy=true connections implement
// neither ExecerContext nor QueryerContext, forcing database/sql's prepare path.
type fakeDriver struct {
	legacy bool
	execs  atomic.Int64
}

var errBoom = errors.New("boom")

func init() {
	sql.Register("fakedb", &fakeDriver{})
	sql.Register("fakedb-legacy", &fakeDriver{legacy: true})
}

func (d *fakeDriver) Open(string) (driver.Conn, error) {
	c := &fakeConn{d: d}
	if d.legacy {
		return &legacyConn{c}, nil
	}
	return c, nil
}

type fakeConn struct{ d *fakeDriver }

func (c *fakeConn) Prepare(q string) (driver.Stmt, error) { return &fakeStmt{c: c, q: q}, nil }
func (c *fakeConn) Close() error                          { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)             { return fakeTx{}, nil }

func (c *fakeConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	return c.exec(q)
}

func (c *fakeConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	return c.query(q)
}

func (c *fakeConn) exec(q string) (driver.Result, error) {
	c.d.execs.Add(1)
	if q == "FAIL" {
		return nil, errBoom
	}
	return driver.RowsAffected(1), nil
}

func (c *fakeConn) query(q string) (driver.Rows, error) {
	if q == "FAIL" {
		return nil, errBoom
	}
	return &fakeRows{}, nil
}

// legacyConn hides the context interfaces of fakeConn.
type legacyConn struct{ c *fakeConn }

func (l *legacyConn) Prepare(q string) (driver.Stmt, error) { return &fakeStmt{c: l.c, q: q}, nil }
func (l *legacyConn) Close() error                          { return nil }
func (l *legacyConn) Begin() (driver.Tx, error)             { return fakeTx{}, nil }

type fakeStmt struct {
	c *fakeConn
	q string
}

func (s *fakeStmt) Close() error                               { return nil }
func (s *fakeStmt) NumInput() int                              { return -1 }
func (s *fakeStmt) Exec([]driver.Value) (driver.Result, error) { return s.c.exec(s.q) }
func (s *fakeStmt) Query([]driver.Value) (driver.Rows, error)  { return s.c.query(s.q) }

type fakeTx struct{}

func (fakeTx) Commit() error   { return nil }
func (fakeTx) Rollback() error { return nil }

type fakeRows struct{ done bool }

func (r *fakeRows) Columns() []string { return []string{"n"} }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = int64(1)
	return nil
}
