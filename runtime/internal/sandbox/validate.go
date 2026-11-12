package sandbox

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"sandbox/runtime/internal/config"
)

const (
	maxEnvVars     = 64
	maxEnvBytes    = 16 * 1024
	maxEnvKeyBytes = 128
)

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedEnv may not be set by callers: the image and Runtime control them.
var reservedEnv = map[string]bool{"PATH": true, "EXECD_CONFIG": true}

// CreateRequest is the validated input for creating a Sandbox.
// It has no image field: the image is fixed by Runtime configuration.
type CreateRequest struct {
	// ClientID is an optional caller-supplied key that makes creation idempotent.
	ClientID string
	// CPUs, MemoryMB and PidsLimit are optional; zero selects the configured default.
	CPUs      float64
	MemoryMB  int64
	PidsLimit int64
	// Env holds container-level default environment variables.
	Env map[string]string
	// Timeout bounds the wait for healthy. Zero selects ReadyTimeout.
	Timeout time.Duration
}

// limits are the resolved, validated resource settings.
type limits struct {
	CPUs      float64
	MemoryMB  int64
	PidsLimit int64
}

func resolveLimits(cfg config.Config, req CreateRequest) (limits, error) {
	l := limits{CPUs: req.CPUs, MemoryMB: req.MemoryMB, PidsLimit: req.PidsLimit}
	if l.CPUs == 0 {
		l.CPUs = cfg.DefaultCPUs
	}
	if l.MemoryMB == 0 {
		l.MemoryMB = cfg.DefaultMemoryMB
	}
	if l.PidsLimit == 0 {
		l.PidsLimit = cfg.DefaultPidsLimit
	}
	if l.CPUs <= 0 || l.CPUs > cfg.MaxCPUs {
		return l, fmt.Errorf("%w: cpus must be > 0 and <= %g", ErrInvalid, cfg.MaxCPUs)
	}
	if l.MemoryMB <= 0 || l.MemoryMB > cfg.MaxMemoryMB {
		return l, fmt.Errorf("%w: memory_mb must be > 0 and <= %d", ErrInvalid, cfg.MaxMemoryMB)
	}
	if l.PidsLimit <= 0 || l.PidsLimit > cfg.MaxPidsLimit {
		return l, fmt.Errorf("%w: pids_limit must be > 0 and <= %d", ErrInvalid, cfg.MaxPidsLimit)
	}
	if req.Timeout < 0 {
		return l, fmt.Errorf("%w: timeout must be >= 0", ErrInvalid)
	}
	return l, nil
}

// validateEnv checks names, values and total size, and returns "KEY=VALUE" entries sorted by key.
func validateEnv(env map[string]string) ([]string, error) {
	if len(env) > maxEnvVars {
		return nil, fmt.Errorf("%w: at most %d env vars", ErrInvalid, maxEnvVars)
	}
	keys := make([]string, 0, len(env))
	total := 0
	for k, v := range env {
		if len(k) == 0 || len(k) > maxEnvKeyBytes || !envKeyPattern.MatchString(k) {
			return nil, fmt.Errorf("%w: invalid env name %q", ErrInvalid, k)
		}
		if reservedEnv[k] {
			return nil, fmt.Errorf("%w: env %s is managed by Runtime", ErrInvalid, k)
		}
		if strings.ContainsRune(v, 0) {
			return nil, fmt.Errorf("%w: env %s contains NUL", ErrInvalid, k)
		}
		total += len(k) + len(v)
		keys = append(keys, k)
	}
	if total > maxEnvBytes {
		return nil, fmt.Errorf("%w: env exceeds %d bytes", ErrInvalid, maxEnvBytes)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out, nil
}
