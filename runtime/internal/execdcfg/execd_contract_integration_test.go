//go:build integration

package execdcfg

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRealExecdAcceptsGeneratedConfig starts the sandbox base image with a generated
// config file and checks that Execd reads it and reports healthy. The container name
// is unique to this test run and only that container is removed.
func TestRealExecdAcceptsGeneratedConfig(t *testing.T) {
	const image = "sandbox-base:py3.13-uv0.13.7"
	if out, err := exec.Command("docker", "image", "inspect", image).CombinedOutput(); err != nil {
		t.Skipf("image %s not available: %s", image, out)
	}
	// EXECD_PORT comes from SANDBOX.env (loaded by `make integration`). It is not guessed.
	v := os.Getenv("EXECD_PORT")
	if v == "" {
		t.Fatal("EXECD_PORT is not set; run via `make integration` so SANDBOX.env is loaded")
	}
	var port int
	if _, err := fmt.Sscanf(v, "%d", &port); err != nil || port < 1 {
		t.Fatalf("EXECD_PORT %q is not a valid port", v)
	}
	body, err := Build(Options{Port: port, ShutdownGrace: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("sbx-execdcfg-contract-%d", time.Now().UnixNano())
	run := exec.Command("docker", "run", "-d", "--name", name,
		"-e", "EXECD_CONFIG="+ConfigPath,
		"-v", cfgPath+":"+ConfigPath+":ro",
		image)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	deadline := time.Now().Add(60 * time.Second)
	status := ""
	for time.Now().Before(deadline) {
		out, _ := exec.Command("docker", "inspect", "-f", "{{.State.Health.Status}}", name).CombinedOutput()
		status = strings.TrimSpace(string(out))
		if status == "healthy" || status == "unhealthy" {
			break
		}
		time.Sleep(time.Second)
	}
	if status != "healthy" {
		logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
		t.Fatalf("health = %q, want healthy; logs:\n%s", status, logs)
	}
}
