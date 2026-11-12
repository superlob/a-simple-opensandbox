package security

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePath(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(ws, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ws, "escape")); err != nil {
		t.Fatal(err)
	}

	ok := []string{"a.txt", "d/x/y.txt", filepath.Join(ws, "d", "f"), "./d/../d/z"}
	for _, p := range ok {
		if _, err := ResolvePath(ws, p); err != nil {
			t.Errorf("ResolvePath(%q) unexpected error: %v", p, err)
		}
	}
	bad := []string{"../etc/passwd", "/etc/passwd", "d/../../x", "escape", "escape/new.txt", filepath.Join(outside, "f")}
	for _, p := range bad {
		if _, err := ResolvePath(ws, p); !errors.Is(err, ErrOutsideWorkspace) {
			t.Errorf("ResolvePath(%q) = %v, want ErrOutsideWorkspace", p, err)
		}
	}
	if _, err := ResolvePath(ws, ""); err == nil {
		t.Error("empty path should fail")
	}
}

func TestIsWorkspaceRoot(t *testing.T) {
	ws := t.TempDir()
	if !IsWorkspaceRoot(ws, ws) || IsWorkspaceRoot(ws, filepath.Join(ws, "x")) {
		t.Error("IsWorkspaceRoot wrong")
	}
}

func TestValidateArgv(t *testing.T) {
	if ValidateArgv(nil) == nil || ValidateArgv([]string{""}) == nil || ValidateArgv([]string{"a\x00b"}) == nil {
		t.Error("invalid argv accepted")
	}
	if err := ValidateArgv([]string{"echo", "hi"}); err != nil {
		t.Error(err)
	}
}

func TestValidateEnv(t *testing.T) {
	if ValidateEnv(map[string]string{"1BAD": "x"}) == nil {
		t.Error("bad key accepted")
	}
	if ValidateEnv(map[string]string{"A": "x\x00"}) == nil {
		t.Error("NUL value accepted")
	}
	if err := ValidateEnv(map[string]string{"VIRTUAL_ENV": "/workspace/.venv"}); err != nil {
		t.Error(err)
	}
}

func TestLimitedBufferAndWriter(t *testing.T) {
	b := NewLimitedBuffer(4)
	n, err := b.Write([]byte("abcdef"))
	if n != 6 || err != nil || string(b.Bytes()) != "abcd" || !b.Truncated() {
		t.Errorf("buffer: n=%d err=%v got=%q trunc=%v", n, err, b.Bytes(), b.Truncated())
	}
	var sb strings.Builder
	w := NewLimitedWriter(&sb, 3)
	if n, err := w.Write([]byte("hello")); n != 5 || err != nil {
		t.Errorf("writer: n=%d err=%v", n, err)
	}
	if _, err := w.Write([]byte("more")); err != nil {
		t.Error(err)
	}
	if sb.String() != "hel" || !w.Truncated() {
		t.Errorf("writer got %q trunc=%v", sb.String(), w.Truncated())
	}
}
