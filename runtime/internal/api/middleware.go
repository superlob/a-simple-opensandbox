package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// requestLog logs one structured line per request. Bodies, headers, and env values
// are never logged. Only the sandbox ID path parameter is included.
func requestLog(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		attrs := []any{
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
		}
		if id := c.Param("id"); id != "" {
			attrs = append(attrs, "sandbox_id", id)
		}
		logger.Info("request", attrs...)
	}
}

// recovery turns panics into a 500 error envelope.
func recovery(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("panic", "panic", r, "path", c.Request.URL.Path)
				failWith(c, http.StatusInternalServerError, CodeInternal, "internal error")
			}
		}()
		c.Next()
	}
}

// bodyLimit caps the request body for JSON endpoints.
func bodyLimit(max int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, max)
		c.Next()
	}
}
