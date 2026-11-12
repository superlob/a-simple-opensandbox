package endpoint

import (
	"errors"
	"sync"
	"testing"

	"sandbox/runtime/internal/docker"
)

// running returns a running container publishing 44772/tcp on hostPort (0 = unpublished).
func running(_ int, hostPort int) docker.Inspect {
	in := docker.Inspect{Status: docker.StateRunning, Running: true, HostPorts: map[string]int{}}
	if hostPort > 0 {
		in.HostPorts["44772/tcp"] = hostPort
	}
	return in
}

func TestResolveBuildsURL(t *testing.T) {
	got, err := Resolve("203.0.113.5", 44772, running(44772, 49153))
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://203.0.113.5:49153" {
		t.Errorf("url = %q", got)
	}
}

func TestResolveIPv6Host(t *testing.T) {
	got, err := Resolve("2001:db8::1", 44772, running(44772, 49153))
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://[2001:db8::1]:49153" {
		t.Errorf("url = %q", got)
	}
}

func TestResolveRejectsStoppedContainer(t *testing.T) {
	in := running(44772, 49153)
	in.Running = false
	in.Status = docker.StateExited
	if _, err := Resolve("h", 44772, in); !errors.Is(err, ErrNotAvailable) {
		t.Fatalf("err = %v, want ErrNotAvailable", err)
	}
}

func TestResolveRejectsUnpublishedPort(t *testing.T) {
	if _, err := Resolve("h", 44772, running(44772, 0)); !errors.Is(err, ErrNotAvailable) {
		t.Fatalf("err = %v, want ErrNotAvailable", err)
	}
}

func TestResolveRejectsEmptyPublicHost(t *testing.T) {
	if _, err := Resolve("", 44772, running(44772, 49153)); err == nil {
		t.Fatal("expected error for empty public host")
	}
}

func TestRegistryLifecycle(t *testing.T) {
	r := NewRegistry()
	// Start: endpoint observed with port 49153.
	if _, err := r.Refresh("s1", "h", 44772, running(44772, 49153)); err != nil {
		t.Fatal(err)
	}
	if u, ok := r.Get("s1"); !ok || u != "http://h:49153" {
		t.Fatalf("after start: %q %v", u, ok)
	}
	// Restart: the daemon assigns a new host port; refresh must replace the cached value.
	if _, err := r.Refresh("s1", "h", 44772, running(44772, 49999)); err != nil {
		t.Fatal(err)
	}
	if u, _ := r.Get("s1"); u != "http://h:49999" {
		t.Fatalf("after restart: %q", u)
	}
	// Stop: refresh against a stopped container drops the entry.
	stopped := running(44772, 0)
	stopped.Running = false
	stopped.Status = docker.StateExited
	if _, err := r.Refresh("s1", "h", 44772, stopped); !errors.Is(err, ErrNotAvailable) {
		t.Fatalf("refresh stopped: %v", err)
	}
	if _, ok := r.Get("s1"); ok {
		t.Fatal("stopped sandbox must not keep a cached endpoint")
	}
}

func TestRegistryInvalidateAndUnknown(t *testing.T) {
	r := NewRegistry()
	r.Set("a", "http://x:1")
	r.Invalidate("a")
	if _, ok := r.Get("a"); ok {
		t.Error("invalidate did not remove entry")
	}
	r.Invalidate("never-set") // must not panic
	if _, ok := r.Get("never-set"); ok {
		t.Error("unknown id should not be found")
	}
}

func TestRegistryConcurrentAccess(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "s"
			r.Set(id, "http://h:1")
			r.Get(id)
			if i%2 == 0 {
				r.Invalidate(id)
			}
		}(i)
	}
	wg.Wait()
}
