package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func validEnv() map[string]string {
	return map[string]string{
		"RUNTIME_BIND_HOST":   "127.0.0.1",
		"RUNTIME_PUBLIC_HOST": "example.test",
		"RUNTIME_PORT":        "8080",
		"EXECD_PORT":          "44772",
	}
}

func TestLoadDefaultsWithRequiredEnv(t *testing.T) {
	cfg, err := LoadFrom(env(validEnv()))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.SandboxImage != "sandbox-base:py3.13-uv0.13.7" {
		t.Errorf("image default = %q", cfg.SandboxImage)
	}
	if cfg.ReadyTimeout.Std() != 60*time.Second || cfg.StopTimeout.Std() != 15*time.Second {
		t.Errorf("timeout defaults wrong: ready=%v stop=%v", cfg.ReadyTimeout.Std(), cfg.StopTimeout.Std())
	}
	if cfg.DefaultMemoryMB != 1024 || cfg.DefaultPidsLimit != 512 || cfg.DefaultCPUs != 1 {
		t.Errorf("resource defaults wrong: %+v", cfg)
	}
	if cfg.ExecdPort != 44772 || cfg.Port != 8080 {
		t.Errorf("ports wrong: runtime=%d execd=%d", cfg.Port, cfg.ExecdPort)
	}
}

func TestRequiredValuesHaveNoDefaults(t *testing.T) {
	for _, key := range []string{"RUNTIME_BIND_HOST", "RUNTIME_PUBLIC_HOST", "RUNTIME_PORT", "EXECD_PORT"} {
		m := validEnv()
		delete(m, key)
		_, err := LoadFrom(env(m))
		if err == nil {
			t.Errorf("missing %s: expected error", key)
			continue
		}
		if !strings.Contains(err.Error(), key) {
			t.Errorf("missing %s: error %q does not name the variable", key, err)
		}
	}
}

func TestInvalidPorts(t *testing.T) {
	for _, v := range []string{"0", "65536", "-1"} {
		m := validEnv()
		m["RUNTIME_PORT"] = v
		if _, err := LoadFrom(env(m)); err == nil {
			t.Errorf("RUNTIME_PORT=%s: expected error", v)
		}
	}
	m := validEnv()
	m["EXECD_PORT"] = "8080"
	if _, err := LoadFrom(env(m)); err == nil {
		t.Error("RUNTIME_PORT equal to EXECD_PORT: expected error")
	}
	m = validEnv()
	m["RUNTIME_PORT"] = "abc"
	if _, err := LoadFrom(env(m)); err == nil {
		t.Error("non-numeric RUNTIME_PORT: expected error")
	}
}

func TestBindHostMustBeIPOrLocalhost(t *testing.T) {
	m := validEnv()
	m["RUNTIME_BIND_HOST"] = "not a host"
	if _, err := LoadFrom(env(m)); err == nil {
		t.Error("invalid bind host: expected error")
	}
}

func TestEnvOverridesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.json")
	body := `{"bind_host":"10.0.0.1","port":9000,"public_host":"file.test","execd_port":44772,"max_sandboxes":5,"ready_timeout":"30s"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	m := map[string]string{"RUNTIME_CONFIG": path, "RUNTIME_PORT": "9100", "RUNTIME_PUBLIC_HOST": "env.test"}
	cfg, err := LoadFrom(env(m))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.Port != 9100 {
		t.Errorf("env should override file: port=%d", cfg.Port)
	}
	if cfg.PublicHost != "env.test" {
		t.Errorf("env should override file: public=%q", cfg.PublicHost)
	}
	if cfg.BindHost != "10.0.0.1" || cfg.MaxSandboxes != 5 || cfg.ReadyTimeout.Std() != 30*time.Second {
		t.Errorf("file values not applied: %+v", cfg)
	}
}

func TestFileRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.json")
	if err := os.WriteFile(path, []byte(`{"bogus":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFrom(env(map[string]string{"RUNTIME_CONFIG": path})); err == nil {
		t.Error("unknown field: expected error")
	}
}

func TestResourceLimits(t *testing.T) {
	m := validEnv()
	cfg, err := LoadFrom(env(m))
	if err != nil {
		t.Fatal(err)
	}
	cfg.DefaultMemoryMB = cfg.MaxMemoryMB + 1
	if err := cfg.Validate(); err == nil {
		t.Error("default above max: expected error")
	}
}

func TestInvalidDurationAndCount(t *testing.T) {
	m := validEnv()
	m["RUNTIME_STOP_TIMEOUT"] = "soon"
	if _, err := LoadFrom(env(m)); err == nil {
		t.Error("invalid duration: expected error")
	}
	m = validEnv()
	m["RUNTIME_MAX_SANDBOXES"] = "0"
	if _, err := LoadFrom(env(m)); err == nil {
		t.Error("zero max_sandboxes: expected error")
	}
}
