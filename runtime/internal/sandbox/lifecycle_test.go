package sandbox

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"sandbox/runtime/internal/config"
)

// createOne creates a Sandbox against the fake and returns its ID.
func createOne(t *testing.T, svc *Service) string {
	t.Helper()
	sb, err := svc.Create(context.Background(), CreateRequest{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return sb.ID
}

func TestStopMakesSandboxStoppedAndInvalidatesEndpoint(t *testing.T) {
	f := newFakeDocker()
	svc, reg := newTestService(t, f, nil)
	id := createOne(t, svc)

	sb, err := svc.Stop(context.Background(), id)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if sb.Status != StatusStopped || sb.Endpoint != "" {
		t.Errorf("after stop: status=%q endpoint=%q", sb.Status, sb.Endpoint)
	}
	if _, ok := reg.Get(id); ok {
		t.Error("registry still holds endpoint after stop")
	}
	if f.containers["sbx-"+id].status != "exited" {
		t.Errorf("container status = %q", f.containers["sbx-"+id].status)
	}
}

func TestStopIsIdempotent(t *testing.T) {
	f := newFakeDocker()
	svc, _ := newTestService(t, f, nil)
	id := createOne(t, svc)
	if _, err := svc.Stop(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	sb, err := svc.Stop(context.Background(), id)
	if err != nil {
		t.Fatalf("second stop: %v", err)
	}
	if sb.Status != StatusStopped {
		t.Errorf("status = %q", sb.Status)
	}
}

func TestStartRestoresReadyAndRefreshesEndpoint(t *testing.T) {
	f := newFakeDocker()
	svc, reg := newTestService(t, f, nil)
	id := createOne(t, svc)
	if _, err := svc.Stop(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	// The daemon assigns a different host port after restart.
	sb, err := svc.Start(context.Background(), id)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if sb.Status != StatusReady || sb.Endpoint == "" {
		t.Errorf("after start: %+v", sb)
	}
	if got, ok := reg.Get(id); !ok || got != sb.Endpoint {
		t.Errorf("registry = %q %v, want %q", got, ok, sb.Endpoint)
	}
}

func TestStartIsIdempotentWhenRunning(t *testing.T) {
	f := newFakeDocker()
	svc, _ := newTestService(t, f, nil)
	id := createOne(t, svc)
	sb, err := svc.Start(context.Background(), id)
	if err != nil {
		t.Fatalf("Start on running sandbox: %v", err)
	}
	if sb.Status != StatusReady {
		t.Errorf("status = %q", sb.Status)
	}
}

func TestStartKeepsContainerWhenNotHealthy(t *testing.T) {
	f := newFakeDocker()
	svc, reg := newTestService(t, f, func(c *config.Config) { c.ReadyTimeout = config.Duration(30 * time.Millisecond) })
	id := createOne(t, svc)
	if _, err := svc.Stop(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	f.nextHealthy = -1 // the restarted service never becomes healthy

	_, err := svc.Start(context.Background(), id)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	c, ok := f.containers["sbx-"+id]
	if !ok {
		t.Fatal("container must be kept after a failed start")
	}
	if c.status != "exited" {
		t.Errorf("container should be stopped again, status = %q", c.status)
	}
	if _, ok := reg.Get(id); ok {
		t.Error("registry must not hold an endpoint for an unhealthy sandbox")
	}
}

func TestDeleteRemovesContainerAndEndpoint(t *testing.T) {
	f := newFakeDocker()
	svc, reg := newTestService(t, f, nil)
	id := createOne(t, svc)
	if err := svc.Delete(context.Background(), id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if f.count() != 0 {
		t.Errorf("containers left = %d", f.count())
	}
	if _, ok := reg.Get(id); ok {
		t.Error("registry still holds endpoint after delete")
	}
}

func TestDeleteStoppedSandbox(t *testing.T) {
	f := newFakeDocker()
	svc, _ := newTestService(t, f, nil)
	id := createOne(t, svc)
	if _, err := svc.Stop(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(context.Background(), id); err != nil {
		t.Fatalf("Delete stopped: %v", err)
	}
	if f.count() != 0 {
		t.Errorf("containers left = %d", f.count())
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	f := newFakeDocker()
	svc, _ := newTestService(t, f, nil)
	id := createOne(t, svc)
	if err := svc.Delete(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(context.Background(), id); err != nil {
		t.Fatalf("second delete should succeed: %v", err)
	}
}

func TestDeleteRetryAfterFailedRemove(t *testing.T) {
	f := newFakeDocker()
	svc, _ := newTestService(t, f, nil)
	id := createOne(t, svc)

	f.failRemove = errors.New("daemon hiccup")
	if err := svc.Delete(context.Background(), id); err == nil {
		t.Fatal("expected error on failed remove")
	}
	// The container is still visible, so the caller can see it and retry.
	if _, err := svc.Get(context.Background(), id); err != nil {
		t.Fatalf("container should still be visible after failed delete: %v", err)
	}
	f.failRemove = nil
	if err := svc.Delete(context.Background(), id); err != nil {
		t.Fatalf("retry delete: %v", err)
	}
	if f.count() != 0 {
		t.Errorf("containers left = %d", f.count())
	}
}

func TestLifecycleRejectsForeignAndMalformedIDs(t *testing.T) {
	f := newFakeDocker()
	svc, _ := newTestService(t, f, nil)

	// A container with the right name but no management labels must not be touched.
	f.containers["sbx-11111111-1111-4111-8111-111111111111"] = &fakeContainer{
		spec:   specWithLabels(map[string]string{}),
		status: "running",
		copied: map[string][]byte{},
	}
	foreign := "11111111-1111-4111-8111-111111111111"
	for name, op := range map[string]func() error{
		"get":    func() error { _, err := svc.Get(context.Background(), foreign); return err },
		"start":  func() error { _, err := svc.Start(context.Background(), foreign); return err },
		"stop":   func() error { _, err := svc.Stop(context.Background(), foreign); return err },
		"delete": func() error { return svc.Delete(context.Background(), foreign) },
	} {
		err := op()
		if name == "delete" {
			// Delete of a missing sandbox is a success; a foreign container is not reported as ours.
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s on foreign container: err = %v, want ErrNotFound", name, err)
		}
	}
	if _, ok := f.containers["sbx-11111111-1111-4111-8111-111111111111"]; !ok {
		t.Error("foreign container was removed")
	}
	for _, bad := range []string{"", "../x", "not-a-uuid", "sbx-11111111"} {
		if _, err := svc.Get(context.Background(), bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get(%q) err = %v, want ErrNotFound", bad, err)
		}
	}
}

func TestLifecycleOpsOnSameIDAreSerialised(t *testing.T) {
	f := newFakeDocker()
	svc, _ := newTestService(t, f, nil)
	id := createOne(t, svc)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = svc.Stop(context.Background(), id) }()
		go func() { defer wg.Done(); _, _ = svc.Start(context.Background(), id) }()
	}
	wg.Wait()
	if f.count() != 1 {
		t.Fatalf("containers = %d, want 1", f.count())
	}
	// Whatever the final state, it must be consistent: running ⇔ endpoint known.
	sb, err := svc.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	running := sb.Status == StatusReady || sb.Status == StatusStarting
	if running != (sb.Endpoint != "") {
		t.Errorf("inconsistent state: status=%q endpoint=%q", sb.Status, sb.Endpoint)
	}
}

func TestKeyLocksReleaseUnusedEntries(t *testing.T) {
	k := newKeyLocks()
	u := k.lock("a")
	u()
	k.mu.Lock()
	n := len(k.locks)
	k.mu.Unlock()
	if n != 0 {
		t.Errorf("lock entries left = %d", n)
	}
}
