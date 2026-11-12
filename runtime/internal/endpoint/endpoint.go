// Package endpoint maps Sandbox IDs to the Execd HTTP URL that external callers use.
//
// Docker is the source of truth for published host ports, and those ports can change
// when a container is restarted. The registry therefore caches only what was last
// observed, and callers must refresh it after every start and invalidate it on stop.
package endpoint

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"

	"sandbox/runtime/internal/docker"
)

// ErrNotAvailable means the Sandbox has no reachable Execd endpoint right now
// (for example, it is stopped, or the port is not published).
var ErrNotAvailable = errors.New("execd endpoint not available")

// Resolve builds the Execd URL from a container's inspect result.
//
// publicHost is RUNTIME_PUBLIC_HOST. containerPort is EXECD_PORT. The container must be
// running and must have a published host port for containerPort.
func Resolve(publicHost string, containerPort int, in docker.Inspect) (string, error) {
	if publicHost == "" {
		return "", errors.New("public host is empty")
	}
	if !in.Running || in.Status != docker.StateRunning {
		return "", fmt.Errorf("%w: container is %s", ErrNotAvailable, in.Status)
	}
	key := strconv.Itoa(containerPort) + "/tcp"
	hostPort, ok := in.HostPorts[key]
	if !ok {
		return "", fmt.Errorf("%w: port %s is not published", ErrNotAvailable, key)
	}
	return "http://" + net.JoinHostPort(publicHost, strconv.Itoa(hostPort)), nil
}

// Registry is a concurrency-safe map from Sandbox ID to its last known endpoint.
type Registry struct {
	mu        sync.RWMutex
	endpoints map[string]string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{endpoints: map[string]string{}}
}

// Set records the endpoint observed for a Sandbox, replacing any previous value.
func (r *Registry) Set(id, url string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.endpoints[id] = url
}

// Get returns the cached endpoint. ok is false if none is known.
func (r *Registry) Get(id string) (url string, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	url, ok = r.endpoints[id]
	return url, ok
}

// Invalidate removes the cached endpoint, e.g. after the Sandbox stops or is deleted.
func (r *Registry) Invalidate(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.endpoints, id)
}

// Refresh recomputes the endpoint from a fresh inspect result and updates the cache.
// If the endpoint is not available, the cached entry is removed and the error returned.
func (r *Registry) Refresh(id, publicHost string, containerPort int, in docker.Inspect) (string, error) {
	url, err := Resolve(publicHost, containerPort, in)
	if err != nil {
		r.Invalidate(id)
		return "", err
	}
	r.Set(id, url)
	return url, nil
}

// Len returns the number of cached endpoints.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.endpoints)
}
