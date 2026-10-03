//go:build unix

package sessionevidence

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestReadRegularFileAtStaysUnderOpenedRootAfterReplacement(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "transcript.txt"), []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(rootFD)

	movedRoot := filepath.Join(parent, "opened-root")
	if err := os.Rename(root, movedRoot); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(filepath.Join(outside, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "nested", "transcript.txt"), []byte("replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}

	got, err := readRegularFileAt(rootFD, filepath.Join("nested", "transcript.txt"), 0, false)
	if err != nil {
		t.Fatalf("read through opened root: %v", err)
	}
	if string(got) != "original\n" {
		t.Fatalf("content = %q, want original inode", got)
	}
}

func TestReadRegularFileUnderRejectsFIFOWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "transcript.pipe")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadRegularFileUnder(root, path)
	if !errors.Is(err, ErrNotRegular) {
		t.Fatalf("FIFO error = %v, want ErrNotRegular", err)
	}
}
