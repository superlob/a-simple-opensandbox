package sandbox

import (
	"context"
	"fmt"
)

// RecoveryReport summarises what Recover found and did.
type RecoveryReport struct {
	// Adopted lists ready Sandboxes whose endpoints were restored into the registry.
	Adopted []string
	// Removed lists leftover containers from creations that never started. Only filled
	// when RecoverLeftovers is enabled.
	Removed []string
	// Leftovers lists leftover containers that were reported but not removed.
	Leftovers []string
	// Unready lists running Sandboxes that are not healthy. They are kept, not removed,
	// because a stopped-then-restarted user Sandbox looks the same as a half-built one.
	Unready []string
	// Errors collects per-Sandbox failures. Recovery continues past them.
	Errors []error
}

// Recover restores in-memory state from Docker after a Runtime restart. Docker labels
// are the only source of truth, so there is no database to replay.
//
//   - Ready Sandboxes get their endpoints back in the registry.
//   - "created" containers are leftovers from a create that died before start. A
//     successful create always starts the container, so they can be removed. Removal
//     is opt-in (RecoverLeftovers) because it applies to every managed container on the
//     daemon; by default they are only reported.
//   - Running but unhealthy containers are reported, not removed.
//
// Call Recover before serving requests.
func (s *Service) Recover(ctx context.Context) (RecoveryReport, error) {
	var rep RecoveryReport
	all, err := s.List(ctx, "")
	if err != nil {
		return rep, err
	}
	for _, sb := range all {
		switch sb.Status {
		case StatusReady:
			if sb.Endpoint != "" {
				s.endpoints.Set(sb.ID, sb.Endpoint)
			}
			rep.Adopted = append(rep.Adopted, sb.ID)
		case StatusCreating:
			if !s.cfg.RecoverLeftovers || (s.leftoverScope != nil && !s.leftoverScope[sb.ID]) {
				rep.Leftovers = append(rep.Leftovers, sb.ID)
				continue
			}
			if err := s.Delete(ctx, sb.ID); err != nil {
				rep.Errors = append(rep.Errors, fmt.Errorf("remove leftover %s: %w", sb.ID, err))
				continue
			}
			rep.Removed = append(rep.Removed, sb.ID)
		case StatusStarting, StatusUnhealthy, StatusUnknown:
			s.endpoints.Invalidate(sb.ID)
			rep.Unready = append(rep.Unready, sb.ID)
		case StatusStopped:
			s.endpoints.Invalidate(sb.ID)
		}
	}
	return rep, nil
}
