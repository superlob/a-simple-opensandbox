package lifecycle

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// An orphaned grandchild is re-parented to the subreaper (this test process).
// The reaper must collect it, so its /proc entry disappears or stops being a zombie.
func TestReaperCollectsOrphans(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartReaper(ctx, true)

	cmd := exec.Command("sh", "-c", "sleep 0.2 & echo $!")
	out, err := startTrackedOutput(cmd)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(out))
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return // reaped
		}
		if f := strings.Fields(string(b)); len(f) > 2 && f[2] == "Z" {
			if time.Now().After(deadline) {
				t.Fatalf("orphan %d still a zombie", pid)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("orphan %d not reaped", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func startTrackedOutput(cmd *exec.Cmd) (string, error) {
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := StartTracked(cmd); err != nil {
		return "", err
	}
	defer Untrack(cmd.Process.Pid)
	buf := make([]byte, 64)
	n, _ := pipe.Read(buf)
	cmd.Wait()
	return string(buf[:n]), nil
}
