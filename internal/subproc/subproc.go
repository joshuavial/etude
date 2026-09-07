// Package subproc runs a managed external command so that its descendants do
// not outlive the invocation.
//
// # Contract
//
// On Unix the command is started in its own process group (Setpgid) and the
// whole group is SIGKILLed on context cancellation and again after the command
// exits. Consequently no process left in the group survives Run — including
// after a successful exit — so a runner that deliberately backgrounds a helper
// must expect that helper to be killed when Run returns. This is a lifecycle
// guarantee, not a sandbox: a child that calls setsid/setpgid leaves the group
// and is not reached, and a killed descendant may linger as a zombie until its
// parent reaps it.
//
// On non-Unix platforms Run is exactly today's behavior: exec.CommandContext
// kills the direct child and WaitDelay bounds pipe draining. Windows job
// objects are out of scope.
//
// cmd MUST have been created with exec.CommandContext: exec.Start rejects a
// command with a non-nil Cancel that has no context.
package subproc

import (
	"os/exec"
	"time"
)

// Run assigns waitDelay to cmd.WaitDelay, configures group-scoped cancellation,
// runs the command to completion, and terminates any surviving group member
// before returning the run error unchanged.
func Run(cmd *exec.Cmd, waitDelay time.Duration) error {
	cmd.WaitDelay = waitDelay
	configure(cmd)
	err := cmd.Run()
	if cmd.Process != nil {
		// Start succeeded, so a process group exists (or existed) for this pid.
		// cleanup runs on every path: success, non-zero exit, deadline, cancel
		// and WaitDelay abort.
		cleanup(cmd)
	}
	return err
}
