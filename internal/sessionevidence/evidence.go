package sessionevidence

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	ErrNotRegular = errors.New("transcript is not a regular file")
	ErrSecret     = errors.New("transcript contains secret-looking content")
	ErrSymlink    = errors.New("transcript path contains symlink")
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`ghp_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|secret)[[:space:]]*[:=][[:space:]]*['"]?[A-Za-z0-9_./+=-]{16,}`),
}

// ReadRegularFile reads a transcript without following symlinks.
//
// Unix builds keep O_NOFOLLOW on the final open for atomic final-component
// protection. When the path is relative to or under the current working
// directory, parent-chain checks reject configured symlink components and
// strengthen non-Unix behavior. Those parent checks are not race-free
// containment; a fully race-free parent walk would need an openat-style
// implementation.
func ReadRegularFile(path string) ([]byte, error) {
	checkedPath, err := checkedRegularPath(path, "")
	if err != nil {
		return nil, err
	}
	return readCheckedRegularFile(checkedPath)
}

// ReadRegularFileUnder reads path only when it is inside root and has no symlink
// components below that root. Use this for run-owned scratch/worktree paths.
func ReadRegularFileUnder(root, path string) ([]byte, error) {
	checkedPath, err := checkedRegularPath(path, root)
	if err != nil {
		return nil, err
	}
	return readCheckedRegularFile(checkedPath)
}

func readCheckedRegularFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|nofollowFlag, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, ErrNotRegular
	}
	return io.ReadAll(f)
}

func checkedRegularPath(path, root string) (string, error) {
	clean := filepath.Clean(path)
	if clean == "." || clean == string(filepath.Separator) {
		return "", fmt.Errorf("%w: %s", ErrNotRegular, path)
	}
	if root != "" {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return "", err
		}
		absPath := clean
		if !filepath.IsAbs(absPath) {
			absPath = filepath.Join(absRoot, absPath)
		}
		absPath, err = filepath.Abs(absPath)
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(absRoot, absPath)
		if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
			return "", fmt.Errorf("%w: %s is outside %s", ErrNotRegular, absPath, absRoot)
		}
		if err := rejectSymlinkComponents(absRoot, rel); err != nil {
			return "", err
		}
		return absPath, nil
	}
	if filepath.IsAbs(clean) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(cwd, clean)
		if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
			info, err := os.Lstat(clean)
			if err != nil {
				return "", err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("%w: %s", ErrSymlink, clean)
			}
			return clean, nil
		}
		if err := rejectSymlinkComponents(cwd, rel); err != nil {
			return "", err
		}
		return clean, nil
	}
	if err := rejectSymlinkComponents("", clean); err != nil {
		return "", err
	}
	return clean, nil
}

func rejectSymlinkComponents(base, remaining string) error {
	current := ""
	if base != "" {
		current = base
	}
	for _, part := range strings.Split(remaining, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		if current == "" {
			current = part
		} else {
			current = filepath.Join(current, part)
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrSymlink, current)
		}
	}
	return nil
}

// ScanForSecrets is a conservative marker scan. It is not a complete redactor;
// it fails closed on common token and private-key shapes.
func ScanForSecrets(content []byte) error {
	for _, marker := range [][]byte{
		[]byte("-----BEGIN PRIVATE KEY-----"),
		[]byte("-----BEGIN RSA PRIVATE KEY-----"),
		[]byte("-----BEGIN OPENSSH PRIVATE KEY-----"),
	} {
		if bytes.Contains(content, marker) {
			return fmt.Errorf("%w: private key marker", ErrSecret)
		}
	}
	for _, re := range secretPatterns {
		if re.Find(content) != nil {
			return fmt.Errorf("%w: %s", ErrSecret, re.String())
		}
	}
	return nil
}
