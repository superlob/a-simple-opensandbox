// Package execdcfg generates the Execd configuration file that Runtime injects
// into each Sandbox container. Field names must match the JSON tags of
// execd/internal/config.Config; contract_test.go checks this against the source.
package execdcfg

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Paths fixed by the sandbox base image (images/sandbox-base/Dockerfile).
const (
	// ConfigPath is where the config file is copied inside the container.
	ConfigPath = "/etc/execd/config.json"
	// ConfigDir is the directory containing ConfigPath.
	ConfigDir = "/etc/execd"
	// WorkspaceDir is the default working directory for Sandboxes.
	WorkspaceDir = "/workspace"
	// DataDir stores Execd task records and logs (container writable layer).
	DataDir = "/var/lib/execd"
	// ListenAddr binds Execd on all container interfaces so Docker can publish it.
	ListenAddr = "0.0.0.0"
)

// Options are the per-Sandbox values Runtime provides.
type Options struct {
	// Port is the container port Execd listens on (EXECD_PORT).
	Port int
	// ShutdownGrace is Execd's graceful shutdown window. Must be shorter than the
	// Docker stop timeout. Zero leaves Execd's default in place.
	ShutdownGrace time.Duration
}

// config mirrors the subset of execd's Config that Runtime sets. Fields that are
// omitted take Execd's own defaults.
type config struct {
	ListenAddr    string  `json:"listen_addr"`
	Port          int     `json:"port"`
	WorkspaceDir  string  `json:"workspace_dir"`
	DataDir       string  `json:"data_dir"`
	ShutdownGrace *string `json:"shutdown_grace,omitempty"`
}

// Build returns the JSON document for the given options.
func Build(o Options) ([]byte, error) {
	if o.Port < 1 || o.Port > 65535 {
		return nil, fmt.Errorf("execd port must be in 1..65535, got %d", o.Port)
	}
	if o.ShutdownGrace < 0 {
		return nil, errors.New("shutdown grace must be >= 0")
	}
	c := config{
		ListenAddr:   ListenAddr,
		Port:         o.Port,
		WorkspaceDir: WorkspaceDir,
		DataDir:      DataDir,
	}
	if o.ShutdownGrace > 0 {
		s := o.ShutdownGrace.String()
		c.ShutdownGrace = &s
	}
	return json.MarshalIndent(c, "", "  ")
}

// Keys returns the JSON keys Build can emit, in struct order. Used by the contract test.
func Keys() []string {
	return []string{"listen_addr", "port", "workspace_dir", "data_dir", "shutdown_grace"}
}
