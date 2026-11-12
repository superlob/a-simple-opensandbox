package sandbox

import "errors"

// Sentinel errors. The API layer maps them to HTTP status codes and error codes
// (invalid_argument, resource_exhausted, conflict, not_found, timeout, ...).
var (
	ErrInvalid           = errors.New("invalid argument")
	ErrResourceExhausted = errors.New("resource exhausted")
	ErrConflict          = errors.New("conflict")
	ErrNotFound          = errors.New("sandbox not found")
	ErrTimeout           = errors.New("sandbox did not become ready in time")
	ErrImageUnavailable  = errors.New("sandbox image is not available locally")
	ErrDockerUnavailable = errors.New("docker daemon unavailable")
	ErrStartFailed       = errors.New("sandbox container failed to start")
)
