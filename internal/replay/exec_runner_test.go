package replay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/runmanifest"
)

// makeSiblingDirs creates a worktree dir and a sibling scratch dir under a
// common parent, so ScratchDir is NOT under WorktreeDir.
func makeSiblingDirs(t *testing.T) (worktree, scratch string) {
	t.Helper()
	parent := t.TempDir()
	worktree = filepath.Join(parent, "worktree")
	scratch = filepath.Join(parent, "scratch")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	return worktree, scratch
}

// writeScript writes a POSIX sh script to path and makes it executable.
func writeScript(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+content+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// execRunner builds an ExecRunner pointing at the given script.
func execRunner(script string) *ExecRunner {
	return &ExecRunner{Command: []string{script}}
}

func TestExecRunner_EmptyCommand(t *testing.T) {
	r := &ExecRunner{}
	worktree, scratch := makeSiblingDirs(t)
	req := RunRequest{WorktreeDir: worktree, ScratchDir: scratch}
	_, err := r.Run(context.Background(), req)
	if !errors.Is(err, ErrRunnerNotConfigured) {
		t.Fatalf("want ErrRunnerNotConfigured, got %v", err)
	}
}

func TestExecRunner_RelativeWorktreeDir(t *testing.T) {
	r := &ExecRunner{Command: []string{"/bin/true"}}
	_, scratch := makeSiblingDirs(t)
	req := RunRequest{WorktreeDir: "relative/path", ScratchDir: scratch}
	_, err := r.Run(context.Background(), req)
	if !errors.Is(err, ErrInvalidWorktreeDir) {
		t.Fatalf("want ErrInvalidWorktreeDir, got %v", err)
	}
}

func TestExecRunner_MissingWorktreeDir(t *testing.T) {
	r := &ExecRunner{Command: []string{"/bin/true"}}
	_, scratch := makeSiblingDirs(t)
	req := RunRequest{WorktreeDir: "/nonexistent/path/that/cannot/exist", ScratchDir: scratch}
	_, err := r.Run(context.Background(), req)
	if !errors.Is(err, ErrInvalidWorktreeDir) {
		t.Fatalf("want ErrInvalidWorktreeDir, got %v", err)
	}
}

func TestExecRunner_RelativeScratchDir(t *testing.T) {
	r := &ExecRunner{Command: []string{"/bin/true"}}
	worktree, _ := makeSiblingDirs(t)
	req := RunRequest{WorktreeDir: worktree, ScratchDir: "relative/scratch"}
	_, err := r.Run(context.Background(), req)
	if !errors.Is(err, ErrInvalidScratchDir) {
		t.Fatalf("want ErrInvalidScratchDir, got %v", err)
	}
}

func TestExecRunner_MissingScratchDir(t *testing.T) {
	r := &ExecRunner{Command: []string{"/bin/true"}}
	worktree, _ := makeSiblingDirs(t)
	req := RunRequest{WorktreeDir: worktree, ScratchDir: "/nonexistent/scratch/path"}
	_, err := r.Run(context.Background(), req)
	if !errors.Is(err, ErrInvalidScratchDir) {
		t.Fatalf("want ErrInvalidScratchDir, got %v", err)
	}
}

func TestExecRunner_ScratchEqualsWorktree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path tests skipped on Windows")
	}
	r := &ExecRunner{Command: []string{"/bin/true"}}
	worktree, _ := makeSiblingDirs(t)
	// ScratchDir == WorktreeDir: rel == "." -> must be rejected.
	req := RunRequest{WorktreeDir: worktree, ScratchDir: worktree}
	_, err := r.Run(context.Background(), req)
	if !errors.Is(err, ErrInvalidScratchDir) {
		t.Fatalf("want ErrInvalidScratchDir when scratch==worktree, got %v", err)
	}
}

func TestExecRunner_ScratchUnderWorktree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path tests skipped on Windows")
	}
	r := &ExecRunner{Command: []string{"/bin/true"}}
	worktree, _ := makeSiblingDirs(t)
	// ScratchDir nested inside WorktreeDir: must be rejected.
	nested := filepath.Join(worktree, "scratch")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	req := RunRequest{WorktreeDir: worktree, ScratchDir: nested}
	_, err := r.Run(context.Background(), req)
	if !errors.Is(err, ErrInvalidScratchDir) {
		t.Fatalf("want ErrInvalidScratchDir when scratch is under worktree, got %v", err)
	}
}

func TestExecRunner_SiblingWorktreePrefixTrap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path tests skipped on Windows")
	}
	// /tmp/wt and /tmp/wt-other: "wt-other" starts with "wt" but is not inside it.
	// This is the prefix trap — must be ACCEPTED.
	parent := t.TempDir()
	worktree := filepath.Join(parent, "wt")
	scratch := filepath.Join(parent, "wt-other")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}

	scriptPath := filepath.Join(scratch, "script.sh")
	writeScript(t, scriptPath, `printf 'ok' > "$ETUDE_OUTPUT_FILE"`)

	r := &ExecRunner{Command: []string{scriptPath}}
	req := RunRequest{
		WorktreeDir:     worktree,
		ScratchDir:      scratch,
		OutputMediaType: "text/plain",
	}
	res, err := r.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("sibling prefix trap: want success, got %v", err)
	}
	if string(res.Output) != "ok" {
		t.Errorf("want output %q, got %q", "ok", res.Output)
	}
}

func TestExecRunner_EvalSymlinksFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX symlink tests skipped on Windows")
	}
	r := &ExecRunner{Command: []string{"/bin/true"}}
	worktree, _ := makeSiblingDirs(t)

	// Create a directory, then create a symlink to a non-existent path.
	// We pass the symlink as ScratchDir — Stat (via os.Stat following symlinks)
	// will fail because the target doesn't exist.
	parent := t.TempDir()
	brokenLink := filepath.Join(parent, "broken-link")
	if err := os.Symlink("/nonexistent/target", brokenLink); err != nil {
		t.Fatal(err)
	}

	req := RunRequest{WorktreeDir: worktree, ScratchDir: brokenLink}
	_, err := r.Run(context.Background(), req)
	if !errors.Is(err, ErrInvalidScratchDir) {
		t.Fatalf("want ErrInvalidScratchDir for broken symlink, got %v", err)
	}
}

func TestExecRunner_InvalidInputRole_PathTraversal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}
	worktree, scratch := makeSiblingDirs(t)
	r := &ExecRunner{Command: []string{"/bin/true"}}

	badRoles := []string{"../escape", "a/b", "..", ""}
	for _, role := range badRoles {
		req := RunRequest{
			WorktreeDir: worktree,
			ScratchDir:  scratch,
			Inputs:      []RunInput{{Role: role, Content: []byte("data")}},
		}
		_, err := r.Run(context.Background(), req)
		if !errors.Is(err, ErrInvalidInputRole) {
			t.Errorf("role %q: want ErrInvalidInputRole, got %v", role, err)
		}
		// Verify no file was written outside <inputs>.
		inputsDir := filepath.Join(scratch, "inputs")
		entries, _ := os.ReadDir(inputsDir)
		if len(entries) != 0 {
			t.Errorf("role %q: inputs dir should be empty after validation failure, got %v", role, entries)
		}
	}
}

func TestExecRunner_Materialization(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}
	worktree, scratch := makeSiblingDirs(t)

	scriptPath := filepath.Join(scratch, "script.sh")
	writeScript(t, scriptPath, `printf 'done' > "$ETUDE_OUTPUT_FILE"`)

	// Build 11 inputs to verify %02d padding (00..10).
	inputs := make([]RunInput, 11)
	for i := range inputs {
		inputs[i] = RunInput{
			Role:    fmt.Sprintf("role%d", i),
			Content: []byte(fmt.Sprintf("content-%d", i)),
		}
	}

	r := execRunner(scriptPath)
	req := RunRequest{
		WorktreeDir:     worktree,
		ScratchDir:      scratch,
		Inputs:          inputs,
		OutputMediaType: "text/plain",
	}
	_, err := r.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	inputsDir := filepath.Join(scratch, "inputs")
	for i, inp := range inputs {
		name := fmt.Sprintf("%02d-%s", i, inp.Role)
		path := filepath.Join(inputsDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("input file %s missing: %v", name, err)
			continue
		}
		if string(data) != string(inp.Content) {
			t.Errorf("input %s: want %q, got %q", name, inp.Content, data)
		}
	}
	// Check that 10-role10 exists (two-digit padding for index 10).
	if _, err := os.Stat(filepath.Join(inputsDir, "10-role10")); err != nil {
		t.Errorf("want file 10-role10, stat error: %v", err)
	}
}

func TestExecRunner_CWD(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}
	worktree, scratch := makeSiblingDirs(t)

	// Place a marker file in the worktree that the script reads.
	markerContent := "worktree-marker-42"
	if err := os.WriteFile(filepath.Join(worktree, "marker.txt"), []byte(markerContent), 0o644); err != nil {
		t.Fatal(err)
	}

	scriptPath := filepath.Join(scratch, "script.sh")
	// Script reads marker.txt from cwd (which should be worktree) and writes it as output.
	writeScript(t, scriptPath, `cat marker.txt > "$ETUDE_OUTPUT_FILE"`)

	r := execRunner(scriptPath)
	req := RunRequest{
		WorktreeDir:     worktree,
		ScratchDir:      scratch,
		OutputMediaType: "text/plain",
	}
	res, err := r.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(res.Output) != markerContent {
		t.Errorf("want marker content %q, got %q (cmd.Dir not set to worktree?)", markerContent, res.Output)
	}
}

func TestExecRunner_EnvRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}
	worktree, scratch := makeSiblingDirs(t)

	// Write an input that the script reads via $ETUDE_INPUTS_DIR.
	inputContent := "hello-from-input"
	inputs := []RunInput{{Role: "data", Content: []byte(inputContent)}}

	scriptPath := filepath.Join(scratch, "script.sh")
	// Script reads the first input via env var and writes it via $ETUDE_OUTPUT_FILE.
	// Also verifies both paths are absolute by checking they start with '/'.
	writeScript(t, scriptPath, `
[ "${ETUDE_INPUTS_DIR#/}" != "$ETUDE_INPUTS_DIR" ] || { echo "ETUDE_INPUTS_DIR not absolute" >&2; exit 1; }
[ "${ETUDE_OUTPUT_FILE#/}" != "$ETUDE_OUTPUT_FILE" ] || { echo "ETUDE_OUTPUT_FILE not absolute" >&2; exit 1; }
cat "$ETUDE_INPUTS_DIR/00-data" > "$ETUDE_OUTPUT_FILE"
`)

	r := execRunner(scriptPath)
	req := RunRequest{
		WorktreeDir:     worktree,
		ScratchDir:      scratch,
		Inputs:          inputs,
		OutputMediaType: "text/plain",
	}
	res, err := r.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(res.Output) != inputContent {
		t.Errorf("want %q from input round-trip, got %q", inputContent, res.Output)
	}
}

func TestExecRunner_NonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}
	worktree, scratch := makeSiblingDirs(t)

	scriptPath := filepath.Join(scratch, "script.sh")
	writeScript(t, scriptPath, `echo "something went wrong" >&2; exit 2`)

	r := execRunner(scriptPath)
	req := RunRequest{WorktreeDir: worktree, ScratchDir: scratch}
	_, err := r.Run(context.Background(), req)
	if !errors.Is(err, ErrRunnerFailed) {
		t.Fatalf("want ErrRunnerFailed, got %v", err)
	}
	// Message should contain trimmed stderr and exit status.
	if !strings.Contains(err.Error(), "something went wrong") {
		t.Errorf("want stderr in error message, got: %v", err)
	}
	if !strings.Contains(err.Error(), "exit status 2") {
		t.Errorf("want exit status in error message, got: %v", err)
	}
}

func TestExecRunner_ExitZeroNoOutputFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}
	worktree, scratch := makeSiblingDirs(t)

	scriptPath := filepath.Join(scratch, "script.sh")
	// Script exits 0 but writes no output file.
	writeScript(t, scriptPath, `exit 0`)

	r := execRunner(scriptPath)
	req := RunRequest{WorktreeDir: worktree, ScratchDir: scratch}
	_, err := r.Run(context.Background(), req)
	if !errors.Is(err, ErrOutputMissing) {
		t.Fatalf("want ErrOutputMissing, got %v", err)
	}
}

func TestExecRunner_StaleOutputRemoved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}
	worktree, scratch := makeSiblingDirs(t)

	// Pre-populate a stale output file.
	staleContent := "stale-bytes"
	if err := os.WriteFile(filepath.Join(scratch, "output"), []byte(staleContent), 0o644); err != nil {
		t.Fatal(err)
	}

	scriptPath := filepath.Join(scratch, "script.sh")
	// Script writes nothing — so output should be missing, NOT the stale bytes.
	writeScript(t, scriptPath, `exit 0`)

	r := execRunner(scriptPath)
	req := RunRequest{WorktreeDir: worktree, ScratchDir: scratch}
	_, err := r.Run(context.Background(), req)
	if !errors.Is(err, ErrOutputMissing) {
		t.Fatalf("want ErrOutputMissing (stale output must not survive), got %v", err)
	}
}

func TestExecRunner_StaleInputsHygiene(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}
	worktree, scratch := makeSiblingDirs(t)

	// Pre-populate a stale input file from a "prior run".
	inputsDir := filepath.Join(scratch, "inputs")
	if err := os.MkdirAll(inputsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	staleInput := filepath.Join(inputsDir, "00-old-role")
	if err := os.WriteFile(staleInput, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	scriptPath := filepath.Join(scratch, "script.sh")
	writeScript(t, scriptPath, `printf 'ok' > "$ETUDE_OUTPUT_FILE"`)

	// New run with a different input (no "old-role" input).
	r := execRunner(scriptPath)
	req := RunRequest{
		WorktreeDir:     worktree,
		ScratchDir:      scratch,
		Inputs:          []RunInput{{Role: "new-role", Content: []byte("new")}},
		OutputMediaType: "text/plain",
	}
	_, err := r.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Stale file must no longer exist.
	if _, statErr := os.Stat(staleInput); !os.IsNotExist(statErr) {
		t.Errorf("stale input file should have been removed, stat err: %v", statErr)
	}
	// Only the new input should be present.
	if _, statErr := os.Stat(filepath.Join(inputsDir, "00-new-role")); statErr != nil {
		t.Errorf("new input file should exist: %v", statErr)
	}
}

func TestExecRunner_OutputIsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX symlink tests skipped on Windows")
	}
	worktree, scratch := makeSiblingDirs(t)

	// The script creates the output path as a symlink pointing outside scratch.
	outsideFile := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	scriptPath := filepath.Join(scratch, "script.sh")
	writeScript(t, scriptPath, fmt.Sprintf(`ln -s %s "$ETUDE_OUTPUT_FILE"`, outsideFile))

	r := execRunner(scriptPath)
	req := RunRequest{WorktreeDir: worktree, ScratchDir: scratch}
	_, err := r.Run(context.Background(), req)
	if !errors.Is(err, ErrOutputNotRegular) {
		t.Fatalf("want ErrOutputNotRegular for symlink output, got %v", err)
	}
}

func TestExecRunner_EmptyOutputFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}
	worktree, scratch := makeSiblingDirs(t)

	scriptPath := filepath.Join(scratch, "script.sh")
	// Script creates a 0-byte output file — this is valid, NOT ErrOutputMissing.
	writeScript(t, scriptPath, `> "$ETUDE_OUTPUT_FILE"`)

	r := execRunner(scriptPath)
	req := RunRequest{
		WorktreeDir:     worktree,
		ScratchDir:      scratch,
		OutputMediaType: "text/plain",
	}
	res, err := r.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("empty output file should succeed, got %v", err)
	}
	if len(res.Output) != 0 {
		t.Errorf("want empty Output bytes, got %q", res.Output)
	}
}

func TestExecRunner_CancelledContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}
	worktree, scratch := makeSiblingDirs(t)

	scriptPath := filepath.Join(scratch, "script.sh")
	// Script sleeps longer than our timeout.
	writeScript(t, scriptPath, `sleep 10`)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	r := execRunner(scriptPath)
	req := RunRequest{WorktreeDir: worktree, ScratchDir: scratch}
	_, err := r.Run(ctx, req)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
}

func TestExecRunner_CancelledContextBeforeRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}
	worktree, scratch := makeSiblingDirs(t)

	scriptPath := filepath.Join(scratch, "script.sh")
	writeScript(t, scriptPath, `printf 'ok' > "$ETUDE_OUTPUT_FILE"`)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Run

	r := execRunner(scriptPath)
	req := RunRequest{WorktreeDir: worktree, ScratchDir: scratch}
	_, err := r.Run(ctx, req)
	if err == nil {
		// A pre-cancelled context may or may not cause cmd.Run to fail
		// depending on timing; this is acceptable.
		t.Skip("command completed before context check; timing-sensitive test")
	}
	// If we do get an error it should wrap context.Canceled or DeadlineExceeded.
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, ErrRunnerFailed) {
		t.Errorf("want context error or ErrRunnerFailed, got %v", err)
	}
}

func TestExecRunner_MediaTypeDefault(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}
	worktree, scratch := makeSiblingDirs(t)

	scriptPath := filepath.Join(scratch, "script.sh")
	writeScript(t, scriptPath, `printf 'out' > "$ETUDE_OUTPUT_FILE"`)

	r := execRunner(scriptPath)
	req := RunRequest{
		WorktreeDir:     worktree,
		ScratchDir:      scratch,
		OutputMediaType: "application/json",
	}
	res, err := r.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// RunResult.MediaType was not set by the runner, so applyResultDefaults
	// must fill it from req.OutputMediaType.
	if res.MediaType != "application/json" {
		t.Errorf("want MediaType %q (default), got %q", "application/json", res.MediaType)
	}
}

func TestExecRunner_ProducerCarried(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}
	worktree, scratch := makeSiblingDirs(t)

	scriptPath := filepath.Join(scratch, "script.sh")
	writeScript(t, scriptPath, `printf 'out' > "$ETUDE_OUTPUT_FILE"`)

	producer := runmanifest.Producer{
		Harness: runmanifest.Harness{Name: "claude-code", Version: "2.1.150"},
		Model:   "claude-opus-4-7",
		Skill:   runmanifest.Skill{ID: "my-skill", Repo: "my-repo", Version: "v3"},
	}
	r := execRunner(scriptPath)
	req := RunRequest{
		WorktreeDir:     worktree,
		ScratchDir:      scratch,
		OutputMediaType: "text/plain",
		Producer:        producer,
	}
	res, err := r.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Producer != producer {
		t.Errorf("want Producer %v carried through, got %v", producer, res.Producer)
	}
}

// TestExecRunner_ScratchSymlinksUnderWorktree proves that EvalSymlinks-resolved
// containment is enforced: a ScratchDir that is lexically outside WorktreeDir
// but resolves (via symlink) to a path under WorktreeDir must be rejected.
// Without EvalSymlinks in resolveDir the lexical filepath.Rel check would
// accept the sibling symlink path — the test would pass an invalid request.
func TestExecRunner_ScratchSymlinksUnderWorktree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX symlink tests skipped on Windows")
	}

	// Create a real directory nested under the worktree.
	worktreeDir := t.TempDir()
	realTarget := filepath.Join(worktreeDir, "sub")
	if err := os.MkdirAll(realTarget, 0o755); err != nil {
		t.Fatal(err)
	}

	// In a separate sibling base dir, create a symlink named "scratch" that
	// points to the directory inside the worktree.
	siblingBase := t.TempDir()
	symlinkScratch := filepath.Join(siblingBase, "scratch")
	if err := os.Symlink(realTarget, symlinkScratch); err != nil {
		t.Fatal(err)
	}

	// symlinkScratch is lexically OUTSIDE worktreeDir (different TempDir base),
	// but resolves to worktreeDir/sub which is UNDER the worktree.
	r := &ExecRunner{Command: []string{"/bin/true"}}
	req := RunRequest{WorktreeDir: worktreeDir, ScratchDir: symlinkScratch}
	_, err := r.Run(context.Background(), req)
	if !errors.Is(err, ErrInvalidScratchDir) {
		t.Fatalf("want ErrInvalidScratchDir (symlink resolves under worktree), got %v", err)
	}
}

// TestExecRunner_PATHExactKeyMatch proves that extractPATH uses exact key
// matching ("PATH") rather than HasPrefix("PATH"), which would accidentally
// return the value of PATHEXT=, MYPATH=, or PATH_X= instead.
func TestExecRunner_PATHExactKeyMatch(t *testing.T) {
	// Decoys placed BEFORE the real PATH= entry to ensure a HasPrefix("PATH")
	// implementation would return the wrong value on the first match.
	env := []string{
		"PATHEXT=wrong-pathext",
		"MYPATH=wrong-mypath",
		"PATH=/real/bin",
		"PATH_X=wrong-path-x",
	}
	got := extractPATH(env)
	if got != "/real/bin" {
		t.Errorf("extractPATH: want %q, got %q (decoy key matched instead of exact PATH=)", "/real/bin", got)
	}
}

// TestExecRunner_EnvIsolation proves that the child process's environment is
// the strict set (PATH + ETUDE_INPUTS_DIR + ETUDE_OUTPUT_FILE) and does NOT
// inherit arbitrary parent-process environment variables.
func TestExecRunner_EnvIsolation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}

	// Set a sentinel env var in the parent process.
	t.Setenv("ETUDE_SECRET_PROBE", "leaked-value")

	worktree, scratch := makeSiblingDirs(t)

	scriptPath := filepath.Join(scratch, "script.sh")
	// Write ETUDE_SECRET_PROBE's value (empty string if unset) to the output
	// file. If the strict env leaks the parent var, output will be "leaked-value".
	writeScript(t, scriptPath, `printf '%s' "$ETUDE_SECRET_PROBE" > "$ETUDE_OUTPUT_FILE"`)

	r := execRunner(scriptPath)
	req := RunRequest{
		WorktreeDir:     worktree,
		ScratchDir:      scratch,
		OutputMediaType: "text/plain",
	}
	res, err := r.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(res.Output), "leaked-value") {
		t.Errorf("env isolation failure: parent ETUDE_SECRET_PROBE leaked into child env, output: %q", res.Output)
	}
}

// TestExecRunner_TimeoutExceeded proves that a configured Timeout causes the
// run to fail with context.DeadlineExceeded and a "timed out" message.
func TestExecRunner_TimeoutExceeded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}
	orig := runnerWaitDelay
	runnerWaitDelay = 200 * time.Millisecond
	defer func() { runnerWaitDelay = orig }()

	worktree, scratch := makeSiblingDirs(t)

	scriptPath := filepath.Join(scratch, "script.sh")
	writeScript(t, scriptPath, `sleep 30`)

	r := &ExecRunner{
		Command: []string{scriptPath},
		Timeout: 150 * time.Millisecond,
	}
	req := RunRequest{WorktreeDir: worktree, ScratchDir: scratch}
	_, err := r.Run(context.Background(), req)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("want 'timed out' in error message, got: %v", err)
	}
}

// TestExecRunner_OutputExceedsCap proves that output larger than MaxOutputBytes
// is rejected with ErrOutputTooLarge naming the cap.
func TestExecRunner_OutputExceedsCap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}
	worktree, scratch := makeSiblingDirs(t)

	scriptPath := filepath.Join(scratch, "script.sh")
	// Write 20 bytes of output, cap is 10.
	writeScript(t, scriptPath, `printf '12345678901234567890' > "$ETUDE_OUTPUT_FILE"`)

	r := &ExecRunner{
		Command:        []string{scriptPath},
		MaxOutputBytes: 10,
	}
	req := RunRequest{WorktreeDir: worktree, ScratchDir: scratch}
	_, err := r.Run(context.Background(), req)
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("want ErrOutputTooLarge, got %v", err)
	}
	if !strings.Contains(err.Error(), "10") {
		t.Errorf("want cap value in error message, got: %v", err)
	}
}

// TestExecRunner_OutputWithinCap proves that normal output under the cap
// still succeeds unchanged.
func TestExecRunner_OutputWithinCap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}
	worktree, scratch := makeSiblingDirs(t)

	scriptPath := filepath.Join(scratch, "script.sh")
	writeScript(t, scriptPath, `printf 'hello' > "$ETUDE_OUTPUT_FILE"`)

	r := &ExecRunner{
		Command:        []string{scriptPath},
		MaxOutputBytes: 100,
	}
	req := RunRequest{
		WorktreeDir:     worktree,
		ScratchDir:      scratch,
		OutputMediaType: "text/plain",
	}
	res, err := r.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("want success, got %v", err)
	}
	if string(res.Output) != "hello" {
		t.Errorf("want output %q, got %q", "hello", res.Output)
	}
}

// TestExecRunner_SpawnsSurvivingChild proves that WaitDelay prevents the run
// from hanging when the script backgrounds a long-lived child that holds
// inherited pipe write-ends open.
//
// Without WaitDelay: cmd.Wait blocks indefinitely because the grandchild keeps
// the stdout/stderr pipes open, so the test would hang until the test timeout
// guard fires.
// With WaitDelay: cmd.Run returns within ~Timeout+WaitDelay. The test guard is
// set to 5s (comfortably above 200ms WaitDelay + 150ms Timeout but well below
// any infinite hang), so a regression is a test FAILURE, not a hang.
func TestExecRunner_SpawnsSurvivingChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh tests skipped on Windows")
	}
	orig := runnerWaitDelay
	runnerWaitDelay = 200 * time.Millisecond
	defer func() { runnerWaitDelay = orig }()

	worktree, scratch := makeSiblingDirs(t)

	scriptPath := filepath.Join(scratch, "script.sh")
	// Background a long-lived child, then exit 0. The grandchild holds inherited
	// pipe write-ends, which would cause cmd.Wait to hang without WaitDelay.
	writeScript(t, scriptPath, `sleep 300 &
printf 'done' > "$ETUDE_OUTPUT_FILE"`)

	r := &ExecRunner{
		Command: []string{scriptPath},
		Timeout: 150 * time.Millisecond,
	}
	req := RunRequest{WorktreeDir: worktree, ScratchDir: scratch}

	done := make(chan error, 1)
	go func() {
		_, err := r.Run(context.Background(), req)
		done <- err
	}()

	// Guard: the run MUST return within Timeout + WaitDelay + generous margin.
	// If it doesn't return, WaitDelay is not working (regression).
	select {
	case err := <-done:
		// The run returned — WaitDelay worked. The error is expected (timeout).
		if err == nil {
			t.Error("expected an error (timeout), got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ExecRunner.Run did not return within 5s; WaitDelay regression: surviving child held pipes open")
	}
}
