package sessionevidence

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
)

var (
	ErrNotRegular = errors.New("transcript is not a regular file")
	ErrSecret     = errors.New("transcript contains secret-looking content")
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`ghp_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|secret)[[:space:]]*[:=][[:space:]]*['"]?[A-Za-z0-9_./+=-]{16,}`),
}

// ReadRegularFile reads a transcript without following symlinks.
func ReadRegularFile(path string) ([]byte, error) {
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
