package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"sandbox/runtime/internal/sandbox"
)

type handlers struct {
	svc *sandbox.Service
}

// createBody is the JSON accepted by POST /v1/sandboxes. There is no image field:
// the image is fixed by Runtime configuration.
type createBody struct {
	Resources *struct {
		CPUs      float64 `json:"cpus"`
		MemoryMB  int64   `json:"memory_mb"`
		PidsLimit int64   `json:"pids_limit"`
	} `json:"resources"`
	Env            map[string]string `json:"env"`
	TimeoutSeconds *int              `json:"timeout_seconds"`
}

type resourcesJSON struct {
	CPUs      float64 `json:"cpus"`
	MemoryMB  int64   `json:"memory_mb"`
	PidsLimit int64   `json:"pids_limit"`
}

type sandboxJSON struct {
	ID        string        `json:"id"`
	ClientID  string        `json:"client_id,omitempty"`
	Image     string        `json:"image"`
	Status    string        `json:"status"`
	CreatedAt time.Time     `json:"created_at"`
	Resources resourcesJSON `json:"resources"`
	Endpoint  string        `json:"endpoint,omitempty"`
}

func toJSON(sb sandbox.Sandbox) sandboxJSON {
	return sandboxJSON{
		ID:        sb.ID,
		ClientID:  sb.ClientID,
		Image:     sb.Image,
		Status:    sb.Status,
		CreatedAt: sb.CreatedAt,
		Resources: resourcesJSON{CPUs: sb.CPUs, MemoryMB: sb.MemoryMB, PidsLimit: sb.PidsLimit},
		Endpoint:  sb.Endpoint,
	}
}

// decodeStrict decodes exactly one JSON object and rejects unknown fields and trailing data.
func decodeStrict(r io.Reader, v any) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil // empty body means all defaults
		}
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

func (h *handlers) create(c *gin.Context) {
	var body createBody
	if err := decodeStrict(c.Request.Body, &body); err != nil {
		var tooBig *http.MaxBytesError
		switch {
		case errors.As(err, &tooBig):
			failWith(c, http.StatusRequestEntityTooLarge, "payload_too_large", "request body too large")
		case strings.Contains(err.Error(), `unknown field "image"`):
			failWith(c, http.StatusBadRequest, CodeInvalidArgument,
				"image cannot be set: the sandbox image is fixed by Runtime configuration")
		default:
			failWith(c, http.StatusBadRequest, CodeInvalidArgument, "invalid JSON: "+err.Error())
		}
		return
	}

	req := sandbox.CreateRequest{Env: body.Env}
	if body.Resources != nil {
		req.CPUs = body.Resources.CPUs
		req.MemoryMB = body.Resources.MemoryMB
		req.PidsLimit = body.Resources.PidsLimit
	}
	if body.TimeoutSeconds != nil {
		if *body.TimeoutSeconds < 0 {
			failWith(c, http.StatusBadRequest, CodeInvalidArgument, "timeout_seconds must be >= 0")
			return
		}
		req.Timeout = time.Duration(*body.TimeoutSeconds) * time.Second
	}
	if key := c.GetHeader("Idempotency-Key"); key != "" {
		if len(key) > 128 || strings.ContainsAny(key, " \t\r\n") {
			failWith(c, http.StatusBadRequest, CodeInvalidArgument, "Idempotency-Key must be at most 128 non-space characters")
			return
		}
		req.ClientID = key
	}

	sb, err := h.svc.Create(c.Request.Context(), req)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, toJSON(sb))
}

var allowedStatus = map[string]bool{
	sandbox.StatusCreating: true, sandbox.StatusStarting: true, sandbox.StatusReady: true,
	sandbox.StatusUnhealthy: true, sandbox.StatusStopped: true, sandbox.StatusUnknown: true,
}

func (h *handlers) list(c *gin.Context) {
	status := c.Query("status")
	if status != "" && !allowedStatus[status] {
		failWith(c, http.StatusBadRequest, CodeInvalidArgument, fmt.Sprintf("unknown status %q", status))
		return
	}
	items, err := h.svc.List(c.Request.Context(), status)
	if err != nil {
		fail(c, err)
		return
	}
	out := make([]sandboxJSON, 0, len(items))
	for _, sb := range items {
		out = append(out, toJSON(sb))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func (h *handlers) get(c *gin.Context) {
	sb, err := h.svc.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, toJSON(sb))
}

func (h *handlers) start(c *gin.Context) {
	sb, err := h.svc.Start(c.Request.Context(), c.Param("id"))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, toJSON(sb))
}

func (h *handlers) stop(c *gin.Context) {
	sb, err := h.svc.Stop(c.Request.Context(), c.Param("id"))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, toJSON(sb))
}

func (h *handlers) delete(c *gin.Context) {
	if err := h.svc.Delete(c.Request.Context(), c.Param("id")); err != nil {
		fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *handlers) stats(c *gin.Context) {
	st, err := h.svc.Stats(c.Request.Context(), c.Param("id"))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"cpu_percent":        st.CPUPercent,
		"memory_usage_bytes": st.MemoryUsage,
		"memory_limit_bytes": st.MemoryLimit,
		"pids_current":       st.PidsCurrent,
		"read_at":            st.Read,
	})
}

func (h *handlers) endpoint(c *gin.Context) {
	sb, err := h.svc.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		fail(c, err)
		return
	}
	if sb.Endpoint == "" {
		failWith(c, http.StatusConflict, CodeConflict, "sandbox has no reachable endpoint (status: "+sb.Status+")")
		return
	}
	c.JSON(http.StatusOK, gin.H{"endpoint": sb.Endpoint})
}
