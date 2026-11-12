package sandbox

import (
	"context"
	"time"
)

// ReapExpired deletes Sandboxes older than SandboxTTL, whatever their state. It returns
// the IDs it removed. With SandboxTTL = 0 reaping is disabled. Failures on individual
// Sandboxes are collected so that one bad container does not block the rest.
func (s *Service) ReapExpired(ctx context.Context) (removed []string, errs []error) {
	if s.cfg.SandboxTTL.Std() <= 0 {
		return nil, nil
	}
	all, err := s.List(ctx, "")
	if err != nil {
		return nil, []error{err}
	}
	cutoff := s.now().Add(-s.cfg.SandboxTTL.Std())
	for _, sb := range all {
		if sb.CreatedAt.After(cutoff) {
			continue
		}
		if err := s.Delete(ctx, sb.ID); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, sb.ID)
	}
	return removed, errs
}

// RunReaper calls ReapExpired every interval until ctx is cancelled. It reports each
// pass through report, which may be nil.
func (s *Service) RunReaper(ctx context.Context, interval time.Duration, report func(removed []string, errs []error)) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			removed, errs := s.ReapExpired(ctx)
			if report != nil && (len(removed) > 0 || len(errs) > 0) {
				report(removed, errs)
			}
		}
	}
}
