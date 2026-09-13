package postgresql

import (
	"fmt"
	"math/big"
	"strconv"

	"github.com/jackc/pgx/v5/pgtype"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

type commonKV = commonpb.KeyValue

// str converts a query value to a string (NULL → "").
func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case fmt.Stringer:
		return x.String()
	}
	return fmt.Sprint(v)
}

// i64 converts a query value to int64 (NULL/unparsable → 0).
func i64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int32:
		return int64(x)
	case int16:
		return int64(x)
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case float32:
		return int64(x)
	case pgtype.Numeric:
		if f, err := x.Float64Value(); err == nil && f.Valid {
			return int64(f.Float64)
		}
		return 0
	case *big.Int:
		return x.Int64()
	}
	n, err := strconv.ParseInt(str(v), 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(str(v), 64)
		if ferr != nil {
			return 0
		}
		return int64(f)
	}
	return n
}

// f64 converts a query value to float64.
func f64(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int64:
		return float64(x)
	case int32:
		return float64(x)
	case pgtype.Numeric:
		if f, err := x.Float64Value(); err == nil && f.Valid {
			return f.Float64
		}
		return 0
	}
	f, _ := strconv.ParseFloat(str(v), 64)
	return f
}
