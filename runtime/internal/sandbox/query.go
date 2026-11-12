package sandbox

import (
	"context"
	"errors"
	"fmt"

	"sandbox/runtime/internal/docker"
)

// List returns every Sandbox managed by Runtime. If status is non-empty, only
// Sandboxes with that status are returned.
func (s *Service) List(ctx context.Context, status string) ([]Sandbox, error) {
	managed, err := s.docker.ContainersByLabel(ctx, LabelManaged, "true")
	if err != nil {
		return nil, s.translate("list sandboxes", err)
	}
	out := make([]Sandbox, 0, len(managed))
	for _, m := range managed {
		id := m.Labels[LabelID]
		if !idPattern.MatchString(id) {
			continue
		}
		sb, err := s.view(ctx, m.Name)
		if err != nil {
			// A container may disappear between listing and inspecting it.
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		if status != "" && sb.Status != status {
			continue
		}
		out = append(out, sb)
	}
	return out, nil
}

// Stats returns a single resource sample. The Sandbox must be running.
func (s *Service) Stats(ctx context.Context, id string) (docker.Stats, error) {
	ref, in, err := s.resolve(ctx, id)
	if err != nil {
		return docker.Stats{}, err
	}
	if !in.Running {
		return docker.Stats{}, fmt.Errorf("%w: sandbox is %s", ErrConflict, in.Status)
	}
	st, err := s.docker.ContainerStatsOnce(ctx, ref)
	if err != nil {
		return docker.Stats{}, s.translate("stats", err)
	}
	return st, nil
}
