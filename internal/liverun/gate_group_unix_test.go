//go:build unix

package liverun

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

	"github.com/joshuavial/etude/internal/replay"
)

// TestExecCheckRunner_TimeoutKillsDescendants proves that a timed-out check
// leaves no ordinary descendant behind: the check backgrounds a subshell that
// writes a marker one second later and then waits for it.
func TestExecCheckRunner_TimeoutKillsDescendants(t *testing.T) {
	worktree := t.TempDir()
	scratch := t.TempDir()
	marker := filepath.Join(scratch, "marker")
	pidfile := filepath.Join(scratch, "pidfile")
	warm := filepath.Join(scratch, "warm")

	// The paths are written literally into the script because the check env is
	// strict (PATH plus the ETUDE_* names only). The leading guard makes the
	// first execution a no-op: macOS scans a newly written executable on its
	// first exec, which can cost about a second — more than this timeout — so
	// the script is warmed before the timed invocation.
	script := filepath.Join(scratch, "check.sh")
	body := fmt.Sprintf("#!/bin/sh\nif [ ! -f %q ]; then : > %q; exit 0; fi\n"+
		"( sleep 1; echo survived > %q ) &\necho $! > %q\nwait\n", warm, warm, marker, pidfile)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(script).Run(); err != nil {
		t.Fatalf("warm-up run of %s: %v", script, err)
	}

	r := &execCheckRunner{command: []string{script}, timeout: 200 * time.Millisecond}
	launched := time.Now()
	passed, _, detail := r.RunCheck(context.Background(), replay.RunRequest{
		WorktreeDir: worktree,
		ScratchDir:  scratch,
	})
	if passed {
		t.Fatal("want the timed-out check to block")
	}
	if !strings.Contains(detail, "check timed out after") {
		t.Errorf("want a timeout detail, got %q", detail)
	}

	assertCheckDescendantGone(t, pidfile, marker, launched)
}

// assertCheckDescendantGone waits (bounded) for the pid in pidfile to be
// terminated and then asserts the marker file was never written. A zombie
// counts as terminated: it has already been killed and can never write.
func assertCheckDescendantGone(t *testing.T, pidfile, marker string, launched time.Time) {
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
	for !checkDescendantTerminated(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("descendant pid %d still running 3s after RunCheck returned", pid)
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

// checkDescendantTerminated reports whether pid is gone (ESRCH) or a zombie.
func checkDescendantTerminated(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return errors.Is(err, syscall.ESRCH)
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}
