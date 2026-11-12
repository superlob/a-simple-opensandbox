package task

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"sandbox/execd/internal/config"
	"sandbox/execd/internal/store"
)

func testCfg(t *testing.T) (config.Config, string) {
	t.Helper()
	ws := t.TempDir()
	cfg := config.Default()
	cfg.Port = 8080
	cfg.WorkspaceDir = ws
	cfg.DataDir = t.TempDir()
	cfg.MaxConcurrentTasks = 2
	cfg.ShutdownGrace = config.Duration(time.Second)
	return cfg, ws
}

func newMgr(t *testing.T, cfg config.Config) *Manager {
	t.Helper()
	st, err := store.New(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func waitStatus(t *testing.T, m *Manager, id, want string) store.Record {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		r, err := m.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status == want {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s status %s, want %s", id, r.Status, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestBackgroundSucceedsAndLogs(t *testing.T) {
	cfg, _ := testCfg(t)
	m := newMgr(t, cfg)
	id, err := m.Submit(Request{Argv: []string{"sh", "-c", "echo out; echo err >&2"}})
	if err != nil {
		t.Fatal(err)
	}
	r := waitStatus(t, m, id, StatusSucceeded)
	if r.ExitCode == nil || *r.ExitCode != 0 {
		t.Errorf("exit code %v", r.ExitCode)
	}
	data, _, _, err := m.ReadLog(id, store.StreamStdout, 0, 100)
	if err != nil || string(data) != "out\n" {
		t.Errorf("stdout log %q %v", data, err)
	}
}

func TestCancelRunningTask(t *testing.T) {
	cfg, _ := testCfg(t)
	m := newMgr(t, cfg)
	id, _ := m.Submit(Request{Argv: []string{"sleep", "60"}})
	waitStatus(t, m, id, StatusRunning)
	if _, err := m.Cancel(id); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, id, StatusCanceled)
	if _, err := m.Cancel(id); err != nil { // idempotent
		t.Errorf("second cancel: %v", err)
	}
}

func TestTimeoutStatus(t *testing.T) {
	cfg, _ := testCfg(t)
	m := newMgr(t, cfg)
	id, _ := m.Submit(Request{Argv: []string{"sleep", "60"}, Timeout: 200 * time.Millisecond})
	r := waitStatus(t, m, id, StatusTimedOut)
	if !r.TimedOut {
		t.Error("TimedOut flag not set")
	}
}

func TestConcurrencyLimit(t *testing.T) {
	cfg, _ := testCfg(t)
	m := newMgr(t, cfg)
	a, _ := m.Submit(Request{Argv: []string{"sleep", "60"}})
	b, _ := m.Submit(Request{Argv: []string{"sleep", "60"}})
	if _, err := m.Submit(Request{Argv: []string{"true"}}); !errors.Is(err, ErrBusy) {
		t.Errorf("third submit: %v", err)
	}
	if _, err := m.RunForeground(context.Background(), Request{Argv: []string{"true"}}); !errors.Is(err, ErrBusy) {
		t.Errorf("foreground while full: %v", err)
	}
	m.Cancel(a)
	m.Cancel(b)
	waitStatus(t, m, a, StatusCanceled)
}

func TestForegroundNotPersisted(t *testing.T) {
	cfg, _ := testCfg(t)
	m := newMgr(t, cfg)
	res, err := m.RunForeground(context.Background(), Request{Argv: []string{"sh", "-c", "echo fg; exit 4"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 4 || string(res.Stdout) != "fg\n" {
		t.Errorf("res %+v", res.Result)
	}
	if len(m.List("")) != 0 {
		t.Error("foreground execution was recorded")
	}
}

func TestCwdAndEnvValidation(t *testing.T) {
	cfg, _ := testCfg(t)
	m := newMgr(t, cfg)
	if _, err := m.RunForeground(context.Background(), Request{Argv: []string{"true"}, Cwd: "../.."}); !errors.Is(err, ErrInvalid) {
		t.Errorf("escaping cwd: %v", err)
	}
	if _, err := m.RunForeground(context.Background(), Request{Argv: []string{"true"}, Env: map[string]string{"1X": "a"}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad env: %v", err)
	}
	if _, err := m.RunForeground(context.Background(), Request{Argv: []string{"true"}, Timeout: 2 * cfg.MaxTimeout.Std()}); !errors.Is(err, ErrInvalid) {
		t.Errorf("timeout over max: %v", err)
	}
}

func TestRestartRecoveryMarksInterrupted(t *testing.T) {
	cfg, _ := testCfg(t)
	st, _ := store.New(cfg.DataDir)
	now := time.Now()
	id := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	st.SaveRecord(store.Record{ID: id, Status: StatusRunning, CreatedAt: now, Argv: []string{"sleep"}})
	done := "ffffffff-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	fin := now
	st.SaveRecord(store.Record{ID: done, Status: StatusSucceeded, CreatedAt: now, FinishedAt: &fin})

	m, err := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	r, _ := m.Get(id)
	if r.Status != StatusInterrupted || r.FinishedAt == nil {
		t.Errorf("running task after restart: %+v", r)
	}
	if d, _ := m.Get(done); d.Status != StatusSucceeded {
		t.Errorf("finished task changed: %s", d.Status)
	}
}

func TestPruneByCount(t *testing.T) {
	cfg, _ := testCfg(t)
	cfg.MaxFinishedTasks = 1
	m := newMgr(t, cfg)
	var ids []string
	for i := 0; i < 3; i++ {
		id, err := m.Submit(Request{Argv: []string{"true"}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		waitStatus(t, m, id, StatusSucceeded)
	}
	if n := len(m.List("")); n != 1 {
		t.Errorf("records kept: %d", n)
	}
	if _, err := os.Stat(cfg.DataDir + "/tasks/" + ids[0]); !os.IsNotExist(err) {
		t.Error("pruned task files still present")
	}
}

func TestShutdownCancelsRunning(t *testing.T) {
	cfg, _ := testCfg(t)
	m := newMgr(t, cfg)
	id, _ := m.Submit(Request{Argv: []string{"sleep", "60"}})
	waitStatus(t, m, id, StatusRunning)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	r, _ := m.Get(id)
	if r.Status != StatusCanceled {
		t.Errorf("status after shutdown %s", r.Status)
	}
	if _, err := m.Submit(Request{Argv: []string{"true"}}); !errors.Is(err, ErrShuttingDown) {
		t.Errorf("submit after shutdown: %v", err)
	}
}
