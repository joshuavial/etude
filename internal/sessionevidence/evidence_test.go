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

func TestReadRegularFileUnderRejectsFinalSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires platform support")
	}
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("outside identity\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "transcript.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	_, err := ReadRegularFileUnder(root, link)
	if !errors.Is(err, ErrSymlink) {
		t.Fatalf("ReadRegularFileUnder final symlink error = %v, want ErrSymlink", err)
	}
}

func TestReadRegularFileUnderPreservesRootAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires platform support")
	}
	realRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(realRoot, "transcript.txt"), []byte("aliased\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "root-alias")
	if err := os.Symlink(realRoot, alias); err != nil {
		t.Fatal(err)
	}

	got, err := ReadRegularFileUnder(alias, filepath.Join(alias, "transcript.txt"))
	if err != nil {
		t.Fatalf("ReadRegularFileUnder alias: %v", err)
	}
	if string(got) != "aliased\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestReadRegularFileUnderRejectsMissingAndDirectory(t *testing.T) {
	root := t.TempDir()
	missingRoot := filepath.Join(root, "missing-root")
	if _, err := ReadRegularFileUnder(missingRoot, filepath.Join(missingRoot, "transcript.txt")); !os.IsNotExist(err) {
		t.Fatalf("missing root error = %v, want os.IsNotExist", err)
	}
	if _, err := ReadRegularFileUnder(root, filepath.Join(root, "missing")); !os.IsNotExist(err) {
		t.Fatalf("missing error = %v, want os.IsNotExist", err)
	}
	if _, err := ReadRegularFileUnder(root, root); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("root directory error = %v, want ErrNotRegular", err)
	}
	dir := filepath.Join(root, "directory")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegularFileUnder(root, dir); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("directory error = %v, want ErrNotRegular", err)
	}
}

func TestReadRegularFileUnderLimitUsesHardCap(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.json")
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRegularFileUnderLimit(root, path, 5)
	if err != nil {
		t.Fatalf("exact limit: %v", err)
	}
	if string(got) != "12345" {
		t.Fatalf("exact-limit content = %q", got)
	}
	if _, err := ReadRegularFileUnderLimit(root, path, 4); err == nil {
		t.Fatal("over-limit read succeeded")
	}
}
