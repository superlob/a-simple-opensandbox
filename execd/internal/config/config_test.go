package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultsAndWorkspace(t *testing.T) {
	d := Default()
	if d.WorkspaceDir != "/workspace" || d.DataDir != "/var/lib/execd" {
		t.Errorf("defaults: %+v", d)
	}
	if d.Port != 0 {
		t.Error("port must not have a default")
	}
}

func TestLoadFileAndEnvOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "execd.json")
	if err := os.WriteFile(path, []byte(`{"port": 9001, "default_timeout": "5s", "max_timeout": "1m"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EXECD_CONFIG", path)
	t.Setenv("EXECD_PORT", "9002")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 9002 {
		t.Errorf("env override: port=%d", cfg.Port)
	}
	if cfg.DefaultTimeout.Std().Seconds() != 5 || cfg.WorkspaceDir != "/workspace" {
		t.Errorf("file values not applied: %+v", cfg)
	}
}

func TestLoadRejectsBadConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(path, []byte(`{"port": 9001, "unknown_key": 1}`), 0o600)
	t.Setenv("EXECD_CONFIG", path)
	if _, err := Load(); err == nil {
		t.Error("unknown field should be rejected")
	}
	t.Setenv("EXECD_CONFIG", "")
	t.Setenv("EXECD_PORT", "")
	if _, err := Load(); err == nil {
		t.Error("missing port should be rejected")
	}
}

func TestValidateTimeouts(t *testing.T) {
	cfg := Default()
	cfg.Port = 8080
	cfg.DefaultTimeout = Duration(2 * 3600 * 1e9)
	if err := cfg.Validate(); err == nil {
		t.Error("default_timeout > max_timeout should fail")
	}
}
