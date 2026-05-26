//go:build unix

package cli

import "syscall"

// nofollowFlag, OR'd into an os.OpenFile flag, makes the open fail (ELOOP)
// rather than follow a final-component symlink. On unix it is syscall.O_NOFOLLOW.
const nofollowFlag = syscall.O_NOFOLLOW
