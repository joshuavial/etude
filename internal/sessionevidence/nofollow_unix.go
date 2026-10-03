//go:build unix

package sessionevidence

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const nofollowFlag = unix.O_NOFOLLOW

func readRegularFileUnder(root, path string, limit int64, limited bool) ([]byte, error) {
	absRoot, rel, err := relativePathUnder(root, path)
	if err != nil {
		return nil, err
	}
	rootFD, err := unix.Open(absRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open transcript root", Path: absRoot, Err: err}
	}
	defer unix.Close(rootFD)
	return readRegularFileAt(rootFD, rel, limit, limited)
}

func readRegularFileAt(rootFD int, relativePath string, limit int64, limited bool) ([]byte, error) {
	clean := filepath.Clean(relativePath)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%w: invalid descendant %s", ErrNotRegular, relativePath)
	}
	parts := strings.Split(clean, string(filepath.Separator))
	currentFD := rootFD
	ownedCurrent := false
	defer func() {
		if ownedCurrent {
			_ = unix.Close(currentFD)
		}
	}()

	for _, part := range parts[:len(parts)-1] {
		nextFD, err := unix.Openat(
			currentFD,
			part,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
			0,
		)
		if err != nil {
			return nil, classifyOpenatError(currentFD, part, err)
		}
		if ownedCurrent {
			_ = unix.Close(currentFD)
		}
		currentFD = nextFD
		ownedCurrent = true
	}

	name := parts[len(parts)-1]
	fd, err := unix.Openat(
		currentFD,
		name,
		unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, classifyOpenatError(currentFD, name, err)
	}
	f := os.NewFile(uintptr(fd), name)
	if f == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open transcript file: invalid descriptor")
	}
	defer f.Close()
	return readOpenedRegularFile(f, limit, limited)
}

func classifyOpenatError(dirFD int, name string, openErr error) error {
	if isNoFollowError(openErr) || isSymlinkAt(dirFD, name) {
		return fmt.Errorf("%w: %s", ErrSymlink, name)
	}
	return &os.PathError{Op: "openat", Path: name, Err: openErr}
}

func isSymlinkAt(dirFD int, name string) bool {
	var stat unix.Stat_t
	if err := unix.Fstatat(dirFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false
	}
	return stat.Mode&unix.S_IFMT == unix.S_IFLNK
}
