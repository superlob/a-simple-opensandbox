//go:build integration

package sandbox

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"sandbox/runtime/internal/config"
	"sandbox/runtime/internal/docker"
	"sandbox/runtime/internal/endpoint"
)

// Security and resource checks against real containers. Each test creates and removes
// only its own Sandboxes, by exact name. Nothing here touches other containers.

// execCode runs a command in the container and returns its exit code and combined output.
func execCode(t *testing.T, ref string, args ...string) (int, string) {
	t.Helper()
	out, err := exec.Command("docker", append([]string{"exec", ref}, args...)...).CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), string(out)
	}
	t.Fatalf("docker exec: %v", err)
	return -1, ""
}

func healthGet(t *testing.T, url string) int {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url + "/health")
	if err != nil {
		t.Fatalf("GET %s/health: %v", url, err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func TestRealSecurityBaseline(t *testing.T) {
	svc, _ := realService(t)
	ctx := context.Background()
	sb, err := svc.Create(ctx, CreateRequest{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ref := NamePrefix + sb.ID
	cleanupContainer(t, ref)

	// No capabilities: the effective set is empty.
	if caps := dockerOut(t, "exec", ref, "sh", "-c", "grep CapEff /proc/self/status"); !strings.Contains(caps, "0000000000000000") {
		t.Errorf("effective capabilities not empty: %s", caps)
	}
	// no-new-privileges is set.
	if nnp := dockerOut(t, "exec", ref, "sh", "-c", "grep NoNewPrivs /proc/self/status"); !strings.Contains(nnp, "1") {
		t.Errorf("NoNewPrivs not set: %s", nnp)
	}
	// A privileged operation fails without CAP_SYS_ADMIN. First confirm the tool exists,
	// so that a missing binary cannot pass as a denied mount.
	if code, _ := execCode(t, ref, "sh", "-c", "command -v mount"); code != 0 {
		t.Fatal("mount binary missing from the image; the denial check cannot run")
	}
	if code, out := execCode(t, ref, "sh", "-c", "mkdir -p /tmp/m && mount -t tmpfs none /tmp/m"); code == 0 {
		t.Errorf("mount succeeded inside an unprivileged sandbox: %s", out)
	}
	// The Docker socket is not present, and no host path is mounted.
	if code, _ := execCode(t, ref, "test", "-e", "/var/run/docker.sock"); code == 0 {
		t.Error("docker socket is visible inside the sandbox")
	}
	if mounts := dockerOut(t, "inspect", "-f", "{{len .Mounts}}", ref); mounts != "0" {
		t.Errorf("mount count = %s, want 0", mounts)
	}
	// Privileged mode is off.
	if priv := dockerOut(t, "inspect", "-f", "{{.HostConfig.Privileged}}", ref); priv != "false" {
		t.Errorf("Privileged = %s", priv)
	}
}

func TestRealPidsLimitContainsForkBomb(t *testing.T) {
	svc, _ := realService(t)
	ctx := context.Background()
	const limit = 32
	sb, err := svc.Create(ctx, CreateRequest{PidsLimit: limit})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ref := NamePrefix + sb.ID
	cleanupContainer(t, ref)

	// Try to start far more processes than the limit allows. Failures are expected.
	execCode(t, ref, "sh", "-c",
		"i=0; while [ $i -lt 300 ]; do sleep 5 >/dev/null 2>&1 & i=$((i+1)); done; wait; true")

	st, err := svc.Stats(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st.PidsCurrent > limit+2 {
		t.Errorf("pids = %d, exceeds limit %d", st.PidsCurrent, limit)
	}
	// The limit contains the workload: Execd is still up and answering.
	if code := healthGet(t, sb.Endpoint); code != http.StatusOK {
		t.Errorf("/health after fork pressure = %d", code)
	}
}

func TestRealMemoryLimitEnforced(t *testing.T) {
	svc, _ := realService(t)
	ctx := context.Background()
	sb, err := svc.Create(ctx, CreateRequest{MemoryMB: 64})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ref := NamePrefix + sb.ID
	cleanupContainer(t, ref)

	// Allocate well above the 64 MiB limit. The kernel's OOM killer should end the process.
	code, out := execCode(t, ref, "python3", "-c",
		"x = bytearray(400 * 1024 * 1024); print('allocated')")
	// 137 = killed by SIGKILL, the OOM killer's signal. Anything else (including 127,
	// "command not found") means the test did not exercise the limit.
	if code != 137 {
		t.Errorf("exit code = %d, want 137 (OOM kill); output: %s", code, out)
	}
	if strings.Contains(out, "allocated") {
		t.Errorf("process printed success despite the limit: %s", out)
	}
	// Only the workload dies; the sandbox stays up.
	if code := healthGet(t, sb.Endpoint); code != http.StatusOK {
		t.Errorf("/health after OOM = %d", code)
	}
}

func TestRealCrashIsReportedAndRecoverable(t *testing.T) {
	svc, _ := realService(t)
	ctx := context.Background()
	sb, err := svc.Create(ctx, CreateRequest{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ref := NamePrefix + sb.ID
	cleanupContainer(t, ref)

	// Kill PID 1 (Execd) outright, as a crash would.
	dockerOut(t, "kill", "--signal", "KILL", ref)
	waitForStatus(t, svc, sb.ID, StatusStopped)

	got, err := svc.Get(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Get after crash: %v", err)
	}
	if got.Endpoint != "" {
		t.Errorf("crashed sandbox still advertises endpoint %q", got.Endpoint)
	}
	if _, err := svc.Stats(ctx, sb.ID); !errors.Is(err, ErrConflict) {
		t.Errorf("stats after crash: err = %v, want ErrConflict", err)
	}

	restarted, err := svc.Start(ctx, sb.ID)
	if err != nil {
		t.Fatalf("Start after crash: %v", err)
	}
	if restarted.Status != StatusReady {
		t.Errorf("status after restart = %q", restarted.Status)
	}
	if code := healthGet(t, restarted.Endpoint); code != http.StatusOK {
		t.Errorf("/health after restart = %d", code)
	}
}

func TestRealUnavailableDaemonIsReportedCleanly(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	dc, err := docker.New(docker.Options{
		Host:        "unix:///nonexistent-runtime-r9/docker.sock",
		CallTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("docker.New: %v", err)
	}
	defer dc.Close()
	svc := NewService(cfg, dc, endpoint.NewRegistry(), Options{})
	ctx := context.Background()

	if _, err := svc.Create(ctx, CreateRequest{}); !errors.Is(err, ErrDockerUnavailable) {
		t.Errorf("Create err = %v, want ErrDockerUnavailable", err)
	}
	// A daemon outage must not look like "sandbox not found".
	if _, err := svc.Get(ctx, "11111111-1111-4111-8111-111111111111"); !errors.Is(err, ErrDockerUnavailable) {
		t.Errorf("Get err = %v, want ErrDockerUnavailable", err)
	}
}

// waitForStatus polls until the Sandbox reports the wanted status or the test times out.
func waitForStatus(t *testing.T, svc *Service, id, want string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	last := ""
	for time.Now().Before(deadline) {
		sb, err := svc.Get(context.Background(), id)
		if err == nil {
			last = sb.Status
			if last == want {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("status = %q, want %q", last, want)
}
