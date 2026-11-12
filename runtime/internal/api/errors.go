// Package api exposes the Runtime HTTP API (runtime-design §1.3, plan R6).
package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"sandbox/runtime/internal/sandbox"
)

// Error codes returned in the "error.code" field.
const (
	CodeInvalidArgument   = "invalid_argument"
	CodeNotFound          = "not_found"
	CodeConflict          = "conflict"
	CodeResourceExhaust   = "resource_exhausted"
	CodeTimeout           = "timeout"
	CodeImageUnavailable  = "image_unavailable"
	CodeDockerUnavailable = "docker_unavailable"
	CodeStartFailed       = "start_failed"
	CodeInternal          = "internal"
)

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// statusAndCode maps a service error onto an HTTP status and error code.
func statusAndCode(err error) (int, string) {
	switch {
	case errors.Is(err, sandbox.ErrInvalid):
		return http.StatusBadRequest, CodeInvalidArgument
	case errors.Is(err, sandbox.ErrNotFound):
		return http.StatusNotFound, CodeNotFound
	case errors.Is(err, sandbox.ErrConflict):
		return http.StatusConflict, CodeConflict
	case errors.Is(err, sandbox.ErrResourceExhausted):
		return http.StatusTooManyRequests, CodeResourceExhaust
	case errors.Is(err, sandbox.ErrTimeout):
		return http.StatusGatewayTimeout, CodeTimeout
	case errors.Is(err, sandbox.ErrImageUnavailable):
		return http.StatusServiceUnavailable, CodeImageUnavailable
	case errors.Is(err, sandbox.ErrDockerUnavailable):
		return http.StatusServiceUnavailable, CodeDockerUnavailable
	case errors.Is(err, sandbox.ErrStartFailed):
		return http.StatusInternalServerError, CodeStartFailed
	default:
		return http.StatusInternalServerError, CodeInternal
	}
}

// fail writes the error envelope. Internal details are logged, not returned.
func fail(c *gin.Context, err error) {
	status, code := statusAndCode(err)
	msg := err.Error()
	if code == CodeInternal {
		msg = "internal error"
	}
	c.AbortWithStatusJSON(status, errorBody{Error: errorDetail{Code: code, Message: msg}})
}

// failWith writes an error with an explicit status and code.
func failWith(c *gin.Context, status int, code, msg string) {
	c.AbortWithStatusJSON(status, errorBody{Error: errorDetail{Code: code, Message: msg}})
}
