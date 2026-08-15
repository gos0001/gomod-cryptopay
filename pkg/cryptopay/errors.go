package cryptopay

import (
	"errors"
	"fmt"
	"net/http"
)

// Sentinels for errors.Is. Every failed call returns an *APIError, and these are
// what it compares equal to, so a caller can branch on the case without knowing
// status codes:
//
//	if errors.Is(err, cryptopay.ErrNotFound) { ... }
//
// The full message stays reachable through errors.As.
var (
	ErrInvalidInput = errors.New("cryptopay: invalid input")
	ErrUnauthorized = errors.New("cryptopay: unauthorized")
	ErrForbidden    = errors.New("cryptopay: forbidden")
	ErrNotFound     = errors.New("cryptopay: not found")
	ErrConflict     = errors.New("cryptopay: conflict")
	ErrRateLimited  = errors.New("cryptopay: rate limited")
	ErrServer       = errors.New("cryptopay: server error")
)

// APIError is a response the service refused.
//
// A transport failure — no connection, a timeout, a cancelled context — is not
// an APIError: it is returned wrapped as it came, so errors.Is against
// context.DeadlineExceeded still works.
type APIError struct {
	// StatusCode is the HTTP status.
	StatusCode int
	// Message is the text from the {"error": "..."} envelope. Empty when the
	// response did not carry one, which usually means it came from something in
	// front of the service rather than from the service.
	Message string
	// Endpoint is what was called, as "POST /api/v1/invoices". Without it a
	// bare "404 not found" in a log names neither the invoice nor the route.
	Endpoint string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("cryptopay: %s: %d %s",
			e.Endpoint, e.StatusCode, http.StatusText(e.StatusCode))
	}
	return fmt.Sprintf("cryptopay: %s: %d: %s", e.Endpoint, e.StatusCode, e.Message)
}

// Is maps the status onto a sentinel. 5xx all collapse into ErrServer: from a
// caller's side there is nothing to distinguish between them, and both mean
// "retry later, this one is not your fault".
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrInvalidInput:
		return e.StatusCode == http.StatusBadRequest
	case ErrUnauthorized:
		return e.StatusCode == http.StatusUnauthorized
	case ErrForbidden:
		return e.StatusCode == http.StatusForbidden
	case ErrNotFound:
		return e.StatusCode == http.StatusNotFound
	case ErrConflict:
		return e.StatusCode == http.StatusConflict
	case ErrRateLimited:
		return e.StatusCode == http.StatusTooManyRequests
	case ErrServer:
		return e.StatusCode >= 500
	}
	return false
}
