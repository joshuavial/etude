//go:build unix

package subproc

import (
	"bytes"
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

// writeScript writes a POSIX sh script to path, makes it executable, and runs
// it once so its leading guard trips. The warm-up matters because macOS scans a
// newly written executable on its first exec, which can cost about a second —
// more than the sub-second timeouts these tests use — while a second exec of
// the same file costs milliseconds. Every script here therefore starts with a
// guard that makes the first execution a no-op.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	warm := filepath.Join(dir, name+".warm")
	guard := fmt.Sprintf("if [ ! -f %q ]; then : > %q; exit 0; fi\n", warm, warm)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+guard+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(path).Run(); err != nil {
		t.Fatalf("warm-up run of %s: %v", path, err)
	}
	return path
}

// assertDescendantGone waits (bounded) for the pid recorded in pidfile to be
// terminated and then asserts the marker file was never written. A zombie
// counts as terminated: it has already been killed and can never write.
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
	if marker == "" {
		return
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

// TestRun_TimeoutKillsWaitingGrandchild covers the audited failure: a script
// that backgrounds a helper and waits for it. Killing the direct child alone
// leaves the helper running, and it writes into the caller's tree after Run has
// already returned.
func TestRun_TimeoutKillsWaitingGrandchild(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	pidfile := filepath.Join(dir, "pidfile")
	script := writeScript(t, dir, "wait.sh", fmt.Sprintf(
		"( sleep 1; echo survived > %q ) &\necho $! > %q\nwait", marker, pidfile))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	launched := time.Now()
	err := Run(cmd, 200*time.Millisecond)
	if err == nil {
		t.Fatal("want an error from the killed command, got nil")
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("ctx.Err() = %v, want context.DeadlineExceeded", ctx.Err())
	}
	assertDescendantGone(t, pidfile, marker, launched)
}

// TestRun_WrapperExitsEarlyChildHoldsPipes covers a wrapper that exits
// immediately while a backgrounded child keeps the inherited pipe write-ends
// open. WaitDelay bounds the wait; the post-run group kill removes the child.
func TestRun_WrapperExitsEarlyChildHoldsPipes(t *testing.T) {
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "pidfile")
	script := writeScript(t, dir, "pipes.sh", fmt.Sprintf(
		"sleep 5 &\necho $! > %q\nexit 0", pidfile))

	cmd := exec.CommandContext(context.Background(), script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	launched := time.Now()
	err := Run(cmd, 200*time.Millisecond)
	if elapsed := time.Since(launched); elapsed > 3*time.Second {
		t.Fatalf("Run took %v; WaitDelay did not bound the pipe drain", elapsed)
	}
	if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("want nil or exec.ErrWaitDelay, got %v", err)
	}
	assertDescendantGone(t, pidfile, "", launched)
}

// TestRun_DetachedStdioChildOutlivesWrapper covers the case exec.CommandContext
// cannot handle at all: the wrapper exits 0 promptly and its child redirected
// its stdio, so nothing about the command is pending and nothing would ever be
// signalled. Only the post-run group kill reaches the child.
func TestRun_DetachedStdioChildOutlivesWrapper(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	pidfile := filepath.Join(dir, "pidfile")
	script := writeScript(t, dir, "detached.sh", fmt.Sprintf(
		"( sleep 1; echo survived > %q ) </dev/null >/dev/null 2>&1 &\necho $! > %q\nexit 0",
		marker, pidfile))

	cmd := exec.CommandContext(context.Background(), script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	launched := time.Now()
	if err := Run(cmd, 200*time.Millisecond); err != nil {
		t.Fatalf("want a clean exit, got %v", err)
	}
	assertDescendantGone(t, pidfile, marker, launched)
}

// TestRun_SuccessAndOutputPreserved proves the ordinary path is untouched:
// exit status, stdout and stderr all survive the group handling.
func TestRun_SuccessAndOutputPreserved(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "ok.sh", "printf 'out'\nprintf 'err' >&2\nexit 0")

	cmd := exec.CommandContext(context.Background(), script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := Run(cmd, 10*time.Second); err != nil {
		t.Fatalf("want success, got %v", err)
	}
	if stdout.String() != "out" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "out")
	}
	if stderr.String() != "err" {
		t.Errorf("stderr = %q, want %q", stderr.String(), "err")
	}

	// A non-zero exit is still reported as an ExitError, unchanged.
	failing := writeScript(t, dir, "fail.sh", "exit 3")
	cmd = exec.CommandContext(context.Background(), failing)
	err := Run(cmd, 10*time.Second)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("want exit status 3, got %v", err)
	}
}

// TestRun_ContextCancelClassification proves an explicit cancel (not a
// deadline) still reports as a failed command with ctx.Err() == Canceled, so
// callers that test ctx.Err() before the run error classify it as before.
func TestRun_ContextCancelClassification(t *testing.T) {
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "pidfile")
	script := writeScript(t, dir, "cancel.sh", fmt.Sprintf("echo $$ > %q\nsleep 5", pidfile))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	defer cancel()

	cmd := exec.CommandContext(ctx, script)
	err := Run(cmd, 200*time.Millisecond)
	if err == nil {
		t.Fatal("want an error from the cancelled command, got nil")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("ctx.Err() = %v, want context.Canceled", ctx.Err())
	}
	if errors.Is(err, os.ErrProcessDone) {
		t.Errorf("os.ErrProcessDone leaked to the caller: %v", err)
	}
}

// TestRun_RequiresCommandContext pins the documented contract: a command built
// with exec.Command rather than exec.CommandContext fails loudly at Start
// instead of silently losing group cancellation.
func TestRun_RequiresCommandContext(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "noctx.sh", "exit 0")

	cmd := exec.Command(script)
	err := Run(cmd, time.Second)
	if err == nil {
		t.Fatal("want an error for a command built without a context, got nil")
	}
	if !strings.Contains(err.Error(), "Cancel") {
		t.Errorf("want the exec Cancel/Context contract error, got %v", err)
	}
	if cmd.Process != nil {
		t.Error("no process should have started")
	}
}

// TestKillGroup_RejectsNonPositivePid pins the guard that keeps a zero or
// negative pid from turning kill(-pid) into a kill of etude's own group.
func TestKillGroup_RejectsNonPositivePid(t *testing.T) {
	for _, pid := range []int{0, -1} {
		if err := killGroup(pid); !errors.Is(err, errInvalidPID) {
			t.Errorf("killGroup(%d) = %v, want errInvalidPID", pid, err)
		}
	}
}

// TestKillGroup_ESRCHIsProcessDone proves an already-gone group is reported as
// os.ErrProcessDone, which is what keeps cmd.Wait from turning a race between
// exit and cancellation into a spurious error.
func TestKillGroup_ESRCHIsProcessDone(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "quick.sh", "exit 0")

	cmd := exec.CommandContext(context.Background(), script)
	if err := Run(cmd, time.Second); err != nil {
		t.Fatalf("want success, got %v", err)
	}
	// The command has been waited for and its group killed, so the group id is
	// gone: kill(-pgid) reports ESRCH.
	if err := killGroup(cmd.Process.Pid); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("killGroup on a finished group = %v, want os.ErrProcessDone", err)
	}
}
