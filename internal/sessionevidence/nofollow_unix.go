//go:build unix

package sessionevidence

import "syscall"

const nofollowFlag = syscall.O_NOFOLLOW
