//go:build !unix

package subproc

import "os/exec"

// configure is a no-op off Unix: there is no portable process-group primitive
// here, so the fallback is exactly today's behavior — exec.CommandContext kills
// the direct child and cmd.WaitDelay bounds pipe draining. Windows job objects
// are out of scope.
func configure(cmd *exec.Cmd) {}

// cleanup is a no-op off Unix; see configure.
func cleanup(cmd *exec.Cmd) {}
