package sandbox

import (
	"context"
	"fmt"
	"sync"
	"time"

	"sandbox/runtime/internal/docker"
)

// fakeContainer is the in-memory state of one container.
type fakeContainer struct {
	spec     docker.Spec
	status   string
	exitCode int
	// healthPolls is how many inspects must happen while running before the container
	// reports healthy. -1 means it never becomes healthy.
	healthPolls int
	healthy     bool
	copied      map[string][]byte
}

// fakeDocker is an in-memory Docker daemon for service tests.
type fakeDocker struct {
	mu         sync.Mutex
	images     map[string]bool
	containers map[string]*fakeContainer // keyed by name

	// Fault injection.
	failCreate  error
	failCopy    error
	failStart   error
	failStop    error
	failRemove  error
	imageGate   chan struct{} // if non-nil, ImageExists waits for it to be closed
	creates     int
	nextHealthy int // healthPolls assigned to new containers
	startExits  bool
	// stopExitCode is the exit code recorded when a container is stopped.
	stopExitCode int
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{
		images:      map[string]bool{"sandbox-base:py3.13-uv0.13.7": true},
		containers:  map[string]*fakeContainer{},
		nextHealthy: 2,
	}
}

func (f *fakeDocker) ImageExists(_ context.Context, ref string) (bool, error) {
	// imageGate blocks outside the service lock, so tests can hold a creation in flight.
	if gate := f.imageGate; gate != nil {
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.images[ref], nil
}

func (f *fakeDocker) ContainerCreate(_ context.Context, spec docker.Spec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failCreate != nil {
		return "", f.failCreate
	}
	if _, ok := f.containers[spec.Name]; ok {
		return "", fmt.Errorf("%w: name in use", docker.ErrConflict)
	}
	f.creates++
	f.containers[spec.Name] = &fakeContainer{
		spec:        spec,
		status:      docker.StateCreated,
		healthPolls: f.nextHealthy,
		copied:      map[string][]byte{},
	}
	return "docker-" + spec.Name, nil
}

func (f *fakeDocker) get(ref string) (*fakeContainer, error) {
	c, ok := f.containers[ref]
	if !ok {
		return nil, fmt.Errorf("%w: no such container %s", docker.ErrNotFound, ref)
	}
	return c, nil
}

func (f *fakeDocker) CopyFileToContainer(_ context.Context, id, dir, name string, content []byte, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failCopy != nil {
		return f.failCopy
	}
	c, err := f.get(id)
	if err != nil {
		return err
	}
	c.copied[dir+"/"+name] = append([]byte(nil), content...)
	return nil
}

func (f *fakeDocker) ContainerStart(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failStart != nil {
		return f.failStart
	}
	c, err := f.get(id)
	if err != nil {
		return err
	}
	if f.startExits {
		c.status = docker.StateExited
		c.exitCode = 1
		return nil
	}
	c.status = docker.StateRunning
	// A (re)start begins a fresh health countdown, as a real container does.
	c.healthy = false
	c.healthPolls = f.nextHealthy
	return nil
}

func (f *fakeDocker) ContainerStop(_ context.Context, id string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failStop != nil {
		return f.failStop
	}
	c, err := f.get(id)
	if err != nil {
		return err
	}
	c.status = docker.StateExited
	c.exitCode = f.stopExitCode
	c.healthy = false
	return nil
}

func (f *fakeDocker) ContainerRemove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failRemove != nil {
		return f.failRemove
	}
	if _, err := f.get(id); err != nil {
		return err
	}
	delete(f.containers, id)
	return nil
}

func (f *fakeDocker) ContainerInspect(_ context.Context, id string) (docker.Inspect, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.get(id)
	if err != nil {
		return docker.Inspect{}, err
	}
	in := docker.Inspect{
		ID:        "docker-" + id,
		Name:      id,
		Labels:    c.spec.Labels,
		Image:     c.spec.Image,
		Status:    c.status,
		Running:   c.status == docker.StateRunning,
		ExitCode:  c.exitCode,
		Health:    docker.HealthNone,
		HostPorts: map[string]int{},
	}
	if c.spec.Labels != nil {
		in.Created = time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	}
	if c.status == docker.StateRunning {
		in.Health = docker.HealthStarting
		if c.healthPolls >= 0 {
			if c.healthPolls == 0 {
				c.healthy = true
			} else {
				c.healthPolls--
			}
		}
		if c.healthy {
			in.Health = docker.HealthHealthy
		}
		in.HostPorts[fmt.Sprintf("%d/tcp", c.spec.ContainerPort)] = 49153
	}
	return in, nil
}

func (f *fakeDocker) ContainersByLabel(_ context.Context, key, value string) ([]docker.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []docker.Summary
	for name, c := range f.containers {
		if c.spec.Labels[key] == value {
			out = append(out, docker.Summary{Name: name, Labels: c.spec.Labels, Status: c.status})
		}
	}
	return out, nil
}

// count returns the number of containers currently held (test helper).
func (f *fakeDocker) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.containers)
}

func specWithLabels(l map[string]string) docker.Spec {
	return docker.Spec{Labels: l}
}

// ContainerStatsOnce returns fixed stats for a running container.
func (f *fakeDocker) ContainerStatsOnce(_ context.Context, id string) (docker.Stats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.get(id); err != nil {
		return docker.Stats{}, err
	}
	return docker.Stats{CPUPercent: 12.5, MemoryUsage: 1 << 20, MemoryLimit: 1 << 30, PidsCurrent: 3}, nil
}

var errBoom = errorString("boom")

type errorString string

func (e errorString) Error() string { return string(e) }
