package sessionevidence

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReadRegularFileAcceptsRegularFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.txt")
	if err := os.WriteFile(path, []byte("transcript\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	got, err := ReadRegularFile(path)
	if err != nil {
		t.Fatalf("ReadRegularFile: %v", err)
	}
	if string(got) != "transcript\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestReadRegularFileRejectsFinalSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires platform support")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("secret target\n"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(dir, "transcript-link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unsupported: %v", err)
	}

	_, err := ReadRegularFile(link)
	if !errors.Is(err, ErrSymlink) {
		t.Fatalf("ReadRegularFile final symlink error = %v, want ErrSymlink", err)
	}
}

func TestReadRegularFileRejectsIntermediateSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires platform support")
	}
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "transcript.txt"), []byte("via symlink\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	linkDir := filepath.Join(dir, "linkdir")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("symlink creation unsupported: %v", err)
	}

	_, err := ReadRegularFileUnder(dir, filepath.Join(linkDir, "transcript.txt"))
	if !errors.Is(err, ErrSymlink) {
		t.Fatalf("ReadRegularFile intermediate symlink error = %v, want ErrSymlink", err)
	}
}

func TestReadRegularFileUnderRejectsOutsideRoot(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "transcript.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}

	_, err := ReadRegularFileUnder(dir, outside)
	if !errors.Is(err, ErrNotRegular) {
		t.Fatalf("ReadRegularFileUnder outside root error = %v, want ErrNotRegular", err)
	}
}
