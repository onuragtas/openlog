package postgres

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/onuragtas/openlog/internal/auth"
)

// mapErr decides what an operator is told when the database refuses a statement, and every driver error it
// does not recognise ends up as "authentication backend unavailable" — one message for an outage, a broken
// password, and a schema that is simply behind the binary. The last of those is a deployment fault with an
// obvious fix, and it was indistinguishable from the other two until 42703/42P01 were mapped.
func TestMapErrClassifiesDriverErrors(t *testing.T) {
	for code, want := range map[string]error{
		"23505": auth.ErrAlreadyExists, // unique_violation
		"23503": auth.ErrNotFound,      // foreign_key_violation
		"22P02": auth.ErrNotFound,      // invalid_text_representation
		"42703": auth.ErrSchemaBehind,  // undefined_column
		"42P01": auth.ErrSchemaBehind,  // undefined_table
	} {
		if got := mapErr(&pgconn.PgError{Code: code}); !errors.Is(got, want) {
			t.Errorf("SQLSTATE %s mapped to %v, want %v", code, got, want)
		}
	}
}

// Anything else must pass through untouched. Turning an unknown driver error into a sentinel would be
// worse than the generic message it replaces: the caller would act on a diagnosis nobody made.
func TestMapErrPassesUnknownErrorsThrough(t *testing.T) {
	original := &pgconn.PgError{Code: "08006", Message: "connection failure"} // a real outage
	got := mapErr(original)
	if !errors.Is(got, original) {
		t.Errorf("an unrecognised driver error was rewritten: %v", got)
	}
	for _, sentinel := range []error{auth.ErrSchemaBehind, auth.ErrNotFound, auth.ErrAlreadyExists} {
		if errors.Is(got, sentinel) {
			t.Errorf("a connection failure was classified as %v", sentinel)
		}
	}
	if mapErr(nil) != nil {
		t.Error("mapErr(nil) invented an error")
	}
}

// The message is the whole point of the mapping: it has to say what to do, not merely that something is
// wrong. A 503 that reads like an outage sends an operator to the database logs instead of to the migration.
func TestSchemaBehindSaysWhatToDo(t *testing.T) {
	var e *auth.Error
	if !errors.As(mapErr(&pgconn.PgError{Code: "42703"}), &e) {
		t.Fatal("ErrSchemaBehind is not an *auth.Error, so Service.fail will flatten it into the generic message")
	}
	if e.Code != auth.CodeUnavailable {
		t.Errorf("code = %q, want unavailable: the request genuinely cannot be served", e.Code)
	}
	if !strings.Contains(e.Message, "migrate") {
		t.Errorf("message %q does not name the fix", e.Message)
	}
}
