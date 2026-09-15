//go:build windows

package iis

import (
	"context"
	"errors"
	"fmt"

	"github.com/yusufpapurcu/wmi"
)

func platformSource() Source { return wmiSource{} }

// wmiSource queries the raw performance counter classes. "SELECT *" keeps the query valid when a Windows version lacks
// a property; the missing struct fields are reported as ErrMissingFields while everything else is used.
type wmiSource struct{}

func (wmiSource) WebServices(ctx context.Context) ([]WebService, error) {
	var out []WebService
	err := query(ctx, "SELECT * FROM Win32_PerfRawData_W3SVC_WebService", &out)
	return out, err
}

func (wmiSource) AppPools(ctx context.Context) ([]AppPool, error) {
	var out []AppPool
	err := query(ctx, "SELECT * FROM Win32_PerfRawData_APPPOOLCountersProvider_APPPOOLWAS", &out)
	return out, err
}

// query runs a WMI query bounded by ctx (wmi.Query itself cannot be cancelled; a late result is discarded).
func query[T any](ctx context.Context, q string, dst *[]T) error {
	type result struct {
		rows []T
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		var rows []T
		err := wmi.Query(q, &rows)
		ch <- result{rows, err}
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case r := <-ch:
		*dst = r.rows
		var fm *wmi.ErrFieldMismatch
		if errors.As(r.err, &fm) {
			return fmt.Errorf("%w (%v)", ErrMissingFields, fm)
		}
		return r.err
	}
}
