//go:build unix

package sessionevidence

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
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

func TestReadRegularFileUnderNeverFollowsRacingIntermediateSymlink(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	outside := t.TempDir()
	const filename = "transcript.txt"
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, filename), []byte("inside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, filename), []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertReadNeverEscapesRacingSymlink(
		t,
		root,
		filepath.Join(nested, filename),
		nested,
		outside,
	)
}

func TestReadRegularFileUnderNeverFollowsRacingFinalSymlink(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "transcript.txt")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(path, []byte("inside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertReadNeverEscapesRacingSymlink(t, root, path, path, outside)
}

func assertReadNeverEscapesRacingSymlink(t *testing.T, root, readPath, swapPath, symlinkTarget string) {
	t.Helper()
	parked := swapPath + ".parked"
	stop := make(chan struct{})
	attackerErr := make(chan error, 1)
	started := make(chan struct{})
	var swaps atomic.Int64
	go func() {
		close(started)
		for {
			select {
			case <-stop:
				attackerErr <- nil
				return
			default:
			}
			if err := os.Rename(swapPath, parked); err != nil {
				attackerErr <- err
				return
			}
			if err := os.Symlink(symlinkTarget, swapPath); err != nil {
				attackerErr <- err
				return
			}
			runtime.Gosched()
			if err := os.Remove(swapPath); err != nil {
				attackerErr <- err
				return
			}
			if err := os.Rename(parked, swapPath); err != nil {
				attackerErr <- err
				return
			}
			swaps.Add(1)
			runtime.Gosched()
		}
	}()
	<-started

	for range 5000 {
		got, err := ReadRegularFileUnder(root, readPath)
		if err != nil && !errors.Is(err, ErrSymlink) && !errors.Is(err, unix.ENOTDIR) && !os.IsNotExist(err) {
			close(stop)
			<-attackerErr
			t.Fatalf("read during symlink swap: %v", err)
		}
		if err == nil && string(got) != "inside\n" {
			close(stop)
			<-attackerErr
			t.Fatalf("read escaped through racing intermediate path: %q", got)
		}
	}
	close(stop)
	if err := <-attackerErr; err != nil {
		t.Fatalf("swap path: %v", err)
	}
	if swaps.Load() == 0 {
		t.Fatal("attacker completed no path swaps")
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
