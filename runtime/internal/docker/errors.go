package docker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"

	cerrdefs "github.com/containerd/errdefs"
)

// Sentinel errors returned by this package. Callers match them with errors.Is.
var (
	// ErrUnavailable means the Docker daemon could not be reached.
	ErrUnavailable = errors.New("docker daemon unavailable")
	// ErrNotFound means the referenced container or image does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict means the operation conflicts with the current resource state.
	ErrConflict = errors.New("conflict")
	// ErrTimeout means a daemon call exceeded its deadline.
	ErrTimeout = errors.New("docker call timed out")
	// ErrInvalid means the daemon rejected the request parameters.
	ErrInvalid = errors.New("invalid argument")
)

// classify maps SDK and transport errors onto the package sentinels while keeping
// the original error in the chain for diagnostics.
func classify(op string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case cerrdefs.IsNotFound(err):
		return fmt.Errorf("%s: %w: %w", op, ErrNotFound, err)
	case cerrdefs.IsConflict(err):
		return fmt.Errorf("%s: %w: %w", op, ErrConflict, err)
	case cerrdefs.IsInvalidArgument(err):
		return fmt.Errorf("%s: %w: %w", op, ErrInvalid, err)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%s: %w: %w", op, ErrTimeout, err)
	case isConnectionError(err) || cerrdefs.IsUnavailable(err):
		return fmt.Errorf("%s: %w: %w", op, ErrUnavailable, err)
	default:
		return fmt.Errorf("%s: %w", op, err)
	}
}

func isConnectionError(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var urlErr *url.Error
	return errors.As(err, &urlErr) && urlErr.Err != nil && !errors.Is(urlErr.Err, context.DeadlineExceeded)
}
