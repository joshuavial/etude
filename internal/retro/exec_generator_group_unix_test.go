//go:build unix

package retro

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

// TestExecGenerator_TimeoutKillsDescendants proves that a timed-out generator
// leaves no ordinary descendant behind: the script backgrounds a subshell that
// writes a marker one second later and then waits for it.
func TestExecGenerator_TimeoutKillsDescendants(t *testing.T) {
	orig := generatorWaitDelay
	generatorWaitDelay = 200 * time.Millisecond
	defer func() { generatorWaitDelay = orig }()

	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	pidfile := filepath.Join(dir, "pidfile")
	warm := filepath.Join(dir, "warm")

	// The paths are written literally into the script because the generator env
	// is strict (PATH plus the ETUDE_* names only). The leading guard makes the
	// first execution a no-op: macOS scans a newly written executable on its
	// first exec, which can cost about a second — more than this timeout — so
	// the script is warmed before the timed invocation.
	script := filepath.Join(dir, "gen.sh")
	writeGenScript(t, script, fmt.Sprintf("if [ ! -f %q ]; then : > %q; exit 0; fi\n"+
		"( sleep 1; echo survived > %q ) &\necho $! > %q\nwait", warm, warm, marker, pidfile))
	if err := exec.Command(script).Run(); err != nil {
		t.Fatalf("warm-up run of %s: %v", script, err)
	}

	g := &ExecGenerator{
		Command: []string{script},
		Timeout: 200 * time.Millisecond,
	}
	launched := time.Now()
	_, err := g.Generate(context.Background(), GenerateRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("want 'timed out' in error message, got: %v", err)
	}

	assertGenDescendantGone(t, pidfile, marker, launched)
}

// assertGenDescendantGone waits (bounded) for the pid in pidfile to be
// terminated and then asserts the marker file was never written. A zombie
// counts as terminated: it has already been killed and can never write.
func assertGenDescendantGone(t *testing.T, pidfile, marker string, launched time.Time) {
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
	for !genDescendantTerminated(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("descendant pid %d still running 3s after Generate returned", pid)
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

// genDescendantTerminated reports whether pid is gone (ESRCH) or a zombie.
func genDescendantTerminated(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return errors.Is(err, syscall.ESRCH)
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}
