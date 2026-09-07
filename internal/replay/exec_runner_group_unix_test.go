//go:build unix

package replay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestExecRunner_TimeoutKillsDescendants proves that a timed-out runner leaves
// no ordinary descendant behind: the script backgrounds a subshell that writes
// a marker one second later and then waits for it, so with direct-child-only
// cancellation the marker still appears after Run returns.
func TestExecRunner_TimeoutKillsDescendants(t *testing.T) {
	orig := runnerWaitDelay
	runnerWaitDelay = 200 * time.Millisecond
	defer func() { runnerWaitDelay = orig }()

	worktree, scratch := makeSiblingDirs(t)
	marker := filepath.Join(scratch, "marker")
	pidfile := filepath.Join(scratch, "pidfile")

	scriptPath := filepath.Join(scratch, "script.sh")
	writeScript(t, scriptPath, descendantScript(scratch, marker, pidfile))
	warmScript(t, scriptPath)

	r := &ExecRunner{
		Command: []string{scriptPath},
		Timeout: 200 * time.Millisecond,
	}
	req := RunRequest{WorktreeDir: worktree, ScratchDir: scratch}

	launched := time.Now()
	_, err := r.Run(context.Background(), req)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("want 'timed out' in error message, got: %v", err)
	}

	assertDescendantGone(t, pidfile, marker, launched)
}

// descendantScript returns a script body that backgrounds a subshell writing
// marker after a second, records the subshell pid in pidfile, and waits. The
// paths are written literally into the script because the child environment is
// strict (PATH plus the ETUDE_* names only). The leading guard makes the first
// execution a no-op: see warmScript.
func descendantScript(dir, marker, pidfile string) string {
	warm := filepath.Join(dir, "warm")
	return fmt.Sprintf("if [ ! -f %q ]; then : > %q; exit 0; fi\n"+
		"( sleep 1; echo survived > %q ) &\necho $! > %q\nwait", warm, warm, marker, pidfile)
}

// warmScript executes the script once so its guard trips. macOS scans a newly
// written executable on its first exec, which can cost a second — far more than
// the sub-second timeouts these tests use — while a second exec of the same
// file costs milliseconds. Without this the timed invocation would be killed
// before the script ran a single command and the test would prove nothing.
func warmScript(t *testing.T, path string) {
	t.Helper()
	if err := exec.Command(path).Run(); err != nil {
		t.Fatalf("warm-up run of %s: %v", path, err)
	}
}

// assertDescendantGone waits (bounded) for the pid in pidfile to be terminated
// and then asserts the marker file was never written. A zombie counts as
// terminated: it has already been killed and can never write.
func assertDescendantGone(t *testing.T, pidfile, marker string, launched time.Time) {
	t.Helper()
	raw, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatalf("read pidfile: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse pid %q: %v", raw, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !descendantTerminated(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("descendant pid %d still running 3s after Run returned", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if wait := time.Until(launched.Add(1300 * time.Millisecond)); wait > 0 {
		time.Sleep(wait)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("marker %s exists: descendant outlived the invocation", marker)
	}
}

// descendantTerminated reports whether pid is gone (ESRCH) or already a zombie.
func descendantTerminated(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return errors.Is(err, syscall.ESRCH)
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}
