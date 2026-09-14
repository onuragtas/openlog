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

// ErrLastOwner is returned when an operation would leave an organization without an owner.
var ErrLastOwner = &Error{Code: CodeFailedPrecondition, Message: "an organization must keep at least one owner"}
