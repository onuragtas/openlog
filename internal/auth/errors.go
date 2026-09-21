package auth

import "fmt"

// Code is a machine-readable error code (docs/contracts/api.md).
type Code string

// Error codes returned by the auth service.
const (
	CodeInvalidArgument    Code = "invalid_argument"
	CodeUnauthenticated    Code = "unauthenticated"
	CodePermissionDenied   Code = "permission_denied"
	CodeNotFound           Code = "not_found"
	CodeAlreadyExists      Code = "already_exists"
	CodeFailedPrecondition Code = "failed_precondition"
	CodeResourceExhausted  Code = "resource_exhausted"
	CodeUnavailable        Code = "unavailable"
	// CodeQuotaExceeded: a plan limit (e.g. users in SaaS mode) forbids the operation (HTTP 403).
	CodeQuotaExceeded Code = "quota_exceeded"
)

// Error is an auth error with a code and a message that is safe to show to
// clients. Sentinels (empty Message) match any Error of the same code with
// errors.Is.
type Error struct {
	Code    Code
	Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Message
}

// Is matches sentinel errors by code.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code && t.Message == ""
}

// Sentinel errors for errors.Is.
var (
	ErrInvalidArgument    = &Error{Code: CodeInvalidArgument}
	ErrUnauthenticated    = &Error{Code: CodeUnauthenticated}
	ErrPermissionDenied   = &Error{Code: CodePermissionDenied}
	ErrNotFound           = &Error{Code: CodeNotFound}
	ErrAlreadyExists      = &Error{Code: CodeAlreadyExists}
	ErrFailedPrecondition = &Error{Code: CodeFailedPrecondition}
	ErrResourceExhausted  = &Error{Code: CodeResourceExhausted}
	ErrUnavailable        = &Error{Code: CodeUnavailable}
)

func newError(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ErrSchemaBehind is returned when the database is missing a column or table this build requires — the
// binary is ahead of the schema.
//
// It is an *Error, so Service.fail passes it through instead of flattening it into the generic store
// failure, and an operator reads the actual problem in the UI rather than only in the log. The status stays
// 503: the request really cannot be served. What changes is that "unavailable" stops meaning "the database
// is down" when it means "run the migration".
//
// Rolling updates are supposed to make this unreachable: migrations run for N+1 before N+1 services start
// (releases-updates.md §6), so the supported mixed state is an older binary against a newer schema, never
// the reverse. Reaching this message means that order was not followed — a skipped or failed migrate step.
var ErrSchemaBehind = &Error{
	Code:    CodeUnavailable,
	Message: "the database schema is behind this build: run openlog-migrate, then retry",
}

// ErrLastOwner is returned when an operation would leave an organization without an owner.
var ErrLastOwner = &Error{Code: CodeFailedPrecondition, Message: "an organization must keep at least one owner"}
