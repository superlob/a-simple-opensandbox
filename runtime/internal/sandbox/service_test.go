package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"sandbox/runtime/internal/config"
	"sandbox/runtime/internal/docker"
	"sandbox/runtime/internal/endpoint"
)

func testConfig(t *testing.T) config.Config {
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
		t.Fatalf("config: %v", err)
	}
	cfg.ReadyTimeout = config.Duration(2 * time.Second)
	return cfg
}

func newTestService(t *testing.T, f *fakeDocker, mutate func(*config.Config)) (*Service, *endpoint.Registry) {
	t.Helper()
	cfg := testConfig(t)
	if mutate != nil {
		mutate(&cfg)
	}
	reg := endpoint.NewRegistry()
	return NewService(cfg, f, reg, Options{PollInterval: time.Millisecond}), reg
}

func TestCreateSucceedsAndReturnsReadySandbox(t *testing.T) {
	f := newFakeDocker()
	svc, reg := newTestService(t, f, nil)

	sb, err := svc.Create(context.Background(), CreateRequest{Env: map[string]string{"FOO": "bar"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sb.Status != StatusReady {
		t.Errorf("status = %q, want ready", sb.Status)
	}
	if sb.Endpoint != "http://public.test:49153" {
		t.Errorf("endpoint = %q", sb.Endpoint)
	}
	if sb.Image != "sandbox-base:py3.13-uv0.13.7" {
		t.Errorf("image = %q", sb.Image)
	}
	if sb.CPUs != 1 || sb.MemoryMB != 1024 || sb.PidsLimit != 512 {
		t.Errorf("defaults not applied: %+v", sb)
	}
	if got, ok := reg.Get(sb.ID); !ok || got != sb.Endpoint {
		t.Errorf("registry = %q %v", got, ok)
	}

	c := f.containers["sbx-"+sb.ID]
	if c == nil {
		t.Fatal("container not created under sbx-<id>")
	}
	if c.status != "running" {
		t.Errorf("container status = %q", c.status)
	}
	if c.spec.Labels[LabelManaged] != "true" || c.spec.Labels[LabelID] != sb.ID || c.spec.Labels[LabelImage] != sb.Image {
		t.Errorf("labels = %v", c.spec.Labels)
	}
	if c.spec.Labels[LabelRuntimeVersion] == "" || c.spec.Labels[LabelCreatedAt] == "" {
		t.Errorf("missing labels: %v", c.spec.Labels)
	}
	if c.spec.MemoryBytes != 1024<<20 || c.spec.PidsLimit != 512 || c.spec.CPUs != 1 {
		t.Errorf("limits = mem %d pids %d cpus %v", c.spec.MemoryBytes, c.spec.PidsLimit, c.spec.CPUs)
	}
	if c.spec.BindHost.String() != "127.0.0.1" || c.spec.ContainerPort != 44772 {
		t.Errorf("port publishing = %v:%d", c.spec.BindHost, c.spec.ContainerPort)
	}
	env := strings.Join(c.spec.Env, "\n")
	if !strings.Contains(env, "EXECD_CONFIG=/etc/execd/config.json") || !strings.Contains(env, "FOO=bar") {
		t.Errorf("env = %v", c.spec.Env)
	}
}

func TestCreateDeliversExecdConfig(t *testing.T) {
	f := newFakeDocker()
	svc, _ := newTestService(t, f, nil)
	sb, err := svc.Create(context.Background(), CreateRequest{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	raw := f.containers["sbx-"+sb.ID].copied["/etc/execd/config.json"]
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("config not valid JSON: %v (%s)", err, raw)
	}
	if m["port"] != float64(44772) || m["workspace_dir"] != "/workspace" {
		t.Errorf("execd config = %v", m)
	}
}

func TestCreateRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name string
		req  CreateRequest
	}{
		{"cpus above max", CreateRequest{CPUs: 99}},
		{"memory above max", CreateRequest{MemoryMB: 1 << 30}},
		{"pids above max", CreateRequest{PidsLimit: 1 << 30}},
		{"negative timeout", CreateRequest{Timeout: -time.Second}},
		{"bad env name", CreateRequest{Env: map[string]string{"1BAD": "x"}}},
		{"reserved PATH", CreateRequest{Env: map[string]string{"PATH": "/x"}}},
		{"reserved EXECD_CONFIG", CreateRequest{Env: map[string]string{"EXECD_CONFIG": "/x"}}},
		{"NUL in value", CreateRequest{Env: map[string]string{"A": "x\x00y"}}},
		{"too many env", CreateRequest{Env: manyEnv(65)}},
		{"env too large", CreateRequest{Env: map[string]string{"A": strings.Repeat("x", 17*1024)}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeDocker()
			svc, _ := newTestService(t, f, nil)
			_, err := svc.Create(context.Background(), tc.req)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if f.creates != 0 {
				t.Errorf("container created despite invalid input")
			}
		})
	}
}

func manyEnv(n int) map[string]string {
	m := map[string]string{}
	for i := 0; i < n; i++ {
		m["V"+strings.Repeat("x", i%3)+string(rune('A'+i%26))+string(rune('a'+i/26))] = "1"
	}
	return m
}

func TestCreateFailsWhenImageMissing(t *testing.T) {
	f := newFakeDocker()
	delete(f.images, "sandbox-base:py3.13-uv0.13.7")
	svc, _ := newTestService(t, f, nil)
	_, err := svc.Create(context.Background(), CreateRequest{})
	if !errors.Is(err, ErrImageUnavailable) {
		t.Fatalf("err = %v, want ErrImageUnavailable", err)
	}
	if f.creates != 0 {
		t.Error("no container should be created without the image")
	}
}

func TestCreateEnforcesMaxSandboxes(t *testing.T) {
	f := newFakeDocker()
	svc, _ := newTestService(t, f, func(c *config.Config) { c.MaxSandboxes = 2 })
	for i := 0; i < 2; i++ {
		if _, err := svc.Create(context.Background(), CreateRequest{}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	_, err := svc.Create(context.Background(), CreateRequest{})
	if !errors.Is(err, ErrResourceExhausted) {
		t.Fatalf("err = %v, want ErrResourceExhausted", err)
	}
	if f.creates != 2 {
		t.Errorf("creates = %d, want 2", f.creates)
	}
}

func TestConcurrentCreatesRespectCap(t *testing.T) {
	f := newFakeDocker()
	svc, _ := newTestService(t, f, func(c *config.Config) { c.MaxSandboxes = 3 })
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, exhausted := 0, 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Create(context.Background(), CreateRequest{})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrResourceExhausted):
				exhausted++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok != 3 || exhausted != 7 {
		t.Fatalf("ok=%d exhausted=%d, want 3 and 7", ok, exhausted)
	}
	if f.count() != 3 {
		t.Errorf("containers = %d, want 3", f.count())
	}
}

func TestHealthTimeoutRemovesContainer(t *testing.T) {
	f := newFakeDocker()
	f.nextHealthy = -1 // never healthy
	svc, reg := newTestService(t, f, func(c *config.Config) { c.ReadyTimeout = config.Duration(30 * time.Millisecond) })
	_, err := svc.Create(context.Background(), CreateRequest{})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if f.count() != 0 {
		t.Errorf("leftover containers: %d", f.count())
	}
	if reg.Len() != 0 {
		t.Error("registry must not hold an endpoint for a failed sandbox")
	}
}

func TestExitedDuringStartupFailsFast(t *testing.T) {
	f := newFakeDocker()
	f.startExits = true
	svc, _ := newTestService(t, f, func(c *config.Config) { c.ReadyTimeout = config.Duration(5 * time.Second) })
	start := time.Now()
	_, err := svc.Create(context.Background(), CreateRequest{})
	if !errors.Is(err, ErrStartFailed) {
		t.Fatalf("err = %v, want ErrStartFailed", err)
	}
	if time.Since(start) > time.Second {
		t.Error("exited container should fail without waiting for the full timeout")
	}
	if f.count() != 0 {
		t.Errorf("leftover containers: %d", f.count())
	}
}

func TestStepFailuresRollBack(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*fakeDocker)
	}{
		{"copy config fails", func(f *fakeDocker) { f.failCopy = errors.New("copy boom") }},
		{"start fails", func(f *fakeDocker) { f.failStart = errors.New("start boom") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeDocker()
			tc.setup(f)
			svc, _ := newTestService(t, f, nil)
			if _, err := svc.Create(context.Background(), CreateRequest{}); err == nil {
				t.Fatal("expected error")
			}
			if f.count() != 0 {
				t.Errorf("container not rolled back: %d left", f.count())
			}
		})
	}
}

func TestRollbackFailureIsReported(t *testing.T) {
	f := newFakeDocker()
	f.failStart = errors.New("start boom")
	f.failRemove = errors.New("remove boom")
	svc, _ := newTestService(t, f, nil)
	_, err := svc.Create(context.Background(), CreateRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "sbx-") || !strings.Contains(err.Error(), "remove boom") {
		t.Errorf("leftover container not named in error: %v", err)
	}
	if !strings.Contains(err.Error(), "start boom") {
		t.Errorf("original cause lost: %v", err)
	}
}

func TestCancelledCallerStillRollsBack(t *testing.T) {
	f := newFakeDocker()
	f.nextHealthy = -1
	svc, _ := newTestService(t, f, func(c *config.Config) { c.ReadyTimeout = config.Duration(5 * time.Second) })
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err := svc.Create(ctx, CreateRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if f.count() != 0 {
		t.Errorf("cancelled creation left %d containers", f.count())
	}
}

func TestClientIDIsIdempotent(t *testing.T) {
	f := newFakeDocker()
	svc, _ := newTestService(t, f, nil)
	first, err := svc.Create(context.Background(), CreateRequest{ClientID: "job-1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Create(context.Background(), CreateRequest{ClientID: "job-1"})
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("ids differ: %s vs %s", first.ID, second.ID)
	}
	if f.creates != 1 {
		t.Errorf("creates = %d, want 1", f.creates)
	}
	if second.ClientID != "job-1" || second.Endpoint == "" {
		t.Errorf("returned sandbox = %+v", second)
	}
}

func TestConcurrentSameClientIDIsConflict(t *testing.T) {
	f := newFakeDocker()
	gate := make(chan struct{})
	f.imageGate = gate
	svc, _ := newTestService(t, f, nil)

	done := make(chan error, 1)
	go func() {
		_, err := svc.Create(context.Background(), CreateRequest{ClientID: "dup"})
		done <- err
	}()
	// Wait until the first request has registered its in-flight client ID.
	waitFor(t, func() bool {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		return svc.inflight["dup"]
	})
	_, err := svc.Create(context.Background(), CreateRequest{ClientID: "dup"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("second concurrent create err = %v, want ErrConflict", err)
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatalf("first create: %v", err)
	}
	if f.creates != 1 {
		t.Errorf("creates = %d, want 1", f.creates)
	}
}

func TestStatusOf(t *testing.T) {
	tests := []struct {
		status, health string
		want           string
	}{
		{"created", "", StatusCreating},
		{"running", "starting", StatusStarting},
		{"running", "healthy", StatusReady},
		{"running", "unhealthy", StatusUnhealthy},
		{"exited", "", StatusStopped},
		{"dead", "", StatusStopped},
		{"paused", "", StatusUnknown},
	}
	for _, tc := range tests {
		in := docker.Inspect{Status: tc.status, Health: tc.health, Running: tc.status == docker.StateRunning}
		if got := StatusOf(in); got != tc.want {
			t.Errorf("StatusOf(%s,%s)=%s want %s", tc.status, tc.health, got, tc.want)
		}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
