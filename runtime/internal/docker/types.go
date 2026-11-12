package docker

import (
	"net/netip"
	"time"
)

// Container states as reported by Docker (State.Status).
const (
	StateCreated    = "created"
	StateRunning    = "running"
	StateExited     = "exited"
	StateRestarting = "restarting"
	StatePaused     = "paused"
	StateDead       = "dead"
)

// Health states as reported by Docker (State.Health.Status).
const (
	HealthNone      = ""
	HealthStarting  = "starting"
	HealthHealthy   = "healthy"
	HealthUnhealthy = "unhealthy"
)

// Spec describes a container to create. All limits are already validated by the caller.
type Spec struct {
	Name  string
	Image string
	Env   []string
	// Labels are the management labels, e.g. sandbox.managed=true.
	Labels map[string]string
	// ContainerPort is the port published from inside the container (Execd).
	ContainerPort int
	// BindHost is the host address the published port is bound to.
	BindHost netip.Addr
	// CPUs is the CPU quota in cores.
	CPUs float64
	// MemoryBytes is the memory limit in bytes.
	MemoryBytes int64
	// PidsLimit caps the number of processes in the container.
	PidsLimit int64
	// StopTimeout is the SIGTERM-to-SIGKILL grace period used by docker stop.
	StopTimeout time.Duration
}

// Inspect is the subset of container state the Runtime needs.
type Inspect struct {
	ID       string
	Name     string
	Image    string
	Created  time.Time
	Labels   map[string]string
	Status   string // one of the State* constants
	Running  bool
	ExitCode int
	Health   string // one of the Health* constants; HealthNone if no healthcheck
	// HostPorts maps "<containerPort>/tcp" to the published host port.
	HostPorts map[string]int
}

// Summary is a container returned by a label listing.
type Summary struct {
	ID     string
	Name   string
	Labels map[string]string
	Status string
}

// Stats is a single resource-usage sample.
type Stats struct {
	Read        time.Time
	CPUPercent  float64
	MemoryUsage uint64
	MemoryLimit uint64
	PidsCurrent uint64
}
