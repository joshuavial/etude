//go:build freebsd

package sessionevidence

import (
	"errors"

	"golang.org/x/sys/unix"
)

func isNoFollowError(err error) bool {
	return errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EMLINK)
}
