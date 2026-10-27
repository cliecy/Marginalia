package vault

import (
	"errors"
	"net/http"
)

// Error is a failure the HTTP layer reports to the client verbatim, as
// {"detail": Message} with the given Status. It mirrors the Python backend's
// VaultError (400), NotFound (404) and Conflict (409) exceptions, including
// their messages.
type Error struct {
	Status  int    // HTTP status code: 400, 404 or 409
	Message string // human-readable detail, identical to the Python text
}

func (e *Error) Error() string {
	if e.Message == "" {
		return http.StatusText(e.Status)
	}
	return e.Message
}

// Is makes errors.Is(err, vault.ErrNotFound) (and the other sentinels) match
// any *Error with the same status.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Message == "" && t.Status == e.Status
}

// Sentinels for errors.Is. They carry no message; use the constructors to
// create errors that do.
var (
	ErrInvalid  = &Error{Status: http.StatusBadRequest} // VaultError
	ErrNotFound = &Error{Status: http.StatusNotFound}   // NotFound
	ErrConflict = &Error{Status: http.StatusConflict}   // Conflict
)

// Invalid returns a 400 error (Python's VaultError).
func Invalid(msg string) error { return &Error{Status: http.StatusBadRequest, Message: msg} }

// NotFound returns a 404 error (Python's NotFound).
func NotFound(msg string) error { return &Error{Status: http.StatusNotFound, Message: msg} }

// Conflict returns a 409 error (Python's Conflict).
func Conflict(msg string) error { return &Error{Status: http.StatusConflict, Message: msg} }

// StatusOf returns the HTTP status for err: the Status of a wrapped *Error,
// or 500 for anything else (I/O failures and other unexpected errors).
func StatusOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return http.StatusInternalServerError
}
