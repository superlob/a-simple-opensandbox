// Package config loads and validates the Runtime configuration.
//
// Settings come from an optional JSON file named by RUNTIME_CONFIG, and
// environment variables override individual fields. Host, IP and port values
// are environment-specific: they have no defaults and must be supplied through
// SANDBOX.env (see RequiredEnv below).
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"
)

// Duration is a time.Duration read from JSON as a Go duration string.
type Duration time.Duration

// UnmarshalJSON parses a quoted duration string such as "30s".
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string such as \"30s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// MarshalJSON writes the duration as a Go duration string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Config is the complete Runtime configuration.
type Config struct {
	// BindHost is the address the Runtime HTTP API listens on. Required (RUNTIME_BIND_HOST).
	BindHost string `json:"bind_host"`
	// Port is the Runtime HTTP API port. Required (RUNTIME_PORT).
	Port int `json:"port"`
	// PublicHost is the host clients use to reach published Sandbox ports.
	// Required (RUNTIME_PUBLIC_HOST).
	PublicHost string `json:"public_host"`
	// ExecdPort is the port Execd listens on inside each Sandbox container.
	// Required (EXECD_PORT). It is written into the Execd config and used for port publishing.
	ExecdPort int `json:"execd_port"`

	// DockerHost overrides the Docker daemon address. Empty means the SDK default
	// (DOCKER_HOST or the local socket).
	DockerHost string `json:"docker_host"`
	// SandboxImage is the fixed base image used for every Sandbox. Creation
	// requests cannot override it. Default "sandbox-base:py3.13-uv0.13.7".
	SandboxImage string `json:"sandbox_image"`

	// DefaultCPUs is the CPU limit (in cores) applied when a request omits resources. Default 1.
	DefaultCPUs float64 `json:"default_cpus"`
	// DefaultMemoryMB is the memory limit in MiB applied by default. Default 1024.
	DefaultMemoryMB int64 `json:"default_memory_mb"`
	// DefaultPidsLimit is the PIDs limit applied by default. Default 512.
	DefaultPidsLimit int64 `json:"default_pids_limit"`
	// MaxCPUs is the upper bound for requested CPUs. Default 4.
	MaxCPUs float64 `json:"max_cpus"`
	// MaxMemoryMB is the upper bound for requested memory in MiB. Default 8192.
	MaxMemoryMB int64 `json:"max_memory_mb"`
	// MaxPidsLimit is the upper bound for requested PIDs. Default 4096.
	MaxPidsLimit int64 `json:"max_pids_limit"`

	// ReadyTimeout bounds waiting for a Sandbox to report healthy after create or start. Default 60s.
	ReadyTimeout Duration `json:"ready_timeout"`
	// StopTimeout is passed to Docker as the SIGTERM-to-SIGKILL grace period.
	// It must exceed Execd's shutdown_grace (10s by default). Default 15s.
	StopTimeout Duration `json:"stop_timeout"`
	// MaxSandboxes caps the number of Sandbox containers managed at once. Default 50.
	MaxSandboxes int `json:"max_sandboxes"`
	// SandboxTTL is the maximum age of a Sandbox before reaping. Zero disables reaping. Default 0.
	SandboxTTL Duration `json:"sandbox_ttl"`
	// RecoverLeftovers lets startup recovery remove "created"-state managed containers
	// (leftovers of interrupted creations). Default false: they are only reported. The
	// removal scope is every managed container on the daemon, so it is opt-in.
	RecoverLeftovers bool `json:"recover_leftovers"`
}

// Default returns the configuration with all non-environment defaults applied.
// Required fields are left zero and must be filled by Load.
func Default() Config {
	return Config{
		SandboxImage:     "sandbox-base:py3.13-uv0.13.7",
		DefaultCPUs:      1,
		DefaultMemoryMB:  1024,
		DefaultPidsLimit: 512,
		MaxCPUs:          4,
		MaxMemoryMB:      8192,
		MaxPidsLimit:     4096,
		ReadyTimeout:     Duration(60 * time.Second),
		StopTimeout:      Duration(15 * time.Second),
		MaxSandboxes:     50,
	}
}

// Load builds the configuration from RUNTIME_CONFIG (if set), applies
// environment overrides, and validates the result.
func Load() (Config, error) {
	return LoadFrom(os.Getenv)
}

// LoadFrom is Load with an injectable environment lookup, for tests.
func LoadFrom(get func(string) string) (Config, error) {
	cfg := Default()
	if path := get("RUNTIME_CONFIG"); path != "" {
		if err := loadFile(path, &cfg); err != nil {
			return Config{}, err
		}
	}
	if err := applyEnvOverrides(&cfg, get); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func loadFile(path string, cfg *Config) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config file: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return fmt.Errorf("parse config file %s: %w", path, err)
	}
	return nil
}

// applyEnvOverrides maps environment variables onto the configuration.
// Variables: RUNTIME_BIND_HOST, RUNTIME_PORT, RUNTIME_PUBLIC_HOST, EXECD_PORT,
// RUNTIME_DOCKER_HOST, RUNTIME_SANDBOX_IMAGE, RUNTIME_MAX_SANDBOXES,
// RUNTIME_READY_TIMEOUT, RUNTIME_STOP_TIMEOUT, RUNTIME_SANDBOX_TTL.
func applyEnvOverrides(cfg *Config, get func(string) string) error {
	if v := get("RUNTIME_BIND_HOST"); v != "" {
		cfg.BindHost = v
	}
	if v := get("RUNTIME_PUBLIC_HOST"); v != "" {
		cfg.PublicHost = v
	}
	if v := get("RUNTIME_DOCKER_HOST"); v != "" {
		cfg.DockerHost = v
	}
	if v := get("RUNTIME_RECOVER_LEFTOVERS"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("RUNTIME_RECOVER_LEFTOVERS: %w", err)
		}
		cfg.RecoverLeftovers = b
	}
	if v := get("RUNTIME_SANDBOX_IMAGE"); v != "" {
		cfg.SandboxImage = v
	}
	if v := get("RUNTIME_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("RUNTIME_PORT: %w", err)
		}
		cfg.Port = n
	}
	if v := get("EXECD_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("EXECD_PORT: %w", err)
		}
		cfg.ExecdPort = n
	}
	if v := get("RUNTIME_MAX_SANDBOXES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("RUNTIME_MAX_SANDBOXES: %w", err)
		}
		cfg.MaxSandboxes = n
	}
	durations := map[string]*Duration{
		"RUNTIME_READY_TIMEOUT": &cfg.ReadyTimeout,
		"RUNTIME_STOP_TIMEOUT":  &cfg.StopTimeout,
		"RUNTIME_SANDBOX_TTL":   &cfg.SandboxTTL,
	}
	for name, dst := range durations {
		if v := get(name); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			*dst = Duration(d)
		}
	}
	return nil
}

// Validate checks required fields and cross-field constraints.
func (c Config) Validate() error {
	var errs []error
	if c.BindHost == "" {
		errs = append(errs, errors.New("RUNTIME_BIND_HOST is required"))
	} else if net.ParseIP(c.BindHost) == nil && c.BindHost != "localhost" {
		errs = append(errs, fmt.Errorf("RUNTIME_BIND_HOST %q is not an IP address", c.BindHost))
	}
	if c.PublicHost == "" {
		errs = append(errs, errors.New("RUNTIME_PUBLIC_HOST is required"))
	}
	if err := validPort("RUNTIME_PORT", c.Port); err != nil {
		errs = append(errs, err)
	}
	if err := validPort("EXECD_PORT", c.ExecdPort); err != nil {
		errs = append(errs, err)
	}
	if c.Port != 0 && c.Port == c.ExecdPort {
		errs = append(errs, errors.New("RUNTIME_PORT and EXECD_PORT must differ"))
	}
	if c.SandboxImage == "" {
		errs = append(errs, errors.New("sandbox image must not be empty"))
	}
	if c.DefaultCPUs <= 0 || c.DefaultCPUs > c.MaxCPUs {
		errs = append(errs, errors.New("default_cpus must be > 0 and <= max_cpus"))
	}
	if c.DefaultMemoryMB <= 0 || c.DefaultMemoryMB > c.MaxMemoryMB {
		errs = append(errs, errors.New("default_memory_mb must be > 0 and <= max_memory_mb"))
	}
	if c.DefaultPidsLimit <= 0 || c.DefaultPidsLimit > c.MaxPidsLimit {
		errs = append(errs, errors.New("default_pids_limit must be > 0 and <= max_pids_limit"))
	}
	if c.MaxCPUs <= 0 || c.MaxMemoryMB <= 0 || c.MaxPidsLimit <= 0 {
		errs = append(errs, errors.New("max_cpus, max_memory_mb and max_pids_limit must be > 0"))
	}
	if c.ReadyTimeout.Std() <= 0 {
		errs = append(errs, errors.New("ready_timeout must be > 0"))
	}
	if c.StopTimeout.Std() <= 0 {
		errs = append(errs, errors.New("stop_timeout must be > 0"))
	}
	if c.MaxSandboxes <= 0 {
		errs = append(errs, errors.New("max_sandboxes must be > 0"))
	}
	if c.SandboxTTL.Std() < 0 {
		errs = append(errs, errors.New("sandbox_ttl must be >= 0"))
	}
	return errors.Join(errs...)
}

func validPort(name string, p int) error {
	if p < 1 || p > 65535 {
		return fmt.Errorf("%s must be in 1..65535, got %d", name, p)
	}
	return nil
}
