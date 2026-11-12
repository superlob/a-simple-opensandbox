// Package security holds input validation helpers: workspace path resolution,
// argv and environment checks, and bounded output writers.
package security

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// ErrOutsideWorkspace is returned when a path resolves outside the workspace,
// either lexically or through a symbolic link.
var ErrOutsideWorkspace = errors.New("path is outside the workspace")

// ResolvePath maps a user-supplied path to an absolute path inside workspace.
//
// Relative paths are joined to the workspace. Absolute paths are accepted only
// if they already lie inside the workspace. For paths that exist, symbolic links
// are resolved and the target must still be inside the workspace. For paths
// that do not exist yet, the nearest existing ancestor is checked instead, so
// creating a file through a symlinked directory is also rejected.
func ResolvePath(workspace, userPath string) (string, error) {
	if userPath == "" {
		return "", errors.New("path must not be empty")
	}
	if strings.ContainsRune(userPath, 0) {
		return "", errors.New("path must not contain NUL bytes")
	}
	ws, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	var candidate string
	if filepath.IsAbs(userPath) {
		candidate = filepath.Clean(userPath)
	} else {
		candidate = filepath.Join(ws, userPath)
	}
	if !within(ws, candidate) {
		return "", ErrOutsideWorkspace
	}

	realWS, err := filepath.EvalSymlinks(ws)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	// Walk up to the nearest existing ancestor and resolve it.
	existing := candidate
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		existing = parent
	}
	realExisting, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	if !within(realWS, realExisting) {
		return "", ErrOutsideWorkspace
	}
	return candidate, nil
}

// within reports whether p is equal to or below root. Both must be clean absolute paths.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// IsWorkspaceRoot reports whether the resolved path is the workspace itself.
func IsWorkspaceRoot(workspace, resolved string) bool {
	ws, err := filepath.Abs(workspace)
	if err != nil {
		return false
	}
	return filepath.Clean(resolved) == filepath.Clean(ws)
}

var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

const (
	maxArgs       = 1024
	maxArgBytes   = 128 << 10
	maxEnvEntries = 256
	maxEnvBytes   = 64 << 10
)

// ValidateArgv checks an argv array. No shell is involved; the first element is the program.
func ValidateArgv(argv []string) error {
	if len(argv) == 0 {
		return errors.New("argv must not be empty")
	}
	if len(argv) > maxArgs {
		return fmt.Errorf("argv has more than %d elements", maxArgs)
	}
	if argv[0] == "" {
		return errors.New("argv[0] must not be empty")
	}
	total := 0
	for i, a := range argv {
		if strings.ContainsRune(a, 0) {
			return fmt.Errorf("argv[%d] contains a NUL byte", i)
		}
		total += len(a)
	}
	if total > maxArgBytes {
		return fmt.Errorf("argv exceeds %d bytes", maxArgBytes)
	}
	return nil
}

// ValidateEnv checks user-supplied environment variables.
func ValidateEnv(env map[string]string) error {
	if len(env) > maxEnvEntries {
		return fmt.Errorf("env has more than %d entries", maxEnvEntries)
	}
	total := 0
	for k, v := range env {
		if !envKeyRe.MatchString(k) {
			return fmt.Errorf("invalid environment variable name %q", k)
		}
		if strings.ContainsRune(v, 0) {
			return fmt.Errorf("environment variable %s contains a NUL byte", k)
		}
		total += len(k) + len(v)
	}
	if total > maxEnvBytes {
		return fmt.Errorf("env exceeds %d bytes", maxEnvBytes)
	}
	return nil
}

// LimitedBuffer is a concurrency-safe byte buffer that keeps at most Limit
// bytes. Writes beyond the limit are discarded and Truncated reports true.
// Writes never fail, so a noisy child process is not blocked or killed.
type LimitedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int64
	truncated bool
}

// NewLimitedBuffer returns a buffer that keeps at most limit bytes.
func NewLimitedBuffer(limit int64) *LimitedBuffer {
	return &LimitedBuffer{limit: limit}
}

// Write implements io.Writer.
func (b *LimitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	room := b.limit - int64(b.buf.Len())
	if room <= 0 {
		if n > 0 {
			b.truncated = true
		}
		return n, nil
	}
	if int64(n) > room {
		b.buf.Write(p[:room])
		b.truncated = true
		return n, nil
	}
	b.buf.Write(p)
	return n, nil
}

// Bytes returns a copy of the retained bytes.
func (b *LimitedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

// Truncated reports whether any bytes were discarded.
func (b *LimitedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

// LimitedWriter wraps w and forwards at most limit bytes to it. Excess bytes
// are discarded and reported through Truncated. It is used for log files.
type LimitedWriter struct {
	mu        sync.Mutex
	w         io.Writer
	remaining int64
	truncated bool
}

// NewLimitedWriter returns a writer that forwards at most limit bytes to w.
func NewLimitedWriter(w io.Writer, limit int64) *LimitedWriter {
	return &LimitedWriter{w: w, remaining: limit}
}

// Write implements io.Writer. It never returns an error for discarded bytes.
func (l *LimitedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(p)
	if l.remaining <= 0 {
		if n > 0 {
			l.truncated = true
		}
		return n, nil
	}
	chunk := p
	if int64(len(p)) > l.remaining {
		chunk = p[:l.remaining]
		l.truncated = true
	}
	written, err := l.w.Write(chunk)
	l.remaining -= int64(written)
	if err != nil {
		return written, err
	}
	return n, nil
}

// Truncated reports whether any bytes were discarded.
func (l *LimitedWriter) Truncated() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.truncated
}
