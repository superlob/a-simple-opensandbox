// Package api defines the HTTP routes and handlers of Execd.
package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"sandbox/execd/internal/config"
	"sandbox/execd/internal/fileops"
	"sandbox/execd/internal/middleware"
	"sandbox/execd/internal/store"
	"sandbox/execd/internal/task"
)

// Version is reported by GET /v1/info.
const Version = "0.1.0"

// Deps are the collaborators the router needs.
type Deps struct {
	Config    config.Config
	Files     *fileops.Service
	Tasks     *task.Manager
	Logger    *slog.Logger
	StartedAt time.Time
}

// NewRouter builds the Gin engine with all routes registered.
func NewRouter(d Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery(), middleware.RequestLogger(d.Logger))

	h := &handlers{d: d}
	r.GET("/health", h.health)

	// Upload has its own, larger body limit; it is registered outside the group below.
	r.POST("/v1/files",
		middleware.BodyLimit(d.Config.MaxUploadBytes+64<<10),
		h.uploadFile)

	v1 := r.Group("/v1", middleware.BodyLimit(d.Config.MaxRequestBodyBytes))
	v1.GET("/info", h.info)

	v1.POST("/command", h.command)

	v1.GET("/files", h.downloadFile)
	v1.GET("/files/stat", h.statFile)
	v1.DELETE("/files", h.deleteFile)

	v1.POST("/dirs", h.mkdir)
	v1.GET("/dirs", h.listDir)

	v1.GET("/tasks", h.listTasks)
	v1.GET("/tasks/:id", h.getTask)
	v1.GET("/tasks/:id/logs", h.taskLogs)
	v1.POST("/tasks/:id/cancel", h.cancelTask)
	return r
}

type handlers struct {
	d Deps
}

func (h *handlers) health(c *gin.Context) {
	if h.d.Tasks.Closing() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "shutting_down"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *handlers) info(c *gin.Context) {
	running, total := h.d.Tasks.Counts()
	c.JSON(http.StatusOK, gin.H{
		"version":        Version,
		"workspace_dir":  h.d.Config.WorkspaceDir,
		"uptime_seconds": int64(time.Since(h.d.StartedAt).Seconds()),
		"tasks": gin.H{
			"running": running,
			"total":   total,
		},
		"limits": gin.H{
			"max_concurrent_tasks": h.d.Config.MaxConcurrentTasks,
			"max_upload_bytes":     h.d.Config.MaxUploadBytes,
			"max_output_bytes":     h.d.Config.MaxOutputBytes,
			"max_timeout_seconds":  h.d.Config.MaxTimeout.Std().Seconds(),
		},
	})
}

// ---- commands ----

type commandRequest struct {
	Argv       []string          `json:"argv" binding:"required"`
	Cwd        string            `json:"cwd"`
	Env        map[string]string `json:"env"`
	Timeout    string            `json:"timeout"`
	Background bool              `json:"background"`
}

type foregroundResponse struct {
	ExitCode        int       `json:"exit_code"`
	Stdout          string    `json:"stdout"`
	Stderr          string    `json:"stderr"`
	StdoutTruncated bool      `json:"stdout_truncated"`
	StderrTruncated bool      `json:"stderr_truncated"`
	TimedOut        bool      `json:"timed_out"`
	Canceled        bool      `json:"canceled"`
	DurationMs      int64     `json:"duration_ms"`
	StartedAt       time.Time `json:"started_at"`
	FinishedAt      time.Time `json:"finished_at"`
}

func (h *handlers) command(c *gin.Context) {
	var req commandRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeBindError(c, err)
		return
	}
	var timeout time.Duration
	if req.Timeout != "" {
		d, err := time.ParseDuration(req.Timeout)
		if err != nil {
			writeError(c, fmt.Errorf("%w: timeout: %v", task.ErrInvalid, err))
			return
		}
		timeout = d
	}
	tr := task.Request{Argv: req.Argv, Cwd: req.Cwd, Env: req.Env, Timeout: timeout}

	if req.Background {
		id, err := h.d.Tasks.Submit(tr)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"execution_id": id, "status": task.StatusPending})
		return
	}

	res, err := h.d.Tasks.RunForeground(c.Request.Context(), tr)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, foregroundResponse{
		ExitCode:        res.ExitCode,
		Stdout:          string(res.Stdout),
		Stderr:          string(res.Stderr),
		StdoutTruncated: res.StdoutTruncated,
		StderrTruncated: res.StderrTruncated,
		TimedOut:        res.TimedOut,
		Canceled:        res.Canceled,
		DurationMs:      res.Duration().Milliseconds(),
		StartedAt:       res.StartedAt,
		FinishedAt:      res.FinishedAt,
	})
}

// ---- files ----

func (h *handlers) uploadFile(c *gin.Context) {
	path := c.PostForm("path")
	if path == "" {
		writeError(c, fmt.Errorf("%w: form field path is required", task.ErrInvalid))
		return
	}
	fh, err := c.FormFile("file")
	if err != nil {
		writeBindError(c, err)
		return
	}
	f, err := fh.Open()
	if err != nil {
		writeError(c, err)
		return
	}
	defer f.Close()
	res, err := h.d.Files.Upload(path, f)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, res)
}

func (h *handlers) downloadFile(c *gin.Context) {
	f, info, err := h.d.Files.Open(c.Query("path"))
	if err != nil {
		writeError(c, err)
		return
	}
	defer f.Close()
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, info.Name))
	c.DataFromReader(http.StatusOK, info.Size, "application/octet-stream", f, nil)
}

func (h *handlers) statFile(c *gin.Context) {
	info, err := h.d.Files.Stat(c.Query("path"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, info)
}

func (h *handlers) deleteFile(c *gin.Context) {
	recursive := c.Query("recursive") == "true"
	if err := h.d.Files.Delete(c.Query("path"), recursive); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

type mkdirRequest struct {
	Path string `json:"path" binding:"required"`
}

func (h *handlers) mkdir(c *gin.Context) {
	var req mkdirRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeBindError(c, err)
		return
	}
	info, err := h.d.Files.MkdirAll(req.Path)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, info)
}

func (h *handlers) listDir(c *gin.Context) {
	limit := 1000
	if v := c.Query("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 10000 {
			writeError(c, fmt.Errorf("%w: limit must be 1..10000", task.ErrInvalid))
			return
		}
		limit = n
	}
	entries, truncated, err := h.d.Files.List(c.Query("path"), limit)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"entries": entries, "truncated": truncated})
}

// ---- tasks ----

func (h *handlers) listTasks(c *gin.Context) {
	tasks := h.d.Tasks.List(c.Query("status"))
	c.JSON(http.StatusOK, gin.H{"tasks": tasks})
}

func (h *handlers) getTask(c *gin.Context) {
	rec, err := h.d.Tasks.Get(c.Param("id"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, rec)
}

const (
	defaultLogChunk = 64 << 10
	maxLogChunk     = 1 << 20
)

func (h *handlers) taskLogs(c *gin.Context) {
	stream := c.DefaultQuery("stream", store.StreamStdout)
	if stream != store.StreamStdout && stream != store.StreamStderr {
		writeError(c, fmt.Errorf("%w: stream must be stdout or stderr", task.ErrInvalid))
		return
	}
	offset, err := parseNonNegative(c.DefaultQuery("offset", "0"))
	if err != nil {
		writeError(c, fmt.Errorf("%w: offset: %v", task.ErrInvalid, err))
		return
	}
	limit, err := parseNonNegative(c.DefaultQuery("limit", strconv.Itoa(defaultLogChunk)))
	if err != nil || limit == 0 || limit > maxLogChunk {
		writeError(c, fmt.Errorf("%w: limit must be 1..%d", task.ErrInvalid, maxLogChunk))
		return
	}
	data, next, eof, err := h.d.Tasks.ReadLog(c.Param("id"), stream, offset, limit)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"stream":      stream,
		"content":     string(data),
		"next_offset": next,
		"eof":         eof,
	})
}

func (h *handlers) cancelTask(c *gin.Context) {
	rec, err := h.d.Tasks.Cancel(c.Param("id"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, rec)
}

func parseNonNegative(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, errors.New("must not be negative")
	}
	return n, nil
}
