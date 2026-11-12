package execdcfg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// execdConfigSource is the authoritative definition of Execd's configuration.
const execdConfigSource = "../../../execd/internal/config/config.go"

var jsonTag = regexp.MustCompile("`json:\"([a-z_]+)\"`")

// execdKeys extracts every json tag declared on execd's Config struct.
func execdKeys(t *testing.T) map[string]bool {
	t.Helper()
	path, err := filepath.Abs(execdConfigSource)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read execd config source (contract source moved?): %v", err)
	}
	src := string(b)
	start := strings.Index(src, "type Config struct {")
	if start < 0 {
		t.Fatal("type Config struct not found in execd source")
	}
	end := strings.Index(src[start:], "\n}\n")
	if end < 0 {
		t.Fatal("end of Config struct not found")
	}
	keys := map[string]bool{}
	for _, m := range jsonTag.FindAllStringSubmatch(src[start:start+end], -1) {
		keys[m[1]] = true
	}
	if len(keys) == 0 {
		t.Fatal("no json tags found in execd Config")
	}
	return keys
}

func TestGeneratedKeysExistInExecdConfig(t *testing.T) {
	known := execdKeys(t)
	for _, k := range Keys() {
		if !known[k] {
			t.Errorf("runtime emits %q, which execd Config does not declare", k)
		}
	}
}

func TestBuildOutputUsesOnlyExecdKeys(t *testing.T) {
	known := execdKeys(t)
	b, err := Build(Options{Port: 44772, ShutdownGrace: 10e9})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var got []string
	for k := range m {
		got = append(got, k)
		if !known[k] {
			t.Errorf("emitted key %q is not an execd config key", k)
		}
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "data_dir,listen_addr,port,shutdown_grace,workspace_dir" {
		t.Errorf("keys = %v", got)
	}
}

func TestBuildValues(t *testing.T) {
	b, err := Build(Options{Port: 44772})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["listen_addr"] != "0.0.0.0" || m["workspace_dir"] != "/workspace" || m["data_dir"] != "/var/lib/execd" {
		t.Errorf("values = %v", m)
	}
	if m["port"] != float64(44772) {
		t.Errorf("port = %v", m["port"])
	}
	if _, ok := m["shutdown_grace"]; ok {
		t.Error("shutdown_grace should be omitted when zero")
	}
}

func TestBuildRejectsInvalidPort(t *testing.T) {
	for _, p := range []int{0, -1, 65536} {
		if _, err := Build(Options{Port: p}); err == nil {
			t.Errorf("port %d: expected error", p)
		}
	}
}
