package replay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/joshuavial/etude/internal/runmanifest"
)

// Sentinel errors for ExecRunner.
var (
	ErrRunnerNotConfigured = errors.New("runner not configured")
	ErrInvalidWorktreeDir  = errors.New("invalid worktree dir")
	ErrInvalidScratchDir   = errors.New("invalid scratch dir")
	ErrInvalidInputRole    = errors.New("invalid input role")
	ErrRunnerFailed        = errors.New("runner failed")
	ErrOutputMissing       = errors.New("output missing")
	ErrOutputNotRegular    = errors.New("output is not a regular file")
)

// ExecRunner satisfies Runner by invoking a configured external command
// headlessly. The command is launched with a strict environment (PATH,
// ETUDE_INPUTS_DIR, ETUDE_OUTPUT_FILE) and its working directory set to
// the resolved WorktreeDir.
type ExecRunner struct {
	// Command is the executable and its arguments. Command[0] is the binary;
	// Command[1:] are arguments. Must be non-empty to run.
	Command []string
}

// compile-time interface satisfaction assertion.
var _ Runner = (*ExecRunner)(nil)

// Run implements Runner for ExecRunner. It materializes inputs into
// <ScratchDir>/inputs/<NN>-<role>, invokes the configured command, and reads
// the output from <ScratchDir>/output.
func (r *ExecRunner) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	// Step 1: command must be configured.
	if len(r.Command) == 0 {
		return RunResult{}, ErrRunnerNotConfigured
	}

	// Step 2: validate and resolve WorktreeDir.
	resolvedWorktree, err := resolveDir(req.WorktreeDir)
	if err != nil {
		return RunResult{}, fmt.Errorf("%w: %v", ErrInvalidWorktreeDir, err)
	}

	// Step 2: validate and resolve ScratchDir.
	resolvedScratch, err := resolveDir(req.ScratchDir)
	if err != nil {
		return RunResult{}, fmt.Errorf("%w: %v", ErrInvalidScratchDir, err)
	}

	// Step 3: ScratchDir must NOT be at or under WorktreeDir.
	rel, relErr := filepath.Rel(resolvedWorktree, resolvedScratch)
	if relErr == nil {
		// rel == "." means ScratchDir IS WorktreeDir (reject).
		// A path that doesn't start with ".." means ScratchDir is inside WorktreeDir (reject).
		// A true sibling yields a rel starting with ".." (e.g. "../sibling") and is accepted.
		if rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..") {
			return RunResult{}, fmt.Errorf("%w: scratch dir must not be at or under worktree dir", ErrInvalidScratchDir)
		}
	}

	// Step 4: validate all input roles BEFORE any filesystem write.
	for _, inp := range req.Inputs {
		if !runmanifest.IsValidIdentifier(inp.Role) || inp.Role != filepath.Base(inp.Role) || inp.Role == ".." {
			return RunResult{}, fmt.Errorf("%w: %q", ErrInvalidInputRole, inp.Role)
		}
	}

	// Step 5: scratch hygiene — remove stale output and reset inputs dir.
	outputPath := filepath.Join(resolvedScratch, "output")
	inputsDir := filepath.Join(resolvedScratch, "inputs")

	_ = os.Remove(outputPath) // ignore os.IsNotExist; stale output must not survive

	if err := os.RemoveAll(inputsDir); err != nil {
		return RunResult{}, fmt.Errorf("%w: remove inputs dir: %v", ErrInvalidScratchDir, err)
	}
	if err := os.MkdirAll(inputsDir, 0o755); err != nil {
		return RunResult{}, fmt.Errorf("%w: create inputs dir: %v", ErrInvalidScratchDir, err)
	}

	// Write each input to <inputs>/<NN>-<role>. Nil/empty Inputs slice is
	// intentional — range over nil is a no-op.
	for i, inp := range req.Inputs {
		name := fmt.Sprintf("%02d-%s", i, inp.Role)
		path := filepath.Join(inputsDir, name)
		if err := os.WriteFile(path, inp.Content, 0o644); err != nil {
			return RunResult{}, fmt.Errorf("%w: write input %s: %v", ErrInvalidScratchDir, name, err)
		}
	}

	// Step 6: build strict env — only PATH, ETUDE_INPUTS_DIR, ETUDE_OUTPUT_FILE.
	pathVal := extractPATH(os.Environ())
	env := []string{
		"PATH=" + pathVal,
		"ETUDE_INPUTS_DIR=" + inputsDir,
		"ETUDE_OUTPUT_FILE=" + outputPath,
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, r.Command[0], r.Command[1:]...)
	cmd.Dir = resolvedWorktree
	cmd.Env = env
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()

	// Step 7: ctx taxonomy — context cancellation/timeout takes precedence.
	if ctx.Err() != nil {
		return RunResult{}, fmt.Errorf("runner: context done: %w", ctx.Err())
	}
	if runErr != nil {
		stderr := strings.TrimSpace(stderrBuf.String())
		return RunResult{}, fmt.Errorf("%w: %s: %v: %s", ErrRunnerFailed, r.Command[0], runErr, stderr)
	}

	// Step 8: read output (symlink-safe via Lstat).
	fi, statErr := os.Lstat(outputPath)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return RunResult{}, ErrOutputMissing
		}
		return RunResult{}, fmt.Errorf("%w: %v", ErrOutputMissing, statErr)
	}
	if !fi.Mode().IsRegular() {
		return RunResult{}, ErrOutputNotRegular
	}

	// An empty-but-present regular file is valid: Output will be empty bytes,
	// not ErrOutputMissing. Only absence triggers ErrOutputMissing.
	data, err := os.ReadFile(outputPath)
	if err != nil {
		return RunResult{}, fmt.Errorf("%w: read output: %v", ErrOutputMissing, err)
	}

	// Step 9: assemble result and apply defaults.
	res := RunResult{
		Output:   data,
		Producer: req.Producer,
	}
	applyResultDefaults(req, &res)
	return res, nil
}

// resolveDir validates that path is non-empty, absolute, exists, and is a
// directory, then returns its symlink-resolved form.
func resolveDir(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is empty")
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path %q is not absolute", path)
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat %q: %w", path, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%q is not a directory", path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("eval symlinks %q: %w", path, err)
	}
	return resolved, nil
}

// extractPATH returns the value of the PATH variable from the provided
// environment slice. It splits each entry on the FIRST '=' and matches
// by exact key "PATH" — using HasPrefix("PATH=") would match MYPATH=
// and similar variables.
func extractPATH(environ []string) string {
	for _, entry := range environ {
		idx := strings.IndexByte(entry, '=')
		if idx < 0 {
			continue
		}
		if entry[:idx] == "PATH" {
			return entry[idx+1:]
		}
	}
	return ""
}
