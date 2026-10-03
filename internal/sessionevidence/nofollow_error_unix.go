//go:build unix && !dragonfly && !freebsd && !netbsd

package sessionevidence

import (
	"errors"

	"golang.org/x/sys/unix"
)

func isNoFollowError(err error) bool {
	return errors.Is(err, unix.ELOOP)
}
