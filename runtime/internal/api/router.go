package api

import (
	"log/slog"

	"github.com/gin-gonic/gin"

	"sandbox/runtime/internal/sandbox"
)

// Deps are the collaborators of the HTTP API.
type Deps struct {
	Service *sandbox.Service
	Logger  *slog.Logger
	// MaxBodyBytes caps JSON request bodies. Zero selects 64 KiB.
	MaxBodyBytes int64
}

// NewRouter builds the Runtime HTTP API.
func NewRouter(d Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.MaxBodyBytes <= 0 {
		d.MaxBodyBytes = 64 << 10
	}
	h := &handlers{svc: d.Service}

	r := gin.New()
	r.Use(recovery(d.Logger), requestLog(d.Logger))
	r.NoRoute(func(c *gin.Context) {
		failWith(c, 404, CodeNotFound, "no such route")
	})

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	v1 := r.Group("/v1", bodyLimit(d.MaxBodyBytes))
	v1.POST("/sandboxes", h.create)
	v1.GET("/sandboxes", h.list)
	v1.GET("/sandboxes/:id", h.get)
	v1.POST("/sandboxes/:id/start", h.start)
	v1.POST("/sandboxes/:id/stop", h.stop)
	v1.DELETE("/sandboxes/:id", h.delete)
	v1.GET("/sandboxes/:id/stats", h.stats)
	v1.GET("/sandboxes/:id/endpoint", h.endpoint)
	return r
}
