//go:build integration

package sandbox

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"sandbox/runtime/internal/config"
	"sandbox/runtime/internal/docker"
	"sandbox/runtime/internal/endpoint"
)

// These tests create real containers from the sandbox base image. Each test removes
// only the container it created, by its exact name. Run via `make integration`, which
// loads SANDBOX.env.

func realService(t *testing.T) (*Service, config.Config) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load (is SANDBOX.env loaded?): %v", err)
	}
	cfg.ReadyTimeout = config.Duration(90 * time.Second)
	dc, err := docker.New(docker.Options{CallTimeout: 60 * time.Second})
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	t.Cleanup(func() { _ = dc.Close() })
	if err := dc.Ping(context.Background()); err != nil {
		t.Skipf("docker daemon not reachable: %v", err)
	}
	return NewService(cfg, dc, endpoint.NewRegistry(), Options{}), cfg
}

func dockerOut(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func cleanupContainer(t *testing.T, name string) {
	t.Helper()
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
}

func TestRealSandboxCreateAndVerify(t *testing.T) {
	svc, _ := realService(t)
	ctx := context.Background()

	sb, err := svc.Create(ctx, CreateRequest{Env: map[string]string{"R4_TEST": "yes"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ref := NamePrefix + sb.ID
	cleanupContainer(t, ref)

	if sb.Status != StatusReady {
		t.Errorf("status = %q, want ready", sb.Status)
	}

	// Execd is PID 1.
	if pid1 := dockerOut(t, "exec", ref, "cat", "/proc/1/comm"); pid1 != "execd" {
		t.Errorf("PID 1 = %q, want execd", pid1)
	}
	// /workspace is writable and sits in the container writable layer (no mounts).
	dockerOut(t, "exec", ref, "sh", "-c", "touch /workspace/r4-probe && rm /workspace/r4-probe")
	if mounts := dockerOut(t, "inspect", "-f", "{{len .Mounts}}", ref); mounts != "0" {
		t.Errorf("mount count = %s, want 0", mounts)
	}
	// PATH and environment.
	if path := dockerOut(t, "exec", ref, "sh", "-c", "echo $PATH"); !strings.HasPrefix(path, "/workspace/.venv/bin:") {
		t.Errorf("PATH = %q", path)
	}
	if v := dockerOut(t, "exec", ref, "sh", "-c", "echo $R4_TEST"); v != "yes" {
		t.Errorf("request env not applied: %q", v)
	}
	// Python version from the image.
	if py := dockerOut(t, "exec", ref, "python", "--version"); !strings.Contains(py, "3.13") {
		t.Errorf("python = %q", py)
	}
	// Security baseline.
	if priv := dockerOut(t, "inspect", "-f", "{{.HostConfig.Privileged}}", ref); priv != "false" {
		t.Errorf("Privileged = %s", priv)
	}
	if caps := dockerOut(t, "inspect", "-f", "{{.HostConfig.CapDrop}}", ref); !strings.Contains(caps, "ALL") {
		t.Errorf("CapDrop = %s", caps)
	}
	if sec := dockerOut(t, "inspect", "-f", "{{.HostConfig.SecurityOpt}}", ref); !strings.Contains(sec, "no-new-privileges") {
		t.Errorf("SecurityOpt = %s", sec)
	}
	if mem := dockerOut(t, "inspect", "-f", "{{.HostConfig.Memory}}", ref); mem != "1073741824" {
		t.Errorf("Memory = %s", mem)
	}
	if pids := dockerOut(t, "inspect", "-f", "{{.HostConfig.PidsLimit}}", ref); pids != "512" {
		t.Errorf("PidsLimit = %s", pids)
	}
	// The published Execd endpoint answers /health.
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(sb.Endpoint + "/health")
	if err != nil {
		t.Fatalf("GET %s/health: %v", sb.Endpoint, err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/health status = %d", resp.StatusCode)
	}
}

func TestRealSandboxClientIDIsIdempotent(t *testing.T) {
	svc, _ := realService(t)
	ctx := context.Background()
	first, err := svc.Create(ctx, CreateRequest{ClientID: "r4-idem-" + time.Now().Format("150405.000000000")})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	cleanupContainer(t, NamePrefix+first.ID)
	second, err := svc.Create(ctx, CreateRequest{ClientID: first.ClientID})
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("second ID %s != first %s", second.ID, first.ID)
	}
}

func TestRealSandboxMissingImageCreatesNothing(t *testing.T) {
	svc, cfg := realService(t)
	cfg.SandboxImage = "sandbox-r4-missing:none"
	svc.cfg = cfg
	_, err := svc.Create(context.Background(), CreateRequest{})
	if !errors.Is(err, ErrImageUnavailable) {
		t.Fatalf("err = %v, want ErrImageUnavailable", err)
	}
}

func TestRealSandboxLifecycle(t *testing.T) {
	svc, cfg := realService(t)
	ctx := context.Background()

	sb, err := svc.Create(ctx, CreateRequest{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ref := NamePrefix + sb.ID
	cleanupContainer(t, ref)

	// Write a workspace file that must survive stop and start.
	dockerOut(t, "exec", ref, "sh", "-c", "echo keep > /workspace/persist.txt")

	// Stop: graceful SIGTERM, exit code 0, within the configured grace period.
	start := time.Now()
	stopped, err := svc.Stop(ctx, sb.ID)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if stopped.Status != StatusStopped || stopped.Endpoint != "" {
		t.Errorf("after stop: %+v", stopped)
	}
	if code := dockerOut(t, "inspect", "-f", "{{.State.ExitCode}}", ref); code != "0" {
		t.Errorf("exit code after SIGTERM = %s, want 0", code)
	}
	if elapsed > cfg.StopTimeout.Std() {
		t.Errorf("stop took %s, longer than stop_timeout %s", elapsed, cfg.StopTimeout.Std())
	}

	// Start: healthy again, workspace intact, endpoint refreshed and reachable.
	restarted, err := svc.Start(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if restarted.Status != StatusReady || restarted.Endpoint == "" {
		t.Errorf("after start: %+v", restarted)
	}
	if v := dockerOut(t, "exec", ref, "cat", "/workspace/persist.txt"); v != "keep" {
		t.Errorf("workspace file after restart = %q", v)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(restarted.Endpoint + "/health")
	if err != nil {
		t.Fatalf("GET /health after restart: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/health status = %d", resp.StatusCode)
	}

	// Delete: container gone; repeated delete succeeds.
	if err := svc.Delete(ctx, sb.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if out, err := exec.Command("docker", "inspect", ref).CombinedOutput(); err == nil {
		t.Errorf("container still exists after delete: %s", out)
	}
	if err := svc.Delete(ctx, sb.ID); err != nil {
		t.Errorf("second delete: %v", err)
	}
}

// TestRealStatsReflectWorkload checks that CPU, memory, and PIDs stats respond to a
// real workload inside the container, and that stats are refused when stopped.
func TestRealStatsReflectWorkload(t *testing.T) {
	svc, _ := realService(t)
	ctx := context.Background()
	sb, err := svc.Create(ctx, CreateRequest{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ref := NamePrefix + sb.ID
	cleanupContainer(t, ref)

	idle, err := svc.Stats(ctx, sb.ID)
	if err != nil {
		t.Fatalf("idle Stats: %v", err)
	}
	if idle.MemoryUsage == 0 || idle.PidsCurrent == 0 {
		t.Errorf("idle stats look empty: %+v", idle)
	}

	// Busy loop for ~8 seconds inside the container, in the background.
	dockerOut(t, "exec", "-d", ref, "sh", "-c",
		"end=$(( $(date +%s) + 8 )); while [ $(date +%s) -lt $end ]; do :; done")
	time.Sleep(1 * time.Second)

	busy, err := svc.Stats(ctx, sb.ID)
	if err != nil {
		t.Fatalf("busy Stats: %v", err)
	}
	if busy.CPUPercent < 20 {
		t.Errorf("CPU under a busy loop = %.1f%%, expected a clearly non-zero value", busy.CPUPercent)
	}
	if busy.PidsCurrent < 2 {
		t.Errorf("pids under load = %d, expected at least 2", busy.PidsCurrent)
	}

	if _, err := svc.Stop(ctx, sb.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := svc.Stats(ctx, sb.ID); !errors.Is(err, ErrConflict) {
		t.Errorf("stats while stopped: err = %v, want ErrConflict", err)
	}
}

// TestRealRecoverAfterRestart simulates a Runtime restart: a new service over the same
// daemon must adopt the running Sandbox's endpoint and remove a leftover "created"
// container from an interrupted create.
//
// Recover acts on every created-state managed container on the daemon, not only on
// containers this test made. It is therefore skipped unless explicitly enabled.
func TestRealRecoverAfterRestart(t *testing.T) {
	if os.Getenv("RUNTIME_RECOVERY_INTEGRATION") != "1" {
		t.Skip("set RUNTIME_RECOVERY_INTEGRATION=1 to run (Recover removes created-state managed containers daemon-wide)")
	}
	svc1, cfg := realService(t)
	ctx := context.Background()
	sb, err := svc1.Create(ctx, CreateRequest{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cleanupContainer(t, NamePrefix+sb.ID)

	// Leftover from a create that never started, made with the CLI using the same labels.
	leftoverID, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	leftover := NamePrefix + leftoverID
	cleanupContainer(t, leftover)
	dockerOut(t, "create", "--name", leftover,
		"--label", LabelManaged+"=true",
		"--label", LabelID+"="+leftoverID,
		"--label", LabelCreatedAt+"="+time.Now().UTC().Format(time.RFC3339),
		cfg.SandboxImage)

	// New service and empty registry, as after a restart.
	svc2, _ := realService(t)
	svc2.cfg.RecoverLeftovers = true
	// Scope removal to this test's own leftover, so no other managed container is touched.
	svc2.leftoverScope = map[string]bool{leftoverID: true}
	rep, err := svc2.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got, ok := svc2.endpoints.Get(sb.ID); !ok || got != sb.Endpoint {
		t.Errorf("endpoint after recovery = %q %v, want %q", got, ok, sb.Endpoint)
	}
	if len(rep.Removed) != 1 || rep.Removed[0] != leftoverID {
		t.Errorf("removed = %v, want [%s]", rep.Removed, leftoverID)
	}
	if out, err := exec.Command("docker", "inspect", leftover).CombinedOutput(); err == nil {
		t.Errorf("leftover still exists: %s", out)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(sb.Endpoint + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/health status = %d", resp.StatusCode)
	}
}
