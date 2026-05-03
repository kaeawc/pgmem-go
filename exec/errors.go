package exec

import "fmt"

// SQLError is an executor-level error that carries a Postgres SQLSTATE.
// The wire layer pulls the code out via errors.As so it can populate
// ErrorResponse.Code — pgx and similar clients pattern-match on the
// SQLSTATE, not the message.
//
// Plain Go errors returned from operators surface as XX000 (internal
// error). Use SQLError whenever a constraint, type mismatch, or other
// SQL-visible failure has a defined SQLSTATE in the PG manual.
type SQLError struct {
	Code    string // five-character SQLSTATE, e.g. "23502"
	Message string
}

func (e *SQLError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// NotNullViolation reports SQLSTATE 23502 in the format real PG uses:
// `null value in column "<col>" of relation "<table>" violates not-null
// constraint`. Clients (notably sqlc-generated test code) sometimes
// parse the message in addition to the code.
func NotNullViolation(table, column string) *SQLError {
	return &SQLError{
		Code:    "23502",
		Message: fmt.Sprintf("null value in column %q of relation %q violates not-null constraint", column, table),
	}
}

// UniqueViolation reports SQLSTATE 23505 with the message PG produces
// for a single-column unique constraint:
// `duplicate key value violates unique constraint "<table>_<col>_key"`.
// The constraint name follows the implicit-name convention PG uses for
// column-level UNIQUE; sqlc test code sometimes parses it.
func UniqueViolation(table, column string) *SQLError {
	return &SQLError{
		Code:    "23505",
		Message: fmt.Sprintf("duplicate key value violates unique constraint %q", table+"_"+column+"_key"),
	}
}
