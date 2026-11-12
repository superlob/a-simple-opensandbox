package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDaemon emulates the subset of the Docker Engine API the wrapper uses.
// It is served over real HTTP so the SDK's own request and error handling runs.
type fakeDaemon struct {
	mu       sync.Mutex
	created  []map[string]any
	stopTime string
	slowPing time.Duration
}

var versionPrefix = regexp.MustCompile(`^/v[0-9.]+`)

func (f *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := versionPrefix.ReplaceAllString(r.URL.Path, "")
	if path == "/_ping" && f.slowPing > 0 {
		time.Sleep(f.slowPing)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Api-Version", "1.51")
	w.Header().Set("Ostype", "linux")
	w.Header().Set("Content-Type", "application/json")
	switch {
	case path == "/_ping":
		io.WriteString(w, "OK")
	case path == "/containers/create" && r.Method == http.MethodPost:
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Query().Get("name") == "dup" {
			w.WriteHeader(http.StatusConflict)
			io.WriteString(w, `{"message":"Conflict. The container name is already in use"}`)
			return
		}
		f.created = append(f.created, body)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"Id":"c-123","Warnings":[]}`)
	case strings.HasSuffix(path, "/start") && r.Method == http.MethodPost:
		w.WriteHeader(http.StatusNoContent)
	case strings.HasSuffix(path, "/stop") && r.Method == http.MethodPost:
		f.stopTime = r.URL.Query().Get("t")
		w.WriteHeader(http.StatusNoContent)
	case path == "/containers/json":
		io.WriteString(w, `[{"Id":"c-123","Names":["/sbx-1"],"Labels":{"sandbox.managed":"true"},"State":"exited"}]`)
	case strings.HasSuffix(path, "/json") && strings.HasPrefix(path, "/containers/"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/json")
		if id != "c-123" {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"message":"No such container: `+id+`"}`)
			return
		}
		io.WriteString(w, `{"Id":"c-123","Name":"/sbx-1","Created":"2026-11-10T12:00:00Z",
			"Image":"sha256:img","Config":{"Labels":{"sandbox.managed":"true"}},
			"State":{"Status":"running","Running":true,"ExitCode":0,"Health":{"Status":"healthy"}},
			"NetworkSettings":{"Ports":{"44772/tcp":[{"HostIp":"127.0.0.1","HostPort":"49153"}]}}}`)
	case path == "/images/sandbox-base:py3.13-uv0.13.7/json":
		io.WriteString(w, `{"Id":"sha256:img"}`)
	case path == "/images/missing:tag/json":
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"message":"No such image"}`)
	case strings.HasSuffix(path, "/archive") && r.Method == http.MethodPut:
		w.WriteHeader(http.StatusOK)
	case strings.HasSuffix(path, "/stats"):
		io.WriteString(w, `{"read":"2026-11-10T12:00:01Z",
			"cpu_stats":{"cpu_usage":{"total_usage":2000},"system_cpu_usage":20000,"online_cpus":2},
			"precpu_stats":{"cpu_usage":{"total_usage":1000},"system_cpu_usage":10000},
			"memory_stats":{"usage":1048576,"limit":1073741824},
			"pids_stats":{"current":7}}`)
	case r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotImplemented)
		io.WriteString(w, `{"message":"not implemented in fake: `+path+`"}`)
	}
}

func newTestClient(t *testing.T, f *fakeDaemon, callTimeout time.Duration) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := New(Options{
		Host:        "tcp://" + strings.TrimPrefix(srv.URL, "http://"),
		CallTimeout: callTimeout,
		HTTPClient:  srv.Client(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestPingNegotiatesVersion(t *testing.T) {
	c := newTestClient(t, &fakeDaemon{}, 5*time.Second)
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestPingUnavailableWhenDaemonDown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	c, err := New(Options{Host: "tcp://" + addr, CallTimeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()
	if err := c.Ping(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Ping error = %v, want ErrUnavailable", err)
	}
}

func TestCallTimeoutIsClassified(t *testing.T) {
	c := newTestClient(t, &fakeDaemon{slowPing: 500 * time.Millisecond}, 50*time.Millisecond)
	if err := c.Ping(context.Background()); !errors.Is(err, ErrTimeout) {
		t.Fatalf("Ping error = %v, want ErrTimeout", err)
	}
}

func TestCancelledContextIsNotUnavailable(t *testing.T) {
	c := newTestClient(t, &fakeDaemon{slowPing: 500 * time.Millisecond}, 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := c.Ping(ctx)
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, ErrUnavailable) {
		t.Fatalf("cancelled context must not be reported as unavailable: %v", err)
	}
}

func TestImageExists(t *testing.T) {
	c := newTestClient(t, &fakeDaemon{}, 5*time.Second)
	if ok, err := c.ImageExists(context.Background(), "sandbox-base:py3.13-uv0.13.7"); err != nil || !ok {
		t.Fatalf("ImageExists(present) = %v, %v", ok, err)
	}
	if ok, err := c.ImageExists(context.Background(), "missing:tag"); err != nil || ok {
		t.Fatalf("ImageExists(missing) = %v, %v; want false, nil", ok, err)
	}
}

func TestContainerCreateSecurityBaseline(t *testing.T) {
	f := &fakeDaemon{}
	c := newTestClient(t, f, 5*time.Second)
	id, err := c.ContainerCreate(context.Background(), Spec{
		Name:          "sbx-1",
		Image:         "sandbox-base:py3.13-uv0.13.7",
		Env:           []string{"EXECD_CONFIG=/etc/execd/config.json"},
		Labels:        map[string]string{"sandbox.managed": "true"},
		ContainerPort: 44772,
		BindHost:      netip.MustParseAddr("127.0.0.1"),
		CPUs:          1,
		MemoryBytes:   1 << 30,
		PidsLimit:     512,
		StopTimeout:   15 * time.Second,
	})
	if err != nil {
		t.Fatalf("ContainerCreate: %v", err)
	}
	if id != "c-123" {
		t.Errorf("id = %q", id)
	}
	if len(f.created) != 1 {
		t.Fatalf("create calls = %d", len(f.created))
	}
	hc, _ := f.created[0]["HostConfig"].(map[string]any)
	if hc == nil {
		t.Fatal("HostConfig missing")
	}
	if priv, _ := hc["Privileged"].(bool); priv {
		t.Error("Privileged must be false")
	}
	if caps, _ := hc["CapDrop"].([]any); len(caps) != 1 || caps[0] != "ALL" {
		t.Errorf("CapDrop = %v, want [ALL]", caps)
	}
	if opts, _ := hc["SecurityOpt"].([]any); len(opts) != 1 || opts[0] != "no-new-privileges" {
		t.Errorf("SecurityOpt = %v", opts)
	}
	if mem, _ := hc["Memory"].(float64); mem != 1<<30 {
		t.Errorf("Memory = %v", mem)
	}
	if pids, _ := hc["PidsLimit"].(float64); pids != 512 {
		t.Errorf("PidsLimit = %v", pids)
	}
	if binds := hc["Binds"]; binds != nil {
		t.Errorf("no host mounts expected, got %v", binds)
	}
	// The create body flattens the container config to the top level.
	if stop, _ := f.created[0]["StopTimeout"].(float64); stop != 15 {
		t.Errorf("StopTimeout = %v, want 15", f.created[0]["StopTimeout"])
	}
}

func TestContainerCreateConflict(t *testing.T) {
	c := newTestClient(t, &fakeDaemon{}, 5*time.Second)
	_, err := c.ContainerCreate(context.Background(), Spec{Name: "dup", Image: "x", ContainerPort: 1, BindHost: netip.MustParseAddr("127.0.0.1")})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
}

func TestContainerStopPassesTimeout(t *testing.T) {
	f := &fakeDaemon{}
	c := newTestClient(t, f, 5*time.Second)
	if err := c.ContainerStop(context.Background(), "c-123", 15*time.Second); err != nil {
		t.Fatalf("ContainerStop: %v", err)
	}
	if f.stopTime != "15" {
		t.Errorf("t = %q, want 15", f.stopTime)
	}
}

func TestContainerInspectMapsState(t *testing.T) {
	c := newTestClient(t, &fakeDaemon{}, 5*time.Second)
	in, err := c.ContainerInspect(context.Background(), "c-123")
	if err != nil {
		t.Fatalf("ContainerInspect: %v", err)
	}
	if in.Name != "sbx-1" || in.Status != StateRunning || !in.Running || in.Health != HealthHealthy {
		t.Errorf("inspect = %+v", in)
	}
	if in.HostPorts["44772/tcp"] != 49153 {
		t.Errorf("HostPorts = %v", in.HostPorts)
	}
	if in.Labels["sandbox.managed"] != "true" {
		t.Errorf("labels = %v", in.Labels)
	}
	if in.Created.IsZero() {
		t.Error("Created not parsed")
	}
}

func TestContainerInspectNotFound(t *testing.T) {
	c := newTestClient(t, &fakeDaemon{}, 5*time.Second)
	if _, err := c.ContainerInspect(context.Background(), "gone"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestContainerRemove(t *testing.T) {
	c := newTestClient(t, &fakeDaemon{}, 5*time.Second)
	if err := c.ContainerRemove(context.Background(), "c-123"); err != nil {
		t.Fatalf("ContainerRemove: %v", err)
	}
}

func TestContainersByLabel(t *testing.T) {
	c := newTestClient(t, &fakeDaemon{}, 5*time.Second)
	list, err := c.ContainersByLabel(context.Background(), "sandbox.managed", "true")
	if err != nil {
		t.Fatalf("ContainersByLabel: %v", err)
	}
	if len(list) != 1 || list[0].Name != "sbx-1" || list[0].Status != "exited" {
		t.Errorf("list = %+v", list)
	}
}

func TestCopyFileToContainer(t *testing.T) {
	c := newTestClient(t, &fakeDaemon{}, 5*time.Second)
	if err := c.CopyFileToContainer(context.Background(), "c-123", "/etc/execd", "config.json", []byte(`{"port":1}`), 0o644); err != nil {
		t.Fatalf("CopyFileToContainer: %v", err)
	}
}

func TestContainerStatsOnceComputesCPUAndMemory(t *testing.T) {
	c := newTestClient(t, &fakeDaemon{}, 5*time.Second)
	s, err := c.ContainerStatsOnce(context.Background(), "c-123")
	if err != nil {
		t.Fatalf("ContainerStatsOnce: %v", err)
	}
	// cpu delta 1000 / system delta 10000 * 2 cpus * 100 = 20%
	if s.CPUPercent < 19.9 || s.CPUPercent > 20.1 {
		t.Errorf("CPUPercent = %v, want ~20", s.CPUPercent)
	}
	if s.MemoryUsage != 1048576 || s.MemoryLimit != 1073741824 || s.PidsCurrent != 7 {
		t.Errorf("stats = %+v", s)
	}
}
