package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"sandbox/execd/internal/fileops"
	"sandbox/execd/internal/security"
	"sandbox/execd/internal/store"
	"sandbox/execd/internal/task"
)

// errorBody is the JSON shape of every error response.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError maps a service error to an HTTP status and JSON body.
func writeError(c *gin.Context, err error) {
	status, code := classify(err)
	c.AbortWithStatusJSON(status, errorBody{Error: errorDetail{Code: code, Message: err.Error()}})
}

// writeBindError handles malformed request bodies and oversized bodies.
func writeBindError(c *gin.Context, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge,
			errorBody{Error: errorDetail{Code: "payload_too_large", Message: "request body exceeds the size limit"}})
		return
	}
	c.AbortWithStatusJSON(http.StatusBadRequest,
		errorBody{Error: errorDetail{Code: "invalid_request", Message: err.Error()}})
}

func classify(err error) (int, string) {
	switch {
	case errors.Is(err, fileops.ErrNotFound), errors.Is(err, task.ErrNotFound), errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, fileops.ErrOutside), errors.Is(err, security.ErrOutsideWorkspace):
		return http.StatusForbidden, "path_outside_workspace"
	case errors.Is(err, fileops.ErrForbidden):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, fileops.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, "payload_too_large"
	case errors.Is(err, fileops.ErrNotEmpty):
		return http.StatusConflict, "directory_not_empty"
	case errors.Is(err, fileops.ErrIsDirectory):
		return http.StatusBadRequest, "is_directory"
	case errors.Is(err, fileops.ErrNotDirectory):
		return http.StatusBadRequest, "not_directory"
	case errors.Is(err, task.ErrBusy):
		return http.StatusTooManyRequests, "too_many_executions"
	case errors.Is(err, task.ErrShuttingDown):
		return http.StatusServiceUnavailable, "shutting_down"
	case errors.Is(err, task.ErrInvalid):
		return http.StatusBadRequest, "invalid_request"
	default:
		return http.StatusInternalServerError, "internal_error"
	}
}
