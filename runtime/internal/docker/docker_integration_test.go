//go:build integration

package docker

import (
	"context"
	"testing"
	"time"
)

// Read-only checks against the real local Docker daemon.
func TestRealDaemonPingAndImage(t *testing.T) {
	c, err := New(Options{CallTimeout: 15 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()
	ctx := context.Background()
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("Ping against real daemon: %v", err)
	}
	ok, err := c.ImageExists(ctx, "sandbox-base:py3.13-uv0.13.7")
	if err != nil {
		t.Fatalf("ImageExists: %v", err)
	}
	if !ok {
		t.Error("sandbox-base:py3.13-uv0.13.7 should exist locally (built in R1)")
	}
	if _, err := c.ContainerInspect(ctx, "sandbox-r2-does-not-exist"); err == nil {
		t.Error("inspect of missing container should fail")
	}
	if _, err := c.ContainersByLabel(ctx, "sandbox.managed", "true"); err != nil {
		t.Errorf("ContainersByLabel: %v", err)
	}
}
