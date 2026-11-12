package runner

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func spec(argv ...string) Spec {
	return Spec{Argv: argv, Cwd: os.TempDir(), MaxOutput: 1 << 20, KillGrace: time.Second}
}

func TestExitCodeAndOutput(t *testing.T) {
	var out, errb bytes.Buffer
	res, err := Run(context.Background(), spec("sh", "-c", "echo hi; echo err >&2; exit 3"), &out, &errb)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 || out.String() != "hi\n" || errb.String() != "err\n" || res.TimedOut {
		t.Errorf("res=%+v out=%q err=%q", res, out.String(), errb.String())
	}
}

func TestEnvAndCwd(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	s := spec("sh", "-c", "pwd; echo $FOO")
	s.Cwd = dir
	s.Env = map[string]string{"FOO": "bar"}
	if _, err := Run(context.Background(), s, &out, nil); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if lines[0] != dir && !strings.HasSuffix(lines[0], filepath.Base(dir)) || lines[1] != "bar" {
		t.Errorf("output %q", out.String())
	}
}

func TestStartFailure(t *testing.T) {
	if _, err := Run(context.Background(), spec("/nonexistent/binary-xyz"), nil, nil); err == nil {
		t.Error("expected start error")
	}
}

func TestTimeoutKillsProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	// The grandchild writes its PID and keeps running after the shell is killed.
	s := spec("sh", "-c", "sleep 30 & echo $! > "+pidFile+"; wait")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	res, err := Run(ctx, s, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || time.Since(start) > 5*time.Second {
		t.Errorf("timed out=%v after %v", res.TimedOut, time.Since(start))
	}
	b, _ := os.ReadFile(pidFile)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid > 0 {
		// A killed orphan may linger as a zombie until its (non-Execd) parent reaps it.
		if st, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
			if f := strings.Fields(string(st)); len(f) > 2 && f[2] != "Z" {
				t.Errorf("grandchild %d still running (state %s)", pid, f[2])
			}
		}
	}
}

func TestOutputLimit(t *testing.T) {
	s := spec("sh", "-c", "head -c 5000 /dev/zero")
	s.MaxOutput = 100
	var out bytes.Buffer
	res, err := Run(context.Background(), s, &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Len() != 100 || !res.StdoutTruncated || res.ExitCode != 0 {
		t.Errorf("len=%d trunc=%v exit=%d", out.Len(), res.StdoutTruncated, res.ExitCode)
	}
}

func TestSignalExitCode(t *testing.T) {
	res, err := Run(context.Background(), spec("sh", "-c", "kill -9 $$"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 137 {
		t.Errorf("exit=%d want 137", res.ExitCode)
	}
}

func TestMergeEnvOverrides(t *testing.T) {
	t.Setenv("EXECD_TEST_KEEP", "base")
	t.Setenv("EXECD_TEST_OVR", "base")
	env := mergeEnv(os.Environ(), map[string]string{"EXECD_TEST_OVR": "new"})
	var keep, ovr int
	for _, kv := range env {
		switch kv {
		case "EXECD_TEST_KEEP=base":
			keep++
		case "EXECD_TEST_OVR=new":
			ovr++
		}
	}
	if keep != 1 || ovr != 1 {
		t.Errorf("keep=%d ovr=%d", keep, ovr)
	}
}
