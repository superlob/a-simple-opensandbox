// Package config loads and validates the Execd runtime configuration.
//
// Configuration is read from the JSON file named by the EXECD_CONFIG
// environment variable. A small set of environment variables may override
// individual fields (see applyEnvOverrides).
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

// Duration is a time.Duration that is read from JSON as a Go duration string,
// e.g. "30s" or "2h".
type Duration time.Duration

// UnmarshalJSON parses a quoted duration string.
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

// Config is the complete Execd configuration.
type Config struct {
	// ListenAddr is the bind address. Default "0.0.0.0".
	ListenAddr string `json:"listen_addr"`
	// Port is the TCP port to listen on. Required; no default is applied.
	Port int `json:"port"`
	// WorkspaceDir is the root of all file operations and the default command
	// working directory. Default "/workspace".
	WorkspaceDir string `json:"workspace_dir"`
	// DataDir stores task records and task logs. Default "/var/lib/execd".
	DataDir string `json:"data_dir"`

	// MaxConcurrentTasks bounds the number of commands running at once,
	// counting both foreground and background executions. Default 8.
	MaxConcurrentTasks int `json:"max_concurrent_tasks"`
	// MaxUploadBytes bounds a single file upload. Default 100 MiB.
	MaxUploadBytes int64 `json:"max_upload_bytes"`
	// MaxRequestBodyBytes bounds non-upload request bodies. Default 1 MiB.
	MaxRequestBodyBytes int64 `json:"max_request_body_bytes"`
	// MaxOutputBytes bounds stdout and stderr per execution (each stream). Default 1 MiB.
	MaxOutputBytes int64 `json:"max_output_bytes"`

	// DefaultTimeout applies when a request does not specify a timeout. Default 60s.
	DefaultTimeout Duration `json:"default_timeout"`
	// MaxTimeout is the upper bound for any requested timeout. Default 1h.
	MaxTimeout Duration `json:"max_timeout"`
	// ShutdownGrace is used for graceful shutdown of HTTP and running tasks, and
	// as the SIGTERM-to-SIGKILL delay for killed process groups. Default 10s.
	ShutdownGrace Duration `json:"shutdown_grace"`

	// TaskRetention is how long finished task records are kept. Default 24h.
	TaskRetention Duration `json:"task_retention"`
	// MaxFinishedTasks caps the number of finished task records kept. Default 1000.
	MaxFinishedTasks int `json:"max_finished_tasks"`
}

// Default returns a Config populated with defaults. Port is left at zero and
// must be set by the file or environment.
func Default() Config {
	return Config{
		ListenAddr:          "0.0.0.0",
		WorkspaceDir:        "/workspace",
		DataDir:             "/var/lib/execd",
		MaxConcurrentTasks:  8,
		MaxUploadBytes:      100 << 20,
		MaxRequestBodyBytes: 1 << 20,
		MaxOutputBytes:      1 << 20,
		DefaultTimeout:      Duration(60 * time.Second),
		MaxTimeout:          Duration(time.Hour),
		ShutdownGrace:       Duration(10 * time.Second),
		TaskRetention:       Duration(24 * time.Hour),
		MaxFinishedTasks:    1000,
	}
}

// Load reads configuration from EXECD_CONFIG (if set), applies environment
// overrides, and validates the result.
func Load() (Config, error) {
	cfg := Default()
	if path := os.Getenv("EXECD_CONFIG"); path != "" {
		if err := loadFile(path, &cfg); err != nil {
			return Config{}, err
		}
	}
	if err := applyEnvOverrides(&cfg, os.Getenv); err != nil {
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

// applyEnvOverrides lets these variables override file values:
// EXECD_LISTEN_ADDR, EXECD_PORT, EXECD_WORKSPACE_DIR, EXECD_DATA_DIR.
func applyEnvOverrides(cfg *Config, get func(string) string) error {
	if v := get("EXECD_LISTEN_ADDR"); v != "" {
		cfg.ListenAddr = v
	}
	if v := get("EXECD_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("EXECD_PORT: %w", err)
		}
		cfg.Port = p
	}
	if v := get("EXECD_WORKSPACE_DIR"); v != "" {
		cfg.WorkspaceDir = v
	}
	if v := get("EXECD_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	return nil
}

// Validate checks all fields. It does not touch the filesystem beyond the
// directory checks in CheckDirs.
func (c *Config) Validate() error {
	var errs []error
	if c.ListenAddr == "" {
		errs = append(errs, errors.New("listen_addr must not be empty"))
	} else if ip := net.ParseIP(c.ListenAddr); ip == nil && c.ListenAddr != "localhost" {
		errs = append(errs, fmt.Errorf("listen_addr %q is not an IP address", c.ListenAddr))
	}
	if c.Port < 1 || c.Port > 65535 {
		errs = append(errs, fmt.Errorf("port must be in 1..65535, got %d", c.Port))
	}
	if c.WorkspaceDir == "" || c.WorkspaceDir[0] != '/' {
		errs = append(errs, fmt.Errorf("workspace_dir must be an absolute path, got %q", c.WorkspaceDir))
	}
	if c.DataDir == "" || c.DataDir[0] != '/' {
		errs = append(errs, fmt.Errorf("data_dir must be an absolute path, got %q", c.DataDir))
	}
	positive := map[string]int64{
		"max_concurrent_tasks":   int64(c.MaxConcurrentTasks),
		"max_upload_bytes":       c.MaxUploadBytes,
		"max_request_body_bytes": c.MaxRequestBodyBytes,
		"max_output_bytes":       c.MaxOutputBytes,
		"max_finished_tasks":     int64(c.MaxFinishedTasks),
		"default_timeout":        int64(c.DefaultTimeout),
		"max_timeout":            int64(c.MaxTimeout),
		"shutdown_grace":         int64(c.ShutdownGrace),
		"task_retention":         int64(c.TaskRetention),
	}
	for name, v := range positive {
		if v <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive", name))
		}
	}
	if c.DefaultTimeout > c.MaxTimeout {
		errs = append(errs, errors.New("default_timeout must not exceed max_timeout"))
	}
	return errors.Join(errs...)
}

// CheckDirs verifies that the workspace exists and is writable, and creates
// the data directory if needed.
func (c *Config) CheckDirs() error {
	st, err := os.Stat(c.WorkspaceDir)
	if err != nil {
		return fmt.Errorf("workspace_dir: %w", err)
	}
	if !st.IsDir() {
		return fmt.Errorf("workspace_dir %s is not a directory", c.WorkspaceDir)
	}
	probe, err := os.CreateTemp(c.WorkspaceDir, ".execd-probe-*")
	if err != nil {
		return fmt.Errorf("workspace_dir is not writable: %w", err)
	}
	probe.Close()
	os.Remove(probe.Name())
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data_dir: %w", err)
	}
	return nil
}
