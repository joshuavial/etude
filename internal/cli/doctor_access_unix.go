//go:build !windows

package cli

import (
	"strings"

	"golang.org/x/sys/unix"
)

func doctorCanSearch(path string) error {
	return unix.Access(path, unix.X_OK)
}

func doctorSplitPlatformCommand(input string) ([]string, error) {
	return doctorSplitPOSIX(input)
}

func doctorShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
