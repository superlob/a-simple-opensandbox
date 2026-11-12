package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"sandbox/execd/internal/config"
	"sandbox/execd/internal/fileops"
	"sandbox/execd/internal/store"
	"sandbox/execd/internal/task"
)

func setup(t *testing.T) (http.Handler, config.Config) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := config.Default()
	cfg.Port = 8080
	cfg.WorkspaceDir = t.TempDir()
	cfg.DataDir = t.TempDir()
	cfg.MaxConcurrentTasks = 4
	cfg.MaxRequestBodyBytes = 4096
	cfg.MaxUploadBytes = 1024
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.New(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := task.New(cfg, st, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mgr.Shutdown(t.Context()) })
	r := NewRouter(Deps{
		Config:    cfg,
		Files:     &fileops.Service{Workspace: cfg.WorkspaceDir, MaxUploadBytes: cfg.MaxUploadBytes},
		Tasks:     mgr,
		Logger:    logger,
		StartedAt: time.Now(),
	})
	return r, cfg
}

func do(t *testing.T, h http.Handler, method, url string, body io.Reader, ctype string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, url, body)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func jsonBody(v any) io.Reader {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

func decode(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
}

func TestHealthAndInfo(t *testing.T) {
	h, _ := setup(t)
	if w := do(t, h, "GET", "/health", nil, ""); w.Code != 200 {
		t.Errorf("health %d", w.Code)
	}
	if w := do(t, h, "GET", "/v1/info", nil, ""); w.Code != 200 {
		t.Errorf("info %d", w.Code)
	}
}

func TestForegroundCommandDefaultsCwd(t *testing.T) {
	h, cfg := setup(t)
	w := do(t, h, "POST", "/v1/command", jsonBody(map[string]any{
		"argv": []string{"sh", "-c", "pwd; echo $GREETING"},
		"env":  map[string]string{"GREETING": "hey"},
	}), "application/json")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var res struct {
		ExitCode int    `json:"exit_code"`
		Stdout   string `json:"stdout"`
	}
	decode(t, w, &res)
	if res.ExitCode != 0 || !strings.HasPrefix(res.Stdout, cfg.WorkspaceDir) || !strings.HasSuffix(res.Stdout, "hey\n") {
		t.Errorf("res %+v", res)
	}
}

func TestBackgroundSubmitAndQuery(t *testing.T) {
	h, _ := setup(t)
	w := do(t, h, "POST", "/v1/command", jsonBody(map[string]any{
		"argv": []string{"sh", "-c", "echo bg"}, "background": true,
	}), "application/json")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d", w.Code)
	}
	var sub struct {
		ID string `json:"execution_id"`
	}
	decode(t, w, &sub)
	deadline := time.Now().Add(10 * time.Second)
	for {
		var rec store.Record
		decode(t, do(t, h, "GET", "/v1/tasks/"+sub.ID, nil, ""), &rec)
		if rec.Status == task.StatusSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task stuck in %s", rec.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	lw := do(t, h, "GET", "/v1/tasks/"+sub.ID+"/logs?stream=stdout", nil, "")
	var logs struct {
		Content string `json:"content"`
	}
	decode(t, lw, &logs)
	if logs.Content != "bg\n" {
		t.Errorf("logs %q", logs.Content)
	}
	if w := do(t, h, "GET", "/v1/tasks/no-such-id", nil, ""); w.Code != 404 {
		t.Errorf("unknown task status %d", w.Code)
	}
}

func TestCommandValidationErrors(t *testing.T) {
	h, _ := setup(t)
	cases := map[string]any{
		"empty argv":  map[string]any{"argv": []string{}},
		"bad env":     map[string]any{"argv": []string{"true"}, "env": map[string]string{"1X": "v"}},
		"bad timeout": map[string]any{"argv": []string{"true"}, "timeout": "soon"},
		"cwd escape":  map[string]any{"argv": []string{"true"}, "cwd": "../.."},
	}
	for name, body := range cases {
		if w := do(t, h, "POST", "/v1/command", jsonBody(body), "application/json"); w.Code != 400 {
			t.Errorf("%s: status %d", name, w.Code)
		}
	}
}

func TestBodyLimit(t *testing.T) {
	h, _ := setup(t)
	big := strings.Repeat("a", 8192)
	w := do(t, h, "POST", "/v1/command", strings.NewReader(`{"argv":["`+big+`"]}`), "application/json")
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status %d", w.Code)
	}
}

func upload(t *testing.T, h http.Handler, path string, content string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("path", path)
	fw, _ := mw.CreateFormFile("file", "f")
	io.WriteString(fw, content)
	mw.Close()
	return do(t, h, "POST", "/v1/files", &buf, mw.FormDataContentType())
}

func TestFileEndpoints(t *testing.T) {
	h, _ := setup(t)
	if w := upload(t, h, "dir/a.txt", "hello"); w.Code != http.StatusCreated {
		t.Fatalf("upload %d: %s", w.Code, w.Body.String())
	}
	if w := do(t, h, "GET", "/v1/files?path=dir/a.txt", nil, ""); w.Code != 200 || w.Body.String() != "hello" {
		t.Errorf("download %d %q", w.Code, w.Body.String())
	}
	if w := do(t, h, "GET", "/v1/files/stat?path=dir/a.txt", nil, ""); w.Code != 200 {
		t.Errorf("stat %d", w.Code)
	}
	if w := do(t, h, "GET", "/v1/files?path=missing.txt", nil, ""); w.Code != 404 {
		t.Errorf("missing download %d", w.Code)
	}
	if w := do(t, h, "GET", "/v1/files?path=../../etc/passwd", nil, ""); w.Code != 403 {
		t.Errorf("escape download %d", w.Code)
	}
	if w := upload(t, h, "big.bin", strings.Repeat("x", 2048)); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized upload %d", w.Code)
	}
	if w := do(t, h, "POST", "/v1/dirs", jsonBody(map[string]string{"path": "x/y"}), "application/json"); w.Code != 200 {
		t.Errorf("mkdir %d", w.Code)
	}
	if w := do(t, h, "DELETE", "/v1/files?path=missing", nil, ""); w.Code != 404 {
		t.Errorf("delete missing %d", w.Code)
	}
	if w := do(t, h, "DELETE", "/v1/files?path=dir&recursive=true", nil, ""); w.Code != 204 {
		t.Errorf("recursive delete %d", w.Code)
	}
}
