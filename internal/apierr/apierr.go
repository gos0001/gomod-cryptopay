// Package apierr maps domain errors onto HTTP responses.
//
// The architecture contract puts this mapping in the handler, and it stays
// there — a handler with an error the generic table gets wrong still branches on
// it first and calls Write only as the fallback. What this package removes is
// the repeated tail of that switch, where every handler in the service agrees
// that an absent invoice is a 404.
package apierr

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/gos0001/gomod-cryptopay/internal/domain"
	httpserver "github.com/gos0001/gomod-cryptopay/pkg/http_server"
)

// Write maps err onto a response. Unrecognised errors are logged with their
// detail and answered with a flat 500 — the client never sees err.Error(),
// because an unmapped error is as likely to be a DSN or a constraint name as
// anything the caller could act on.
func Write(c *gin.Context, logger *zap.SugaredLogger, op string, err error) {
	switch {
	case errors.Is(err, domain.ErrInvoiceNotFound),
		errors.Is(err, domain.ErrAssetNotFound),
		errors.Is(err, domain.ErrNotFound):
		httpserver.NotFound(c, "not found")

	case errors.Is(err, domain.ErrExternalIDTaken):
		httpserver.Conflict(c, "external_id is already used for a different invoice")

	case errors.Is(err, domain.ErrAlreadyExists):
		httpserver.Conflict(c, "already exists")

	// A refused transition is almost always "someone got there first": the
	// invoice was cancelled twice, or a payment landed between the read and the
	// write. 409 rather than 400 — the request was well formed, the state moved.
	case errors.Is(err, domain.ErrInvalidTransition):
		httpserver.Conflict(c, "the invoice is no longer in a state that allows this")

	// Retryable, and the only reason this is not a 500: every payment amount for
	// this asset is held right now, and holds expire. Telling the caller to try
	// again is both true and actionable.
	case errors.Is(err, domain.ErrAmountSpaceExhausted):
		httpserver.TooManyRequests(c,
			"no free payment amount for this asset right now, retry shortly")

	case errors.Is(err, domain.ErrUnknownNetwork):
		httpserver.BadRequest(c, "unknown network")

	case errors.Is(err, domain.ErrInvalidInput):
		httpserver.BadRequest(c, "invalid request")

	case errors.Is(err, domain.ErrUnauthorized):
		httpserver.Unauthorized(c, "unauthorized")
	case errors.Is(err, domain.ErrForbidden):
		httpserver.Forbidden(c, "forbidden")

	default:
		logger.Errorw(op+" failed", "error", err)
		httpserver.InternalServerError(c, "internal server error")
	}
}

// BadRequest answers 400 with a message the caller can act on.
//
// Separate from Write because validation failures carry detail worth showing —
// "10.5000001 has 7 significant fractional digits, the token has 6" tells the
// caller exactly what to change, where a flat "invalid request" does not.
func BadRequest(c *gin.Context, msg string) {
	httpserver.BadRequest(c, msg)
}

// Message renders a validation error for the client.
//
// Validation errors are built as `fmt.Errorf("%w: amount is required",
// domain.ErrInvalidInput)`, so the sentinel's own text is a prefix that says
// nothing the status code has not already said. This trims it, leaving the half
// written for a human.
func Message(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	prefix := domain.ErrInvalidInput.Error() + ": "
	if strings.HasPrefix(msg, prefix) {
		return strings.TrimPrefix(msg, prefix)
	}
	return msg
}

// WriteInvalidInput answers 400 with the detail when err is a validation
// failure, and falls through to Write otherwise.
func WriteInvalidInput(c *gin.Context, logger *zap.SugaredLogger, op string, err error) {
	if errors.Is(err, domain.ErrInvalidInput) {
		BadRequest(c, Message(err))
		return
	}
	Write(c, logger, op, err)
}
