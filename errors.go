package tsuzuri

import "fmt"

// Error is a classified failure with recovery details and an optional cause.
type Error struct {
	Kind     string `json:"kind"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	ExitCode int    `json:"exitCode"`
	Details  any    `json:"details"`
	cause    error
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error               { return e.cause }
func (e *Error) causedBy(cause error) *Error { e.cause = cause; return e }
func failure(kind, message string) *Error {
	code := 3
	switch kind {
	case "usage":
		code = 2
	case "unavailable", "canonical-checkout":
		code = 4
	case "claim-conflict", "lost-ownership", "idempotency-conflict", "stale-input":
		code = 5
	case "remote":
		code = 6
	case "indeterminate":
		code = 7
	case "preserved-work", "local-materialization":
		code = 8
	}
	return &Error{Kind: kind, Code: kind, Message: message, ExitCode: code}
}
func invalid(format string, args ...any) *Error {
	return failure("validation", fmt.Sprintf(format, args...))
}
