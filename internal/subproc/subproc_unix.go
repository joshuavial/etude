//go:build unix

package subproc

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

var errInvalidPID = errors.New("subproc: process group pid must be positive")

// configure puts the child in its own process group and makes context
// cancellation kill that whole group rather than the direct child alone.
func configure(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		err := killGroup(cmd.Process.Pid)
		if err != nil && !errors.Is(err, os.ErrProcessDone) {
			// The group kill failed for a reason other than "already gone"
			// (EPERM, for instance, when a descendant changed uid). Fall back
			// to at least killing the direct child; the original error is what
			// cmd.Wait reports.
			_ = cmd.Process.Kill()
		}
		return err
	}
}

// cleanup kills any group member still alive once the command has been waited
// for. The direct child is already reaped at this point; the group id stays
// valid while any member lives, so this reaches descendants that outlived the
// command (a backgrounded helper, a wrapper's worker, a child with redirected
// stdio). Best effort: ESRCH is the normal case and there is nothing to report.
func cleanup(cmd *exec.Cmd) {
	_ = killGroup(cmd.Process.Pid)
}

// killGroup SIGKILLs the process group led by pid. With Setpgid the child's
// pgid equals its pid. ESRCH means the group is already gone, which is reported
// as os.ErrProcessDone — Go's documented sentinel — so cmd.Wait does not turn
// an already-exited process into a spurious cancellation error. Any other
// errno is returned unchanged.
func killGroup(pid int) error {
	if pid <= 0 {
		// kill(0, ...) and kill(-0, ...) signal the CALLER's own group. Callers
		// only pass a started child's pid, so this is unreachable insurance.
		return errInvalidPID
	}
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
