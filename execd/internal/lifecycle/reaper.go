// Package lifecycle manages child-process tracking and zombie reaping for Execd.
//
// When Execd runs as PID 1 inside a container, orphaned grandchildren are
// re-parented to it and must be reaped, or they remain as zombies. Reaping with
// wait(-1) would race with exec.Cmd.Wait, which needs the exit status of its own
// children. The reaper therefore peeks with waitid(WNOWAIT) and only reaps
// processes that are not tracked. Tracked children are always left to
// exec.Cmd.Wait.
//
// This package targets Linux only.
package lifecycle

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	pAll                = 0 // idtype P_ALL
	wExited             = 0x4
	wNoHang             = 0x1
	wNoWait             = 0x1000000
	prSetChildSubreaper = 36
	siPidOffset         = 16 // offset of si_pid in siginfo_t on 64-bit Linux
)

var (
	trackMu sync.Mutex
	tracked = map[int]struct{}{}
)

// StartTracked starts cmd and registers its PID so the reaper will not reap it.
// The registration happens under the same lock the reaper holds while peeking,
// so a child can never be reaped between Start and registration.
func StartTracked(cmd *exec.Cmd) error {
	trackMu.Lock()
	defer trackMu.Unlock()
	if err := cmd.Start(); err != nil {
		return err
	}
	tracked[cmd.Process.Pid] = struct{}{}
	return nil
}

// Untrack removes a PID after exec.Cmd.Wait has returned for it.
func Untrack(pid int) {
	trackMu.Lock()
	defer trackMu.Unlock()
	delete(tracked, pid)
}

// StartReaper reaps untracked child processes until ctx is done.
// If subreaper is true, the process is first marked as a child subreaper so
// that orphaned descendants are re-parented to it. This is needed when Execd is
// not PID 1 (for example in tests); as PID 1 it is implicit.
func StartReaper(ctx context.Context, subreaper bool) {
	if subreaper {
		_, _, _ = syscall.RawSyscall(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0)
	}
	sigCh := make(chan os.Signal, 16)
	signal.Notify(sigCh, syscall.SIGCHLD)
	go func() {
		defer signal.Stop(sigCh)
		ticker := time.NewTicker(5 * time.Second) // backstop in case a signal is coalesced behind a tracked child
		defer ticker.Stop()
		reapUntracked()
		for {
			select {
			case <-ctx.Done():
				return
			case <-sigCh:
			case <-ticker.C:
			}
			reapUntracked()
		}
	}()
}

// reapUntracked reaps zombie children that are not tracked by StartTracked.
// It stops at the first tracked zombie; that child is reaped by its Cmd.Wait
// and the next SIGCHLD or tick triggers another pass.
func reapUntracked() {
	trackMu.Lock()
	defer trackMu.Unlock()
	for {
		var info [128]byte
		_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, pAll, 0,
			uintptr(unsafe.Pointer(&info[0])), wExited|wNoHang|wNoWait, 0, 0)
		if errno != 0 {
			return // ECHILD: no children at all
		}
		pid := int(*(*int32)(unsafe.Pointer(&info[siPidOffset])))
		if pid == 0 {
			return // no child changed state
		}
		if _, ok := tracked[pid]; ok {
			return
		}
		var ws syscall.WaitStatus
		_, _ = syscall.Wait4(pid, &ws, 0, nil)
	}
}
