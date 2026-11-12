package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"sandbox/runtime/internal/config"
	"sandbox/runtime/internal/docker"
	"sandbox/runtime/internal/endpoint"
	"sandbox/runtime/internal/sandbox"
)

// memDocker is a minimal in-memory Docker: containers become healthy as soon as they start.
type memDocker struct {
	mu         sync.Mutex
	containers map[string]*memC
	failCreate error
	noImage    bool
}

type memC struct {
	labels  map[string]string
	status  string
	running bool
}

func newMemDocker() *memDocker { return &memDocker{containers: map[string]*memC{}} }

func (m *memDocker) ImageExists(_ context.Context, _ string) (bool, error) {
	if m.noImage {
		return false, nil
	}
	return true, nil
}

func (m *memDocker) ContainerCreate(_ context.Context, spec docker.Spec) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failCreate != nil {
		return "", m.failCreate
	}
	m.containers[spec.Name] = &memC{labels: spec.Labels, status: docker.StateCreated}
	return spec.Name, nil
}

func (m *memDocker) get(ref string) (*memC, error) {
	c, ok := m.containers[ref]
	if !ok {
		return nil, fmt.Errorf("%w: %s", docker.ErrNotFound, ref)
	}
	return c, nil
}

func (m *memDocker) CopyFileToContainer(_ context.Context, id, _, _ string, _ []byte, _ int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err := m.get(id)
	return err
}

func (m *memDocker) ContainerStart(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, err := m.get(id)
	if err != nil {
		return err
	}
	c.status, c.running = docker.StateRunning, true
	return nil
}

func (m *memDocker) ContainerStop(_ context.Context, id string, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, err := m.get(id)
	if err != nil {
		return err
	}
	c.status, c.running = docker.StateExited, false
	return nil
}

func (m *memDocker) ContainerRemove(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.get(id); err != nil {
		return err
	}
	delete(m.containers, id)
	return nil
}

func (m *memDocker) ContainerInspect(_ context.Context, id string) (docker.Inspect, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, err := m.get(id)
	if err != nil {
		return docker.Inspect{}, err
	}
	in := docker.Inspect{
		Name: id, Labels: c.labels, Status: c.status, Running: c.running,
		Health: docker.HealthNone, HostPorts: map[string]int{},
	}
	if c.running {
		in.Health = docker.HealthHealthy
		in.HostPorts["44772/tcp"] = 49153
	}
	return in, nil
}

func (m *memDocker) ContainersByLabel(_ context.Context, key, value string) ([]docker.Summary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []docker.Summary
	for name, c := range m.containers {
		if c.labels[key] == value {
			out = append(out, docker.Summary{Name: name, Labels: c.labels, Status: c.status})
		}
	}
	return out, nil
}

func (m *memDocker) ContainerStatsOnce(_ context.Context, id string) (docker.Stats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.get(id); err != nil {
		return docker.Stats{}, err
	}
	return docker.Stats{CPUPercent: 3, MemoryUsage: 100, MemoryLimit: 1000, PidsCurrent: 2, Read: time.Unix(0, 0)}, nil
}

type harness struct {
	srv    *httptest.Server
	docker *memDocker
	logs   *bytes.Buffer
}

func newHarness(t *testing.T, mutate func(*config.Config), d func(*memDocker)) *harness {
	t.Helper()
	cfg, err := config.LoadFrom(func(k string) string {
		return map[string]string{
			"RUNTIME_BIND_HOST":   "127.0.0.1",
			"RUNTIME_PUBLIC_HOST": "public.test",
			"RUNTIME_PORT":        "8080",
			"EXECD_PORT":          "44772",
		}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(&cfg)
	}
	md := newMemDocker()
	if d != nil {
		d(md)
	}
	var logs bytes.Buffer
	svc := sandbox.NewService(cfg, md, endpoint.NewRegistry(), sandbox.Options{PollInterval: time.Millisecond})
	router := NewRouter(Deps{
		Service:      svc,
		Logger:       slog.New(slog.NewJSONHandler(&logs, nil)),
		MaxBodyBytes: 4096,
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return &harness{srv: srv, docker: md, logs: &logs}
}

func (h *harness) do(t *testing.T, method, path, body string, headers map[string]string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, h.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if resp.StatusCode != http.StatusNoContent {
		_ = json.NewDecoder(resp.Body).Decode(&out)
	}
	return resp.StatusCode, out
}

func errCode(t *testing.T, body map[string]any) string {
	t.Helper()
	e, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error envelope in %v", body)
	}
	code, _ := e["code"].(string)
	return code
}

func createSandbox(t *testing.T, h *harness) string {
	t.Helper()
	status, body := h.do(t, http.MethodPost, "/v1/sandboxes", `{}`, nil)
	if status != http.StatusCreated {
		t.Fatalf("create: status %d body %v", status, body)
	}
	return body["id"].(string)
}

func TestHealth(t *testing.T) {
	h := newHarness(t, nil, nil)
	status, body := h.do(t, http.MethodGet, "/health", "", nil)
	if status != 200 || body["status"] != "ok" {
		t.Fatalf("health = %d %v", status, body)
	}
}

func TestCreateReturnsSandboxWithResources(t *testing.T) {
	h := newHarness(t, nil, nil)
	status, body := h.do(t, http.MethodPost, "/v1/sandboxes",
		`{"resources":{"cpus":2,"memory_mb":512,"pids_limit":256},"env":{"FOO":"bar"},"timeout_seconds":30}`, nil)
	if status != http.StatusCreated {
		t.Fatalf("status = %d body %v", status, body)
	}
	if body["status"] != "ready" || body["endpoint"] != "http://public.test:49153" {
		t.Errorf("body = %v", body)
	}
	res := body["resources"].(map[string]any)
	if res["cpus"] != 2.0 || res["memory_mb"] != 512.0 || res["pids_limit"] != 256.0 {
		t.Errorf("resources = %v", res)
	}
	if body["image"] != "sandbox-base:py3.13-uv0.13.7" {
		t.Errorf("image = %v", body["image"])
	}
}

func TestCreateRejectsImageField(t *testing.T) {
	h := newHarness(t, nil, nil)
	status, body := h.do(t, http.MethodPost, "/v1/sandboxes", `{"image":"evil:latest"}`, nil)
	if status != http.StatusBadRequest || errCode(t, body) != CodeInvalidArgument {
		t.Fatalf("status %d body %v", status, body)
	}
	if msg := body["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "fixed by Runtime") {
		t.Errorf("message = %q", msg)
	}
	if len(h.docker.containers) != 0 {
		t.Error("no container should be created")
	}
}

func TestCreateRejectsMalformedBodies(t *testing.T) {
	cases := map[string]string{
		"unknown field":    `{"bogus":1}`,
		"bad json":         `{"env":`,
		"trailing data":    `{} {}`,
		"negative timeout": `{"timeout_seconds":-1}`,
		"bad env name":     `{"env":{"1X":"y"}}`,
		"reserved env":     `{"env":{"PATH":"/x"}}`,
		"cpus too big":     `{"resources":{"cpus":100}}`,
	}
	h := newHarness(t, nil, nil)
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			status, out := h.do(t, http.MethodPost, "/v1/sandboxes", body, nil)
			if status != http.StatusBadRequest || errCode(t, out) != CodeInvalidArgument {
				t.Fatalf("status %d body %v", status, out)
			}
		})
	}
}

func TestCreateBodyTooLarge(t *testing.T) {
	h := newHarness(t, nil, nil)
	big := `{"env":{"A":"` + strings.Repeat("x", 5000) + `"}}`
	status, body := h.do(t, http.MethodPost, "/v1/sandboxes", big, nil)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d body %v", status, body)
	}
}

func TestEmptyCreateBodyUsesDefaults(t *testing.T) {
	h := newHarness(t, nil, nil)
	status, body := h.do(t, http.MethodPost, "/v1/sandboxes", "", nil)
	if status != http.StatusCreated {
		t.Fatalf("status = %d body %v", status, body)
	}
}

func TestCreateCapacityIs429(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.MaxSandboxes = 1 }, nil)
	createSandbox(t, h)
	status, body := h.do(t, http.MethodPost, "/v1/sandboxes", `{}`, nil)
	if status != http.StatusTooManyRequests || errCode(t, body) != CodeResourceExhaust {
		t.Fatalf("status %d body %v", status, body)
	}
}

func TestIdempotencyKey(t *testing.T) {
	h := newHarness(t, nil, nil)
	hdr := map[string]string{"Idempotency-Key": "job-42"}
	_, first := h.do(t, http.MethodPost, "/v1/sandboxes", `{}`, hdr)
	status, second := h.do(t, http.MethodPost, "/v1/sandboxes", `{}`, hdr)
	if status != http.StatusCreated || second["id"] != first["id"] {
		t.Fatalf("second create = %d %v, first %v", status, second, first)
	}
	if len(h.docker.containers) != 1 {
		t.Errorf("containers = %d", len(h.docker.containers))
	}
	status, body := h.do(t, http.MethodPost, "/v1/sandboxes", `{}`, map[string]string{"Idempotency-Key": "has space"})
	if status != http.StatusBadRequest {
		t.Errorf("bad key status = %d body %v", status, body)
	}
}

func TestGetListFilterAndNotFound(t *testing.T) {
	h := newHarness(t, nil, nil)
	id := createSandbox(t, h)

	status, body := h.do(t, http.MethodGet, "/v1/sandboxes/"+id, "", nil)
	if status != 200 || body["id"] != id {
		t.Fatalf("get = %d %v", status, body)
	}
	status, body = h.do(t, http.MethodGet, "/v1/sandboxes", "", nil)
	if status != 200 || len(body["items"].([]any)) != 1 {
		t.Fatalf("list = %d %v", status, body)
	}
	status, body = h.do(t, http.MethodGet, "/v1/sandboxes?status=ready", "", nil)
	if status != 200 || len(body["items"].([]any)) != 1 {
		t.Fatalf("filtered list = %d %v", status, body)
	}
	status, body = h.do(t, http.MethodGet, "/v1/sandboxes?status=stopped", "", nil)
	if status != 200 || len(body["items"].([]any)) != 0 {
		t.Fatalf("filtered-out list = %d %v", status, body)
	}
	status, body = h.do(t, http.MethodGet, "/v1/sandboxes?status=bogus", "", nil)
	if status != http.StatusBadRequest || errCode(t, body) != CodeInvalidArgument {
		t.Fatalf("bad status filter = %d %v", status, body)
	}

	for _, p := range []string{"/v1/sandboxes/11111111-1111-4111-8111-111111111111", "/v1/sandboxes/not-an-id"} {
		status, body = h.do(t, http.MethodGet, p, "", nil)
		if status != http.StatusNotFound || errCode(t, body) != CodeNotFound {
			t.Errorf("get %s = %d %v", p, status, body)
		}
	}
}

func TestLifecycleOverHTTP(t *testing.T) {
	h := newHarness(t, nil, nil)
	id := createSandbox(t, h)
	base := "/v1/sandboxes/" + id

	status, body := h.do(t, http.MethodPost, base+"/stop", "", nil)
	if status != 200 || body["status"] != "stopped" {
		t.Fatalf("stop = %d %v", status, body)
	}
	status, body = h.do(t, http.MethodGet, base+"/endpoint", "", nil)
	if status != http.StatusConflict || errCode(t, body) != CodeConflict {
		t.Fatalf("endpoint while stopped = %d %v", status, body)
	}
	status, body = h.do(t, http.MethodGet, base+"/stats", "", nil)
	if status != http.StatusConflict {
		t.Fatalf("stats while stopped = %d %v", status, body)
	}

	status, body = h.do(t, http.MethodPost, base+"/start", "", nil)
	if status != 200 || body["status"] != "ready" {
		t.Fatalf("start = %d %v", status, body)
	}
	status, body = h.do(t, http.MethodGet, base+"/endpoint", "", nil)
	if status != 200 || body["endpoint"] != "http://public.test:49153" {
		t.Fatalf("endpoint = %d %v", status, body)
	}
	status, body = h.do(t, http.MethodGet, base+"/stats", "", nil)
	if status != 200 || body["pids_current"] != 2.0 {
		t.Fatalf("stats = %d %v", status, body)
	}

	status, _ = h.do(t, http.MethodDelete, base, "", nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete = %d", status)
	}
	status, _ = h.do(t, http.MethodDelete, base, "", nil)
	if status != http.StatusNoContent {
		t.Fatalf("second delete = %d", status)
	}
	status, body = h.do(t, http.MethodGet, base, "", nil)
	if status != http.StatusNotFound {
		t.Fatalf("get after delete = %d %v", status, body)
	}
}

func TestDockerFailuresMapToStatusCodes(t *testing.T) {
	h := newHarness(t, nil, func(m *memDocker) { m.noImage = true })
	status, body := h.do(t, http.MethodPost, "/v1/sandboxes", `{}`, nil)
	if status != http.StatusServiceUnavailable || errCode(t, body) != CodeImageUnavailable {
		t.Errorf("missing image = %d %v", status, body)
	}

	h = newHarness(t, nil, func(m *memDocker) { m.failCreate = fmt.Errorf("%w: refused", docker.ErrUnavailable) })
	status, body = h.do(t, http.MethodPost, "/v1/sandboxes", `{}`, nil)
	if status != http.StatusServiceUnavailable || errCode(t, body) != CodeDockerUnavailable {
		t.Errorf("docker down = %d %v", status, body)
	}
}

func TestInternalErrorsDoNotLeakDetails(t *testing.T) {
	h := newHarness(t, nil, func(m *memDocker) { m.failCreate = errors.New("secret-internal-detail") })
	status, body := h.do(t, http.MethodPost, "/v1/sandboxes", `{}`, nil)
	if status != http.StatusInternalServerError || errCode(t, body) != CodeInternal {
		t.Fatalf("status %d body %v", status, body)
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "secret-internal-detail") {
		t.Errorf("internal detail leaked: %s", raw)
	}
}

func TestUnknownRouteReturnsEnvelope(t *testing.T) {
	h := newHarness(t, nil, nil)
	status, body := h.do(t, http.MethodGet, "/v1/nope", "", nil)
	if status != http.StatusNotFound || errCode(t, body) != CodeNotFound {
		t.Fatalf("status %d body %v", status, body)
	}
}

func TestRequestLogOmitsEnvValues(t *testing.T) {
	h := newHarness(t, nil, nil)
	secret := "super-secret-value-123"
	h.do(t, http.MethodPost, "/v1/sandboxes", `{"env":{"TOKEN":"`+secret+`"}}`, nil)
	if strings.Contains(h.logs.String(), secret) {
		t.Errorf("request log contains env value: %s", h.logs.String())
	}
	if !strings.Contains(h.logs.String(), `"method":"POST"`) {
		t.Errorf("request log missing entries: %s", h.logs.String())
	}
}
