//go:build integration

// Process-level tests: they build the real binary, run it as a child process,
// and send real signals. Run with: go test -tags integration ./test/integration/...
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "execd-itest-")
	if err != nil {
		panic(err)
	}
	binPath = filepath.Join(dir, "execd")
	build := exec.Command("go", "build", "-o", binPath, "sandbox/execd/cmd/execd")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build failed:", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type instance struct {
	cmd  *exec.Cmd
	base string
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// start launches execd with the given workspace and data directories and waits for /health.
func start(t *testing.T, ws, data string) *instance {
	t.Helper()
	port := freePort(t)
	cfgPath := filepath.Join(t.TempDir(), "execd.json")
	cfg := fmt.Sprintf(`{"listen_addr":"127.0.0.1","port":%d,"workspace_dir":%q,"data_dir":%q,"shutdown_grace":"3s"}`, port, ws, data)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binPath)
	cmd.Env = append(os.Environ(), "EXECD_CONFIG="+cfgPath)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	inst := &instance{cmd: cmd, base: "http://127.0.0.1:" + strconv.Itoa(port)}
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get(inst.base + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return inst
			}
		}
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			t.Fatal("execd did not become healthy")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (i *instance) post(t *testing.T, path string, body any, out any) int {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(i.base+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func (i *instance) get(t *testing.T, path string, out any) int {
	t.Helper()
	resp, err := http.Get(i.base + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

type taskRec struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error"`
}

func waitStatus(t *testing.T, i *instance, id, want string) taskRec {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var rec taskRec
		i.get(t, "/v1/tasks/"+id, &rec)
		if rec.Status == want {
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s status %s, want %s", id, rec.Status, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// submitSleep starts a long background sleep. The shell writes the sleep's own PID
// to pidFile (via exec), so the test can clean up exactly that process and nothing else.
func submitSleep(t *testing.T, i *instance, pidFile string) string {
	t.Helper()
	var sub struct {
		ID string `json:"execution_id"`
	}
	argv := []string{"sh", "-c", "echo $$ > " + pidFile + " && exec sleep 600"}
	if code := i.post(t, "/v1/command", map[string]any{"argv": argv, "background": true}, &sub); code != 202 {
		t.Fatalf("submit status %d", code)
	}
	waitStatus(t, i, sub.ID, "running")
	return sub.ID
}

func killRecordedPID(t *testing.T, pidFile string) {
	t.Helper()
	b, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return
	}
	syscall.Kill(pid, syscall.SIGKILL)
}

// SIGKILL gives no chance to persist state. After restart the running task must be
// marked interrupted and must not be re-executed.
func TestKill9ThenRestartMarksInterrupted(t *testing.T) {
	ws := t.TempDir()
	data := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "sleep.pid")
	t.Cleanup(func() { killRecordedPID(t, pidFile) })

	first := start(t, ws, data)
	id := submitSleep(t, first, pidFile)
	if err := first.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	first.cmd.Wait()

	second := start(t, ws, data)
	defer stopGracefully(t, second)
	rec := waitStatus(t, second, id, "interrupted")
	if rec.Error == "" {
		t.Error("interrupted task should explain why")
	}
}

// SIGTERM must cancel running tasks, persist their final state, and exit with status 0.
func TestSigtermCancelsAndPersists(t *testing.T) {
	ws := t.TempDir()
	data := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "sleep.pid")
	t.Cleanup(func() { killRecordedPID(t, pidFile) })

	inst := start(t, ws, data)
	id := submitSleep(t, inst, pidFile)
	if err := inst.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- inst.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("exit: %v", err)
		}
	case <-time.After(15 * time.Second):
		inst.cmd.Process.Kill()
		t.Fatal("execd did not exit after SIGTERM")
	}

	second := start(t, ws, data)
	defer stopGracefully(t, second)
	waitStatus(t, second, id, "canceled")
}

func stopGracefully(t *testing.T, i *instance) {
	t.Helper()
	i.cmd.Process.Signal(syscall.SIGTERM)
	i.cmd.Wait()
}
