// Package httpserver holds transport-level response helpers.
// It has zero domain imports — handlers map domain errors to these, never the
// other way round.
package httpserver

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Every success response is {"data": ...} and every failure is {"error": "..."}.
// Handlers must go through these helpers rather than calling c.JSON directly,
// so the shape stays consistent across the whole API.

func OK(c *gin.Context, data any)      { c.JSON(http.StatusOK, gin.H{"data": data}) }
func Created(c *gin.Context, data any) { c.JSON(http.StatusCreated, gin.H{"data": data}) }
func NoContent(c *gin.Context)         { c.Status(http.StatusNoContent) }

func BadRequest(c *gin.Context, msg string)   { fail(c, http.StatusBadRequest, msg) }
func Unauthorized(c *gin.Context, msg string) { fail(c, http.StatusUnauthorized, msg) }
func Forbidden(c *gin.Context, msg string)    { fail(c, http.StatusForbidden, msg) }
func NotFound(c *gin.Context, msg string)     { fail(c, http.StatusNotFound, msg) }
func Conflict(c *gin.Context, msg string)     { fail(c, http.StatusConflict, msg) }

func TooManyRequests(c *gin.Context, msg string) { fail(c, http.StatusTooManyRequests, msg) }

// InternalServerError never echoes the underlying error to the client — log it
// in the handler instead.
func InternalServerError(c *gin.Context, msg string) {
	fail(c, http.StatusInternalServerError, msg)
}

func fail(c *gin.Context, code int, msg string) {
	c.JSON(code, gin.H{"error": msg})
}
