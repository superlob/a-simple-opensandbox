// Package sandbox implements Sandbox lifecycle logic on top of the Docker adapter.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"sandbox/runtime/internal/config"
	"sandbox/runtime/internal/docker"
	"sandbox/runtime/internal/endpoint"
	"sandbox/runtime/internal/execdcfg"
)

// Label keys written to every managed container (runtime-design §2.1).
const (
	LabelManaged        = "sandbox.managed"
	LabelID             = "sandbox.id"
	LabelCreatedAt      = "sandbox.created_at"
	LabelImage          = "sandbox.image"
	LabelRuntimeVersion = "sandbox.runtime_version"
	LabelClientID       = "sandbox.client_id"
	LabelCPUs           = "sandbox.cpus"
	LabelMemoryMB       = "sandbox.memory_mb"
	LabelPidsLimit      = "sandbox.pids_limit"
	// NamePrefix is prepended to the Sandbox ID to form the container name.
	NamePrefix = "sbx-"
)

// Version is reported in the runtime_version label. Set at build time.
var Version = "dev"

// Docker is the subset of the Docker adapter the sandbox service needs.
// *docker.Client satisfies it; tests use an in-memory fake.
type Docker interface {
	ImageExists(ctx context.Context, ref string) (bool, error)
	ContainerCreate(ctx context.Context, spec docker.Spec) (string, error)
	CopyFileToContainer(ctx context.Context, id, dir, name string, content []byte, mode int64) error
	ContainerStart(ctx context.Context, id string) error
	ContainerStop(ctx context.Context, id string, timeout time.Duration) error
	ContainerRemove(ctx context.Context, id string) error
	ContainerInspect(ctx context.Context, id string) (docker.Inspect, error)
	ContainersByLabel(ctx context.Context, key, value string) ([]docker.Summary, error)
	ContainerStatsOnce(ctx context.Context, id string) (docker.Stats, error)
}

// The real Docker adapter must satisfy the interface.
var _ Docker = (*docker.Client)(nil)

// Sandbox is the caller-facing view of a Sandbox.
type Sandbox struct {
	ID        string
	ClientID  string
	Image     string
	CreatedAt time.Time
	Status    string
	CPUs      float64
	MemoryMB  int64
	PidsLimit int64
	Endpoint  string // empty unless the Sandbox is ready
}

// Service owns Sandbox creation, lifecycle, and endpoint bookkeeping.
type Service struct {
	cfg       config.Config
	docker    Docker
	endpoints *endpoint.Registry
	now       func() time.Time
	pollEvery time.Duration

	locks *keyLocks
	// leftoverScope, if non-nil, limits which leftover IDs Recover may remove.
	// Production leaves it nil (no limit). Tests set it to their own IDs.
	leftoverScope map[string]bool
	mu            sync.Mutex
	pending       int             // creations in progress, counted against MaxSandboxes
	inflight      map[string]bool // client IDs with a creation in progress
}

// Options configure a Service. Zero values select production defaults.
type Options struct {
	Now          func() time.Time
	PollInterval time.Duration
}

// NewService returns a Service. cfg must already be validated.
func NewService(cfg config.Config, d Docker, endpoints *endpoint.Registry, o Options) *Service {
	s := &Service{
		cfg:       cfg,
		docker:    d,
		endpoints: endpoints,
		now:       o.Now,
		pollEvery: o.PollInterval,
		inflight:  map[string]bool{},
		locks:     newKeyLocks(),
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.pollEvery <= 0 {
		s.pollEvery = 500 * time.Millisecond
	}
	return s
}

// Create validates the request, creates the container, delivers the Execd config,
// starts it, and waits until Docker reports it healthy. On any failure the container
// is removed. If ClientID matches an existing Sandbox, that Sandbox is returned.
func (s *Service) Create(ctx context.Context, req CreateRequest) (Sandbox, error) {
	lim, err := resolveLimits(s.cfg, req)
	if err != nil {
		return Sandbox{}, err
	}
	envList, err := validateEnv(req.Env)
	if err != nil {
		return Sandbox{}, err
	}

	// Reserve a slot or find an existing Sandbox for ClientID. Listing and the
	// capacity check happen under one lock, so concurrent creations cannot exceed the cap.
	s.mu.Lock()
	if req.ClientID != "" && s.inflight[req.ClientID] {
		s.mu.Unlock()
		return Sandbox{}, fmt.Errorf("%w: creation with this client id is in progress", ErrConflict)
	}
	managed, err := s.docker.ContainersByLabel(ctx, LabelManaged, "true")
	if err != nil {
		s.mu.Unlock()
		return Sandbox{}, s.translate("list sandboxes", err)
	}
	if req.ClientID != "" {
		for _, m := range managed {
			if m.Labels[LabelClientID] == req.ClientID {
				s.mu.Unlock()
				return s.view(ctx, m.Name)
			}
		}
	}
	if len(managed)+s.pending >= s.cfg.MaxSandboxes {
		s.mu.Unlock()
		return Sandbox{}, fmt.Errorf("%w: at most %d sandboxes", ErrResourceExhausted, s.cfg.MaxSandboxes)
	}
	s.pending++
	if req.ClientID != "" {
		s.inflight[req.ClientID] = true
	}
	s.mu.Unlock()

	// pending covers the window before the container exists. Once ContainerCreate
	// returns, the container is listed by label and counts on its own. The placeholder
	// is released in the same critical section as ContainerCreate, so a concurrent
	// listing never sees the container and the placeholder together (double count).
	releasedPending := false
	releasePendingLocked := func() {
		if !releasedPending {
			releasedPending = true
			s.pending--
		}
	}
	defer func() {
		s.mu.Lock()
		releasePendingLocked()
		s.mu.Unlock()
	}()
	defer func() {
		if req.ClientID != "" {
			s.mu.Lock()
			delete(s.inflight, req.ClientID)
			s.mu.Unlock()
		}
	}()

	image := s.cfg.SandboxImage
	exists, err := s.docker.ImageExists(ctx, image)
	if err != nil {
		return Sandbox{}, s.translate("check image", err)
	}
	if !exists {
		return Sandbox{}, fmt.Errorf("%w: %s", ErrImageUnavailable, image)
	}

	id, err := newID()
	if err != nil {
		return Sandbox{}, err
	}
	ref := NamePrefix + id
	createdAt := s.now().UTC().Truncate(time.Second) // labels store seconds; keep the response consistent
	bind, err := bindAddr(s.cfg.BindHost)
	if err != nil {
		return Sandbox{}, err
	}

	cfgJSON, err := execdcfg.Build(execdcfg.Options{Port: s.cfg.ExecdPort, ShutdownGrace: 10 * time.Second})
	if err != nil {
		return Sandbox{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}

	labels := map[string]string{
		LabelManaged:        "true",
		LabelID:             id,
		LabelCreatedAt:      createdAt.Format(time.RFC3339),
		LabelImage:          image,
		LabelRuntimeVersion: Version,
	}
	if req.ClientID != "" {
		labels[LabelClientID] = req.ClientID
	}
	// Limits are kept as labels because Docker inspect output does not expose them
	// in a form the service reads back. Labels are fixed at creation, which matches
	// that limits are fixed for the life of the container.
	labels[LabelCPUs] = strconv.FormatFloat(lim.CPUs, 'f', -1, 64)
	labels[LabelMemoryMB] = strconv.FormatInt(lim.MemoryMB, 10)
	labels[LabelPidsLimit] = strconv.FormatInt(lim.PidsLimit, 10)
	env := append([]string{"EXECD_CONFIG=" + execdcfg.ConfigPath}, envList...)

	spec := docker.Spec{
		Name:          ref,
		Image:         image,
		Env:           env,
		Labels:        labels,
		ContainerPort: s.cfg.ExecdPort,
		BindHost:      bind,
		CPUs:          lim.CPUs,
		MemoryBytes:   lim.MemoryMB << 20,
		PidsLimit:     lim.PidsLimit,
		StopTimeout:   s.cfg.StopTimeout.Std(),
	}

	s.mu.Lock()
	_, err = s.docker.ContainerCreate(ctx, spec)
	releasePendingLocked()
	s.mu.Unlock()
	if err != nil {
		return Sandbox{}, s.translate("create container", err)
	}

	if err := s.bootstrap(ctx, ref, cfgJSON, req.Timeout); err != nil {
		return Sandbox{}, s.fail(ref, err)
	}

	in, err := s.docker.ContainerInspect(ctx, ref)
	if err != nil {
		return Sandbox{}, s.fail(ref, s.translate("inspect sandbox", err))
	}
	url, err := s.endpoints.Refresh(id, s.cfg.PublicHost, s.cfg.ExecdPort, in)
	if err != nil {
		return Sandbox{}, s.fail(ref, fmt.Errorf("%w: %v", ErrStartFailed, err))
	}
	return Sandbox{
		ID:        id,
		ClientID:  req.ClientID,
		Image:     image,
		CreatedAt: createdAt,
		Status:    StatusOf(in),
		CPUs:      lim.CPUs,
		MemoryMB:  lim.MemoryMB,
		PidsLimit: lim.PidsLimit,
		Endpoint:  url,
	}, nil
}

// bootstrap delivers the Execd config, starts the container, and waits for healthy.
func (s *Service) bootstrap(ctx context.Context, ref string, cfgJSON []byte, timeout time.Duration) error {
	if err := s.docker.CopyFileToContainer(ctx, ref, execdcfg.ConfigDir, "config.json", cfgJSON, 0o644); err != nil {
		return s.translate("write execd config", err)
	}
	if err := s.docker.ContainerStart(ctx, ref); err != nil {
		return s.translate("start container", err)
	}
	return s.waitHealthy(ctx, ref, timeout)
}

// waitHealthy polls Docker until the container is healthy. It fails fast if the
// container exits, and fails with ErrTimeout when the deadline passes. A transient
// "unhealthy" result is not treated as final, because Docker can recover it.
func (s *Service) waitHealthy(ctx context.Context, ref string, timeout time.Duration) error {
	if timeout == 0 {
		timeout = s.cfg.ReadyTimeout.Std()
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(s.pollEvery)
	defer ticker.Stop()
	for {
		in, err := s.docker.ContainerInspect(waitCtx, ref)
		if err != nil {
			if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("%w after %s", ErrTimeout, timeout)
			}
			return s.translate("inspect while waiting", err)
		}
		switch {
		case in.Status == docker.StateExited || in.Status == docker.StateDead:
			return fmt.Errorf("%w: container exited with code %d", ErrStartFailed, in.ExitCode)
		case in.Running && in.Health == docker.HealthHealthy:
			return nil
		}
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%w after %s", ErrTimeout, timeout)
		case <-ticker.C:
		}
	}
}

// fail removes the container for a failed creation and returns cause. If the
// cleanup also fails, the leftover container is named in the returned error so that
// operators can see it; discovery (R8) reclaims managed containers on restart.
// Cleanup uses its own context, so a cancelled caller cannot leave the container behind.
func (s *Service) fail(ref string, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*s.cfg.StopTimeout.Std()+10*time.Second)
	defer cancel()
	_ = s.docker.ContainerStop(ctx, ref, 0)
	err := s.docker.ContainerRemove(ctx, ref)
	if err == nil || errors.Is(err, docker.ErrNotFound) {
		return cause
	}
	return fmt.Errorf("%w (cleanup of container %s also failed, leftover may remain: %v)", cause, ref, err)
}

// view builds a Sandbox from current Docker state for an existing container.
func (s *Service) view(ctx context.Context, ref string) (Sandbox, error) {
	in, err := s.docker.ContainerInspect(ctx, ref)
	if err != nil {
		return Sandbox{}, s.translate("inspect sandbox", err)
	}
	id := in.Labels[LabelID]
	sb := Sandbox{
		ID:       id,
		ClientID: in.Labels[LabelClientID],
		Image:    in.Labels[LabelImage],
		Status:   StatusOf(in),
	}
	if t, err := time.Parse(time.RFC3339, in.Labels[LabelCreatedAt]); err == nil {
		sb.CreatedAt = t
	}
	sb.CPUs, _ = strconv.ParseFloat(in.Labels[LabelCPUs], 64)
	sb.MemoryMB, _ = strconv.ParseInt(in.Labels[LabelMemoryMB], 10, 64)
	sb.PidsLimit, _ = strconv.ParseInt(in.Labels[LabelPidsLimit], 10, 64)
	if url, err := endpoint.Resolve(s.cfg.PublicHost, s.cfg.ExecdPort, in); err == nil {
		sb.Endpoint = url
	}
	return sb, nil
}

// translate maps adapter errors onto package sentinels, keeping the cause.
func (s *Service) translate(op string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, docker.ErrUnavailable):
		return fmt.Errorf("%s: %w: %v", op, ErrDockerUnavailable, err)
	case errors.Is(err, docker.ErrNotFound):
		return fmt.Errorf("%s: %w: %v", op, ErrNotFound, err)
	case errors.Is(err, docker.ErrConflict):
		return fmt.Errorf("%s: %w: %v", op, ErrConflict, err)
	case errors.Is(err, docker.ErrTimeout):
		return fmt.Errorf("%s: %w: %v", op, ErrTimeout, err)
	case errors.Is(err, docker.ErrInvalid):
		return fmt.Errorf("%s: %w: %v", op, ErrInvalid, err)
	default:
		return fmt.Errorf("%s: %w", op, err)
	}
}

// bindAddr converts RUNTIME_BIND_HOST into the address sandbox ports are published on.
func bindAddr(host string) (netip.Addr, error) {
	if host == "localhost" {
		return netip.AddrFrom4([4]byte{127, 0, 0, 1}), nil
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("bind host %q: %w", host, err)
	}
	return a, nil
}
