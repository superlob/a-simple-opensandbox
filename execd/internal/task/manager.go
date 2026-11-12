// Package task manages background command executions: status tracking, log
// capture, cancellation, timeouts, concurrency limits, persistence and cleanup.
// Foreground executions go through RunForeground, which shares the same
// concurrency limit and runner but is not persisted.
package task

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"

	"sandbox/execd/internal/config"
	"sandbox/execd/internal/runner"
	"sandbox/execd/internal/security"
	"sandbox/execd/internal/store"
)

// Task statuses.
const (
	StatusPending     = "pending"
	StatusRunning     = "running"
	StatusSucceeded   = "succeeded"
	StatusFailed      = "failed"
	StatusTimedOut    = "timed_out"
	StatusCanceled    = "canceled"
	StatusInterrupted = "interrupted"
)

// Sentinel errors.
var (
	ErrBusy         = errors.New("too many concurrent executions")
	ErrShuttingDown = errors.New("execd is shutting down")
	ErrNotFound     = store.ErrNotFound
	ErrInvalid      = errors.New("invalid request")
)

// Request is one command execution.
type Request struct {
	Argv []string
	Cwd  string
	// Env entries are merged over the Execd process environment for this execution only.
	Env map[string]string
	// Timeout is the execution limit. Zero means DefaultTimeout.
	Timeout time.Duration
}

// ForegroundResult is the outcome of a synchronous execution.
type ForegroundResult struct {
	runner.Result
	Stdout []byte
	Stderr []byte
}

type entry struct {
	rec    store.Record
	cancel context.CancelFunc // non-nil only while running
}

// Manager owns all task state. It is safe for concurrent use.
type Manager struct {
	cfg   config.Config
	store *store.Store
	log   *slog.Logger
	sem   chan struct{}

	mu      sync.Mutex
	entries map[string]*entry
	closing bool
	stopCh  chan struct{}
	wg      sync.WaitGroup
}

// New creates a Manager, loads persisted records, marks interrupted tasks, and
// prunes expired records.
func New(cfg config.Config, st *store.Store, logger *slog.Logger) (*Manager, error) {
	if logger == nil {
		logger = slog.Default()
	}
	m := &Manager{
		cfg:     cfg,
		store:   st,
		log:     logger,
		sem:     make(chan struct{}, cfg.MaxConcurrentTasks),
		entries: map[string]*entry{},
		stopCh:  make(chan struct{}),
	}
	recs, skipped, err := st.ListRecords()
	if err != nil {
		return nil, fmt.Errorf("load task records: %w", err)
	}
	if skipped > 0 {
		logger.Warn("skipped unreadable task records", "count", skipped)
	}
	now := time.Now()
	for _, r := range recs {
		if r.Status == StatusPending || r.Status == StatusRunning {
			r.Status = StatusInterrupted
			r.Error = "execd restarted before the task finished"
			r.FinishedAt = &now
			if err := st.SaveRecord(r); err != nil {
				return nil, fmt.Errorf("mark task %s interrupted: %w", r.ID, err)
			}
		}
		m.entries[r.ID] = &entry{rec: r}
	}
	m.prune(now)
	return m, nil
}

// Submit starts a background execution and returns its ID immediately.
func (m *Manager) Submit(req Request) (string, error) {
	timeout, err := m.validate(&req)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return "", ErrShuttingDown
	}
	if !m.tryAcquire() {
		m.mu.Unlock()
		return "", ErrBusy
	}
	id, err := newID()
	if err != nil {
		m.release()
		m.mu.Unlock()
		return "", err
	}
	rec := store.Record{
		ID:             id,
		Argv:           req.Argv,
		Cwd:            req.Cwd,
		EnvKeys:        envKeys(req.Env),
		TimeoutSeconds: timeout.Seconds(),
		Status:         StatusPending,
		CreatedAt:      time.Now().UTC(),
	}
	if err := m.store.SaveRecord(rec); err != nil {
		m.release()
		m.mu.Unlock()
		return "", fmt.Errorf("persist task: %w", err)
	}
	m.entries[id] = &entry{rec: rec}
	m.wg.Add(1)
	m.mu.Unlock()

	go m.execute(id, req, timeout)
	return id, nil
}

// RunForeground runs a command synchronously. It respects reqCtx (client
// disconnect), the timeout, and shutdown. Nothing is persisted.
func (m *Manager) RunForeground(reqCtx context.Context, req Request) (ForegroundResult, error) {
	timeout, err := m.validate(&req)
	if err != nil {
		return ForegroundResult{}, err
	}
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return ForegroundResult{}, ErrShuttingDown
	}
	if !m.tryAcquire() {
		m.mu.Unlock()
		return ForegroundResult{}, ErrBusy
	}
	m.wg.Add(1)
	m.mu.Unlock()
	defer func() {
		m.release()
		m.wg.Done()
	}()

	ctx, cancel := context.WithTimeout(reqCtx, timeout)
	defer cancel()
	stop := m.cancelOnShutdown(cancel)
	defer stop()

	stdout := security.NewLimitedBuffer(m.cfg.MaxOutputBytes)
	stderr := security.NewLimitedBuffer(m.cfg.MaxOutputBytes)
	res, err := runner.Run(ctx, m.spec(req), stdout, stderr)
	if err != nil {
		return ForegroundResult{}, err
	}
	return ForegroundResult{Result: res, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, nil
}

// Get returns a snapshot of a task record.
func (m *Manager) Get(id string) (store.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return store.Record{}, ErrNotFound
	}
	return e.rec, nil
}

// List returns records newest first. An empty status matches all statuses.
func (m *Manager) List(status string) []store.Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]store.Record, 0, len(m.entries))
	for _, e := range m.entries {
		if status == "" || e.rec.Status == status {
			out = append(out, e.rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Cancel requests cancellation. It is idempotent: finished tasks are returned
// unchanged. The returned record may still show "running" until the process has exited.
func (m *Manager) Cancel(id string) (store.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return store.Record{}, ErrNotFound
	}
	switch e.rec.Status {
	case StatusPending:
		// Not yet started: execute() sees the cancel flag and finishes without running.
		e.rec.Error = "canceled before start"
		e.rec.Status = StatusCanceled
		now := time.Now().UTC()
		e.rec.FinishedAt = &now
		m.persist(e)
	case StatusRunning:
		if e.cancel != nil {
			e.cancel()
		}
	}
	return e.rec, nil
}

// ReadLog returns up to limit bytes of a task log from offset.
func (m *Manager) ReadLog(id, stream string, offset, limit int64) ([]byte, int64, bool, error) {
	if _, err := m.Get(id); err != nil {
		return nil, 0, false, err
	}
	data, next, eof, err := m.store.ReadLog(id, stream, offset, limit)
	return data, next, eof, err
}

// Shutdown stops accepting work, cancels running executions, and waits for them
// to finish or for ctx to expire. Records are persisted as they finish.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	if !m.closing {
		m.closing = true
		close(m.stopCh)
	}
	for _, e := range m.entries {
		if e.cancel != nil {
			e.cancel()
		}
	}
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("timed out waiting for tasks to stop: %w", ctx.Err())
	}
}

// execute runs one background task to completion. It owns one semaphore slot.
func (m *Manager) execute(id string, req Request, timeout time.Duration) {
	defer func() {
		m.release()
		m.wg.Done()
	}()

	m.mu.Lock()
	e := m.entries[id]
	if e.rec.Status != StatusPending { // canceled before start
		m.mu.Unlock()
		m.afterFinish()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	e.cancel = cancel
	now := time.Now().UTC()
	e.rec.Status = StatusRunning
	e.rec.StartedAt = &now
	m.persist(e)
	m.mu.Unlock()

	stop := m.cancelOnShutdown(cancel)
	res, runErr := m.runToLogs(ctx, id, req)
	stop()
	cancel()

	m.mu.Lock()
	defer m.mu.Unlock()
	e.cancel = nil
	fin := res.FinishedAt
	if fin.IsZero() {
		fin = time.Now()
	}
	finUTC := fin.UTC()
	e.rec.FinishedAt = &finUTC
	e.rec.StdoutTruncated = res.StdoutTruncated
	e.rec.StderrTruncated = res.StderrTruncated
	switch {
	case runErr != nil:
		e.rec.Status = StatusFailed
		e.rec.Error = runErr.Error()
	case res.TimedOut:
		e.rec.Status = StatusTimedOut
		code := res.ExitCode
		e.rec.ExitCode = &code
		e.rec.TimedOut = true
	case res.Canceled:
		e.rec.Status = StatusCanceled
		code := res.ExitCode
		e.rec.ExitCode = &code
	default:
		code := res.ExitCode
		e.rec.ExitCode = &code
		if code == 0 {
			e.rec.Status = StatusSucceeded
		} else {
			e.rec.Status = StatusFailed
		}
	}
	m.persist(e)
	m.log.Info("task finished", "id", id, "status", e.rec.Status)
	m.pruneLocked(time.Now())
}

// afterFinish exists so the canceled-before-start path uses the same cleanup as normal completion.
func (m *Manager) afterFinish() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(time.Now())
}

// runToLogs runs the command with stdout/stderr going to the task's log files.
func (m *Manager) runToLogs(ctx context.Context, id string, req Request) (runner.Result, error) {
	outF, err := m.store.OpenLog(id, store.StreamStdout)
	if err != nil {
		return runner.Result{}, fmt.Errorf("open stdout log: %w", err)
	}
	defer outF.Close()
	errF, err := m.store.OpenLog(id, store.StreamStderr)
	if err != nil {
		return runner.Result{}, fmt.Errorf("open stderr log: %w", err)
	}
	defer errF.Close()
	return runner.Run(ctx, m.spec(req), outF, errF)
}

// persist saves the entry's record. Caller must hold m.mu.
func (m *Manager) persist(e *entry) {
	if err := m.store.SaveRecord(e.rec); err != nil {
		m.log.Error("persist task failed", "id", e.rec.ID, "err", err)
	}
}

// pruneLocked removes expired finished records and enforces MaxFinishedTasks.
// Caller must hold m.mu.
func (m *Manager) pruneLocked(now time.Time) {
	retention := m.cfg.TaskRetention.Std()
	var finished []*entry
	for id, e := range m.entries {
		if e.rec.FinishedAt == nil {
			continue
		}
		if now.Sub(*e.rec.FinishedAt) > retention {
			m.deleteEntry(id)
			continue
		}
		finished = append(finished, e)
	}
	if extra := len(finished) - m.cfg.MaxFinishedTasks; extra > 0 {
		sort.Slice(finished, func(i, j int) bool {
			return finished[i].rec.FinishedAt.Before(*finished[j].rec.FinishedAt)
		})
		for _, e := range finished[:extra] {
			m.deleteEntry(e.rec.ID)
		}
	}
}

func (m *Manager) prune(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(now)
}

func (m *Manager) deleteEntry(id string) {
	if err := m.store.DeleteTask(id); err != nil {
		m.log.Error("delete task files failed", "id", id, "err", err)
		return
	}
	delete(m.entries, id)
}

func (m *Manager) validate(req *Request) (time.Duration, error) {
	if err := security.ValidateArgv(req.Argv); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := security.ValidateEnv(req.Env); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if req.Cwd == "" {
		req.Cwd = m.cfg.WorkspaceDir
	}
	if err := m.checkCwd(req.Cwd); err != nil {
		return 0, err
	}
	timeout := req.Timeout
	if timeout == 0 {
		timeout = m.cfg.DefaultTimeout.Std()
	}
	if timeout < 0 || timeout > m.cfg.MaxTimeout.Std() {
		return 0, fmt.Errorf("%w: timeout must be in (0, %s]", ErrInvalid, m.cfg.MaxTimeout.Std())
	}
	return timeout, nil
}

func (m *Manager) spec(req Request) runner.Spec {
	return runner.Spec{
		Argv:      req.Argv,
		Cwd:       req.Cwd,
		Env:       req.Env,
		MaxOutput: m.cfg.MaxOutputBytes,
		KillGrace: m.cfg.ShutdownGrace.Std(),
	}
}

// checkCwd ensures cwd resolves inside the workspace and is a directory.
func (m *Manager) checkCwd(cwd string) error {
	p, err := security.ResolvePath(m.cfg.WorkspaceDir, cwd)
	if err != nil {
		return fmt.Errorf("%w: cwd: %v", ErrInvalid, err)
	}
	st, err := os.Stat(p)
	if err != nil {
		return fmt.Errorf("%w: cwd: %v", ErrInvalid, err)
	}
	if !st.IsDir() {
		return fmt.Errorf("%w: cwd is not a directory", ErrInvalid)
	}
	return nil
}

// cancelOnShutdown calls cancel when the manager starts shutting down. The
// returned function must be called when the execution ends.
func (m *Manager) cancelOnShutdown(cancel context.CancelFunc) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-m.stopCh:
			cancel()
		case <-done:
		}
	}()
	return func() { close(done) }
}

// tryAcquire takes a concurrency slot without blocking. Caller must hold m.mu.
func (m *Manager) tryAcquire() bool {
	select {
	case m.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (m *Manager) release() { <-m.sem }

func envKeys(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// newID returns a random RFC 4122 version-4 UUID string.
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

// Closing reports whether Shutdown has been called.
func (m *Manager) Closing() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closing
}

// Counts returns the number of running tasks and the total number of records.
func (m *Manager) Counts() (running, total int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.entries {
		if e.rec.Status == StatusRunning {
			running++
		}
	}
	return running, len(m.entries)
}
