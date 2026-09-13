package openlogsql

import (
	"context"
	"database/sql/driver"
	"errors"
	"sync"
)

// wrappedDriver instruments every connection of a parent driver.
type wrappedDriver struct {
	parent     driver.Driver
	driverName string
	opts       []Option

	mu   sync.Mutex
	cfgs map[string]*config // per DSN
}

func (d *wrappedDriver) cfg(dsn string) *config {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cfgs == nil {
		d.cfgs = map[string]*config{}
	}
	c, ok := d.cfgs[dsn]
	if !ok {
		if len(d.cfgs) > 64 {
			clear(d.cfgs)
		}
		c = newConfig(d.driverName, dsn, d.opts)
		d.cfgs[dsn] = c
	}
	return c
}

func (d *wrappedDriver) Open(name string) (driver.Conn, error) {
	c, err := d.parent.Open(name)
	if err != nil {
		return nil, err
	}
	return &wrappedConn{parent: c, cfg: d.cfg(name)}, nil
}

func (d *wrappedDriver) OpenConnector(name string) (driver.Connector, error) {
	if dc, ok := d.parent.(driver.DriverContext); ok {
		pc, err := dc.OpenConnector(name)
		if err != nil {
			return nil, err
		}
		return &wrappedConnector{parent: pc, cfg: d.cfg(name), driver: d}, nil
	}
	return &dsnConnector{name: name, driver: d}, nil
}

type dsnConnector struct {
	name   string
	driver *wrappedDriver
}

func (c *dsnConnector) Connect(context.Context) (driver.Conn, error) { return c.driver.Open(c.name) }
func (c *dsnConnector) Driver() driver.Driver                        { return c.driver }

type wrappedConnector struct {
	parent driver.Connector
	cfg    *config
	driver driver.Driver
}

func (c *wrappedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.parent.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &wrappedConn{parent: conn, cfg: c.cfg}, nil
}

func (c *wrappedConnector) Driver() driver.Driver { return c.driver }

// Close closes the parent connector if it is an io.Closer (Go 1.17+ sql.DB.Close calls it).
func (c *wrappedConnector) Close() error {
	if cl, ok := c.parent.(interface{ Close() error }); ok {
		return cl.Close()
	}
	return nil
}

type wrappedConn struct {
	parent driver.Conn
	cfg    *config
}

var (
	_ driver.Conn               = (*wrappedConn)(nil)
	_ driver.ConnBeginTx        = (*wrappedConn)(nil)
	_ driver.ConnPrepareContext = (*wrappedConn)(nil)
	_ driver.ExecerContext      = (*wrappedConn)(nil)
	_ driver.QueryerContext     = (*wrappedConn)(nil)
	_ driver.Pinger             = (*wrappedConn)(nil)
	_ driver.SessionResetter    = (*wrappedConn)(nil)
	_ driver.Validator          = (*wrappedConn)(nil)
	_ driver.NamedValueChecker  = (*wrappedConn)(nil)
)

func (c *wrappedConn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *wrappedConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	var s driver.Stmt
	var err error
	if pc, ok := c.parent.(driver.ConnPrepareContext); ok {
		s, err = pc.PrepareContext(ctx, query)
	} else {
		s, err = c.parent.Prepare(query)
	}
	if err != nil {
		return nil, err
	}
	return &wrappedStmt{parent: s, query: query, cfg: c.cfg}, nil
}

func (c *wrappedConn) Close() error { return c.parent.Close() }

//nolint:staticcheck // driver.Conn requires Begin.
func (c *wrappedConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *wrappedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if bt, ok := c.parent.(driver.ConnBeginTx); ok {
		return bt.BeginTx(ctx, opts)
	}
	if opts.Isolation != 0 || opts.ReadOnly {
		return nil, errors.New("openlogsql: driver does not support transaction options")
	}
	return c.parent.Begin() //nolint:staticcheck
}

func (c *wrappedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	ec, ok := c.parent.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip // database/sql falls back to Prepare + Stmt.Exec (instrumented)
	}
	ctx, span, traced := c.cfg.start(ctx, query)
	res, err := ec.ExecContext(ctx, query, args)
	if traced {
		if errors.Is(err, driver.ErrSkip) {
			span.End() // retried through the prepared path; that span is the real one
			return res, err
		}
		end(span, err)
	}
	return res, err
}

func (c *wrappedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	qc, ok := c.parent.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	ctx, span, traced := c.cfg.start(ctx, query)
	rows, err := qc.QueryContext(ctx, query, args)
	if traced {
		if errors.Is(err, driver.ErrSkip) {
			span.End()
			return rows, err
		}
		end(span, err)
	}
	return rows, err
}

func (c *wrappedConn) Ping(ctx context.Context) error {
	if p, ok := c.parent.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *wrappedConn) ResetSession(ctx context.Context) error {
	if r, ok := c.parent.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *wrappedConn) IsValid() bool {
	if v, ok := c.parent.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

func (c *wrappedConn) CheckNamedValue(nv *driver.NamedValue) error {
	if ch, ok := c.parent.(driver.NamedValueChecker); ok {
		return ch.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

type wrappedStmt struct {
	parent driver.Stmt
	query  string
	cfg    *config
}

var (
	_ driver.StmtExecContext   = (*wrappedStmt)(nil)
	_ driver.StmtQueryContext  = (*wrappedStmt)(nil)
	_ driver.NamedValueChecker = (*wrappedStmt)(nil)
)

func (s *wrappedStmt) Close() error  { return s.parent.Close() }
func (s *wrappedStmt) NumInput() int { return s.parent.NumInput() }

//nolint:staticcheck // driver.Stmt requires Exec.
func (s *wrappedStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.parent.Exec(args)
}

//nolint:staticcheck // driver.Stmt requires Query.
func (s *wrappedStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.parent.Query(args)
}

func (s *wrappedStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	ctx, span, traced := s.cfg.start(ctx, s.query)
	var res driver.Result
	var err error
	if ec, ok := s.parent.(driver.StmtExecContext); ok {
		res, err = ec.ExecContext(ctx, args)
	} else {
		var vals []driver.Value
		if vals, err = namedToValues(args); err == nil {
			res, err = s.parent.Exec(vals) //nolint:staticcheck
		}
	}
	if traced {
		end(span, err)
	}
	return res, err
}

func (s *wrappedStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	ctx, span, traced := s.cfg.start(ctx, s.query)
	var rows driver.Rows
	var err error
	if qc, ok := s.parent.(driver.StmtQueryContext); ok {
		rows, err = qc.QueryContext(ctx, args)
	} else {
		var vals []driver.Value
		if vals, err = namedToValues(args); err == nil {
			rows, err = s.parent.Query(vals) //nolint:staticcheck
		}
	}
	if traced {
		end(span, err)
	}
	return rows, err
}

func (s *wrappedStmt) CheckNamedValue(nv *driver.NamedValue) error {
	if ch, ok := s.parent.(driver.NamedValueChecker); ok {
		return ch.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

// ColumnConverter is forwarded for legacy drivers.
func (s *wrappedStmt) ColumnConverter(idx int) driver.ValueConverter {
	if cc, ok := s.parent.(driver.ColumnConverter); ok { //nolint:staticcheck
		return cc.ColumnConverter(idx)
	}
	return driver.DefaultParameterConverter
}

func namedToValues(args []driver.NamedValue) ([]driver.Value, error) {
	out := make([]driver.Value, len(args))
	for i, a := range args {
		if a.Name != "" {
			return nil, errors.New("openlogsql: driver does not support named parameters")
		}
		out[i] = a.Value
	}
	return out, nil
}
