package sandbox

import "sandbox/runtime/internal/docker"

// Status values exposed to callers (runtime-design §1.2).
const (
	StatusCreating  = "creating"
	StatusStarting  = "starting"
	StatusReady     = "ready"
	StatusUnhealthy = "unhealthy"
	StatusStopped   = "stopped"
	StatusUnknown   = "unknown"
)

// StatusOf derives the caller-facing status from Docker's container state and health.
func StatusOf(in docker.Inspect) string {
	switch in.Status {
	case docker.StateCreated:
		return StatusCreating
	case docker.StateExited, docker.StateDead:
		return StatusStopped
	case docker.StateRunning, docker.StateRestarting:
		switch in.Health {
		case docker.HealthHealthy:
			return StatusReady
		case docker.HealthUnhealthy:
			return StatusUnhealthy
		case docker.HealthStarting, docker.HealthNone:
			return StatusStarting
		}
	}
	return StatusUnknown
}
