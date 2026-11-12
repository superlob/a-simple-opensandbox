package sandbox

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"sandbox/runtime/internal/docker"
)

var idPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// Get returns the current view of a Sandbox. It only reports containers that carry
// Runtime's management labels, so unrelated containers are never touched.
func (s *Service) Get(ctx context.Context, id string) (Sandbox, error) {
	ref, err := s.refFor(ctx, id)
	if err != nil {
		return Sandbox{}, err
	}
	return s.view(ctx, ref)
}

// Start starts a stopped Sandbox, waits for it to become healthy, and refreshes its
// endpoint (the published port may change). A Sandbox that is already running is
// returned unchanged. If it does not become healthy, it is stopped again and the
// container and its workspace are kept.
func (s *Service) Start(ctx context.Context, id string) (Sandbox, error) {
	unlock := s.locks.lock(id)
	defer unlock()

	ref, in, err := s.resolve(ctx, id)
	if err != nil {
		return Sandbox{}, err
	}
	if in.Running {
		return s.view(ctx, ref)
	}
	if err := s.docker.ContainerStart(ctx, ref); err != nil {
		return Sandbox{}, s.translate("start container", err)
	}
	if err := s.waitHealthy(ctx, ref, 0); err != nil {
		s.stopQuietly(ref)
		s.endpoints.Invalidate(id)
		return Sandbox{}, err
	}
	in, err = s.docker.ContainerInspect(ctx, ref)
	if err != nil {
		return Sandbox{}, s.translate("inspect sandbox", err)
	}
	if _, err := s.endpoints.Refresh(id, s.cfg.PublicHost, s.cfg.ExecdPort, in); err != nil {
		s.stopQuietly(ref)
		return Sandbox{}, fmt.Errorf("%w: %v", ErrStartFailed, err)
	}
	return s.view(ctx, ref)
}

// Stop sends SIGTERM and waits up to StopTimeout before SIGKILL, so Execd can shut
// down gracefully. A Sandbox that is already stopped is returned unchanged.
// The endpoint is invalidated because it is no longer reachable.
func (s *Service) Stop(ctx context.Context, id string) (Sandbox, error) {
	unlock := s.locks.lock(id)
	defer unlock()

	ref, in, err := s.resolve(ctx, id)
	if err != nil {
		return Sandbox{}, err
	}
	s.endpoints.Invalidate(id)
	if !in.Running {
		return s.view(ctx, ref)
	}
	if err := s.docker.ContainerStop(ctx, ref, s.cfg.StopTimeout.Std()); err != nil {
		return Sandbox{}, s.translate("stop container", err)
	}
	return s.view(ctx, ref)
}

// Delete stops (if needed) and removes the container. The workspace lives in the
// container's writable layer, so it is removed with it. Deleting a Sandbox that no
// longer exists succeeds, so retrying an interrupted delete is safe.
//
// Docker cannot change labels on an existing container, so a partially failed delete
// is not recorded in a label. Instead, the container stays visible and a retry finishes the job.
func (s *Service) Delete(ctx context.Context, id string) error {
	unlock := s.locks.lock(id)
	defer unlock()

	ref, in, err := s.resolve(ctx, id)
	if errors.Is(err, ErrNotFound) {
		s.endpoints.Invalidate(id)
		return nil
	}
	if err != nil {
		return err
	}
	s.endpoints.Invalidate(id)
	if in.Running {
		if err := s.docker.ContainerStop(ctx, ref, s.cfg.StopTimeout.Std()); err != nil {
			return s.translate("stop before delete", err)
		}
	}
	if err := s.docker.ContainerRemove(ctx, ref); err != nil {
		if errors.Is(err, docker.ErrNotFound) {
			return nil
		}
		return s.translate("remove container", err)
	}
	return nil
}

// refFor returns the container reference for a Sandbox ID after checking that the
// container is managed by Runtime and belongs to that ID.
func (s *Service) refFor(ctx context.Context, id string) (string, error) {
	_, _, err := s.resolve(ctx, id)
	if err != nil {
		return "", err
	}
	return NamePrefix + id, nil
}

// resolve inspects the container for a Sandbox ID and verifies its labels.
func (s *Service) resolve(ctx context.Context, id string) (string, docker.Inspect, error) {
	if !idPattern.MatchString(id) {
		return "", docker.Inspect{}, fmt.Errorf("%w: malformed sandbox id", ErrNotFound)
	}
	ref := NamePrefix + id
	in, err := s.docker.ContainerInspect(ctx, ref)
	if err != nil {
		if errors.Is(err, docker.ErrNotFound) {
			return "", docker.Inspect{}, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return "", docker.Inspect{}, s.translate("inspect sandbox", err)
	}
	if in.Labels[LabelManaged] != "true" || in.Labels[LabelID] != id {
		return "", docker.Inspect{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return ref, in, nil
}

// stopQuietly stops a container during error handling. It uses its own context so a
// cancelled caller still leaves the Sandbox stopped.
func (s *Service) stopQuietly(ref string) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.StopTimeout.Std()+10*time.Second)
	defer cancel()
	_ = s.docker.ContainerStop(ctx, ref, s.cfg.StopTimeout.Std())
}

// keyLocks serialises lifecycle operations on the same Sandbox ID.
type keyLocks struct {
	mu    sync.Mutex
	locks map[string]*keyLock
}

type keyLock struct {
	mu   sync.Mutex
	refs int
}

func newKeyLocks() *keyLocks {
	return &keyLocks{locks: map[string]*keyLock{}}
}

func (k *keyLocks) lock(key string) func() {
	k.mu.Lock()
	l, ok := k.locks[key]
	if !ok {
		l = &keyLock{}
		k.locks[key] = l
	}
	l.refs++
	k.mu.Unlock()

	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		k.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}
