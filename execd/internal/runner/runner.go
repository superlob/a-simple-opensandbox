// Package runner executes a single argv-based command. Foreground and background
// executions both use Run; the task manager decides where output goes.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"sandbox/execd/internal/lifecycle"
	"sandbox/execd/internal/security"
)

// Spec describes one execution. Validation of Argv, Cwd and Env is the caller's responsibility.
type Spec struct {
	Argv []string
	Cwd  string
	// Env entries are merged over the Execd process environment; equal keys are overridden.
	Env map[string]string
	// MaxOutput limits each of stdout and stderr. Excess output is discarded and reported.
	MaxOutput int64
	// KillGrace is the delay between SIGTERM and SIGKILL for the process group.
	KillGrace time.Duration
}

// Result describes a finished execution.
type Result struct {
	ExitCode        int
	TimedOut        bool
	Canceled        bool
	StdoutTruncated bool
	StderrTruncated bool
	StartedAt       time.Time
	FinishedAt      time.Time
}

// Duration returns the wall-clock run time.
func (r Result) Duration() time.Duration { return r.FinishedAt.Sub(r.StartedAt) }

// ErrStart wraps failures to start the process (for example, program not found).
var ErrStart = errors.New("failed to start command")

// Run executes spec and waits for it to finish.
//
// The child runs in its own process group. When ctx is done, SIGTERM is sent to
// the whole group, followed by SIGKILL after KillGrace. Processes left in the
// group after the leader exits are also killed. stdout and stderr are copied to
// the given writers (nil discards), each limited to MaxOutput bytes.
//
// A non-nil error means the command could not be started or waited on. A command
// that runs and exits non-zero is reported through Result.ExitCode with a nil error.
func Run(ctx context.Context, spec Spec, stdout, stderr io.Writer) (Result, error) {
	if len(spec.Argv) == 0 {
		return Result{}, fmt.Errorf("%w: empty argv", ErrStart)
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	outW := security.NewLimitedWriter(stdout, spec.MaxOutput)
	errW := security.NewLimitedWriter(stderr, spec.MaxOutput)

	grace := spec.KillGrace
	if grace <= 0 {
		grace = 5 * time.Second
	}

	cmd := exec.Command(spec.Argv[0], spec.Argv[1:]...)
	cmd.Dir = spec.Cwd
	cmd.Env = mergeEnv(os.Environ(), spec.Env)
	cmd.Stdout = outW
	cmd.Stderr = errW
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// After the leader exits (or ctx is done), wait at most grace for output pipes
	// held open by leftover descendants, then close them.
	cmd.WaitDelay = grace

	res := Result{StartedAt: time.Now()}
	if err := lifecycle.StartTracked(cmd); err != nil {
		return res, fmt.Errorf("%w: %v", ErrStart, err)
	}
	pid := cmd.Process.Pid

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-pid, syscall.SIGTERM)
			t := time.NewTimer(grace)
			defer t.Stop()
			select {
			case <-t.C:
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			case <-done:
			}
		case <-done:
		}
	}()

	waitErr := cmd.Wait()
	close(done)
	lifecycle.Untrack(pid)
	// Kill any remaining members of the process group (no-op if none remain).
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	res.FinishedAt = time.Now()

	switch ctxErr := ctx.Err(); {
	case errors.Is(ctxErr, context.DeadlineExceeded):
		res.TimedOut = true
	case errors.Is(ctxErr, context.Canceled):
		res.Canceled = true
	}
	res.StdoutTruncated = outW.Truncated()
	res.StderrTruncated = errW.Truncated()
	res.ExitCode = exitCode(cmd.ProcessState)

	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) || errors.Is(waitErr, exec.ErrWaitDelay) {
			return res, nil
		}
		return res, waitErr
	}
	return res, nil
}

// exitCode maps a finished process to an exit code. A process killed by a signal
// reports 128 + signal number, the usual shell convention.
func exitCode(ps *os.ProcessState) int {
	if ps == nil {
		return -1
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}

func mergeEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(overrides))
	seen := make(map[string]bool, len(overrides))
	for _, kv := range base {
		k := kv
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				k = kv[:i]
				break
			}
		}
		if v, ok := overrides[k]; ok {
			out = append(out, k+"="+v)
			seen[k] = true
			continue
		}
		out = append(out, kv)
	}
	for k, v := range overrides {
		if !seen[k] {
			out = append(out, k+"="+v)
		}
	}
	return out
}
